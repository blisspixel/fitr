package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/blisspixel/fitr/internal/strictjson"
)

const (
	maxDiscoveryBody       = 1 << 20
	maxConcurrentDiscovery = 16
	maxDiscoveryCandidates = 128
	discoverySweepTimeout  = 3 * time.Second
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
func Candidates() ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(u string) error {
		u = strings.TrimRight(u, "/")
		if u == "" || seen[u] {
			return nil
		}
		if len(u) > 2048 {
			return errors.New("discovery URL exceeds 2048 bytes")
		}
		if len(out) >= maxDiscoveryCandidates {
			return fmt.Errorf("discovery is limited to %d unique URLs", maxDiscoveryCandidates)
		}
		seen[u] = true
		out = append(out, u)
		return nil
	}
	for _, c := range wellKnown() {
		if err := add(c.URL); err != nil {
			return nil, err
		}
	}
	for _, extra := range extraOpenAI {
		if err := add(extra); err != nil {
			return nil, err
		}
	}
	for _, extra := range strings.Split(os.Getenv("FITR_DISCOVER_URLS"), ",") {
		if err := add(strings.TrimSpace(extra)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Discover probes the candidate list and returns every runtime that answered
// as a known kind. Identification is by response shape: llama-server speaks
// /v1/models too, so a /props hit wins over an OpenAI-shaped one on the
// same URL. Timeouts are short; a down port must not stall a run.
func Discover(ctx context.Context) ([]Found, error) {
	urls, err := Candidates()
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, discoverySweepTimeout)
	defer cancel()
	return discover(cctx, urls), nil
}

func discover(ctx context.Context, urls []string) []Found {
	if len(urls) == 0 {
		return nil
	}
	kinds := make([]string, len(urls))
	jobs := make(chan int)
	workers := min(len(urls), maxConcurrentDiscovery)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if kind, ok := Identify(ctx, urls[i]); ok {
					kinds[i] = kind
				}
			}
		}()
	}
	for i := range urls {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	// Stable: candidate order, not whichever goroutine finished first.
	var out []Found
	for i, u := range urls {
		if kinds[i] != "" {
			out = append(out, Found{Kind: kinds[i], URL: u})
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
			Models []json.RawMessage `json:"models"`
		}
		if strictjson.Unmarshal(body, &r) == nil && r.Models != nil {
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
		if strictjson.Unmarshal(body, &r) == nil && (r.BuildInfo != "" || r.ModelPath != "") {
			return "llama-server", true
		}
	}
	if body, ok := getJSON(ctx, base+"/v1/models"); ok {
		var r struct {
			Data []json.RawMessage `json:"data"`
		}
		if strictjson.Unmarshal(body, &r) == nil && r.Data != nil {
			return "openai", true
		}
	}
	return "", false
}

func getJSON(ctx context.Context, url string) ([]byte, bool) {
	cctx, cancel := context.WithTimeout(ctx, 600*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, false
	}
	resp, err := discoveryHTTP.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDiscoveryBody+1))
	return body, err == nil && len(body) <= maxDiscoveryBody
}

// Short-timeout client: discovery must fail fast on a closed port.
var discoveryHTTP = &http.Client{Timeout: 700 * time.Millisecond}
