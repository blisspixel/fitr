package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/ollama"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestControlPlaneRequestsCarryDeadline(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > controlPlaneTimeout {
			t.Errorf("control-plane deadline = %v, %v", deadline, ok)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Request: req}, nil
	})}
	c, err := newAtWithHTTP("http://127.0.0.1:1234", CredentialsDisabled, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Tags(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func testClient(h http.HandlerFunc) (*Client, func()) {
	srv := httptest.NewServer(h)
	c, err := newAtWithHTTP(srv.URL, CredentialsDisabled, srv.Client())
	if err != nil {
		srv.Close()
		panic(err)
	}
	return c, srv.Close
}

func TestGenerateStreamsCompletionsWithUsage(t *testing.T) {
	var gotPayload map[string]any
	c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&gotPayload)
		time.Sleep(15 * time.Millisecond) // TTFT is wall-clock; instant replies round to zero
		w.Write([]byte(`data: {"choices":[{"text":"Hel","finish_reason":""}]}` + "\n\n"))
		w.Write([]byte(`data: {"choices":[{"text":"lo","finish_reason":"stop"}]}` + "\n\n"))
		w.Write([]byte(`data: {"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":50}}` + "\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	})
	defer done()

	text, m, err := c.Generate(context.Background(), "m", "hi", ollama.Deterministic(64, 8192))
	if err != nil {
		t.Fatal(err)
	}
	if text != "Hello" {
		t.Fatalf("text = %q", text)
	}
	if m.PromptTokens != 100 || m.EvalCount != 50 {
		t.Fatalf("usage: prompt=%d completion=%d", m.PromptTokens, m.EvalCount)
	}
	if m.TTFTSeconds <= 0 {
		t.Fatal("TTFT must be wall-clock measured")
	}
	if m.PrefillTPS <= 0 {
		t.Fatal("prefill must be derived from prompt tokens over TTFT")
	}
	if m.Truncated {
		t.Fatal("stop finish must not read as truncation")
	}
	if !m.ClientDerived {
		t.Fatal("OpenAI-compat timings are wall-clock; ClientDerived must be set so the scorecard can say so")
	}
	// Usage must be requested, or no counts arrive and every rate is zero.
	if _, ok := gotPayload["stream_options"]; !ok {
		t.Error("stream_options.include_usage must be requested")
	}
	for _, k := range []string{"temperature", "seed", "max_tokens"} {
		if _, ok := gotPayload[k]; !ok {
			t.Errorf("payload missing %s", k)
		}
	}
	for _, extension := range []string{"top_k", "repeat_penalty"} {
		if _, ok := gotPayload[extension]; ok {
			t.Errorf("strict OpenAI payload contains nonstandard %s", extension)
		}
	}
}

func TestGenerateLengthFinishIsTruncation(t *testing.T) {
	c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`data: {"choices":[{"text":"x","finish_reason":"length"}]}` + "\n\n"))
		w.Write([]byte(`data: {"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":64}}` + "\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	})
	defer done()
	_, m, err := c.Generate(context.Background(), "m", "hi", ollama.Deterministic(64, 8192))
	if err != nil || !m.Truncated {
		t.Fatalf("finish_reason=length must set Truncated (err=%v)", err)
	}
}

func TestRejectsNegativeTokenUsage(t *testing.T) {
	t.Run("stream", func(t *testing.T) {
		c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("data: {\"choices\":[{\"text\":\"x\",\"finish_reason\":\"stop\"}]}\n\n"))
			w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":-1,\"completion_tokens\":1}}\n\n"))
			w.Write([]byte("data: [DONE]\n\n"))
		})
		defer done()
		if _, _, err := c.Generate(context.Background(), "m", "hi", ollama.Deterministic(8, 8192)); err == nil || !strings.Contains(err.Error(), "negative token usage") {
			t.Fatalf("negative stream usage error = %v", err)
		}
	})
	t.Run("chat", func(t *testing.T) {
		c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],` +
				`"usage":{"prompt_tokens":1,"completion_tokens":-1}}`))
		})
		defer done()
		if _, _, err := c.Chat(context.Background(), "m", nil, nil, ollama.Deterministic(8, 8192)); err == nil || !strings.Contains(err.Error(), "negative token usage") {
			t.Fatalf("negative chat usage error = %v", err)
		}
	})
}

func TestChatRequiresExactlyOneChoice(t *testing.T) {
	for _, body := range []string{
		`{"choices":[]}`,
		`{"choices":[{"message":{"role":"assistant"}},{"message":{"role":"assistant"}}]}`,
	} {
		c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		})
		_, _, err := c.Chat(context.Background(), "m", nil, nil, ollama.Deterministic(8, 8192))
		done()
		if err == nil || !strings.Contains(err.Error(), "exactly one") {
			t.Fatalf("error = %v, want exact-choice rejection", err)
		}
	}
}

func TestGenerateFallsBackToChatOn404(t *testing.T) {
	c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/completions" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Write([]byte(`data: {"choices":[{"delta":{"content":"Hi"},"finish_reason":"stop"}]}` + "\n\n"))
		w.Write([]byte(`data: {"choices":[],"usage":{"prompt_tokens":8,"completion_tokens":2}}` + "\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	})
	defer done()
	text, m, err := c.Generate(context.Background(), "m", "hi", ollama.Deterministic(64, 8192))
	if err != nil {
		t.Fatal(err)
	}
	if text != "Hi" || m.EvalCount != 2 {
		t.Fatalf("chat fallback: text=%q eval=%d", text, m.EvalCount)
	}
}

func TestGenerateJSONFormatUsesResponseFormat(t *testing.T) {
	var gotPayload map[string]any
	c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("Format=json path = %s, want chat completions", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&gotPayload)
		w.Write([]byte(`data: {"choices":[{"delta":{"content":"{}"},"finish_reason":"stop"}]}` + "\n\n"))
		w.Write([]byte(`data: {"choices":[],"usage":{"prompt_tokens":8,"completion_tokens":2}}` + "\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	})
	defer done()
	s := ollama.Deterministic(64, 8192)
	s.Format = "json"
	if _, _, err := c.Generate(context.Background(), "m", "hi", s); err != nil {
		t.Fatal(err)
	}
	if _, ok := gotPayload["response_format"]; !ok {
		t.Fatal("Format=json must engage response_format - that path is what doctor probes")
	}
}

func TestChatSharesTheOpenAIMapping(t *testing.T) {
	c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"",` +
			`"reasoning_content":"thinking...",` +
			`"tool_calls":[{"id":"c1","type":"function",` +
			`"function":{"name":"f","arguments":"{\"a\":1}"}}]}}]}`))
	})
	defer done()
	msg, _, err := c.Chat(context.Background(), "m",
		[]ollama.Message{{Role: "user", Content: "x"}}, nil, ollama.Deterministic(64, 8192))
	if err != nil {
		t.Fatal(err)
	}
	if msg.Thinking != "thinking..." || len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "c1" {
		t.Fatalf("mapping: %+v", msg)
	}
	var args map[string]any
	if json.Unmarshal(msg.ToolCalls[0].Function.Arguments, &args) != nil || args["a"] != float64(1) {
		t.Fatalf("arguments not normalized: %s", msg.ToolCalls[0].Function.Arguments)
	}
}

func TestTagsListsModels(t *testing.T) {
	c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"qwen3-30b","digest":"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},{"id":"phi4"}]}`))
	})
	defer done()
	tags, err := c.Tags(context.Background())
	if err != nil || len(tags) != 2 || tags[0].Name != "qwen3-30b" || tags[0].ReportedDigest == "" || tags[0].Digest != "" {
		t.Fatalf("tags = %+v, %v", tags, err)
	}
}

func TestAuthenticationEnvironmentPrecedence(t *testing.T) {
	tests := []struct {
		name       string
		fitrKey    string
		openAIKey  string
		wantHeader string
	}{
		{"fitr key wins", " fitr-secret ", "openai-secret", "Bearer fitr-secret"},
		{"generic endpoint does not receive cloud key", "", " openai-secret ", ""},
		{"local endpoint needs no key", "", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FITR_OPENAI_API_KEY", tc.fitrKey)
			t.Setenv("OPENAI_API_KEY", tc.openAIKey)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("Authorization"); got != tc.wantHeader {
					t.Errorf("Authorization = %q, want %q", got, tc.wantHeader)
				}
				w.Write([]byte(`{"object":"list","data":[]}`))
			}))
			defer srv.Close()
			t.Setenv("FITR_OPENAI_URL", srv.URL)
			c := New()
			if _, err := c.Tags(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("official endpoint uses standard OpenAI variable", func(t *testing.T) {
		t.Setenv("FITR_OPENAI_URL", "https://api.openai.com")
		t.Setenv("FITR_OPENAI_API_KEY", "")
		t.Setenv("OPENAI_API_KEY", " official-secret ")
		if got := New().apiKey; got != "official-secret" {
			t.Fatalf("api key=%q", got)
		}
	})
}

func TestAuthenticationIsAppliedToEveryRequestShape(t *testing.T) {
	const token = "private-token"
	t.Setenv("FITR_OPENAI_API_KEY", token)
	t.Setenv("OPENAI_API_KEY", "")
	seen := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("%s Authorization = %q", r.URL.Path, got)
		}
		seen[r.Method+" "+r.URL.Path]++
		switch r.URL.Path {
		case "/v1/models":
			w.Write([]byte(`{"object":"list","data":[]}`))
		case "/version":
			w.Write([]byte(`{"version":"test"}`))
		case "/v1/completions":
			w.Write([]byte("data: {\"choices\":[{\"text\":\"ok\",\"finish_reason\":\"stop\"}]}\n\n"))
			w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\n"))
			w.Write([]byte("data: [DONE]\n\n"))
		case "/v1/chat/completions":
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
		}
	}))
	defer srv.Close()
	t.Setenv("FITR_OPENAI_URL", srv.URL)
	c := New()
	if !c.Reachable(context.Background()) {
		t.Fatal("authenticated models endpoint must be reachable")
	}
	if got := c.Version(context.Background()); got != "openai-compat test" {
		t.Fatalf("version = %q", got)
	}
	if _, err := c.Tags(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Generate(context.Background(), "m", "hi", ollama.Deterministic(8, 8192)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Chat(context.Background(), "m", []ollama.Message{{Role: "user", Content: "hi"}}, nil, ollama.Deterministic(8, 8192)); err != nil {
		t.Fatal(err)
	}
	for _, request := range []string{
		"GET /v1/models", "GET /version", "POST /v1/completions", "POST /v1/chat/completions",
	} {
		if seen[request] == 0 {
			t.Errorf("request %q was not exercised", request)
		}
	}
}

func TestInvalidAPIKeyCannotBecomeAHeader(t *testing.T) {
	requests := 0
	c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
		requests++
	})
	defer done()
	c.apiKey = "secret\ninjected"
	_, err := c.Tags(context.Background())
	if err == nil || requests != 0 {
		t.Fatalf("error=%v requests=%d, want rejection before transport", err, requests)
	}
	if strings.Contains(err.Error(), c.apiKey) {
		t.Fatal("invalid API key leaked through the error")
	}
}

func TestClientConstructionRejectsAPIKeyLineBreaks(t *testing.T) {
	t.Setenv("FITR_OPENAI_API_KEY", "secret\n")
	if _, err := NewAt(DefaultURL, CredentialsFromEnvironment); err == nil || !strings.Contains(err.Error(), "line break") {
		t.Fatalf("error = %v", err)
	}
}

func TestBearerCredentialRequiresHTTPSOrLoopback(t *testing.T) {
	t.Setenv("FITR_OPENAI_API_KEY", "secret")
	_, err := newAtWithHTTP("http://192.0.2.1", CredentialsFromEnvironment, http.DefaultClient)
	if err == nil || !strings.Contains(err.Error(), "non-loopback, non-HTTPS") {
		t.Fatalf("error=%v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("credential leaked through transport error")
	}
}

func TestModelListIdentityMatrix(t *testing.T) {
	const hexDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	tests := []struct {
		name       string
		body       string
		wantDigest string
		wantError  string
	}{
		{"canonical digest", `{"data":[{"id":"m","digest":"sha256:` + hexDigest + `"}]}`, "sha256:" + hexDigest, ""},
		{"bare digest normalizes", `{"data":[{"id":"m","digest":"` + strings.ToUpper(hexDigest) + `"}]}`, "sha256:" + hexDigest, ""},
		{"digest remains optional", `{"data":[{"id":"m"}]}`, "", ""},
		{"wrong digest length", `{"data":[{"id":"m","digest":"abc"}]}`, "", "64 SHA-256"},
		{"wrong digest algorithm", `{"data":[{"id":"m","digest":"md5:` + hexDigest + `"}]}`, "", "unsupported model digest"},
		{"digest whitespace", `{"data":[{"id":"m","digest":" ` + hexDigest + `"}]}`, "", "invalid digest"},
		{"blank id", `{"data":[{"id":""}]}`, "", "invalid model id"},
		{"duplicate id", `{"data":[{"id":"m"},{"id":"m"}]}`, "", "duplicate model id"},
		{"duplicate field", `{"data":[],"data":[]}`, "", "duplicate JSON object name"},
		{"missing data", `{"object":"list"}`, "", "missing the data array"},
		{"trailing JSON", `{"data":[]} {}`, "", "content after"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(tc.body))
			})
			defer done()
			models, err := c.Tags(context.Background())
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("models=%v error=%v, want error containing %q", models, err, tc.wantError)
				}
				return
			}
			if err != nil || len(models) != 1 || models[0].ReportedDigest != tc.wantDigest || models[0].Digest != "" {
				t.Fatalf("models=%+v error=%v, want digest %q", models, err, tc.wantDigest)
			}
		})
	}
}

func TestProtocolErrorMatrix(t *testing.T) {
	const token = "do-not-leak"
	tests := []struct {
		name       string
		path       string
		status     int
		body       string
		wantStatus int
		wantCode   string
		invoke     func(*Client) error
	}{
		{
			name: "models authentication", path: "/v1/models", status: http.StatusUnauthorized,
			body:       `{"error":{"message":"bad key do-not-leak","type":"invalid_request_error","code":"invalid_api_key"}}`,
			wantStatus: http.StatusUnauthorized, wantCode: "invalid_api_key",
			invoke: func(c *Client) error { _, err := c.Tags(context.Background()); return err },
		},
		{
			name: "chat rate limit", path: "/v1/chat/completions", status: http.StatusTooManyRequests,
			body:       `{"error":{"message":"slow down","type":"rate_limit_error","param":null,"code":"rate_limit_exceeded"}}`,
			wantStatus: http.StatusTooManyRequests, wantCode: "rate_limit_exceeded",
			invoke: func(c *Client) error {
				_, _, err := c.Chat(context.Background(), "m", nil, nil, ollama.Deterministic(8, 8192))
				return err
			},
		},
		{
			name: "chat error envelope on success status", path: "/v1/chat/completions", status: http.StatusOK,
			body:       `{"error":{"message":"backend rejected the request","type":"server_error","code":"backend_error"}}`,
			wantStatus: http.StatusOK, wantCode: "backend_error",
			invoke: func(c *Client) error {
				_, _, err := c.Chat(context.Background(), "m", nil, nil, ollama.Deterministic(8, 8192))
				return err
			},
		},
		{
			name: "completion plain server error", path: "/v1/completions", status: http.StatusInternalServerError,
			body: "upstream unavailable", wantStatus: http.StatusInternalServerError,
			invoke: func(c *Client) error {
				_, _, err := c.Generate(context.Background(), "m", "hi", ollama.Deterministic(8, 8192))
				return err
			},
		},
		{
			name: "stream error event", path: "/v1/completions", status: http.StatusOK,
			body:       "data: {\"type\":\"error\",\"code\":\"stream_failed\",\"message\":\"generation failed\"}\n\n",
			wantStatus: http.StatusOK, wantCode: "stream_failed",
			invoke: func(c *Client) error {
				_, _, err := c.Generate(context.Background(), "m", "hi", ollama.Deterministic(8, 8192))
				return err
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					t.Errorf("path=%s, want %s", r.URL.Path, tc.path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer "+token {
					t.Errorf("Authorization=%q", got)
				}
				w.Header().Set("X-Request-Id", "req_test")
				if tc.status == http.StatusTooManyRequests {
					w.Header().Set("Retry-After", "2")
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			})
			defer done()
			c.apiKey = token
			err := tc.invoke(c)
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error=%#v, want APIError", err)
			}
			if apiErr.StatusCode != tc.wantStatus || apiErr.Code != tc.wantCode || apiErr.RequestID != "req_test" {
				t.Fatalf("APIError=%+v", apiErr)
			}
			if tc.status == http.StatusTooManyRequests && apiErr.RetryAfter != "2" {
				t.Fatalf("RetryAfter=%q, want 2", apiErr.RetryAfter)
			}
			if strings.Contains(err.Error(), token) {
				t.Fatal("API token leaked through server error text")
			}
		})
	}
}

func TestErrorBodyIsBounded(t *testing.T) {
	c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(strings.Repeat("x", maxErrorBody+1024)))
	})
	defer done()
	_, err := c.Tags(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !apiErr.BodyTruncated {
		t.Fatalf("error=%#v, want truncated APIError", err)
	}
	if len(err.Error()) > maxErrorBody+512 {
		t.Fatalf("bounded error grew to %d bytes", len(err.Error()))
	}
}

func TestClientBindingAndCredentialRedirectPolicy(t *testing.T) {
	const token = "private-token"
	var firstRequests, secondRequests int
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondRequests++
		if r.Header.Get("Authorization") != "" {
			t.Error("credential reached a different origin")
		}
		w.Write([]byte(`{"data":[]}`))
	}))
	defer second.Close()

	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstRequests++
		http.Redirect(w, r, second.URL+"/v1/models", http.StatusFound)
	}))
	defer first.Close()
	t.Setenv("FITR_OPENAI_API_KEY", token)
	c, err := newAtWithHTTP(first.URL, CredentialsFromEnvironment, first.Client())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FITR_OPENAI_URL", second.URL)
	if _, err := c.Tags(context.Background()); err == nil || !strings.Contains(err.Error(), "different origin") {
		t.Fatalf("cross-origin redirect error = %v", err)
	}
	if firstRequests != 1 || secondRequests != 0 || c.URL() != first.URL {
		t.Fatalf("binding requests=%d/%d URL=%q", firstRequests, secondRequests, c.URL())
	}
}

func TestSameOriginRedirectPreservesLegitimateAuthenticatedFlow(t *testing.T) {
	const token = "private-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization = %q", got)
		}
		if r.URL.Path == "/v1/models" {
			http.Redirect(w, r, "/final", http.StatusTemporaryRedirect)
			return
		}
		w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()
	t.Setenv("FITR_OPENAI_API_KEY", token)
	c, err := newAtWithHTTP(srv.URL, CredentialsFromEnvironment, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Tags(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoveryClientCannotLoadCredentials(t *testing.T) {
	t.Setenv("FITR_OPENAI_API_KEY", "private-token")
	c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("discovery Authorization = %q", got)
		}
		w.Write([]byte(`{"data":[]}`))
	})
	defer done()
	if _, err := c.Tags(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestIndependentModelDigestPin(t *testing.T) {
	const digestA = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const digestB = "sha256:1123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	t.Run("missing pin", func(t *testing.T) {
		t.Setenv("FITR_OPENAI_MODEL_SHA256", "")
		c, err := NewAt(DefaultURL, CredentialsDisabled)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.VerifyModelDigest("m", digestA); err == nil || !strings.Contains(err.Error(), "requires FITR_OPENAI_MODEL_SHA256") {
			t.Fatalf("missing pin error = %v", err)
		}
	})
	t.Run("missing endpoint assertion", func(t *testing.T) {
		t.Setenv("FITR_OPENAI_MODEL_SHA256", digestA)
		c, err := NewAt(DefaultURL, CredentialsDisabled)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.VerifyModelDigest("m", ""); err == nil || !strings.Contains(err.Error(), "did not report") {
			t.Fatalf("missing assertion error = %v", err)
		}
	})
	t.Run("mismatch", func(t *testing.T) {
		t.Setenv("FITR_OPENAI_MODEL_SHA256", digestA)
		c, err := NewAt(DefaultURL, CredentialsDisabled)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.VerifyModelDigest("m", digestB); err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("mismatch error = %v", err)
		}
	})
	t.Run("matching independent receipts", func(t *testing.T) {
		t.Setenv("FITR_OPENAI_MODEL_SHA256", strings.TrimPrefix(digestA, "sha256:"))
		c, err := NewAt(DefaultURL, CredentialsDisabled)
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.VerifyModelDigest("m", digestA)
		if err != nil || got != digestA {
			t.Fatalf("digest = %q, %v", got, err)
		}
	})
}

func TestSuccessfulResponsesAndStreamsAreBounded(t *testing.T) {
	t.Run("JSON body", func(t *testing.T) {
		c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"data":[]}`))
			w.Write([]byte(strings.Repeat(" ", maxSuccessBody)))
		})
		defer done()
		if _, err := c.Tags(context.Background()); err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("oversized JSON error = %v", err)
		}
	})

	t.Run("SSE line", func(t *testing.T) {
		c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("data: " + strings.Repeat("x", maxSSELine) + "\n\n"))
		})
		defer done()
		if _, _, err := c.Generate(context.Background(), "m", "x", ollama.Deterministic(1, 1)); err == nil || !strings.Contains(err.Error(), "SSE") {
			t.Fatalf("oversized line error = %v", err)
		}
	})

	t.Run("SSE event", func(t *testing.T) {
		c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
			part := strings.Repeat("x", maxSSELine-1024)
			for range 3 {
				w.Write([]byte("data: " + part + "\n"))
			}
			w.Write([]byte("\n"))
		})
		defer done()
		if _, _, err := c.Generate(context.Background(), "m", "x", ollama.Deterministic(1, 1)); err == nil || !strings.Contains(err.Error(), "event exceeds") {
			t.Fatalf("oversized event error = %v", err)
		}
	})

	t.Run("assembled output", func(t *testing.T) {
		c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
			chunk := strings.Repeat("x", 512<<10)
			for range maxGeneratedOutput/(512<<10) + 1 {
				fmt.Fprintf(w, "data: {\"choices\":[{\"text\":%q}]}\n\n", chunk)
			}
		})
		defer done()
		if _, _, err := c.Generate(context.Background(), "m", "x", ollama.Deterministic(1, 1)); err == nil || !strings.Contains(err.Error(), "generated output exceeds") {
			t.Fatalf("oversized output error = %v", err)
		}
	})
}

func TestStreamingConformanceMatrix(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		chat      bool
		wantText  string
		wantError string
	}{
		{
			name: "comments no-space data and refusal",
			body: ": keepalive\n\ndata:{\"choices\":[{\"delta\":{\"refusal\":\"cannot comply\"},\"finish_reason\":\"stop\"}]}\n\n" +
				"data:{\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\ndata:[DONE]\n\n",
			chat: true, wantText: "cannot comply",
		},
		{name: "malformed chunk", body: "data: {nope}\n\n", wantError: "decode SSE chunk"},
		{name: "duplicate chunk field", body: "data: {\"choices\":[],\"choices\":[]}\n\n", wantError: "duplicate JSON object name"},
		{
			name: "interrupted before done",
			body: "data: {\"choices\":[{\"text\":\"partial\",\"finish_reason\":\"\"}]}\n\n" +
				"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1}}\n\n",
			wantText: "partial", wantError: "before [DONE]",
		},
		{
			name:     "done without requested usage",
			body:     "data: {\"choices\":[{\"text\":\"partial\",\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
			wantText: "partial", wantError: "without requested usage",
		},
		{
			name: "data after done",
			body: "data: {\"choices\":[{\"text\":\"ok\",\"finish_reason\":\"stop\"}]}\n\n" +
				"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1}}\n\n" +
				"data: [DONE]\n\ndata: {\"choices\":[{\"text\":\"spoof\"}]}\n\n",
			wantText: "ok", wantError: "after [DONE]",
		},
		{
			name: "done without finish reason",
			body: "data: {\"choices\":[{\"text\":\"partial\"}]}\n\n" +
				"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1}}\n\n" +
				"data: [DONE]\n\n",
			wantText: "partial", wantError: "without a finish reason",
		},
		{
			name: "conflicting usage receipts",
			body: "data: {\"choices\":[{\"text\":\"ok\",\"finish_reason\":\"stop\"}]}\n\n" +
				"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1}}\n\n" +
				"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\n" +
				"data: [DONE]\n\n",
			wantText: "ok", wantError: "conflicting usage",
		},
		{
			name:      "multiple choices",
			body:      "data: {\"choices\":[{\"text\":\"a\"},{\"text\":\"b\"}]}\n\n",
			wantError: "at most one",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Write([]byte(tc.body))
			})
			defer done()
			sampling := ollama.Deterministic(8, 8192)
			if tc.chat {
				sampling.Format = "json"
			}
			text, _, err := c.Generate(context.Background(), "m", "hi", sampling)
			if text != tc.wantText {
				t.Fatalf("text=%q, want %q", text, tc.wantText)
			}
			if tc.wantError == "" && err != nil {
				t.Fatal(err)
			}
			if tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("error=%v, want %q", err, tc.wantError)
			}
		})
	}
}

func TestVersionTriesVLLMEndpoint(t *testing.T) {
	c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			w.Write([]byte(`{"version":"9.9.9"}`))
			return
		}
		http.Error(w, "no", http.StatusNotFound)
	})
	defer done()
	if v := c.Version(context.Background()); v != "openai-compat 9.9.9" {
		t.Fatalf("version = %q", v)
	}
}

func TestVersionFallsBackToGenericLabel(t *testing.T) {
	c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusNotFound)
	})
	defer done()
	if v := c.Version(context.Background()); v != "openai-compat" {
		t.Fatalf("version = %q", v)
	}
}

func TestMemoryIsHonestlyUnknown(t *testing.T) {
	c := New()
	ps, err := c.PS(context.Background())
	if err != nil || ps != nil {
		t.Fatalf("PS must report nothing rather than invent: %v, %v", ps, err)
	}
	// Vision must NOT be claimed - an unverifiable positive would turn n/a
	// into a false PASS. Tools may be claimed optimistically because the
	// plumbing diagnostic verifies before any tools verdict is issued.
	mi, _ := c.Show(context.Background(), "m")
	for _, cap := range mi.Capabilities {
		if cap == "vision" {
			t.Fatal("vision must not be claimed without a way to verify it")
		}
	}
}
