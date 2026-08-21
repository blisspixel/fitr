package oai

import (
	"encoding/json"
	"testing"

	"github.com/blisspixel/fitr/internal/ollama"
)

func TestMessageRoundTripPreservesProtocolEvidence(t *testing.T) {
	call := ollama.ToolCall{ID: "call_1"}
	call.Function.Name = "lookup"
	call.Function.Arguments = json.RawMessage(`{"q":"x"}`)
	wire := FromMessages([]ollama.Message{
		{Role: "assistant", Content: "", Thinking: "checking", ToolCalls: []ollama.ToolCall{call}},
		{Role: "tool", ToolName: "lookup", ToolCallID: "call_1", Content: "found"},
	})
	if len(wire) != 2 || wire[0].ReasoningContent != "checking" || wire[1].ToolCallID != "call_1" {
		t.Fatalf("wire messages=%+v", wire)
	}
	if wire[1].Name != "" {
		t.Fatalf("tool result included nonstandard name=%q", wire[1].Name)
	}
	if wire[0].ToolCalls[0].Function.Arguments != `{"q":"x"}` {
		t.Fatalf("arguments=%q", wire[0].ToolCalls[0].Function.Arguments)
	}

	got := ToMessage(Message{Role: "assistant", Refusal: "cannot comply", ReasoningContent: "policy"})
	if got.Content != "cannot comply" || got.Thinking != "policy" {
		t.Fatalf("message=%+v", got)
	}
}

func TestMalformedToolArgumentsRemainMalformed(t *testing.T) {
	var call ToolCall
	call.ID = "call_bad"
	call.Type = "function"
	call.Function.Name = "lookup"
	call.Function.Arguments = `{"q":`
	got := ToMessage(Message{Role: "assistant", ToolCalls: []ToolCall{call}})
	if len(got.ToolCalls) != 1 {
		t.Fatalf("tool calls=%+v", got.ToolCalls)
	}
	var encoded string
	if err := json.Unmarshal(got.ToolCalls[0].Function.Arguments, &encoded); err != nil || encoded != `{"q":` {
		t.Fatalf("malformed arguments=%s error=%v", got.ToolCalls[0].Function.Arguments, err)
	}
}

func TestStrictChatPayloadOmitsRuntimeExtensions(t *testing.T) {
	payload := StrictChatPayload("m", []ollama.Message{{Role: "user", Content: "hi"}}, nil,
		ollama.Deterministic(32, 8192))
	for _, field := range []string{"model", "messages", "stream", "temperature", "seed", "max_tokens"} {
		if _, ok := payload[field]; !ok {
			t.Errorf("strict payload missing %q", field)
		}
	}
	for _, field := range []string{"top_k", "repeat_penalty"} {
		if _, ok := payload[field]; ok {
			t.Errorf("strict payload contains runtime extension %q", field)
		}
	}
}
