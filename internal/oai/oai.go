// Package oai holds the OpenAI chat-completions wire mapping shared by every
// OpenAI-shaped backend (llama-server, LM Studio, vLLM, SGLang...).
//
// Two rules govern the mapping:
//
//  1. reasoning_content and tool_call_id ROUND-TRIP. Dropping either across
//     turns degrades the model and records the loss as the model's failure.
//  2. Malformed tool arguments stay malformed. OpenAI encodes arguments as a
//     string of JSON; a valid one is normalized to the object bytes the
//     harness parses, an invalid one survives as a string so the tool loop
//     COUNTS it instead of laundering it into an empty object.
package oai

import (
	"encoding/json"
	"strings"

	"github.com/blisspixel/fitr/internal/ollama"
)

type Message struct {
	Role             string     `json:"role"`
	Content          string     `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	Name             string     `json:"name,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
}

type ToolCall struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// FromMessages converts harness messages to the OpenAI wire shape.
func FromMessages(msgs []ollama.Message) []Message {
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		om := Message{
			Role: m.Role, Content: m.Content,
			ReasoningContent: m.Thinking,
			ToolCallID:       m.ToolCallID,
		}
		if m.Role == "tool" {
			om.Name = m.ToolName
		}
		for _, tc := range m.ToolCalls {
			otc := ToolCall{ID: tc.ID, Type: "function"}
			otc.Function.Name = tc.Function.Name
			// OpenAI arguments are a STRING containing JSON; ours may be the
			// object itself.
			var asStr string
			if json.Unmarshal(tc.Function.Arguments, &asStr) == nil {
				otc.Function.Arguments = asStr
			} else {
				otc.Function.Arguments = string(tc.Function.Arguments)
			}
			om.ToolCalls = append(om.ToolCalls, otc)
		}
		out = append(out, om)
	}
	return out
}

// ToMessage converts an OpenAI response message back to the harness shape.
func ToMessage(got Message) ollama.Message {
	out := ollama.Message{Role: got.Role, Content: got.Content, Thinking: got.ReasoningContent}
	for _, otc := range got.ToolCalls {
		var tc ollama.ToolCall
		tc.ID = otc.ID
		tc.Function.Name = otc.Function.Name
		raw := strings.TrimSpace(otc.Function.Arguments)
		if json.Valid([]byte(raw)) {
			tc.Function.Arguments = json.RawMessage(raw)
		} else {
			quoted, _ := json.Marshal(otc.Function.Arguments)
			tc.Function.Arguments = quoted
		}
		out.ToolCalls = append(out.ToolCalls, tc)
	}
	return out
}

// ChatPayload builds the /v1/chat/completions request body. top_k and
// repeat_penalty are not in the OpenAI spec but are accepted across the local
// ecosystem (llama-server, vLLM, LM Studio, SGLang) - and they are the knobs
// that pin reproducibility, so they are sent.
func ChatPayload(model string, msgs []ollama.Message, tools []ollama.Tool, s ollama.Sampling) map[string]any {
	payload := map[string]any{
		"model": model, "messages": FromMessages(msgs), "stream": false,
		"temperature": s.Temperature, "top_k": s.TopK, "seed": s.Seed,
		"repeat_penalty": s.RepeatPenalty, "max_tokens": s.NumPredict,
	}
	if len(tools) > 0 {
		payload["tools"] = tools
	}
	if s.Format == "json" {
		payload["response_format"] = map[string]string{"type": "json_object"}
	}
	return payload
}
