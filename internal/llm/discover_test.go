package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
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

func TestCandidatesDedupAndEnv(t *testing.T) {
	t.Setenv("OLLAMA_BASE_URL", "http://127.0.0.1:11434")
	t.Setenv("LLAMA_SERVER_URL", "http://127.0.0.1:8080")
	t.Setenv("FITR_OPENAI_URL", "http://127.0.0.1:1234")
	t.Setenv("FITR_DISCOVER_URLS", "http://127.0.0.1:8000, http://127.0.0.1:9999")
	c := Candidates()
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
