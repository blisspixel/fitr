package openaicompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/ollama"
)

func testClient(h http.HandlerFunc) (*Client, func()) {
	srv := httptest.NewServer(h)
	c := New()
	c.BaseURL = srv.URL
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
	for _, k := range []string{"temperature", "top_k", "seed", "repeat_penalty", "max_tokens"} {
		if _, ok := gotPayload[k]; !ok {
			t.Errorf("payload missing %s", k)
		}
	}
}

func TestGenerateLengthFinishIsTruncation(t *testing.T) {
	c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`data: {"choices":[{"text":"x","finish_reason":"length"}]}` + "\n"))
		w.Write([]byte(`data: {"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":64}}` + "\n"))
	})
	defer done()
	_, m, err := c.Generate(context.Background(), "m", "hi", ollama.Deterministic(64, 8192))
	if err != nil || !m.Truncated {
		t.Fatalf("finish_reason=length must set Truncated (err=%v)", err)
	}
}

func TestGenerateFallsBackToChatOn404(t *testing.T) {
	c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/completions" {
			http.Error(w, "not found", 404)
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Write([]byte(`data: {"choices":[{"delta":{"content":"Hi"},"finish_reason":"stop"}]}` + "\n"))
		w.Write([]byte(`data: {"choices":[],"usage":{"prompt_tokens":8,"completion_tokens":2}}` + "\n"))
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
		json.NewDecoder(r.Body).Decode(&gotPayload)
		w.Write([]byte(`data: {"choices":[{"text":"{}","finish_reason":"stop"}]}` + "\n"))
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
		w.Write([]byte(`{"data":[{"id":"qwen3-30b"},{"id":"phi4"}]}`))
	})
	defer done()
	tags, err := c.Tags(context.Background())
	if err != nil || len(tags) != 2 || tags[0].Name != "qwen3-30b" {
		t.Fatalf("tags = %+v, %v", tags, err)
	}
}

func TestVersionTriesVLLMEndpoint(t *testing.T) {
	c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			w.Write([]byte(`{"version":"0.9.2"}`))
			return
		}
		http.Error(w, "no", 404)
	})
	defer done()
	if v := c.Version(context.Background()); v != "openai-compat 0.9.2" {
		t.Fatalf("version = %q", v)
	}
}

func TestVersionFallsBackToGenericLabel(t *testing.T) {
	c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", 404)
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
