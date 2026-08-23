package eval

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/strictjson"
)

// ---------------------------------------------------------------- refusal
type RefusalVerdict struct {
	Verdict string  `json:"verdict"` // answered | partial | refused | empty
	Outcome Outcome `json:"outcome,omitempty"`
	Chars   int     `json:"chars"`
	Text    string  `json:"text"`
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
	samp := ollama.Deterministic(spec.NumPredict, numCtx(ctx))
	// Stable order so runs are comparable and the display never reshuffles.
	keys := []string{"political", "fiction", "rewrite"}
	var extra []string
	for k := range spec.Prompts {
		found := false
		for _, kk := range keys {
			if kk == k {
				found = true
			}
		}
		if !found {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	keys = append(keys, extra...)
	for _, k := range keys {
		prompt, ok := spec.Prompts[k]
		if !ok {
			continue
		}
		text, _, err := c.Generate(ctx, model, prompt, samp)
		if err != nil {
			out = map[string]RefusalVerdict{}
			for _, failedKey := range keys {
				if _, exists := spec.Prompts[failedKey]; exists {
					out[failedKey] = RefusalVerdict{Verdict: "error", Outcome: OutcomeError}
				}
			}
			return out, 0, failure(FailureTransport, "refusal.generate", err)
		}
		v := ScoreRefusal(k, text, spec.RefusalMarkers)
		complied := v == "answered"
		out[k] = RefusalVerdict{Verdict: v, Outcome: outcomeFor(complied), Chars: len(text), Text: text}
		if !complied {
			refused++
		}
	}
	return out, refused, nil
}

// ---------------------------------------------------------------- plumbing
type Rung struct {
	Pass    bool    `json:"pass"`
	Outcome Outcome `json:"outcome,omitempty"`
	Detail  string  `json:"detail"`
}

type PlumbingResult struct {
	Model   string          `json:"model"`
	Rungs   map[string]Rung `json:"rungs"`
	Order   []string        `json:"order"`
	Verdict string          `json:"verdict"`
	Healthy bool            `json:"healthy"` // rungs 1-4; rung 5 is a model trait
	Outcome Outcome         `json:"outcome,omitempty"`
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
	fail := func(operation string, err error) (PlumbingResult, error) {
		r.Outcome = OutcomeError
		return r, failure(FailureTransport, operation, err)
	}
	add := func(id string, pass bool, detail string) {
		r.Rungs[id] = Rung{Pass: pass, Outcome: outcomeFor(pass), Detail: detail}
		r.Order = append(r.Order, id)
	}

	info, err := c.Show(ctx, model)
	if err != nil {
		return fail("plumbing.show", err)
	}
	hasTools := false
	for _, cp := range info.Capabilities {
		if cp == "tools" {
			hasTools = true
		}
	}
	add("1_capability", hasTools, fmt.Sprintf("capabilities=%v", info.Capabilities))
	if !hasTools {
		r.Outcome = OutcomeSkipped
		r.Verdict = "no tool support advertised - not a fair tools test"
		return r, nil
	}

	samp := ollama.Deterministic(300, numCtx(ctx))
	ask := "What is the temperature in Oslo right now?"
	for _, rg := range spec.Rungs {
		if rg.ID == "2_emits_tool_call" && rg.Prompt != "" {
			ask = rg.Prompt
		}
	}
	msg, _, err := c.Chat(ctx, model, []ollama.Message{{Role: "user", Content: ask}}, spec.Tools, samp)
	if err != nil {
		return fail("plumbing.emit", err)
	}
	emits := len(msg.ToolCalls) > 0
	detail := fmt.Sprintf("n=%d", len(msg.ToolCalls))
	if !emits {
		detail += " -> answered in prose: " + truncate(msg.Content, 90)
	}
	add("2_emits_tool_call", emits, detail)
	if !emits {
		r.Outcome = OutcomeInconclusive
		r.Verdict = "template/parser problem - the model answered in prose instead " +
			"of emitting a tool_calls array"
		return r, nil
	}

	args := map[string]any{}
	argsErr := strictjson.Unmarshal(msg.ToolCalls[0].Function.Arguments, &args)
	_, hasCity := args["city"]
	hasCity = hasCity && argsErr == nil
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
		{
			Role: "tool", ToolName: "get_weather", ToolCallID: msg.ToolCalls[0].ID,
			Content: result,
		},
	}
	final, _, err := c.Chat(ctx, model, msgs, spec.Tools, samp)
	if err != nil {
		return fail("plumbing.roundtrip", err)
	}
	rt := strings.Contains(final.Content, "-3") ||
		strings.Contains(strings.ToLower(final.Content), "minus")
	add("4_roundtrip", rt, truncate(final.Content, 90))

	// Rung 5 needs no ground truth and is the most common local failure.
	irr := "What is the capital of France?"
	for _, rg := range spec.Rungs {
		if rg.ID == "5_irrelevance" && rg.Prompt != "" {
			irr = rg.Prompt
		}
	}
	m3, _, err := c.Chat(ctx, model, []ollama.Message{{Role: "user", Content: irr}}, spec.Tools, samp)
	if err != nil {
		return fail("plumbing.irrelevance", err)
	}
	spurious := len(m3.ToolCalls)
	if spurious == 0 {
		add("5_irrelevance", true, "called nothing (correct)")
	} else {
		add("5_irrelevance", false, fmt.Sprintf("wrongly fired %d tool call(s)", spurious))
	}

	r.Healthy = r.Rungs["1_capability"].Pass && r.Rungs["2_emits_tool_call"].Pass &&
		r.Rungs["3_valid_args"].Pass && r.Rungs["4_roundtrip"].Pass
	r.Outcome = outcomeFor(r.Healthy)
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
	timer := time.NewTimer(1 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return r, ctx.Err()
	case <-timer.C:
	}

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
			r.PctOnGPU = int(100 * (float64(m.SizeVRAM) / float64(m.Size)))
		}
	}
	return r, nil
}

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }
