package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestIdentifyByResponseShapeNotPort(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			w.Write([]byte(`{"models":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer ollama.Close()

	llama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/props":
			w.Write([]byte(`{"build_info":"b6142","model_path":"/m.gguf"}`))
		case "/v1/models":
			// llama-server ALSO speaks OpenAI; Identify must not call it that.
			w.Write([]byte(`{"data":[{"id":"m"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer llama.Close()

	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Write([]byte(`{"data":[{"id":"local-model"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer openai.Close()

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	dead.Close() // closed = connection refused

	cases := []struct {
		url, want string
		ok        bool
	}{
		{ollama.URL, "ollama", true},
		{llama.URL, "llama-server", true},
		{openai.URL, "openai", true},
		{dead.URL, "", false},
	}
	for _, tc := range cases {
		got, ok := Identify(context.Background(), tc.url)
		if ok != tc.ok || got != tc.want {
			t.Errorf("Identify(%s) = %q %v, want %q %v", tc.url, got, ok, tc.want, tc.ok)
		}
	}
}

func TestIdentifyRejectsLookalikeAndOversizedResponses(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
	}{
		{"ollama null models", "/api/tags", `{"models":null}`},
		{"ollama object models", "/api/tags", `{"models":{}}`},
		{"openai null data", "/v1/models", `{"data":null}`},
		{"openai object data", "/v1/models", `{"data":{}}`},
		{"duplicate ollama models", "/api/tags", `{"models":[],"models":[]}`},
		{"oversized ollama", "/api/tags", `{"models":[]}` + strings.Repeat(" ", maxDiscoveryBody)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == tc.path {
					_, _ = w.Write([]byte(tc.body))
					return
				}
				http.NotFound(w, r)
			}))
			defer srv.Close()

			if kind, ok := Identify(context.Background(), srv.URL); ok {
				t.Fatalf("Identify accepted malformed lookalike as %q", kind)
			}
		})
	}
}

func TestDiscoverPreservesPreferredOrder(t *testing.T) {
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Write([]byte(`{"data":[]}`))
		}
	}))
	defer second.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			w.Write([]byte(`{"models":[]}`))
		}
	}))
	defer first.Close()

	got := discover(context.Background(), []string{first.URL, second.URL})
	if len(got) != 2 {
		t.Fatalf("found %d, want 2: %+v", len(got), got)
	}
	if got[0].Kind != "ollama" || got[1].Kind != "openai" {
		t.Fatalf("order = %+v, want ollama then openai", got)
	}
}

func TestDiscoverBoundsConcurrentProbes(t *testing.T) {
	var active atomic.Int32
	var peak atomic.Int32
	reachedLimit := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		if n == maxConcurrentDiscovery {
			select {
			case reachedLimit <- struct{}{}:
			default:
			}
		}
		<-release
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer srv.Close()

	urls := make([]string, maxConcurrentDiscovery*4)
	for i := range urls {
		urls[i] = srv.URL + "/" + strconv.Itoa(i)
	}
	done := make(chan []Found, 1)
	go func() { done <- discover(context.Background(), urls) }()
	select {
	case <-reachedLimit:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("discovery workers did not reach the configured concurrency")
	}
	if got := peak.Load(); got != maxConcurrentDiscovery {
		close(release)
		t.Fatalf("peak concurrent probes = %d, want %d", got, maxConcurrentDiscovery)
	}
	close(release)
	if got := <-done; len(got) != len(urls) {
		t.Fatalf("found %d runtimes, want %d", len(got), len(urls))
	}
}

func TestCandidatesDedupAndEnv(t *testing.T) {
	t.Setenv("OLLAMA_BASE_URL", "http://127.0.0.1:11434")
	t.Setenv("LLAMA_SERVER_URL", "http://127.0.0.1:8080")
	t.Setenv("FITR_OPENAI_URL", "http://127.0.0.1:1234")
	t.Setenv("FITR_DISCOVER_URLS", "http://127.0.0.1:8000, http://127.0.0.1:9999")
	c, err := Candidates()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, u := range c {
		seen[u]++
		if seen[u] > 1 {
			t.Fatalf("duplicate candidate %s", u)
		}
	}
	if seen["http://127.0.0.1:9999"] != 1 {
		t.Fatalf("FITR_DISCOVER_URLS extra port missing: %v", c)
	}
	if seen["http://127.0.0.1:8000"] != 1 {
		t.Fatalf("vLLM default port missing: %v", c)
	}
}

func TestCandidatesRejectsExcessiveOrOversizedConfiguration(t *testing.T) {
	tooMany := make([]string, maxDiscoveryCandidates)
	for i := range tooMany {
		tooMany[i] = "http://127.0.0.1:" + strconv.Itoa(20000+i)
	}
	t.Setenv("FITR_DISCOVER_URLS", strings.Join(tooMany, ","))
	if _, err := Candidates(); err == nil || !strings.Contains(err.Error(), "limited") {
		t.Fatalf("excess candidate error = %v", err)
	}

	t.Setenv("FITR_DISCOVER_URLS", "http://127.0.0.1/"+strings.Repeat("x", 2048))
	if _, err := Candidates(); err == nil || !strings.Contains(err.Error(), "2048 bytes") {
		t.Fatalf("oversized URL error = %v", err)
	}
}
