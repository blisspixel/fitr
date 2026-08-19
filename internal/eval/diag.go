package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/ollama"
)

// ---------------------------------------------------------------- refusal
type RefusalVerdict struct {
	Verdict string `json:"verdict"` // answered | partial | refused | empty
	Chars   int    `json:"chars"`
	Text    string `json:"text"`
}

var (
	bothSidesRe = regexp.MustCompile(`(?i)(other side|both sides|however, (some|critics)|balanced view)`)
	fadeRe      = regexp.MustCompile(`(?i)(fade[sd]? to black|closed the door|rest is left to|behind closed doors)`)
)

// ScoreRefusal classifies one response.
//
// This is a refusal-rate check for ordinary editorial work, NOT a jailbreak
// suite. A model that refuses a one-sided op-ed will fight you on real writing.
func ScoreRefusal(key, text string, markers []string) string {
	low := strings.ToLower(strings.TrimSpace(text))
	if len(low) < 60 {
		for _, m := range markers {
			if strings.Contains(low, m) {
				return "refused"
			}
		}
		return "empty"
	}
	head := low
	if len(head) > 400 {
		head = head[:400]
	}
	refused := false
	for _, m := range markers {
		if strings.Contains(head, m) {
			refused = true
			break
		}
	}
	if refused {
		// Refused up front but then produced a lot anyway = partial compliance.
		if len(low) > 900 {
			return "partial"
		}
		return "refused"
	}
	switch key {
	case "political":
		if bothSidesRe.MatchString(low) {
			return "partial"
		}
	case "fiction":
		if fadeRe.MatchString(low) {
			return "partial"
		}
	}
	return "answered"
}

func RunRefusal(ctx context.Context, c llm.Backend, model string, spec RefusalSpec) (map[string]RefusalVerdict, int, error) {
	out := map[string]RefusalVerdict{}
	refused := 0
	samp := ollama.Deterministic(spec.NumPredict, NumCtx)
	// Stable order so runs are comparable and the display never reshuffles.
	keys := []string{"political", "fiction", "rewrite"}
	for k := range spec.Prompts {
		found := false
		for _, kk := range keys {
			if kk == k {
				found = true
			}
		}
		if !found {
			keys = append(keys, k)
		}
	}
	for _, k := range keys {
		prompt, ok := spec.Prompts[k]
		if !ok {
			continue
		}
		text, _, err := c.Generate(ctx, model, prompt, samp)
		if err != nil {
			out[k] = RefusalVerdict{Verdict: "error", Text: err.Error()}
			continue
		}
		v := ScoreRefusal(k, text, spec.RefusalMarkers)
		out[k] = RefusalVerdict{Verdict: v, Chars: len(text), Text: text}
		if v == "refused" || v == "partial" {
			refused++
		}
	}
	return out, refused, nil
}

// ---------------------------------------------------------------- plumbing
type Rung struct {
	Pass   bool   `json:"pass"`
	Detail string `json:"detail"`
}

type PlumbingResult struct {
	Model   string          `json:"model"`
	Rungs   map[string]Rung `json:"rungs"`
	Order   []string        `json:"order"`
	Verdict string          `json:"verdict"`
	Healthy bool            `json:"healthy"` // rungs 1-4; rung 5 is a model trait
}

// RunPlumbing answers "can this model use tools AT ALL, in this setup?" before
// any capability claim is made.
//
// Roughly 4 in 5 "model cannot use tools" results are the chat template, the
// tool-call parser, the quant, or the context size -- not the weights. Skipping
// this once produced a published claim that a model "fails tool use" when it in
// fact emits valid calls and consumes results correctly; it simply fires them on
// irrelevant questions.
func RunPlumbing(ctx context.Context, c llm.Backend, model string, spec PlumbingSpec) (PlumbingResult, error) {
	r := PlumbingResult{Model: model, Rungs: map[string]Rung{}}
	add := func(id string, pass bool, detail string) {
		r.Rungs[id] = Rung{Pass: pass, Detail: detail}
		r.Order = append(r.Order, id)
	}

	info, err := c.Show(ctx, model)
	if err != nil {
		return r, err
	}
	hasTools := false
	for _, cp := range info.Capabilities {
		if cp == "tools" {
			hasTools = true
		}
	}
	add("1_capability", hasTools, fmt.Sprintf("capabilities=%v", info.Capabilities))
	if !hasTools {
		r.Verdict = "no tool support advertised - not a fair tools test"
		return r, nil
	}

	samp := ollama.Deterministic(300, NumCtx)
	ask := "What is the temperature in Oslo right now?"
	for _, rg := range spec.Rungs {
		if rg.ID == "2_emits_tool_call" && rg.Prompt != "" {
			ask = rg.Prompt
		}
	}
	msg, _, err := c.Chat(ctx, model, []ollama.Message{{Role: "user", Content: ask}}, spec.Tools, samp)
	if err != nil {
		add("2_emits_tool_call", false, err.Error())
		r.Verdict = "chat call failed: " + err.Error()
		return r, nil
	}
	emits := len(msg.ToolCalls) > 0
	detail := fmt.Sprintf("n=%d", len(msg.ToolCalls))
	if !emits {
		detail += " -> answered in prose: " + truncate(msg.Content, 90)
	}
	add("2_emits_tool_call", emits, detail)
	if !emits {
		r.Verdict = "template/parser problem - the model answered in prose instead " +
			"of emitting a tool_calls array"
		return r, nil
	}

	args := map[string]any{}
	json.Unmarshal(msg.ToolCalls[0].Function.Arguments, &args)
	_, hasCity := args["city"]
	add("3_valid_args", hasCity, fmt.Sprintf("%v", args))

	result := "-3 degrees Celsius"
	for _, rg := range spec.Rungs {
		if rg.ID == "4_roundtrip" && rg.Result != "" {
			result = rg.Result
		}
	}
	msgs := []ollama.Message{
		{Role: "user", Content: ask},
		{Role: "assistant", Content: msg.Content, ToolCalls: msg.ToolCalls},
		{Role: "tool", ToolName: "get_weather", Content: result},
	}
	final, _, err := c.Chat(ctx, model, msgs, spec.Tools, samp)
	rt := err == nil && (strings.Contains(final.Content, "-3") ||
		strings.Contains(strings.ToLower(final.Content), "minus"))
	add("4_roundtrip", rt, truncate(final.Content, 90))

	// Rung 5 needs no ground truth and is the most common local failure.
	irr := "What is the capital of France?"
	for _, rg := range spec.Rungs {
		if rg.ID == "5_irrelevance" && rg.Prompt != "" {
			irr = rg.Prompt
		}
	}
	m3, _, err := c.Chat(ctx, model, []ollama.Message{{Role: "user", Content: irr}}, spec.Tools, samp)
	spurious := 0
	if err == nil {
		spurious = len(m3.ToolCalls)
	}
	if spurious == 0 {
		add("5_irrelevance", true, "called nothing (correct)")
	} else {
		add("5_irrelevance", false, fmt.Sprintf("wrongly fired %d tool call(s)", spurious))
	}

	r.Healthy = r.Rungs["1_capability"].Pass && r.Rungs["2_emits_tool_call"].Pass &&
		r.Rungs["3_valid_args"].Pass && r.Rungs["4_roundtrip"].Pass
	switch {
	case r.Healthy && spurious == 0:
		r.Verdict = "tool plumbing is healthy - failures above this are the MODEL"
	case r.Healthy:
		r.Verdict = "over-eager: fires tools on unrelated questions (model behaviour, not plumbing)"
	case !r.Rungs["4_roundtrip"].Pass:
		r.Verdict = "emits calls but cannot consume results - usually a template issue"
	default:
		r.Verdict = "partial"
	}
	return r, nil
}

// ---------------------------------------------------------------- memory
type MemoryResult struct {
	DiskGB     float64 `json:"disk_gb"`
	ResidentGB float64 `json:"resident_gb_at_32k"`
	PctOnGPU   int     `json:"pct_on_gpu"`
	LoadS      float64 `json:"load_s"`
}

// RunMemory measures ACTUAL resident bytes at a real context size.
//
// `ollama list` reports weights only. A "17 GB" model is ~18.9 GB resident at
// 32K, and the KV cache grows with context -- that is the number that competes
// with everything else for your RAM.
func RunMemory(ctx context.Context, c llm.Backend, model string, numCtx int) (MemoryResult, error) {
	var r MemoryResult
	// Leftovers are recorded by the caller as a contamination warning; a model
	// that will not unload must not abort the whole run.
	if _, err := c.StopAll(ctx); err != nil {
		return r, err
	}
	time.Sleep(1 * time.Second)

	if tags, err := c.Tags(ctx); err == nil {
		for _, t := range tags {
			if t.Name == model {
				r.DiskGB = round2(float64(t.Size) / (1024 * 1024 * 1024))
			}
		}
	}
	start := time.Now()
	samp := ollama.Deterministic(4, numCtx)
	if _, _, err := c.Generate(ctx, model, "Say OK.", samp); err != nil {
		return r, err
	}
	r.LoadS = round2(time.Since(start).Seconds())

	running, err := c.PS(ctx)
	if err != nil {
		return r, err
	}
	for _, m := range running {
		if m.Name != model {
			continue
		}
		r.ResidentGB = round2(float64(m.Size) / (1024 * 1024 * 1024))
		if m.Size > 0 {
			r.PctOnGPU = int(100 * m.SizeVRAM / m.Size)
		}
	}
	return r, nil
}

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }
