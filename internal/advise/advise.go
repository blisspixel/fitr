// Package advise answers: does this model fit on this box, and if not, which
// flag to try?
//
// This is design rule 7: a verdict without a remedy is half an answer.
// Compatible / Low memory / Incompatible, with the flag and the resulting
// number on the negative tiers - the consumer-grade thing NIM ships and
// LM Studio / llmfit do not.
//
// Fit prefers an exact-context observed resident size over the weights+KV
// estimate. Observation includes runtime-managed allocation beyond modeled
// weights and KV; the estimate does not and says so. A fitter result remains a
// descriptive allocator projection until its effective context, placement,
// version, and resource domains are captured. When a required input is
// missing the verdict is SKIP, never a fabricated GB number. Decode-speed
// class tracks *active* parameters, so a 30B MoE (~3B active) is not treated
// as a 30B dense model.
package advise

import (
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/render"
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
	HaveGB   float64 // capacity input; HaveSrc defines whether it is a budget
	HaveSrc  string  // where HaveGB came from; empty if unmeasured
	// NVIDIAUnifiedMemory is true when device detection identified an NVIDIA
	// shared-memory SoC. It remains true when --vram-gb replaces HaveGB so a
	// device-only allocator projection cannot be mistaken for whole-pool use.
	NVIDIAUnifiedMemory bool
	// FreeGB is GPU memory not currently committed elsewhere. Display only:
	// it moves minute to minute and must never reach a comparability key.
	FreeGB float64
	Ctx    int // requested context; 0 → architecture max
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
	ResidentCtx int // nonzero only when fitr loaded the model at this exact context
	// FitB is the final device-memory projection printed by llama-fit-params.
	// Zero means the binary was not run. The current adapter does not seal the
	// fitter's effective context or placement, so this remains descriptive and
	// cannot establish a point-specific verdict.
	FitB      int64
	FitSrc    string
	FitCannot bool // llama-fit-params could not stay inside device memory
	// LoadErr is set when --load was asked and the server refused. That is
	// a measurement (the box could not take the model), not a skip.
	LoadErr string
	// Timings overlay decode/prefill from saved runs at an exact ctx. Empty
	// means no overlay; never invent speed from file size.
	Timings []SavedTiming
}

// SavedTiming is a measured decode/prefill at one request context.
type SavedTiming struct {
	Ctx        int
	DecodeTPS  float64
	PrefillTPS float64
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
	// Hybrid recurrent models need runtime state beyond a conventional KV
	// cache. Their metadata is preserved, but weights-plus-KV arithmetic is
	// not allowed to stand in for a measured allocation.
	Hybrid                bool
	FullAttentionInterval int
	RecurrentLayers       int
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
	// Accumulate in float64. The same product in int arithmetic wraps to a
	// negative on absurd dimensions, and a negative cost per token turns the
	// fit comparison inside out.
	embdK := float64(a.KVHeads) * float64(a.headDimK())
	embdV := float64(a.KVHeads) * float64(a.headDimV())
	per := float64(a.Blocks) * (embdK + embdV) * elem
	if math.IsNaN(per) || math.IsInf(per, 0) || per <= 0 {
		return 0
	}
	return per
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
	// Every product below is bounded by metadata from an untrusted file. An
	// unchecked chain wraps to a negative parameter count, which then prints
	// as a negative model size. "Cannot be computed" is already this
	// function's answer for that, so overflow returns it.
	// Gated FFN: gate, up, down.
	expertTotal, expertActive, ok := a.expertParams()
	if !ok {
		return 0, false
	}
	if a.Params > 0 && a.Params > expertTotal {
		rest, ok := addInt64(a.Params-expertTotal, expertActive)
		if !ok {
			return 0, false
		}
		return rest, true
	}
	return a.reconstructActiveParams(expertActive)
}

func (a Arch) expertParams() (int64, int64, bool) {
	expertTotal, totalOK := mulInt64(int64(a.Blocks), 3, int64(a.Embed), int64(a.FFN), int64(a.Experts))
	expertActive, activeOK := mulInt64(int64(a.Blocks), 3, int64(a.Embed), int64(a.FFN), int64(a.ExpertUsed))
	return expertTotal, expertActive, totalOK && activeOK
}

func (a Arch) reconstructActiveParams(expertActive int64) (int64, bool) {
	// Reconstruct without a recorded total: attention + active FFN + embed.
	if a.Heads <= 0 || a.headDimK() <= 0 {
		return 0, false
	}
	qHead, okq := mulInt64(int64(a.Heads), int64(a.headDimK()))
	kvHeadK, okk := mulInt64(int64(a.KVHeads), int64(a.headDimK()))
	kvHeadV, okv := mulInt64(int64(a.KVHeads), int64(a.headDimV()))
	if !okq || !okk || !okv {
		return 0, false
	}
	q, ok1 := mulInt64(int64(a.Embed), qHead)
	k, ok2 := mulInt64(int64(a.Embed), kvHeadK)
	v, ok3 := mulInt64(int64(a.Embed), kvHeadV)
	o, ok4 := mulInt64(qHead, int64(a.Embed))
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return 0, false
	}
	perBlock, ok := addInt64(q, k, v, o)
	if !ok {
		return 0, false
	}
	attn, ok := mulInt64(int64(a.Blocks), perBlock)
	if !ok {
		return 0, false
	}
	embed, ok := mulInt64(int64(a.Vocab), int64(a.Embed))
	if !ok {
		return 0, false
	}
	total, ok := addInt64(attn, expertActive, embed)
	if !ok {
		return 0, false
	}
	return total, true
}

// mulInt64 multiplies non-negative factors, reporting false rather than
// wrapping. A wrapped product is a negative size presented as a measurement.
func mulInt64(factors ...int64) (int64, bool) {
	out := int64(1)
	for _, f := range factors {
		if f < 0 {
			return 0, false
		}
		if f == 0 {
			return 0, true
		}
		if out > math.MaxInt64/f {
			return 0, false
		}
		out *= f
	}
	return out, true
}

// addInt64 sums non-negative terms, reporting false rather than wrapping.
func addInt64(terms ...int64) (int64, bool) {
	out := int64(0)
	for _, t := range terms {
		if t < 0 || out > math.MaxInt64-t {
			return 0, false
		}
		out += t
	}
	return out, true
}

// ReportSchema names the advise JSON contract. Every fitr JSON document
// carries one so a reader can tell what it is holding from the payload alone.
const ReportSchema = "fitr.advise.v1"

type Report struct {
	Schema        string    `json:"schema"`
	Tier          string    `json:"tier"`
	Model         string    `json:"model,omitempty"`
	Quant         string    `json:"quant,omitempty"`
	Why           string    `json:"why"`
	Remedy        string    `json:"remedy,omitempty"`
	KVRemedy      string    `json:"kv_remedy,omitempty"`
	Hint          string    `json:"hint,omitempty"`
	Flag          string    `json:"flag,omitempty"`
	FlagValue     int       `json:"flag_value,omitempty"`
	NeedGB        float64   `json:"need_gb,omitempty"`
	HaveGB        float64   `json:"have_gb,omitempty"`
	WeightsGB     float64   `json:"weights_gb,omitempty"`
	ObservedGB    float64   `json:"observed_gb,omitempty"`
	KVGB          float64   `json:"kv_gb,omitempty"`
	FitsGB        float64   `json:"fits_gb,omitempty"`
	Ctx           int       `json:"ctx,omitempty"`
	MaxCtx        int       `json:"max_ctx,omitempty"`
	TotalParamsB  float64   `json:"total_params_b,omitempty"`
	ActiveParamsB float64   `json:"active_params_b,omitempty"`
	ActiveKnown   bool      `json:"active_known,omitempty"`
	Experts       int       `json:"experts,omitempty"`
	ExpertUsed    int       `json:"expert_used,omitempty"`
	Gaps          []string  `json:"gaps,omitempty"`
	Source        string    `json:"source,omitempty"`
	HaveSource    string    `json:"have_source,omitempty"`
	FreeGB        float64   `json:"free_gb,omitempty"`
	Fit           *FitTable `json:"context_fit,omitempty"`
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

// KVCacheFlag names the knob that sets KV cache dtype for a backend. An empty
// string means the runtime does not expose one, and no dtype remedy is
// offered rather than naming a flag the user cannot set.
func KVCacheFlag(backend string) string {
	switch strings.ToLower(backend) {
	case "llama-server", "llamaserver":
		return "--cache-type-k/--cache-type-v"
	case "openai":
		return ""
	case "ollama", "":
		return "OLLAMA_KV_CACHE_TYPE"
	}
	return ""
}

// kvCacheRemedy offers a quantized KV cache when the cache, not the weights,
// is what overflowed. q8_0 halves bytes per element against f16, so it buys
// back context that dtype was spending -- the third remedy next to "shorter
// window" and "smaller quant". It is only offered when it actually beats the
// f16 outcome, and it always says that the dtype is part of the device
// fingerprint: evidence measured under a different cache dtype is not
// comparable, so this is a re-measure, not a free win.
//
// elem is the current bytes per KV element and fitCtx is the window that fits
// at that dtype. A zero return means no honest improvement to offer.
func kvCacheRemedy(in Input, elem float64, ctx, fitCtx int, haveB float64) string {
	const q8Elem = 1.0
	flag := KVCacheFlag(in.Backend)
	if flag == "" || elem <= q8Elem || in.WeightsB <= 0 {
		return ""
	}
	// Ctx 0 means "the model's max". The main path resolves that before
	// sizing the cache and the --fit path does not, so resolve here: a
	// remedy is offered for a real window or not at all.
	if ctx <= 0 {
		ctx = in.Arch.MaxCtx
	}
	if ctx <= 0 {
		return ""
	}
	perTok := in.Arch.kvBytesPerToken(q8Elem)
	if perTok <= 0 {
		return ""
	}
	room := haveB - float64(in.WeightsB)
	if room <= 0 {
		return ""
	}
	// Does the window the user actually asked for fit once the cache shrinks?
	if float64(ctx)*perTok <= room {
		fits := (float64(in.WeightsB) + float64(ctx)*perTok) / GiB
		return fmt.Sprintf("%s=q8_0 keeps %d ctx -> fits in %s GB; changes the fingerprint, so re-measure",
			flag, ctx, trim1(round1(fits)))
	}
	q8Ctx := (ctxTokens(room/perTok) / 256) * 256
	if q8Ctx < 512 || q8Ctx <= fitCtx {
		return ""
	}
	fits := (float64(in.WeightsB) + float64(q8Ctx)*perTok) / GiB
	return fmt.Sprintf("%s=q8_0 raises the window to %d -> fits in %s GB; changes the fingerprint, so re-measure",
		flag, q8Ctx, trim1(round1(fits)))
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
	r := evaluateCore(in)
	r.Schema = ReportSchema
	r.FreeGB = in.FreeGB
	r.Fit = ContextFit(in)
	return r
}

func evaluateCore(in Input) Report {
	r := newCoreReport(in)
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
	if evaluateResident(in, haveB, &r) {
		return r
	}
	if in.FitB > 0 {
		return evaluateFit(in, r)
	}
	if automaticSharedMemoryCapacity(in) {
		return evaluateAutomaticSharedMemoryCapacity(in, haveB, r)
	}
	if in.Arch.Hybrid {
		r.Tier = Skip
		r.Why = "hybrid recurrent architecture cannot be safely projected from weights plus a conventional KV cache"
		r.Hint = "use --load at the requested context with Ollama"
		r.Gaps = append(r.Gaps, "recurrent state and other runtime allocation were not measured")
		return r
	}
	return evaluateWeightsAndKV(in, haveB, r)
}

func newCoreReport(in Input) Report {
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
	if in.WeightsB > 0 {
		r.WeightsGB = round1(float64(in.WeightsB) / GiB)
	}
	if automaticSharedMemoryCapacity(in) && in.FreeGB <= 0 {
		r.Gaps = append(r.Gaps,
			"shared pool includes the operating system and other processes; whole-pool available memory was not measured")
	}
	return r
}

func evaluateResident(in Input, haveB float64, r *Report) bool {
	if in.ResidentB <= 0 {
		return false
	}
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
		return true
	}
	if sharedMemoryPool(in) {
		return evaluateSharedMemoryResident(in, r)
	}
	if requested := requestedResidentContext(in); requested > 0 {
		if in.ResidentCtx != requested {
			r.Gaps = append(r.Gaps,
				"resident allocation was not observed at the requested context, so it does not prove that point")
			return false
		}
		r.Tier = Compatible
		r.NeedGB = r.ObservedGB
		r.Ctx = requested
		r.Why = fmt.Sprintf("resident %s GB of %s GB available at the requested %d context (measured)",
			trim1(r.ObservedGB), trim1(r.HaveGB), requested)
		r.Source = residentSource(in)
		r.Gaps = append(r.Gaps,
			"resident is total runtime allocation; its non-weight, non-KV remainder is derived")
		if in.Arch.Hybrid {
			r.Gaps = append(r.Gaps, "hybrid recurrent state is included in the measured allocation")
		}
		return true
	}
	// Observed fit at the current load. A requested --ctx still needs
	// architecture to size; without one, this is the answer.
	if in.Ctx <= 0 || !in.Arch.KVReady() {
		r.Tier = Compatible
		r.NeedGB = r.ObservedGB
		r.Why = fmt.Sprintf("resident %s GB of %s GB available (measured)", trim1(r.ObservedGB), trim1(r.HaveGB))
		r.Source = residentSource(in)
		r.Gaps = append(r.Gaps, "resident is total runtime allocation; its non-weight, non-KV remainder is derived")
		if !in.Arch.KVReady() {
			r.Gaps = append(r.Gaps, "other context lengths not sized (no GGUF architecture)")
		}
		return true
	}
	r.Gaps = append(r.Gaps, "current resident "+trim1(r.ObservedGB)+" GB (measured)")
	return false
}

func evaluateSharedMemoryResident(in Input, r *Report) bool {
	if !residentMatchesRequestedContext(in) {
		r.Gaps = append(r.Gaps,
			"resident allocation was not observed at the requested context, so it does not prove that point")
		return false
	}
	r.Tier = Compatible
	r.NeedGB = r.ObservedGB
	r.Ctx = in.ResidentCtx
	capacity := fmt.Sprintf("the %s GB declared planning budget", trim1(r.HaveGB))
	if automaticSharedMemoryCapacity(in) {
		capacity = fmt.Sprintf("the %s GB addressable shared pool", trim1(r.HaveGB))
	}
	r.Why = fmt.Sprintf("resident %s GB within %s at the requested %d context (measured)",
		trim1(r.ObservedGB), capacity, in.ResidentCtx)
	r.Source = residentSource(in)
	r.Gaps = append(r.Gaps,
		"runtime allocation proves this loaded point; it does not establish a reusable free-memory budget")
	return true
}

func residentMatchesRequestedContext(in Input) bool {
	if in.ResidentCtx <= 0 {
		return false
	}
	requested := requestedResidentContext(in)
	return requested > 0 && in.ResidentCtx == requested
}

func requestedResidentContext(in Input) int {
	if in.Ctx > 0 {
		return in.Ctx
	}
	return in.Arch.MaxCtx
}

func residentSource(in Input) string {
	if in.ResidentSrc != "" {
		return in.ResidentSrc
	}
	return "observed resident"
}

func sharedMemoryPool(in Input) bool {
	return in.NVIDIAUnifiedMemory || wholeSystemUnifiedMemorySource(in.HaveSrc)
}

func wholeSystemUnifiedMemorySource(src string) bool {
	switch strings.TrimSpace(src) {
	case device.NVIDIAUnifiedMemorySource, device.NVIDIAUnifiedProbeSource,
		device.AppleLegacyRAMSource:
		return true
	default:
		return false
	}
}

func automaticSharedMemoryCapacity(in Input) bool {
	return sharedMemoryPool(in) && !strings.HasPrefix(strings.TrimSpace(in.HaveSrc), "--vram-gb")
}

func evaluateAutomaticSharedMemoryCapacity(in Input, haveB float64, r Report) Report {
	r.Gaps = append(r.Gaps,
		"automatic shared-memory capacity is not a safe planning budget after the operating system, other processes, and runtime reserve")
	if in.Arch.Hybrid {
		return evaluateAutomaticSharedMemoryHybrid(in, haveB, r)
	}
	lower := evaluateWeightsAndKV(in, haveB, r)
	return constrainAutomaticSharedMemoryClaim(lower)
}

func evaluateAutomaticSharedMemoryHybrid(in Input, haveB float64, r Report) Report {
	if in.WeightsB > 0 {
		r.WeightsGB = round1(float64(in.WeightsB) / GiB)
	}
	if in.WeightsB > 0 && float64(in.WeightsB) > haveB {
		r.Tier = Skip
		r.NeedGB = r.WeightsGB
		r.Why = fmt.Sprintf("artifact bytes are %s GB, above the %s GB addressable shared pool, but mmap, paging, and partial placement were not measured",
			trim1(r.WeightsGB), trim1(r.HaveGB))
		r.Hint = "try a smaller quant, or verify a runtime-supported mmap or partial-placement configuration outside this projection"
		return r
	}
	r.Tier = Skip
	r.Why = "the model's known allocation does not exceed physical capacity, but safe available shared memory was not measured"
	r.Hint = "use --load at the requested context to observe runtime allocation, or pass --vram-gb N as a declared planning budget"
	r.Gaps = append(r.Gaps,
		"hybrid recurrent state and host/device allocation domains were not reconciled")
	return r
}

func constrainAutomaticSharedMemoryClaim(r Report) Report {
	switch r.Tier {
	case Compatible:
		r.Tier = Skip
		r.Why = fmt.Sprintf("%d ctx has a %s GB weights-plus-KV projection within the %s GB addressable pool, but safe available shared memory was not measured",
			r.Ctx, trim1(r.NeedGB), trim1(r.HaveGB))
		r.Hint = "use --load at the requested context to observe runtime allocation, or pass --vram-gb N as a declared planning budget"
	case LowMemory:
		r.Tier = Skip
		r.Why = fmt.Sprintf("%d ctx projects %s GB of artifact bytes plus KV, above the %s GB addressable pool, but mmap, paging, partial placement, and safe availability were not measured",
			r.Ctx, trim1(r.NeedGB), trim1(r.HaveGB))
		r.Hint = "try a smaller quant or shorter context, or verify a resolved runtime configuration with an exact-context load receipt"
		r.Remedy = ""
		clearShorterContextClaim(&r)
	case Incompatible:
		r.Tier = Skip
		r.Why = fmt.Sprintf("the full-residency artifact projection is %s GB, above the %s GB addressable pool, but mmap, paging, partial placement, and safe availability were not measured",
			trim1(r.NeedGB), trim1(r.HaveGB))
		r.Hint = "try a smaller quant or verify a resolved runtime configuration with an exact-context load receipt"
		r.Remedy = ""
		clearShorterContextClaim(&r)
	}
	return r
}

func clearShorterContextClaim(r *Report) {
	r.Flag = ""
	r.FlagValue = 0
	r.FitsGB = 0
	r.KVRemedy = ""
}

func evaluateFit(in Input, r Report) Report {
	r.NeedGB = round1(float64(in.FitB) / GiB)
	if in.FitSrc != "" {
		r.Source = in.FitSrc
	}
	r.Tier = Skip
	r.Why = fmt.Sprintf("llama-fit-params projected %s GB of device memory, but its effective context and placement were not captured",
		trim1(r.NeedGB))
	r.Hint = "use --load at the requested context to observe runtime allocation; omit --fit to see the labeled weights-plus-KV projection"
	r.Gaps = append(r.Gaps,
		"allocator projection is not bound to final context, offload, tensor placement, binary version, or host-memory use")
	if in.FitCannot {
		r.Gaps = append(r.Gaps, "the fitter's final status did not satisfy its device-memory target")
	}
	if automaticSharedMemoryCapacity(in) {
		r.Gaps = append(r.Gaps,
			"automatic shared-memory capacity is not a safe whole-pool budget")
	} else if sharedMemoryPool(in) {
		r.Gaps = append(r.Gaps,
			"host and device allocations share one physical pool and were not reconciled")
	}
	return r
}

func evaluateWeightsAndKV(in Input, haveB float64, r Report) Report {
	prepared, ok := prepareWeightsAndKV(in, haveB, r)
	if !ok {
		return prepared.report
	}
	return evaluateKVContext(in, haveB, prepared)
}

type weightsAndKVEstimate struct {
	report Report
	elem   float64
	ctx    int
}

func prepareWeightsAndKV(in Input, haveB float64, r Report) (weightsAndKVEstimate, bool) {
	if in.WeightsB <= 0 {
		r.Tier = Skip
		r.Why = "model weights were not measured"
		r.Hint = "pass a .gguf path, or use Ollama so /api/show exposes size"
		return weightsAndKVEstimate{report: r}, false
	}
	r.WeightsGB = round1(float64(in.WeightsB) / GiB)

	if float64(in.WeightsB) > haveB {
		r.Tier = Incompatible
		r.Why = fmt.Sprintf("weights alone are %s GB; %s GB available", trim1(r.WeightsGB), trim1(r.HaveGB))
		r.Remedy = "try a smaller quant, or runtime-supported partial/CPU placement; performance is a separate measurement and placement must match for comparison"
		r.Gaps = append(r.Gaps, "other runtime allocation not included")
		return weightsAndKVEstimate{report: r}, false
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
		return weightsAndKVEstimate{report: r}, false
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
		return weightsAndKVEstimate{report: r}, false
	}
	r.Ctx = ctx
	return weightsAndKVEstimate{report: r, elem: elem, ctx: ctx}, true
}

func evaluateKVContext(in Input, haveB float64, prepared weightsAndKVEstimate) Report {
	r, elem, ctx := prepared.report, prepared.elem, prepared.ctx
	perTok := in.Arch.kvBytesPerToken(elem)
	// The --fit branch already refuses a non-positive per-token cost; this
	// path divided and compared without checking. An unsizable cache is a
	// SKIP, never a fit verdict computed from a number that cannot be real.
	if perTok <= 0 {
		r.Tier = Skip
		r.Why = "the KV cache could not be sized from this architecture"
		r.Hint = "pass a .gguf path with readable layer, KV head, and head-dim metadata"
		r.Gaps = append(r.Gaps, "architecture metadata is missing or not believable")
		return r
	}
	kvB := perTok * float64(ctx)
	r.KVGB = round1(kvB / GiB)
	needB := float64(in.WeightsB) + kvB
	r.NeedGB = round1(needB / GiB)
	r.Gaps = append(r.Gaps, "weights + KV only; other runtime allocation not included")

	flag := ContextFlag(in.Backend)
	if needB <= haveB {
		r.Tier = Compatible
		r.Why = fmt.Sprintf("%d ctx needs %s GB; %s GB available", ctx, trim1(r.NeedGB), trim1(r.HaveGB))
		return r
	}

	// Largest context that still fits, aligned down to 256. Below 512 is not
	// a useful chat window - treat as incompatible even though weights fit.
	maxTok := (haveB - float64(in.WeightsB)) / perTok
	fitCtx := ctxTokens(maxTok)
	if fitCtx > ctx {
		fitCtx = ctx
	}
	fitCtx = (fitCtx / 256) * 256
	if fitCtx < 512 {
		r.Tier = Incompatible
		r.Why = fmt.Sprintf("%d ctx needs %s GB; %s GB available", ctx, trim1(r.NeedGB), trim1(r.HaveGB))
		r.Remedy = "try a smaller quant; even a 512-token window does not fit next to the weights"
		r.KVRemedy = kvCacheRemedy(in, elem, ctx, fitCtx, haveB)
		return r
	}
	fitB := float64(in.WeightsB) + perTok*float64(fitCtx)
	r.Tier = LowMemory
	r.Flag = flag
	r.FlagValue = fitCtx
	r.FitsGB = round1(fitB / GiB)
	r.Why = fmt.Sprintf("%d ctx needs %s GB; %s GB available", ctx, trim1(r.NeedGB), trim1(r.HaveGB))
	r.Remedy = remedyLine(flag, fitCtx, r.FitsGB)
	r.KVRemedy = kvCacheRemedy(in, elem, ctx, fitCtx, haveB)
	return r
}

func remedyLine(flag string, ctx int, fits float64) string {
	n := trim1(fits)
	if flag == "" {
		return fmt.Sprintf("try a shorter context (%d) -> fits in %s GB", ctx, n)
	}
	return fmt.Sprintf("try %s=%d -> fits in %s GB", flag, ctx, n)
}

// ctxTokens turns a token budget into a count. The quotient comes from
// user-supplied memory and context figures, and converting a float64 outside
// int range is undefined in Go, so it is clamped. No real context window
// approaches the ceiling; anything that does is already not a measurement.
func ctxTokens(tokens float64) int {
	if math.IsNaN(tokens) || tokens <= 0 {
		return 0
	}
	if tokens > math.MaxInt32 {
		return math.MaxInt32
	}
	return int(tokens)
}

// round1 rounds to one decimal for display. Converting a float64 outside
// int64's range is undefined in Go and in practice yields the most negative
// int64, so an oversized GB figure printed as a large negative number. Values
// that cannot survive the conversion are returned unrounded: a wrong-looking
// big number is a visible fault, a negative one reads as a real measurement.
func round1(v float64) float64 {
	const lim = float64(math.MaxInt64) / 10
	if math.IsNaN(v) || math.IsInf(v, 0) || v > lim || v < -lim {
		return v
	}
	return float64(int64(v*10+0.5)) / 10
}

func trim1(v float64) string {
	s := fmt.Sprintf("%.1f", v)
	return s
}

// adviseLabelWidth holds "  [incompatible]", the widest label in the report,
// so every value in the block starts in the same column.
const adviseLabelWidth = 17

// Write prints the human report. SKIP never prints a 0.0 GB budget as if it
// were a measurement.
func Write(w io.Writer, r Report) {
	var bits []string
	if r.Model != "" {
		bits = append(bits, render.SingleLine(r.Model))
	}
	if r.Quant != "" {
		bits = append(bits, render.SingleLine(r.Quant))
	}
	if p := formatParams(r); p != "" {
		bits = append(bits, p)
	}
	if len(bits) > 0 {
		fmt.Fprintf(w, "  %s\n\n", strings.Join(bits, "  "))
	}
	// Every field wraps under its own column. The verdict line in particular
	// carries an unbounded sentence -- "resident 19.0 GB exceeds the 24.0 GB
	// budget reading; the process is running, so the budget is the suspect
	// number" is 110 characters on its own -- and used to run off the right
	// edge of any terminal narrower than its longest explanation.
	width := render.Width()
	render.Field(w, "  ["+render.SingleLine(r.Label())+"]", adviseLabelWidth, r.Why, width)
	if r.Remedy != "" {
		render.Field(w, "  try", adviseLabelWidth, r.Remedy, width)
	}
	if r.KVRemedy != "" {
		render.Field(w, "  or", adviseLabelWidth, r.KVRemedy, width)
	}
	if r.Hint != "" {
		render.Field(w, "  hint", adviseLabelWidth, r.Hint, width)
	}
	for _, g := range r.Gaps {
		render.Field(w, "  note", adviseLabelWidth, g, width)
	}
	if r.Source != "" {
		render.Field(w, "  source", adviseLabelWidth, r.Source, width)
	}
	if r.HaveSource != "" && r.Tier != Skip {
		render.Field(w, "  memory", adviseLabelWidth,
			fmt.Sprintf("%s GB (%s)", trim1(r.HaveGB), render.SingleLine(r.HaveSource)), width)
		// The verdict above is computed against the whole card, which is the
		// right question only on a machine doing nothing else. Say what is
		// actually free when that is a materially different number, because
		// "compatible" is cold comfort if the model cannot load right now.
		if r.FreeGB > 0 && r.WeightsGB > 0 && r.FreeGB < r.WeightsGB {
			render.Field(w, "  ! free now", adviseLabelWidth, fmt.Sprintf(
				"%s GB, less than this model's %s GB of weights. The verdict above "+
					"assumes the card is yours; right now it is not",
				trim1(r.FreeGB), trim1(r.WeightsGB)), width)
		}
	}
	if r.Fit != nil && len(r.Fit.Points) > 0 {
		render.WriteContextFit(w, fitPresentation(*r.Fit), "auto")
	}
}

func fitPresentation(t FitTable) render.ContextFit {
	out := render.ContextFit{
		HaveGB: t.HaveGB, HaveSource: t.HaveSrc,
		CapacityOnly: t.HaveSemantics == "addressable_capacity", Note: t.Note,
	}
	for _, p := range t.Points {
		marginGB := p.HeadroomGB
		if p.CapacityDeltaGB != nil {
			marginGB = *p.CapacityDeltaGB
		}
		out.Points = append(out.Points, render.ContextFitPoint{
			Ctx: p.Ctx, Tier: p.Tier, WeightsGB: p.WeightsGB, KVGB: p.KVGB,
			OtherGB: p.OtherGB, OtherKnown: p.OtherKnown,
			NeedGB: p.NeedGB, MarginGB: marginGB,
			DecodeTPS: p.DecodeTPS, PrefillTPS: p.PrefillTPS,
			Requested: p.Requested, Suggested: p.Suggested, Note: p.Note,
		})
	}
	return out
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
	Model        string   `json:"model,omitempty"`
	Ctx          int      `json:"ctx"`
	Backend      string   `json:"backend,omitempty"`
	Mutates      bool     `json:"mutates"`
	Note         string   `json:"note"`
	Steps        []string `json:"steps"`
	ServingCtx   int      `json:"serving_ctx,omitempty"`
	ServingKnown bool     `json:"serving_known,omitempty"`
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
	width := render.Width()
	fmt.Fprintln(w, "  apply prints how to persist a measured context.")
	render.Field(w, "", 2, render.SingleLine(p.Note)+".", width)
	fmt.Fprintln(w)
	if p.Model != "" {
		fmt.Fprintf(w, "  model          %s\n", render.SingleLine(p.Model))
	}
	fmt.Fprintf(w, "  ctx            %d\n", p.Ctx)
	if p.Backend != "" {
		fmt.Fprintf(w, "  runtime        %s\n", render.SingleLine(p.Backend))
	}
	if p.ServingKnown && p.ServingCtx > 0 {
		if p.ServingCtx == p.Ctx {
			fmt.Fprintf(w, "  serving        %d (this process already has this window)\n", p.ServingCtx)
		} else {
			fmt.Fprintf(w, "  serving        %d\n", p.ServingCtx)
		}
	}
	fmt.Fprintln(w)
	for _, s := range p.Steps {
		if strings.HasSuffix(s, ":") && !strings.Contains(s, " ") {
			fmt.Fprintf(w, "  %s\n", render.SingleLine(s))
			continue
		}
		// A step is prose plus, sometimes, a command. Wrap it under its own
		// indent rather than letting the terminal break it at an arbitrary
		// column: a step the reader has to reassemble is a step they mistype.
		render.Field(w, "", 4, s, width)
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
	a.Blocks = archDim(first(kvs, p+"block_count"))
	a.Embed = archDim(first(kvs, p+"embedding_length"))
	a.Heads = archDim(first(kvs, p+"attention.head_count"))
	a.KVHeads = archDim(first(kvs, p+"attention.head_count_kv"))
	if a.KVHeads == 0 {
		a.KVHeads = a.Heads
	}
	a.KeyLength = archDim(first(kvs, p+"attention.key_length"))
	a.ValLength = archDim(first(kvs, p+"attention.value_length"))
	a.MaxCtx = archDim(first(kvs, p+"context_length"))
	a.Experts = archDim(first(kvs, p+"expert_count"))
	a.ExpertUsed = archDim(first(kvs, p+"expert_used_count"))
	a.FFN = archDim(first(kvs, p+"expert_feed_forward_length", p+"feed_forward_length"))
	a.FullAttentionInterval = archDim(first(kvs, p+"full_attention_interval"))
	a.RecurrentLayers = archDim(first(kvs, p+"attention.recurrent_layer_count", "attention.recurrent_layer_count"))
	a.Hybrid = a.FullAttentionInterval > 0 || a.RecurrentLayers > 0 ||
		strings.EqualFold(arch, "qwen35") || strings.EqualFold(arch, "qwen3.5")
	return a
}

// maxArchDim bounds a single architecture dimension. The largest real models
// are three orders of magnitude below this, so it rejects nothing genuine
// while keeping products of these fields far inside int64. A GGUF header is
// an untrusted file: a corrupt or hostile one declaring a block count near
// 2^62 overflowed the KV arithmetic to a NEGATIVE bytes-per-token, and advise
// then reported "compatible" for a model that does not fit, with a negative
// GB figure printed next to it. Wrong in the direction that matters.
const maxArchDim = 1 << 20

// archDim reads a dimension and treats an implausible one as unmeasured.
// Zero already means "not known" everywhere downstream, which routes the
// verdict to SKIP -- the correct answer for metadata that cannot be believed.
func archDim(v any) int {
	n := asInt(v)
	if n < 0 || n > maxArchDim {
		return 0
	}
	return n
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
		if _, err := fmt.Sscanf(n, "%d", &x); err != nil {
			return 0
		}
		return x
	}
	return 0
}
