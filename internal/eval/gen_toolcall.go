package eval

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"regexp"
	"sort"
	"strings"

	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/strictjson"
)

// In-channel tool-call families.
//
// fitr already graded tool arguments, but as TEXT: `tool_args` asks the model
// to "return only the JSON arguments object" and grades the reply with a JSON
// parser. That measures a real skill and it is not this one. Handing a model a
// tool and seeing whether it calls it exercises the chat template, the
// runtime's tool-call parser, and the model's willingness to use the channel
// at all -- and the dominant reported local failure lives exactly there: a
// perfectly well-formed call arriving in `content` with a normal stop reason,
// which a text grader cannot distinguish from a correct answer.
//
// The verdict is binary, as every fitr trial is. The failure MODE rides along
// in the detail string because it is the difference between "buy a better
// model" and "your GGUF has no PARSER directive".

// toolFailure names how a tool-call trial failed. It is diagnostic, never a
// score: a trial is pass or fail, and the mode only explains which.
type toolFailure string

const (
	failNoCall       toolFailure = "no_call"
	failProseChannel toolFailure = "prose_channel"
	failWrongName    toolFailure = "wrong_name"
	failExtraCalls   toolFailure = "extra_calls"
	failBadJSON      toolFailure = "bad_json"
	failMissingParam toolFailure = "missing_param"
	failWrongType    toolFailure = "wrong_type"
	failExtraParam   toolFailure = "extra_param"
	failWrongValue   toolFailure = "wrong_value"
)

// proseCallRe spots a tool call that never entered the tool channel. These are
// the wrappers local runtimes emit when a chat template's trigger string is
// missing or the parser did not fire: llama.cpp's lazy grammar leaves the raw
// text, Ollama returns it as content, and the agent sees a chatty non-answer.
var proseCallRe = regexp.MustCompile(
	`<tool_call|</tool_call|<function|<\|tool_call|<\|python_tag|\[TOOL_CALLS\]|"name"\s*:\s*"`)

func genToolCall(params map[string]any, rng *rand.Rand) Instance {
	return toolCallInstance(rng, pInt(params, "distractors", 0), false)
}

// genToolCallStrict adds an enum, a bounded integer and an array so that a
// model can be well-formed and still wrong. A loose schema lets a model pass by
// emitting any object at all.
func genToolCallStrict(params map[string]any, rng *rand.Rand) Instance {
	return toolCallInstance(rng, pInt(params, "distractors", 0), true)
}

// genToolFanout presents the correct tool among plausible distractors. Agent
// harnesses ship 15-60 tools; selecting one of four is not that task, and the
// count is also where runtime grammar builders have been reported to break --
// which surfaces as a transport error and therefore as BLKD, not as a model
// failure.
func genToolFanout(params map[string]any, rng *rand.Rand) Instance {
	return toolCallInstance(rng, pInt(params, "distractors", 11), false)
}

// genToolIrrelevance hands over tools and asks something none of them can
// answer. Restraint at rest: the model must not call anything.
//
// It is a pooled generated family rather than the single plumbing rung it used
// to be, because one observation cannot carry an interval, and "fires tools on
// unrelated questions" is one of the most common local-model complaints.
func genToolIrrelevance(params map[string]any, rng *rand.Rand) Instance {
	tools, _ := buildToolSet(rng, pInt(params, "distractors", 3), false)
	subject := one(rng, []string{
		"a haiku about winter",
		"why the sky looks blue",
		"a two-sentence summary of what a hash table is",
		"three ideas for naming a cat",
		"the difference between a list and a tuple, briefly",
	})
	prompt := "Write " + subject + ". Answer directly in plain text."
	return Instance{
		Prompt: prompt,
		Canon:  "(no tool call)",
		Tools:  tools,
		GradeCall: func(msg ollama.Message) (bool, string) {
			if n := len(msg.ToolCalls); n > 0 {
				return false, fmt.Sprintf("called %s on an unrelated question (%d call(s))",
					toolNames(msg.ToolCalls), n)
			}
			if strings.TrimSpace(msg.Content) == "" {
				return false, "no tool call and no answer"
			}
			return true, "answered without calling a tool"
		},
	}
}

// toolCallInstance builds one "call exactly this tool with exactly these
// arguments" trial.
func toolCallInstance(rng *rand.Rand, distractors int, strict bool) Instance {
	tools, want := buildToolSet(rng, distractors, strict)
	target := tools[0]
	for _, t := range tools {
		if t.Function.Name == want.name {
			target = t
			break
		}
	}
	return Instance{
		Prompt: want.prompt,
		Canon:  want.canonJSON(),
		Tools:  tools,
		GradeCall: func(msg ollama.Message) (bool, string) {
			return gradeToolCall(msg, target.Function.Name, want)
		},
	}
}

// wanted is the computed correct call for one trial.
type wanted struct {
	name   string
	prompt string
	args   map[string]any
	// optional names a parameter the model may omit; supplying it is fine,
	// supplying it wrong is not.
	optional string
}

func (w wanted) canonJSON() string {
	b, _ := json.Marshal(w.args)
	return w.name + " " + string(b)
}

// gradeToolCall is the whole contract, in order of how informative the failure
// is. Order matters: "it answered in prose" and "it called the wrong tool" are
// different problems with different fixes, and reporting the first check that
// trips keeps the detail line honest about which one happened.
func gradeToolCall(msg ollama.Message, name string, want wanted) (bool, string) {
	switch {
	case len(msg.ToolCalls) == 0:
		// The most valuable distinction fitr can draw here. A model that emits
		// a syntactically fine call into `content` is not a model that cannot
		// call tools; it is a template or parser that did not fire.
		if proseCallRe.MatchString(msg.Content) {
			return false, string(failProseChannel) +
				": emitted a tool call as text, not through the tool channel" +
				" (chat template or tool-call parser, not the weights)"
		}
		return false, string(failNoCall) + ": no tool call and no call-shaped text"
	case len(msg.ToolCalls) > 1:
		return false, fmt.Sprintf("%s: made %d calls where one was required (%s)",
			failExtraCalls, len(msg.ToolCalls), toolNames(msg.ToolCalls))
	}

	call := msg.ToolCalls[0]
	if call.Function.Name != name {
		return false, fmt.Sprintf("%s: called %q, expected %q",
			failWrongName, call.Function.Name, name)
	}

	var got map[string]any
	if err := strictjson.Unmarshal(call.Function.Arguments, &got); err != nil {
		return false, fmt.Sprintf("%s: arguments are not a JSON object (%v)", failBadJSON, err)
	}

	for _, key := range sortedKeys(want.args) {
		gotVal, ok := got[key]
		if !ok {
			return false, fmt.Sprintf("%s: %q", failMissingParam, key)
		}
		if ok, detail := sameArg(want.args[key], gotVal); !ok {
			return false, fmt.Sprintf("%s: %q %s", failWrongValue, key, detail)
		}
	}
	for _, key := range sortedKeys(got) {
		if _, expected := want.args[key]; expected || key == want.optional {
			continue
		}
		return false, fmt.Sprintf("%s: %q was not in the schema", failExtraParam, key)
	}
	return true, "called " + name + " with the exact arguments"
}

// sameArg compares a computed expectation against what the model sent.
//
// JSON numbers decode as float64, so an integer expectation is compared
// numerically rather than by Go type: a model that sends 30 is not wrong
// because encoding/json produced a float. Everything else must match exactly,
// including element order in arrays, because the prompt states the order.
func sameArg(want, got any) (bool, string) {
	switch w := want.(type) {
	case string:
		g, ok := got.(string)
		if !ok {
			return false, fmt.Sprintf("should be a string, got %T", got)
		}
		if g != w {
			return false, fmt.Sprintf("= %q, want %q", g, w)
		}
	case int:
		g, ok := got.(float64)
		if !ok {
			return false, fmt.Sprintf("should be a number, got %T", got)
		}
		if g != float64(w) {
			return false, fmt.Sprintf("= %v, want %d", g, w)
		}
	case []string:
		g, ok := got.([]any)
		if !ok {
			return false, fmt.Sprintf("should be an array, got %T", got)
		}
		if len(g) != len(w) {
			return false, fmt.Sprintf("has %d items, want %d", len(g), len(w))
		}
		for i := range w {
			s, ok := g[i].(string)
			if !ok || s != w[i] {
				return false, fmt.Sprintf("item %d = %v, want %q", i, g[i], w[i])
			}
		}
	default:
		return false, fmt.Sprintf("unsupported expectation type %T", want)
	}
	return true, ""
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func toolNames(calls []ollama.ToolCall) string {
	names := make([]string, 0, len(calls))
	for _, c := range calls {
		names = append(names, c.Function.Name)
	}
	return strings.Join(names, ", ")
}
