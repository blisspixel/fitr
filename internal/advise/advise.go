// Package advise answers: does this model fit on this box, and if not, which
// flag to try?
//
// This is design rule 7: a verdict without a remedy is half an answer.
// Compatible / Low memory / Incompatible, with the flag and the resulting
// number on the negative tiers - the consumer-grade thing NIM ships and
// LM Studio / llmfit do not.
//
// Fit prefers an observed resident size (Ollama /api/ps) over the weights+KV
// estimate. Observation includes compute buffers; the estimate does not and
// says so. llama.cpp --fit dummy allocation is still the missing gold
// standard. When a required input is missing the verdict is SKIP, never a
// fabricated GB number. Decode-speed class tracks *active* parameters, so a
// 30B MoE (~3B active) is not treated as a 30B dense model.
package advise

import (
	"fmt"
	"io"
	"strings"
)

const GiB = 1024 * 1024 * 1024

const (
	Compatible   = "compatible"
	LowMemory    = "low_memory"
	Incompatible = "incompatible"
	Skip         = "skip"
)

// Input is everything Evaluate needs. Zero means unknown: Evaluate must SKIP
// rather than invent a substitute.
type Input struct {
	Model    string
	Quant    string
	WeightsB int64   // on-disk size; 0 unknown
	HaveGB   float64 // available GPU / unified memory; 0 unknown
	HaveSrc  string  // where HaveGB came from; empty if unmeasured
	Ctx      int     // requested context; 0 → architecture max
	// KVBytes is bytes per KV element. 0 means f16 (2) was assumed.
	KVBytes float64
	KVSrc   string // "f16 (assumed)" / "OLLAMA_KV_CACHE_TYPE=q8_0"
	Backend string // ollama | llama-server | openai | ""
	Arch    Arch
	Source  string // "GGUF metadata", "Ollama /api/show"
	// ResidentB is the server's own allocation (Ollama /api/ps size). Zero
	// means not observed. A running process is a measurement; file size is
	// not substituted for it.
	ResidentB   int64
	ResidentSrc string
	// FitB is llama-fit-params dummy allocation at Ctx. Zero means the
	// binary was not run. A projection from the allocator beats weights+KV
	// and loses to a live resident.
	FitB      int64
	FitSrc    string
	FitCannot bool // llama-fit-params could not stay inside device memory
	// LoadErr is set when --load was asked and the server refused. That is
	// a measurement (the box could not take the model), not a skip.
	LoadErr string
}

type Arch struct {
	Name       string
	Blocks     int
	Embed      int
	Heads      int
	KVHeads    int
	KeyLength  int // 0 → fall back to embed/heads and say so
	ValLength  int
	MaxCtx     int
	Experts    int
	ExpertUsed int
	FFN        int // expert FFN if MoE, else dense FFN
	Vocab      int
	Params     int64
}

// KVReady is whether the KV cache can be sized without guessing head dim
// from a name. A missing key_length falls back to embed/heads, which is
// correct for Llama and wrong for Qwen3 (128, not 64) - that fallback is
// disclosed, not hidden.
func (a Arch) KVReady() bool {
	return a.Blocks > 0 && a.KVHeads > 0 && a.headDimK() > 0 && a.headDimV() > 0
}

func (a Arch) headDimK() int {
	if a.KeyLength > 0 {
		return a.KeyLength
	}
	if a.Heads > 0 && a.Embed > 0 {
		return a.Embed / a.Heads
	}
	return 0
}

func (a Arch) headDimV() int {
	if a.ValLength > 0 {
		return a.ValLength
	}
	return a.headDimK()
}

func (a Arch) kvBytesPerToken(elem float64) float64 {
	if !a.KVReady() || elem <= 0 {
		return 0
	}
	embdK := a.KVHeads * a.headDimK()
	embdV := a.KVHeads * a.headDimV()
	return float64(a.Blocks) * float64(embdK+embdV) * elem
}

// ActiveParams is the decode-time parameter count. MoE loads every expert
// (weights) but decode only touches expert_used of them. ok is false when
// the split cannot be computed, so callers do not print total as if it were
// active.
func (a Arch) ActiveParams() (int64, bool) {
	if a.Experts <= 0 {
		if a.Params > 0 {
			return a.Params, true
		}
		return 0, false
	}
	if a.ExpertUsed <= 0 || a.FFN <= 0 || a.Embed <= 0 || a.Blocks <= 0 {
		return 0, false
	}
	// Gated FFN: gate, up, down.
	expertTotal := int64(a.Blocks) * 3 * int64(a.Embed) * int64(a.FFN) * int64(a.Experts)
	expertActive := int64(a.Blocks) * 3 * int64(a.Embed) * int64(a.FFN) * int64(a.ExpertUsed)
	if a.Params > 0 && a.Params > expertTotal {
		return (a.Params - expertTotal) + expertActive, true
	}
	// Reconstruct without a recorded total: attention + active FFN + embed.
	if a.Heads <= 0 || a.headDimK() <= 0 {
		return 0, false
	}
	q := int64(a.Embed) * int64(a.Heads*a.headDimK())
	k := int64(a.Embed) * int64(a.KVHeads*a.headDimK())
	v := int64(a.Embed) * int64(a.KVHeads*a.headDimV())
	o := int64(a.Heads*a.headDimK()) * int64(a.Embed)
	attn := int64(a.Blocks) * (q + k + v + o)
	embed := int64(a.Vocab) * int64(a.Embed)
	return attn + expertActive + embed, true
}

type Report struct {
	Tier          string   `json:"tier"`
	Model         string   `json:"model,omitempty"`
	Quant         string   `json:"quant,omitempty"`
	Why           string   `json:"why"`
	Remedy        string   `json:"remedy,omitempty"`
	Hint          string   `json:"hint,omitempty"`
	Flag          string   `json:"flag,omitempty"`
	FlagValue     int      `json:"flag_value,omitempty"`
	NeedGB        float64  `json:"need_gb,omitempty"`
	HaveGB        float64  `json:"have_gb,omitempty"`
	WeightsGB     float64  `json:"weights_gb,omitempty"`
	ObservedGB    float64  `json:"observed_gb,omitempty"`
	KVGB          float64  `json:"kv_gb,omitempty"`
	FitsGB        float64  `json:"fits_gb,omitempty"`
	Ctx           int      `json:"ctx,omitempty"`
	MaxCtx        int      `json:"max_ctx,omitempty"`
	TotalParamsB  float64  `json:"total_params_b,omitempty"`
	ActiveParamsB float64  `json:"active_params_b,omitempty"`
	ActiveKnown   bool     `json:"active_known,omitempty"`
	Experts       int      `json:"experts,omitempty"`
	ExpertUsed    int      `json:"expert_used,omitempty"`
	Gaps          []string `json:"gaps,omitempty"`
	Source        string   `json:"source,omitempty"`
	HaveSource    string   `json:"have_source,omitempty"`
}

func (r Report) ExitCode() int {
	switch r.Tier {
	case LowMemory, Incompatible:
		return 3
	default:
		return 0
	}
}

func (r Report) Label() string {
	switch r.Tier {
	case Skip:
		return "SKIP"
	case LowMemory:
		return "low memory"
	default:
		return r.Tier
	}
}

// ContextFlag is the request-level knob that actually reduces KV for the
// serving runtime. Unknown OpenAI-compat servers do not get a guessed flag.
func ContextFlag(backend string) string {
	switch strings.ToLower(backend) {
	case "llama-server", "llamaserver":
		return "--ctx-size"
	case "openai":
		return ""
	default:
		return "num_ctx"
	}
}

// KVElemBytes maps a cache dtype name onto bytes per element. Unknown
// names return ok=false so the caller assumes f16 and says so, rather
// than inventing a packing.
func KVElemBytes(dtype string) (float64, bool) {
	switch strings.ToLower(strings.TrimSpace(dtype)) {
	case "f16", "float16", "fp16", "bf16", "bfloat16":
		return 2, true
	case "f32", "float32", "fp32":
		return 4, true
	case "q8_0", "q8":
		return 1, true
	case "q4_0", "q4_1":
		return 0.5, true
	}
	return 0, false
}

func kvElemBytes(in Input) (float64, string, bool) {
	if in.KVBytes > 0 {
		src := in.KVSrc
		if src == "" {
			src = "supplied"
		}
		return in.KVBytes, src, false
	}
	return 2, "f16 (assumed; KV cache dtype not measured)", true
}

func Evaluate(in Input) Report {
	r := Report{
		Model:      in.Model,
		Quant:      in.Quant,
		Source:     in.Source,
		HaveSource: in.HaveSrc,
		MaxCtx:     in.Arch.MaxCtx,
		Experts:    in.Arch.Experts,
		ExpertUsed: in.Arch.ExpertUsed,
	}
	if in.Arch.Params > 0 {
		r.TotalParamsB = float64(in.Arch.Params) / 1e9
	}
	if n, ok := in.Arch.ActiveParams(); ok {
		r.ActiveKnown = true
		r.ActiveParamsB = float64(n) / 1e9
	} else if in.Arch.Experts > 0 {
		r.Gaps = append(r.Gaps, "MoE detected but active params not sized (need expert FFN length)")
	}

	if in.ResidentB > 0 {
		r.ObservedGB = round1(float64(in.ResidentB) / GiB)
	}

	if in.HaveGB <= 0 {
		r.Tier = Skip
		r.Why = "GPU memory was not measured"
		if r.ObservedGB > 0 {
			r.Why = fmt.Sprintf("GPU memory was not measured; model is resident at %s GB", trim1(r.ObservedGB))
		}
		r.Hint = "pass --vram-gb N, or run where nvidia-smi / unified memory / drm sysfs is readable"
		return r
	}
	r.HaveGB = round1(in.HaveGB)
	haveB := in.HaveGB * GiB

	if in.LoadErr != "" {
		r.Tier = Incompatible
		r.Why = "server refused the load: " + in.LoadErr
		r.Remedy = "try a smaller quant, or a shorter context (num_ctx=2048)"
		r.Source = "observed load failure"
		return r
	}

	if in.ResidentB > 0 {
		if float64(in.ResidentB) > haveB {
			// The process is running. Calling that Incompatible would
			// trust a budget reading over a live allocation.
			r.Tier = Skip
			r.Why = fmt.Sprintf("resident %s GB exceeds the %s GB budget reading; the process is running, so the budget is the suspect number",
				trim1(r.ObservedGB), trim1(r.HaveGB))
			r.Hint = "pass --vram-gb N if the card is larger than what fitr read"
			r.Source = in.ResidentSrc
			if r.Source == "" {
				r.Source = "observed resident"
			}
			return r
		}
		// Observed fit at the current load. A requested --ctx still needs
		// architecture to size; without one, this is the answer.
		if in.Ctx <= 0 || !in.Arch.KVReady() {
			r.Tier = Compatible
			r.NeedGB = r.ObservedGB
			r.Why = fmt.Sprintf("resident %s GB of %s GB available (measured)", trim1(r.ObservedGB), trim1(r.HaveGB))
			r.Source = in.ResidentSrc
			if r.Source == "" {
				r.Source = "observed resident"
			}
			r.Gaps = append(r.Gaps, "resident includes compute buffers (measured, not estimated)")
			if !in.Arch.KVReady() {
				r.Gaps = append(r.Gaps, "other context lengths not sized (no GGUF architecture)")
			}
			return r
		}
		r.Gaps = append(r.Gaps, "current resident "+trim1(r.ObservedGB)+" GB (measured)")
	}

	if in.FitB > 0 {
		r.NeedGB = round1(float64(in.FitB) / GiB)
		if in.FitSrc != "" {
			r.Source = in.FitSrc
		}
		r.Gaps = append(r.Gaps, "dummy allocation (includes compute buffers)")
		fits := float64(in.FitB) <= haveB && !in.FitCannot
		if fits {
			r.Tier = Compatible
			r.Why = fmt.Sprintf("dummy allocation %s GB of %s GB available", trim1(r.NeedGB), trim1(r.HaveGB))
			return r
		}
		r.Why = fmt.Sprintf("dummy allocation %s GB; %s GB available", trim1(r.NeedGB), trim1(r.HaveGB))
		if in.WeightsB <= 0 || !in.Arch.KVReady() {
			r.Tier = Incompatible
			r.Remedy = "try a smaller quant, or a shorter context"
			return r
		}
		// Allocator said this ctx does not fit; KV math still names a shorter window.
		elem, _, _ := kvElemBytes(in)
		perTok := in.Arch.kvBytesPerToken(elem)
		if perTok <= 0 {
			r.Tier = Incompatible
			r.Remedy = "try a smaller quant, or a shorter context"
			return r
		}
		fitCtx := int((haveB - float64(in.WeightsB)) / perTok)
		fitCtx = (fitCtx / 256) * 256
		if fitCtx < 512 {
			r.Tier = Incompatible
			r.Remedy = "try a smaller quant; even a 512-token window does not fit next to the weights"
			return r
		}
		fitB := float64(in.WeightsB) + perTok*float64(fitCtx)
		r.Tier = LowMemory
		r.Flag = ContextFlag(in.Backend)
		r.FlagValue = fitCtx
		r.FitsGB = round1(fitB / GiB)
		r.Remedy = remedyLine(r.Flag, fitCtx, r.FitsGB)
		return r
	}

	if in.WeightsB <= 0 {
		r.Tier = Skip
		r.Why = "model weights were not measured"
		r.Hint = "pass a .gguf path, or use Ollama so /api/show exposes size"
		return r
	}
	r.WeightsGB = round1(float64(in.WeightsB) / GiB)

	if float64(in.WeightsB) > haveB {
		r.Tier = Incompatible
		r.Why = fmt.Sprintf("weights alone are %s GB; %s GB available", trim1(r.WeightsGB), trim1(r.HaveGB))
		r.Remedy = "try a smaller quant, or CPU offload (decode then tracks RAM bandwidth; doctor flags partial GPU offload)"
		r.Gaps = append(r.Gaps, "compute buffers not included")
		return r
	}

	elem, kvSrc, assumed := kvElemBytes(in)
	if assumed {
		r.Gaps = append(r.Gaps, kvSrc)
	} else if kvSrc != "" {
		r.Gaps = append(r.Gaps, "KV dtype "+kvSrc)
	}

	if !in.Arch.KVReady() {
		r.Tier = Skip
		r.Why = fmt.Sprintf("weights fit (%s GB of %s GB) but the KV cache was not sized",
			trim1(r.WeightsGB), trim1(r.HaveGB))
		r.Hint = "pass a .gguf path so architecture metadata (layers, KV heads, head dim) is readable"
		r.Gaps = append(r.Gaps, "no GGUF architecture metadata")
		return r
	}
	if in.Arch.KeyLength == 0 {
		r.Gaps = append(r.Gaps, "attention head dim assumed embed/heads (key_length missing)")
	}

	ctx := in.Ctx
	if ctx <= 0 {
		ctx = in.Arch.MaxCtx
	}
	if ctx <= 0 {
		r.Tier = Skip
		r.Why = "no context length in metadata"
		r.Hint = "pass --ctx N"
		return r
	}
	r.Ctx = ctx
	perTok := in.Arch.kvBytesPerToken(elem)
	kvB := perTok * float64(ctx)
	r.KVGB = round1(kvB / GiB)
	needB := float64(in.WeightsB) + kvB
	r.NeedGB = round1(needB / GiB)
	r.Gaps = append(r.Gaps, "weights + KV only; compute buffers not included")

	flag := ContextFlag(in.Backend)
	if needB <= haveB {
		r.Tier = Compatible
		r.Why = fmt.Sprintf("%d ctx needs %s GB; %s GB available", ctx, trim1(r.NeedGB), trim1(r.HaveGB))
		return r
	}

	// Largest context that still fits, aligned down to 256. Below 512 is not
	// a useful chat window - treat as incompatible even though weights fit.
	maxTok := (haveB - float64(in.WeightsB)) / perTok
	fitCtx := int(maxTok)
	if fitCtx > ctx {
		fitCtx = ctx
	}
	fitCtx = (fitCtx / 256) * 256
	if fitCtx < 512 {
		r.Tier = Incompatible
		r.Why = fmt.Sprintf("%d ctx needs %s GB; %s GB available", ctx, trim1(r.NeedGB), trim1(r.HaveGB))
		r.Remedy = "try a smaller quant; even a 512-token window does not fit next to the weights"
		return r
	}
	fitB := float64(in.WeightsB) + perTok*float64(fitCtx)
	r.Tier = LowMemory
	r.Flag = flag
	r.FlagValue = fitCtx
	r.FitsGB = round1(fitB / GiB)
	r.Why = fmt.Sprintf("%d ctx needs %s GB; %s GB available", ctx, trim1(r.NeedGB), trim1(r.HaveGB))
	r.Remedy = remedyLine(flag, fitCtx, r.FitsGB)
	return r
}

func remedyLine(flag string, ctx int, fits float64) string {
	n := trim1(fits)
	if flag == "" {
		return fmt.Sprintf("try a shorter context (%d) -> fits in %s GB", ctx, n)
	}
	return fmt.Sprintf("try %s=%d -> fits in %s GB", flag, ctx, n)
}

func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }

func trim1(v float64) string {
	s := fmt.Sprintf("%.1f", v)
	return s
}

// Write prints the human report. SKIP never prints a 0.0 GB budget as if it
// were a measurement.
func Write(w io.Writer, r Report) {
	var bits []string
	if r.Model != "" {
		bits = append(bits, r.Model)
	}
	if r.Quant != "" {
		bits = append(bits, r.Quant)
	}
	if p := formatParams(r); p != "" {
		bits = append(bits, p)
	}
	if len(bits) > 0 {
		fmt.Fprintf(w, "  %s\n\n", strings.Join(bits, "  "))
	}
	fmt.Fprintf(w, "  [%-12s]  %s\n", r.Label(), r.Why)
	if r.Remedy != "" {
		fmt.Fprintf(w, "  try            %s\n", r.Remedy)
	}
	if r.Hint != "" {
		fmt.Fprintf(w, "  hint           %s\n", r.Hint)
	}
	for _, g := range r.Gaps {
		fmt.Fprintf(w, "  note           %s\n", g)
	}
	if r.Source != "" {
		fmt.Fprintf(w, "  source         %s\n", r.Source)
	}
	if r.HaveSource != "" && r.Tier != Skip {
		fmt.Fprintf(w, "  memory         %s GB (%s)\n", trim1(r.HaveGB), r.HaveSource)
	}
}

func formatParams(r Report) string {
	if r.TotalParamsB <= 0 && !r.ActiveKnown {
		return ""
	}
	if r.Experts > 0 && r.ActiveKnown {
		return fmt.Sprintf("%.1fB total, %.1fB active (MoE %d of %d)",
			r.TotalParamsB, r.ActiveParamsB, r.ExpertUsed, r.Experts)
	}
	if r.Experts > 0 {
		return fmt.Sprintf("%.1fB total (MoE; active params not sized)", r.TotalParamsB)
	}
	if r.TotalParamsB > 0 {
		return fmt.Sprintf("%.1fB", r.TotalParamsB)
	}
	return ""
}

// ApplyPlan is the copy-paste to persist a measured context. Mutates is
// always false: fitr prints these commands, it never restarts a server.
type ApplyPlan struct {
	Model   string   `json:"model,omitempty"`
	Ctx     int      `json:"ctx"`
	Backend string   `json:"backend,omitempty"`
	Mutates bool     `json:"mutates"`
	Note    string   `json:"note"`
	Steps   []string `json:"steps"`
}

// PlanApply names the commands that persist ctx on a serving runtime.
// Unknown or empty backend prints every known recipe rather than guessing.
func PlanApply(backend, model string, ctxN int) ApplyPlan {
	p := ApplyPlan{
		Model:   model,
		Ctx:     ctxN,
		Backend: strings.ToLower(backend),
		Mutates: false,
		Note:    "fitr does not restart or mutate the serving process",
	}
	switch p.Backend {
	case "ollama":
		p.Steps = applyOllama(model, ctxN)
	case "llama-server", "llamaserver":
		p.Backend = "llama-server"
		p.Steps = applyLlama(ctxN)
	case "openai":
		p.Steps = applyOpenAI(ctxN)
	default:
		p.Backend = ""
		p.Steps = append(p.Steps, "ollama:")
		p.Steps = append(p.Steps, applyOllama(model, ctxN)...)
		p.Steps = append(p.Steps, "llama-server:")
		p.Steps = append(p.Steps, applyLlama(ctxN)...)
		p.Steps = append(p.Steps, "openai-compat:")
		p.Steps = append(p.Steps, applyOpenAI(ctxN)...)
	}
	return p
}

func applyOllama(model string, ctxN int) []string {
	from := model
	if from == "" {
		from = "<model>"
	}
	tag := fmt.Sprintf("%s-ctx%d", from, ctxN)
	return []string{
		fmt.Sprintf("per-request: already what `fitr run --ctx %d` sends as options.num_ctx", ctxN),
		fmt.Sprintf("persist a derived tag: write a Modelfile with FROM %s and PARAMETER num_ctx %d", from, ctxN),
		fmt.Sprintf("then: ollama create %s -f Modelfile", tag),
	}
}

func applyLlama(ctxN int) []string {
	return []string{
		"KV is allocated at launch; a per-request n_ctx cannot grow it",
		fmt.Sprintf("restart with: llama-server -m <your.gguf> --ctx-size %d", ctxN),
	}
}

func applyOpenAI(ctxN int) []string {
	return []string{
		"context is launch-time on these servers; flag names differ",
		fmt.Sprintf("vLLM: --max-model-len %d", ctxN),
		fmt.Sprintf("SGLang: --context-length %d", ctxN),
		"LM Studio: set context length in the server UI",
	}
}

// WriteApply prints the human form. Never claims fitr ran the commands.
func WriteApply(w io.Writer, p ApplyPlan) {
	fmt.Fprintln(w, "  apply prints how to persist a measured context.")
	fmt.Fprintln(w, "  "+p.Note+".")
	fmt.Fprintln(w)
	if p.Model != "" {
		fmt.Fprintf(w, "  model          %s\n", p.Model)
	}
	fmt.Fprintf(w, "  ctx            %d\n", p.Ctx)
	if p.Backend != "" {
		fmt.Fprintf(w, "  runtime        %s\n", p.Backend)
	}
	fmt.Fprintln(w)
	for _, s := range p.Steps {
		if strings.HasSuffix(s, ":") && !strings.Contains(s, " ") {
			fmt.Fprintf(w, "  %s\n", s)
			continue
		}
		fmt.Fprintf(w, "    %s\n", s)
	}
}

// ArchFromKVs reads GGUF-style metadata keys, including the architecture
// prefix Ollama exposes as model_info. Missing fields stay zero; Evaluate
// SKIPs when they are required.
func ArchFromKVs(kvs map[string]any) Arch {
	if len(kvs) == 0 {
		return Arch{}
	}
	arch := asString(first(kvs, "general.architecture"))
	a := Arch{
		Name:   arch,
		Params: asInt64(first(kvs, "general.parameter_count")),
		Vocab:  asInt(first(kvs, "general.vocabulary_size", arch+".vocab_size")),
	}
	p := arch
	if p != "" {
		p += "."
	}
	a.Blocks = asInt(first(kvs, p+"block_count"))
	a.Embed = asInt(first(kvs, p+"embedding_length"))
	a.Heads = asInt(first(kvs, p+"attention.head_count"))
	a.KVHeads = asInt(first(kvs, p+"attention.head_count_kv"))
	if a.KVHeads == 0 {
		a.KVHeads = a.Heads
	}
	a.KeyLength = asInt(first(kvs, p+"attention.key_length"))
	a.ValLength = asInt(first(kvs, p+"attention.value_length"))
	a.MaxCtx = asInt(first(kvs, p+"context_length"))
	a.Experts = asInt(first(kvs, p+"expert_count"))
	a.ExpertUsed = asInt(first(kvs, p+"expert_used_count"))
	a.FFN = asInt(first(kvs, p+"expert_feed_forward_length", p+"feed_forward_length"))
	return a
}

func first(kvs map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := kvs[k]; ok && v != nil {
			return v
		}
	}
	return nil
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asInt(v any) int { return int(asInt64(v)) }

func asInt64(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case int64:
		return n
	case uint32:
		return int64(n)
	case uint64:
		if n > 1<<63-1 {
			return 0
		}
		return int64(n)
	case float64:
		return int64(n)
	case float32:
		return int64(n)
	case []any:
		if len(n) == 0 {
			return 0
		}
		return asInt64(n[0])
	case string:
		var x int64
		fmt.Sscanf(n, "%d", &x)
		return x
	}
	return 0
}
