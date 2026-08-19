package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Found is one reachable serving runtime. Kind is "ollama", "llama-server",
// or "openai" - identified by the response shape, never by the port.
type Found struct {
	Kind string
	URL  string
}

// wellKnown is the three endpoints people actually run, in the order we
// prefer them when more than one is up. Env overrides replace the default
// URL for that kind; they do not add a fourth slot.
func wellKnown() []Found {
	return []Found{
		{"ollama", envOr("OLLAMA_BASE_URL", "http://127.0.0.1:11434")},
		{"llama-server", envOr("LLAMA_SERVER_URL", "http://127.0.0.1:8080")},
		{"openai", envOr("FITR_OPENAI_URL", "http://127.0.0.1:1234")},
	}
}

// extraOpenAI are ports other local OpenAI-shaped servers sit on. Only
// consulted when the preferred endpoints are down, so a forgotten vLLM on
// :8000 is found without stealing the measurement from a running Ollama.
var extraOpenAI = []string{
	"http://127.0.0.1:8000",  // vLLM, NVIDIA NIM
	"http://127.0.0.1:5000",  // text-generation-webui
	"http://127.0.0.1:5001",  // koboldcpp
	"http://127.0.0.1:1337",  // Jan
	"http://127.0.0.1:30000", // SGLang
	"http://127.0.0.1:4000",  // LiteLLM
}

func envOr(key, fallback string) string {
	if v := strings.TrimRight(os.Getenv(key), "/"); v != "" {
		return v
	}
	return fallback
}

// Candidates is the full probe list: well-known first, then extra ports
// that are not already in that set, then anything in $FITR_DISCOVER_URLS
// (comma-separated). Used for error messages so the user can see what we
// actually tried.
func Candidates() []string {
	seen := map[string]bool{}
	var out []string
	add := func(u string) {
		u = strings.TrimRight(u, "/")
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		out = append(out, u)
	}
	for _, c := range wellKnown() {
		add(c.URL)
	}
	for _, extra := range extraOpenAI {
		add(extra)
	}
	for _, extra := range strings.Split(os.Getenv("FITR_DISCOVER_URLS"), ",") {
		add(strings.TrimSpace(extra))
	}
	return out
}

// Discover probes the candidate list and returns every runtime that answered
// as a known kind. Identification is by response shape: llama-server speaks
// /v1/models too, so a /props hit wins over an OpenAI-shaped one on the
// same URL. Timeouts are short; a down port must not stall a run.
func Discover(ctx context.Context) []Found {
	return discover(ctx, Candidates())
}

func discover(ctx context.Context, urls []string) []Found {
	type hit struct{ url, kind string }
	ch := make(chan hit, len(urls))
	var wg sync.WaitGroup
	for _, u := range urls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if kind, ok := Identify(ctx, u); ok {
				ch <- hit{url: u, kind: kind}
			}
		}()
	}
	go func() { wg.Wait(); close(ch) }()
	found := map[string]string{}
	for h := range ch {
		found[h.url] = h.kind
	}
	// Stable: candidate order, not whichever goroutine finished first.
	var out []Found
	for _, u := range urls {
		if kind, ok := found[u]; ok {
			out = append(out, Found{Kind: kind, URL: u})
		}
	}
	return out
}

// Identify classifies one base URL. Empty kind means nothing we can measure
// through is listening there.
func Identify(ctx context.Context, base string) (string, bool) {
	base = strings.TrimRight(base, "/")
	if base == "" {
		return "", false
	}
	// Ollama first: /api/tags is unique to it.
	if body, ok := getJSON(ctx, base+"/api/tags"); ok {
		var r struct {
			Models json.RawMessage `json:"models"`
		}
		if json.Unmarshal(body, &r) == nil && r.Models != nil {
			return "ollama", true
		}
	}
	// llama-server next: /props with a build or a model path. It also
	// serves /v1/models, so this must beat the OpenAI probe.
	if body, ok := getJSON(ctx, base+"/props"); ok {
		var r struct {
			BuildInfo string `json:"build_info"`
			ModelPath string `json:"model_path"`
		}
		if json.Unmarshal(body, &r) == nil && (r.BuildInfo != "" || r.ModelPath != "") {
			return "llama-server", true
		}
	}
	if body, ok := getJSON(ctx, base+"/v1/models"); ok {
		var r struct {
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(body, &r) == nil && r.Data != nil {
			return "openai", true
		}
	}
	return "", false
}

func getJSON(ctx context.Context, url string) ([]byte, bool) {
	cctx, cancel := context.WithTimeout(ctx, 600*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false
	}
	resp, err := discoveryHTTP.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return body, err == nil
}

// Short-timeout client: discovery must fail fast on a closed port.
var discoveryHTTP = &http.Client{Timeout: 700 * time.Millisecond}
