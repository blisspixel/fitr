package llamaserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/ollama"
)

func testClient(h http.HandlerFunc) (*Client, func()) {
	srv := httptest.NewServer(h)
	c := New()
	c.BaseURL = srv.URL
	return c, srv.Close
}

func TestGenerateParsesSSEStreamAndTimings(t *testing.T) {
	var gotPayload map[string]any
	c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/completion" {
			t.Errorf("path = %s", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&gotPayload)
		w.Header().Set("Content-Type", "text/event-stream")
		// Real llama-server SSE framing: data-prefixed chunks, final carries timings.
		w.Write([]byte(`data: {"content":"Hel","stop":false}` + "\n\n"))
		w.Write([]byte(`data: {"content":"lo","stop":false}` + "\n\n"))
		w.Write([]byte(`data: {"content":"","stop":true,"stop_type":"eos","tokens_cached":12,` +
			`"timings":{"prompt_n":100,"prompt_ms":500.0,"predicted_n":50,"predicted_ms":2000.0}}` + "\n\n"))
	})
	defer done()

	text, m, err := c.Generate(context.Background(), "m", "hi", ollama.Deterministic(64, 8192))
	if err != nil {
		t.Fatal(err)
	}
	if text != "Hello" {
		t.Fatalf("text = %q", text)
	}
	if m.PrefillTPS != 200 || m.DecodeTPS != 25 {
		t.Fatalf("tps: prefill=%v decode=%v, want 200/25", m.PrefillTPS, m.DecodeTPS)
	}
	if m.PromptTokens != 100 || m.CachedTokens != 12 {
		t.Fatalf("prompt=%d cached=%d", m.PromptTokens, m.CachedTokens)
	}
	if m.TTFTSeconds <= 0 {
		t.Fatal("TTFT must be wall-clock measured, not zero")
	}
	if m.Truncated {
		t.Fatal("eos stop must not read as truncation")
	}
	// The sampling knobs that decide reproducibility must actually be sent.
	for _, k := range []string{"temperature", "top_k", "seed", "repeat_penalty", "n_predict"} {
		if _, ok := gotPayload[k]; !ok {
			t.Errorf("payload missing %s", k)
		}
	}
	if _, ok := gotPayload["json_schema"]; ok {
		t.Error("plain generation must not be grammar-constrained")
	}
}

func TestGenerateLimitStopIsTruncation(t *testing.T) {
	c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`data: {"content":"x","stop":true,"stop_type":"limit",` +
			`"timings":{"prompt_n":10,"prompt_ms":10,"predicted_n":64,"predicted_ms":100}}` + "\n"))
	})
	defer done()
	_, m, err := c.Generate(context.Background(), "m", "hi", ollama.Deterministic(64, 8192))
	if err != nil || !m.Truncated {
		t.Fatalf("stop_type=limit must set Truncated (err=%v, m=%+v)", err, m)
	}
}

func TestGenerateJSONFormatSendsSchema(t *testing.T) {
	var gotPayload map[string]any
	c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotPayload)
		w.Write([]byte(`data: {"content":"{}","stop":true,"stop_type":"eos"}` + "\n"))
	})
	defer done()
	s := ollama.Deterministic(64, 8192)
	s.Format = "json"
	if _, _, err := c.Generate(context.Background(), "m", "hi", s); err != nil {
		t.Fatal(err)
	}
	if _, ok := gotPayload["json_schema"]; !ok {
		t.Fatal("Format=json must engage the grammar-constrained path - that path is what doctor probes")
	}
}

func TestChatNormalizesOpenAIToolCalls(t *testing.T) {
	var gotReq map[string]any
	c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&gotReq)
		// OpenAI wire format: arguments is a STRING containing JSON.
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"",` +
			`"reasoning_content":"let me check the weather",` +
			`"tool_calls":[{"id":"call_1","type":"function",` +
			`"function":{"name":"get_weather","arguments":"{\"city\":\"Oslo\"}"}}]}}]}`))
	})
	defer done()

	msg, err := c.Chat(context.Background(), "m",
		[]ollama.Message{{Role: "user", Content: "weather?"}},
		[]ollama.Tool{{Type: "function"}}, ollama.Deterministic(300, 8192))
	if err != nil {
		t.Fatal(err)
	}
	if msg.Thinking != "let me check the weather" {
		t.Fatalf("reasoning_content must land in Thinking, got %q", msg.Thinking)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "call_1" {
		t.Fatalf("tool calls = %+v", msg.ToolCalls)
	}
	// String-encoded arguments must be normalized to object bytes so the
	// harness parses them the same way it parses Ollama's.
	var args map[string]any
	if err := json.Unmarshal(msg.ToolCalls[0].Function.Arguments, &args); err != nil {
		t.Fatalf("arguments not normalized: %v", err)
	}
	if args["city"] != "Oslo" {
		t.Fatalf("args = %v", args)
	}
}

func TestChatKeepsMalformedArgumentsMalformed(t *testing.T) {
	c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"",` +
			`"tool_calls":[{"id":"c1","type":"function",` +
			`"function":{"name":"f","arguments":"{not json"}}]}}]}`))
	})
	defer done()
	msg, err := c.Chat(context.Background(), "m", nil, nil, ollama.Deterministic(64, 8192))
	if err != nil {
		t.Fatal(err)
	}
	// A broken argument string must SURVIVE as a JSON string so the tool loop
	// counts it malformed - laundering it into {} would hide the model's bug.
	var asStr string
	if json.Unmarshal(msg.ToolCalls[0].Function.Arguments, &asStr) != nil {
		t.Fatalf("malformed args should round-trip as a string, got %s", msg.ToolCalls[0].Function.Arguments)
	}
	var asObj map[string]any
	if json.Unmarshal([]byte(asStr), &asObj) == nil {
		t.Fatal("the inner payload must still not parse - that is the point")
	}
}

func TestChatMapsToolResultsToOpenAIShape(t *testing.T) {
	var gotReq struct {
		Messages []map[string]any `json:"messages"`
	}
	c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotReq)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"-3 degrees"}}]}`))
	})
	defer done()

	call := ollama.ToolCall{ID: "call_9"}
	call.Function.Name = "get_weather"
	call.Function.Arguments = json.RawMessage(`{"city":"Oslo"}`)
	msgs := []ollama.Message{
		{Role: "user", Content: "weather?"},
		{Role: "assistant", Thinking: "checking", ToolCalls: []ollama.ToolCall{call}},
		{Role: "tool", ToolName: "get_weather", ToolCallID: "call_9", Content: "-3C"},
	}
	if _, err := c.Chat(context.Background(), "m", msgs, nil, ollama.Deterministic(64, 8192)); err != nil {
		t.Fatal(err)
	}
	if len(gotReq.Messages) != 3 {
		t.Fatalf("messages = %d", len(gotReq.Messages))
	}
	asst := gotReq.Messages[1]
	if asst["reasoning_content"] != "checking" {
		t.Fatal("Thinking must be sent back as reasoning_content - dropping it is the bug this exists to prevent")
	}
	tcs := asst["tool_calls"].([]any)
	fn := tcs[0].(map[string]any)["function"].(map[string]any)
	if _, isString := fn["arguments"].(string); !isString {
		t.Fatal("OpenAI wire format requires arguments as a string")
	}
	tool := gotReq.Messages[2]
	if tool["tool_call_id"] != "call_9" {
		t.Fatal("tool result must echo tool_call_id")
	}
}

func TestShowReadsCapabilitiesFromProps(t *testing.T) {
	c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"build_info":"b6142-abc123","model_path":"/models/qwen3.gguf",` +
			`"chat_template_caps":{"supports_tools":true},"modalities":{"vision":true}}`))
	})
	defer done()
	mi, err := c.Show(context.Background(), "label")
	if err != nil {
		t.Fatal(err)
	}
	has := func(cap string) bool {
		for _, x := range mi.Capabilities {
			if x == cap {
				return true
			}
		}
		return false
	}
	if !has("tools") || !has("vision") || !has("completion") {
		t.Fatalf("capabilities = %v", mi.Capabilities)
	}
	if v := c.Version(context.Background()); v != "llama-server b6142-abc123" {
		t.Fatalf("version = %q", v)
	}
}

func TestShowFallsBackToTemplateScan(t *testing.T) {
	c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"model_path":"m.gguf","chat_template":"{% for tool in tools %}...{% endfor %}"}`))
	})
	defer done()
	mi, _ := c.Show(context.Background(), "label")
	found := false
	for _, x := range mi.Capabilities {
		if x == "tools" {
			found = true
		}
	}
	if !found {
		t.Fatalf("template mentioning tools should report the capability, got %v", mi.Capabilities)
	}
}

func TestReachableUsesHealth(t *testing.T) {
	var path string
	c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Write([]byte(`{"status":"ok"}`))
	})
	defer done()
	if !c.Reachable(context.Background()) {
		t.Fatal("healthy server must be reachable")
	}
	if path != "/health" {
		t.Fatalf("probed %s, want /health", path)
	}
}

func TestStopAllIsANoOpByConstruction(t *testing.T) {
	c := New()
	left, err := c.StopAll(context.Background())
	if err != nil || left != nil {
		t.Fatalf("single-model server: StopAll = %v, %v", left, err)
	}
}

func TestModelNameFromPath(t *testing.T) {
	for path, want := range map[string]string{
		"/models/qwen3-coder-30b-Q4_K_M.gguf": "qwen3-coder-30b-Q4_K_M.gguf",
		`C:\models\phi4.gguf`:                 "phi4.gguf",
		"bare.gguf":                           "bare.gguf",
	} {
		if got := modelName(props{ModelPath: path}); got != want {
			t.Errorf("modelName(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestTagsListsTheSingleServedModel(t *testing.T) {
	c, done := testClient(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"model_path":"/m/served.gguf"}`))
	})
	defer done()
	tags, err := c.Tags(context.Background())
	if err != nil || len(tags) != 1 || tags[0].Name != "served.gguf" {
		t.Fatalf("tags = %+v, %v", tags, err)
	}
	if !strings.Contains(strings.Join(tags[0].Capabilities, ","), "completion") {
		t.Fatalf("capabilities = %v", tags[0].Capabilities)
	}
}
