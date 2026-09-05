package eval

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/modelref"
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

// OrderedRefusalIDs returns the protocol order for refusal prompt IDs. The
// three built-in prompts retain their historical order; extension prompts are
// sorted so a map iteration can never change the sealed schedule.
func OrderedRefusalIDs(ids []string) []string {
	preferred := []string{"political", "fiction", "rewrite"}
	present := make(map[string]bool, len(ids))
	for _, id := range ids {
		present[id] = true
	}
	out := make([]string, 0, len(present))
	for _, id := range preferred {
		if present[id] {
			out = append(out, id)
			delete(present, id)
		}
	}
	extra := make([]string, 0, len(present))
	for id := range present {
		extra = append(extra, id)
	}
	sort.Strings(extra)
	return append(out, extra...)
}

// RefusalPromptIDs returns the exact prompt schedule RunRefusal will execute.
func RefusalPromptIDs(spec RefusalSpec) []string {
	ids := make([]string, 0, len(spec.Prompts))
	for id := range spec.Prompts {
		ids = append(ids, id)
	}
	return OrderedRefusalIDs(ids)
}

func RunRefusal(ctx context.Context, c llm.Backend, model string, spec RefusalSpec) (map[string]RefusalVerdict, int, error) {
	out := map[string]RefusalVerdict{}
	refused := 0
	samp := ollama.Deterministic(spec.NumPredict, numCtx(ctx))
	keys := RefusalPromptIDs(spec)
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
// Most "model cannot use tools" results are the chat template, the
// tool-call parser, the quant, or the context size -- not the weights. Skipping
// this once produced a published claim that a model "fails tool use" when it in
// fact emits valid calls and consumes results correctly; it simply fires them on
// irrelevant questions.
func RunPlumbing(ctx context.Context, c llm.Backend, model string, spec PlumbingSpec) (PlumbingResult, error) {
	r := PlumbingResult{Model: model, Rungs: map[string]Rung{}}
	ready, err := checkPlumbingCapability(ctx, c, model, &r)
	if !ready {
		return r, err
	}
	msg, ask, samp, ready, err := checkPlumbingEmission(ctx, c, model, spec, &r)
	if !ready {
		return r, err
	}
	checkPlumbingArgs(msg, &r)
	if err := checkPlumbingRoundtrip(ctx, c, model, spec, msg, ask, samp, &r); err != nil {
		return r, err
	}
	spurious, err := checkPlumbingIrrelevance(ctx, c, model, spec, samp, &r)
	if err != nil {
		return r, err
	}
	finishPlumbing(spurious, &r)
	return r, nil
}

func addPlumbingRung(r *PlumbingResult, id string, pass bool, detail string) {
	r.Rungs[id] = Rung{Pass: pass, Outcome: outcomeFor(pass), Detail: detail}
	r.Order = append(r.Order, id)
}

func plumbingTransportError(r *PlumbingResult, operation string, err error) error {
	r.Outcome = OutcomeError
	return failure(FailureTransport, operation, err)
}

func checkPlumbingCapability(ctx context.Context, c llm.Backend, model string, r *PlumbingResult) (bool, error) {
	info, err := c.Show(ctx, model)
	if info.IsRemote() {
		return false, plumbingTransportError(r, "plumbing.show", ollama.ErrRemoteExecution)
	}
	if err != nil {
		return false, plumbingTransportError(r, "plumbing.show", err)
	}
	hasTools := false
	for _, cp := range info.Capabilities {
		if cp == "tools" {
			hasTools = true
		}
	}
	addPlumbingRung(r, "1_capability", hasTools, fmt.Sprintf("capabilities=%v", info.Capabilities))
	if !hasTools {
		r.Outcome = OutcomeSkipped
		r.Verdict = "no tool support advertised - not a fair tools test"
		return false, nil
	}
	return true, nil
}

func checkPlumbingEmission(ctx context.Context, c llm.Backend, model string, spec PlumbingSpec,
	r *PlumbingResult) (ollama.Message, string, ollama.Sampling, bool, error) {
	samp := ollama.Deterministic(PlumbingOutputTokens, numCtx(ctx))
	ask := plumbingPrompt(spec, "2_emits_tool_call", "What is the temperature in Oslo right now?")
	msg, _, err := c.Chat(ctx, model, []ollama.Message{{Role: "user", Content: ask}}, spec.Tools, samp)
	if err != nil {
		return ollama.Message{}, "", samp, false, plumbingTransportError(r, "plumbing.emit", err)
	}
	emits := len(msg.ToolCalls) > 0
	detail := fmt.Sprintf("n=%d", len(msg.ToolCalls))
	if !emits {
		detail += " -> answered in prose: " + truncate(msg.Content, 90)
	}
	addPlumbingRung(r, "2_emits_tool_call", emits, detail)
	if !emits {
		r.Outcome = OutcomeInconclusive
		r.Verdict = "template/parser problem - the model answered in prose instead " +
			"of emitting a tool_calls array"
		return msg, ask, samp, false, nil
	}
	return msg, ask, samp, true, nil
}

func checkPlumbingArgs(msg ollama.Message, r *PlumbingResult) {
	args := map[string]any{}
	argsErr := strictjson.Unmarshal(msg.ToolCalls[0].Function.Arguments, &args)
	_, hasCity := args["city"]
	hasCity = hasCity && argsErr == nil
	addPlumbingRung(r, "3_valid_args", hasCity, fmt.Sprintf("%v", args))
}

func checkPlumbingRoundtrip(ctx context.Context, c llm.Backend, model string, spec PlumbingSpec,
	msg ollama.Message, ask string, samp ollama.Sampling, r *PlumbingResult) error {
	result := plumbingResult(spec, "4_roundtrip", "-3 degrees Celsius")
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
		return plumbingTransportError(r, "plumbing.roundtrip", err)
	}
	rt := strings.Contains(final.Content, "-3") ||
		strings.Contains(strings.ToLower(final.Content), "minus")
	addPlumbingRung(r, "4_roundtrip", rt, truncate(final.Content, 90))
	return nil
}

func checkPlumbingIrrelevance(ctx context.Context, c llm.Backend, model string, spec PlumbingSpec,
	samp ollama.Sampling, r *PlumbingResult) (int, error) {
	// Rung 5 needs no ground truth and is the most common local failure.
	irr := plumbingPrompt(spec, "5_irrelevance", "What is the capital of France?")
	m3, _, err := c.Chat(ctx, model, []ollama.Message{{Role: "user", Content: irr}}, spec.Tools, samp)
	if err != nil {
		return 0, plumbingTransportError(r, "plumbing.irrelevance", err)
	}
	spurious := len(m3.ToolCalls)
	if spurious == 0 {
		addPlumbingRung(r, "5_irrelevance", true, "called nothing (correct)")
	} else {
		addPlumbingRung(r, "5_irrelevance", false, fmt.Sprintf("wrongly fired %d tool call(s)", spurious))
	}
	return spurious, nil
}

func finishPlumbing(spurious int, r *PlumbingResult) {
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
}

func plumbingPrompt(spec PlumbingSpec, id, fallback string) string {
	for _, rung := range spec.Rungs {
		if rung.ID == id && rung.Prompt != "" {
			fallback = rung.Prompt
		}
	}
	return fallback
}

func plumbingResult(spec PlumbingSpec, id, fallback string) string {
	for _, rung := range spec.Rungs {
		if rung.ID == id && rung.Result != "" {
			fallback = rung.Result
		}
	}
	return fallback
}

// ---------------------------------------------------------------- memory
type MemoryResult struct {
	Outcome           Outcome `json:"outcome,omitempty"`
	UnavailableReason string  `json:"unavailable_reason,omitempty"`
	DiskGB            float64 `json:"disk_gb"`
	ResidentGB        float64 `json:"resident_gb_at_32k"`
	PctOnGPU          int     `json:"pct_on_gpu"`
	LoadS             float64 `json:"load_s"`
	RequestedCtx      int     `json:"requested_ctx,omitempty"`
	EffectiveCtx      *int    `json:"effective_ctx,omitempty"`
	ResidentBytes     int64   `json:"resident_bytes,omitempty"`
	AcceleratorBytes  int64   `json:"accelerator_bytes,omitempty"`
}

// VerifiedAllocation is one point-specific runtime allocation receipt. The
// runtime classifies AcceleratorBytes; no field claims an exclusive physical
// pool, layer count, or absence of host traffic on unified-memory systems.
type VerifiedAllocation struct {
	ResidentBytes    int64
	AcceleratorBytes int64
	RequestedCtx     int
	EffectiveCtx     int
}

// ValidateReceipt checks the point-specific facts added to new memory probes.
// A zero RequestedCtx identifies a legacy receipt and is validated by its
// historical evidence contract instead.
func (r MemoryResult) ValidateReceipt() error {
	if r.RequestedCtx == 0 {
		return nil
	}
	if err := r.validateRequestAndNumbers(); err != nil {
		return err
	}
	if err := r.validateOutcome(); err != nil {
		return err
	}
	return r.validateDerivedAllocation()
}

func (r MemoryResult) validateRequestAndNumbers() error {
	switch {
	case r.RequestedCtx < 0:
		return errors.New("memory probe requested context is negative")
	case r.EffectiveCtx != nil && *r.EffectiveCtx <= 0:
		return errors.New("memory probe effective context is not positive")
	case r.ResidentBytes < 0 || r.AcceleratorBytes < 0 || r.AcceleratorBytes > r.ResidentBytes:
		return errors.New("memory probe byte receipt is invalid")
	case r.ResidentGB < 0 || r.DiskGB < 0 || r.LoadS < 0 ||
		math.IsNaN(r.ResidentGB) || math.IsInf(r.ResidentGB, 0) ||
		math.IsNaN(r.DiskGB) || math.IsInf(r.DiskGB, 0) ||
		math.IsNaN(r.LoadS) || math.IsInf(r.LoadS, 0):
		return errors.New("memory probe contains an invalid numeric value")
	case r.ResidentGB > 0 && r.ResidentBytes == 0:
		return errors.New("memory probe resident allocation lacks exact bytes")
	case r.PctOnGPU < 0 || r.PctOnGPU > 100:
		return errors.New("memory probe accelerator percentage is invalid")
	}
	return nil
}

func (r MemoryResult) validateOutcome() error {
	switch r.Outcome {
	case OutcomePass:
		if r.ResidentBytes <= 0 {
			return errors.New("measured memory probe has no resident allocation")
		}
		if r.UnavailableReason != "" {
			return errors.New("measured memory probe also claims to be unavailable")
		}
	case OutcomeSkipped:
		if r.ResidentBytes != 0 || r.AcceleratorBytes != 0 || r.ResidentGB != 0 || r.PctOnGPU != 0 ||
			strings.TrimSpace(r.UnavailableReason) == "" {
			return errors.New("unavailable memory probe needs a reason and no allocation")
		}
	case "":
		return errors.New("memory probe outcome is missing")
	default:
		return fmt.Errorf("memory probe has unsupported outcome %q", r.Outcome)
	}
	return nil
}

func (r MemoryResult) validateDerivedAllocation() error {
	if r.ResidentBytes > 0 {
		if r.ResidentGB != round2(float64(r.ResidentBytes)/(1024*1024*1024)) {
			return errors.New("memory probe resident GiB does not match exact bytes")
		}
		want := int(100 * (float64(r.AcceleratorBytes) / float64(r.ResidentBytes)))
		if r.PctOnGPU != want {
			return errors.New("memory probe accelerator percentage does not match exact bytes")
		}
	}
	return nil
}

// VerifiedAt returns the observed resident allocation only when the runtime
// confirmed the same effective context. This is the presentation and scoring
// guard against labeling an arbitrary load as a 32K observation.
func (r MemoryResult) VerifiedAt(ctx int) (float64, bool) {
	_, ok := r.VerifiedAllocationAt(ctx)
	return r.ResidentGB, ok
}

// VerifiedAllocationAt returns resident and accelerator bytes only when the
// runtime confirmed the requested context as the effective context for the
// same allocation point.
func (r MemoryResult) VerifiedAllocationAt(ctx int) (VerifiedAllocation, bool) {
	if r.Outcome != OutcomePass || r.ResidentBytes <= 0 || r.RequestedCtx != ctx ||
		r.EffectiveCtx == nil || *r.EffectiveCtx != ctx {
		return VerifiedAllocation{}, false
	}
	if err := r.ValidateReceipt(); err != nil {
		return VerifiedAllocation{}, false
	}
	return VerifiedAllocation{
		ResidentBytes: r.ResidentBytes, AcceleratorBytes: r.AcceleratorBytes,
		RequestedCtx: r.RequestedCtx, EffectiveCtx: *r.EffectiveCtx,
	}, true
}

// RunMemory measures ACTUAL resident bytes at a real context size.
//
// `ollama list` reports weights only. A "17 GB" model is ~18.9 GB resident at
// 32K, and the KV cache grows with context -- that is the number that competes
// with everything else for your RAM.
func RunMemory(ctx context.Context, c llm.Backend, model string, numCtx int) (MemoryResult, error) {
	r := MemoryResult{
		Outcome: OutcomeSkipped, RequestedCtx: numCtx,
		UnavailableReason: "runtime did not report a resident allocation for the requested model",
	}
	// llama-server exposes context and timing receipts but no resident byte
	// allocation over HTTP. Its model is already loaded at process start, so a
	// fixed-32K generation cannot make that missing receipt appear and may ask
	// for more context than the launch-time allocation. Preserve the explicit
	// gap without issuing inference solely for an unavailable measurement.
	if c.Name() == "llama-server" {
		r.UnavailableReason = "llama-server does not report resident allocation bytes"
		return r, nil
	}
	diskGB, err := memoryDiskGB(ctx, c, model)
	if err != nil {
		r.Outcome = OutcomeError
		return r, err
	}
	r.DiskGB = diskGB
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

	start := time.Now()
	samp := ollama.Deterministic(MemoryProbeOutputTokens, numCtx)
	_, metrics, err := c.Generate(ctx, model, "Say OK.", samp)
	if err != nil {
		return r, err
	}
	r.LoadS = round2(memoryInferenceElapsed(start, metrics).Seconds())

	running, err := c.PS(ctx)
	if err != nil {
		return r, err
	}
	for _, m := range running {
		if m.Name != model {
			continue
		}
		r.ResidentGB = round2(float64(m.Size) / (1024 * 1024 * 1024))
		r.ResidentBytes = m.Size
		r.AcceleratorBytes = m.SizeVRAM
		if m.ContextLength > 0 {
			effective := m.ContextLength
			r.EffectiveCtx = &effective
		}
		if m.Size > 0 {
			r.PctOnGPU = int(100 * (float64(m.SizeVRAM) / float64(m.Size)))
			r.Outcome = OutcomePass
			r.UnavailableReason = ""
		}
	}
	return r, nil
}

func memoryInferenceElapsed(start time.Time, metrics ollama.Metrics) time.Duration {
	if metrics.InferenceElapsedKnown {
		// Owned clients measure after durable admission and before execution
		// validation. Those control-plane costs still count toward the deadline.
		return metrics.InferenceElapsed
	}
	return time.Since(start)
}

func memoryDiskGB(ctx context.Context, c llm.Backend, model string) (float64, error) {
	tags, err := c.Tags(ctx)
	if ollama.IsLocalityError(err) {
		return 0, err
	}
	if err != nil {
		return 0, nil //nolint:nilerr // ordinary unavailable weight metadata remains optional
	}
	var size float64
	for _, tag := range tags {
		if modelref.SameServed(tag.Name, model) && tag.IsRemote() {
			return 0, ollama.ErrRemoteExecution
		}
		if tag.Name == model {
			size = round2(float64(tag.Size) / (1024 * 1024 * 1024))
		}
	}
	return size, nil
}

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }
