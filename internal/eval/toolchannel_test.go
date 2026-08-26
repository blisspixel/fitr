package eval

import (
	"errors"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/ollama"
)

// A model with no tool support must not abandon the run.
//
// Ollama answers a tool request for such a model with a generic HTTP 400. Read
// as a transport fault that aborts and discards every completed measurement --
// which is exactly the bug that once cost three finished batteries. It is a
// capability fact, and it is reported as one.
func TestNoToolSupportIsNotATransportFault(t *testing.T) {
	declines := []string{
		`ollama 400: {"error":"registry.ollama.ai/library/llama2-uncensored:latest does not support tools"}`,
		"model does not support tool calling",
		"Tools are not supported by this model",
		"tool use is not supported",
	}
	for _, msg := range declines {
		if !declinesTools(errors.New(msg)) {
			t.Errorf("not recognised as a capability fact: %q", msg)
		}
	}
	// A real fault must stay a real fault. Misclassifying here would silently
	// convert a broken runtime into a clean "no tool support" verdict.
	faults := []string{
		"connection refused",
		"context deadline exceeded",
		"ollama 500: internal error",
		"unexpected EOF reading tool call",
		"tool call parse error: unexpected token",
	}
	for _, msg := range faults {
		if declinesTools(errors.New(msg)) {
			t.Errorf("a genuine fault was excused as missing tool support: %q", msg)
		}
	}
	if declinesTools(nil) {
		t.Error("nil error must not read as a capability fact")
	}
}

// The whole point of the in-channel family: a correct call that arrived as text
// is a different problem from a model that cannot call tools, and only one of
// them is fixed by choosing a different model.
func TestProseChannelIsDistinguishedFromSilence(t *testing.T) {
	inst := Generate(CheckSpec{
		ID: "t", Kind: "check", Need: "tool_calling", Family: "tool_call", NumPredict: 300,
	}, 42)
	if !inst.UsesToolChannel() {
		t.Fatal("tool_call must run through the tool channel")
	}
	name, args, _ := strings.Cut(inst.Canon, " ")

	wrappers := []string{
		"<tool_call>{\"name\":\"" + name + "\",\"arguments\":" + args + "}</tool_call>",
		"<|tool_call|>[{\"name\":\"" + name + "\"}]",
		"<function=" + name + ">{}</function>",
		"[TOOL_CALLS] " + name,
		"Sure! {\"name\": \"" + name + "\", \"arguments\": {}}",
	}
	for _, w := range wrappers {
		ok, why := inst.GradeCall(ollama.Message{Role: "assistant", Content: w})
		if ok {
			t.Errorf("accepted a call emitted as text: %q", w)
		}
		if !strings.Contains(why, string(failProseChannel)) {
			t.Errorf("%q classified as %q, want prose_channel", w, why)
		}
	}
	// Plain silence is a different diagnosis and must not borrow the label.
	_, why := inst.GradeCall(ollama.Message{Role: "assistant", Content: "I'm not sure."})
	if !strings.Contains(why, string(failNoCall)) {
		t.Errorf("a chatty non-answer classified as %q, want no_call", why)
	}
}

// Every failure mode the grader can report must be reachable, or the taxonomy
// is decoration.
func TestToolCallFailureTaxonomyIsReachable(t *testing.T) {
	inst := Generate(CheckSpec{
		ID: "t", Kind: "check", Need: "tool_calling", Family: "tool_call_strict", NumPredict: 300,
	}, 7)
	name, args, _ := strings.Cut(inst.Canon, " ")
	call := func(n string, a string) ollama.Message {
		m := ollama.Message{Role: "assistant", ToolCalls: []ollama.ToolCall{{}}}
		m.ToolCalls[0].Function.Name = n
		m.ToolCalls[0].Function.Arguments = []byte(a)
		return m
	}
	cases := []struct {
		name string
		msg  ollama.Message
		want toolFailure
	}{
		{"wrong tool", call("cancel_meeting", args), failWrongName},
		{"not an object", call(name, `"just a string"`), failBadJSON},
		{"missing param", call(name, `{"title":"x"}`), failMissingParam},
		{"invented param", func() ollama.Message {
			return call(name, strings.TrimSuffix(args, "}")+`,"colour":"blue"}`)
		}(), failExtraParam},
	}
	for _, tc := range cases {
		ok, why := inst.GradeCall(tc.msg)
		if ok {
			t.Errorf("%s: graded as a pass", tc.name)
			continue
		}
		if !strings.Contains(why, string(tc.want)) {
			t.Errorf("%s: got %q, want %q", tc.name, why, tc.want)
		}
	}
	two := ollama.Message{Role: "assistant", ToolCalls: []ollama.ToolCall{{}, {}}}
	two.ToolCalls[0].Function.Name, two.ToolCalls[1].Function.Name = name, name
	if ok, why := inst.GradeCall(two); ok || !strings.Contains(why, string(failExtraCalls)) {
		t.Errorf("two calls where one was required: ok=%v %q", ok, why)
	}
}

// A fan-out trial must not be passable by always calling the first tool.
func TestFanoutShufflesTheTargetTool(t *testing.T) {
	cs := CheckSpec{ID: "t", Kind: "check", Need: "tool_calling", Family: "tool_fanout",
		Params: map[string]any{"distractors": 11}, NumPredict: 300}
	positions := map[int]bool{}
	for seed := range uint64(24) {
		inst := Generate(cs, seed)
		if len(inst.Tools) != 12 {
			t.Fatalf("seed %d: %d tools, want 12", seed, len(inst.Tools))
		}
		for i, tool := range inst.Tools {
			if tool.Function.Name == "schedule_meeting" {
				positions[i] = true
			}
		}
	}
	if len(positions) < 4 {
		t.Errorf("target tool appeared in only %d distinct positions across 24 seeds; "+
			"a model that always calls the first tool would pass", len(positions))
	}
}
