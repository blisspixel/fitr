package advise

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/blisspixel/fitr/internal/analysis"
)

// Architecture metadata and the cache arithmetic derived from it.
//
// Split from the verdict logic because they answer different questions: this
// file is what the artifact says about its own shape and what that implies for
// a cache, and advise.go is what those figures mean against one machine's
// budget. The fit verdict is arithmetic over the values here, so keeping them
// together makes the arithmetic auditable in one place.

type Arch struct {
	// InvalidMetadata distinguishes a malformed dimension from an absent key.
	// A fallback must never make a wrong-shaped observation projectable.
	InvalidMetadata bool
	// The runtime's boolean per-layer recurrent pattern is recognized as a
	// distinct unsupported layout, never mislabeled as a scalar layer count.
	RecurrentPatternUnsupported bool
	Name                        string
	Blocks                      int
	Embed                       int
	Heads                       int
	KVHeads                     int
	KeyLength                   int // 0 → fall back to embed/heads and say so
	ValLength                   int
	MaxCtx                      int
	Experts                     int
	ExpertUsed                  int
	FFN                         int // expert FFN if MoE, else dense FFN
	Vocab                       int
	Params                      int64
	// Hybrid recurrent models need runtime state beyond a conventional KV
	// cache. Their metadata is preserved, but weights-plus-KV arithmetic is
	// not allowed to stand in for a measured allocation.
	Hybrid                bool
	FullAttentionInterval int
	RecurrentLayers       int
	// PerLayerKVHeads records that head_count_kv arrived as a per-layer array.
	// The artifact then has no single KV head count, so any projection has to
	// sum the layers rather than multiply one figure by the layer count.
	PerLayerKVHeads bool
	// KVHeadsPerLayer is that array, kept only when it is internally consistent
	// with block_count. A layer entry of zero is a layer that does not attend
	// and contributes no cache.
	KVHeadsPerLayer []int
	// SlidingWindow and the SWA lengths are read so their presence can be
	// detected, not so a cache can be sized from them. Which layers slide is
	// decided by llama.cpp's per-architecture dense_first argument to
	// set_swa_pattern, which no GGUF key carries: at pattern 1 the same
	// metadata means every layer is dense or every layer slides, depending
	// only on that argument. See kvClassifiable.
	SlidingWindow        int
	SlidingWindowPattern int
	KeyLengthSWA         int
	ValLengthSWA         int
	// NextNPredictLayers is a multi-token-prediction head counted inside
	// block_count. It is not an attention layer, so the cache arithmetic works
	// from the remainder.
	NextNPredictLayers int
	// The recurrent state of a hybrid's linear-attention layers. It is
	// context-independent, so it is a fixed addition to the cache rather than
	// a per-token cost.
	SSMInnerSize  int
	SSMStateSize  int
	SSMConvKernel int
	SSMGroupCount int
	// An observed zero group count and an absent group count have different
	// meanings for the recurrent convolution width.
	SSMGroupCountKnown bool
}

// modernKVLayout reports an architecture that declares grouped, windowed or
// interval attention. Such an artifact is never pre-GQA, so an absent KV head
// count is a gap in whichever source was read rather than a statement that
// every head carries its own cache.
func (a Arch) modernKVLayout() bool {
	return a.Hybrid || a.FullAttentionInterval > 0 || a.RecurrentLayers > 0 ||
		a.slidingWindowPresent() || a.SSMInnerSize > 0 ||
		inherentHybridArchitecture(a.Name)
}

// realBlocks is the decoder layers that actually attend or recur, excluding a
// multi-token-prediction head that block_count includes.
func (a Arch) realBlocks() int {
	if a.NextNPredictLayers > 0 && a.NextNPredictLayers < a.Blocks {
		return a.Blocks - a.NextNPredictLayers
	}
	return a.Blocks
}

// fullAttentionLayers is how many layers hold a context-scaling KV cache in an
// interval hybrid. Only the count is needed, never which layers they are:
// llama.cpp's pattern places exactly one full-attention layer in every
// full_attention_interval layers whichever way its dense_first argument falls
// only when the interval divides the layer count. A partial final interval
// needs the artifact's per-layer counts rather than a guessed convention.
func (a Arch) fullAttentionLayers() int {
	if a.FullAttentionInterval <= 0 {
		return 0
	}
	return a.realBlocks() / a.FullAttentionInterval
}

// recurrentStateBytes is the fixed allocation held by the linear-attention
// layers: one state tensor and one short convolution window each, in fp32. It
// does not grow with the requested context. ok is false when the artifact does
// not supply the shape, because a hybrid whose recurrent cost is unknown has
// no complete cache projection.
func (a Arch) recurrentStateBytes() (float64, bool) {
	linear := a.linearLayers()
	if linear <= 0 || a.SSMInnerSize <= 0 || a.SSMStateSize <= 0 || a.SSMConvKernel <= 1 || !a.SSMGroupCountKnown {
		return 0, false
	}
	const fp32 = 4
	state := float64(a.SSMInnerSize) * float64(a.SSMStateSize) * fp32
	// llama_hparams::n_embd_r in llama.cpp
	// 4a89937354190cef5a97baf8eeb17336105eb72d stores the recurrent keys as
	// well as values in the convolution window. Omitting these groups silently
	// understates every hybrid's fixed cache allocation.
	convWidth := float64(a.SSMInnerSize) + 2*float64(a.SSMGroupCount)*float64(a.SSMStateSize)
	conv := convWidth * float64(a.SSMConvKernel-1) * fp32
	total := float64(linear) * (state + conv)
	if math.IsNaN(total) || math.IsInf(total, 0) || total <= 0 || total >= float64(math.MaxInt64) {
		return 0, false
	}
	return total, true
}

// UnsizableReason explains why this architecture cannot be sized, and what
// would settle it. It is only meaningful when KVReady is false.
//
// "Metadata is missing" was the only explanation for every cause, which was
// wrong for the artifacts that carry rich metadata and are refused for what
// that metadata says rather than for what it lacks. A reader given the wrong
// reason goes looking for the wrong fix.
func (a Arch) UnsizableReason() (note, hint string) {
	switch {
	case a.InvalidMetadata:
		return "architecture metadata contains a malformed or implausible dimension",
			"use an artifact whose dimensions have the declared numeric shapes"
	case a.RecurrentPatternUnsupported:
		return "the artifact declares a recurrent pattern whose per-layer state allocation is not modeled",
			"observe runtime allocation at the requested context"
	case a.Blocks <= 0 || a.headDimK() <= 0 || a.headDimV() <= 0:
		return "architecture metadata is missing or not believable",
			"pass a GGUF whose layer count, KV heads and head dimensions are readable"
	case a.slidingWindowPresent():
		// The cache length is not uniform across layers and the artifact does
		// not say which layers slide. That is a property of the format, not a
		// gap in this file, so there is nothing to supply.
		return "this artifact declares a sliding-window attention layout, and which layers " +
				"use the bounded window is decided by the runtime rather than carried by the file",
			"observe the real allocation instead: fitr advise <model> --load"
	case a.Hybrid && !a.intervalHybridProjectable():
		return "this hybrid architecture does not carry the recurrent state shape its " +
				"non-attention layers allocate",
			"observe the real allocation instead: fitr advise <model> --load"
	case a.PerLayerKVHeads && len(a.KVHeadsPerLayer) == 0:
		return "the per-layer KV head counts do not agree with the declared layer count",
			"pass a GGUF whose head_count_kv array has one entry per block"
	case a.totalKVHeads() <= 0:
		return "the artifact declares no KV heads to size a cache from",
			"pass a GGUF whose attention.head_count_kv is readable"
	}
	return "architecture metadata is missing or not believable",
		"pass a GGUF whose layer, KV head and head-dimension metadata is readable"
}

// linearLayers is how many layers hold recurrent state instead of a KV cache.
//
// A per-layer head_count_kv array states this directly: a layer with zero KV
// heads does not attend. That is exact, so it is preferred over dividing the
// layer count by the interval, which only describes the repeating pattern.
func (a Arch) linearLayers() int {
	if len(a.KVHeadsPerLayer) > 0 {
		linear := 0
		for _, heads := range a.KVHeadsPerLayer {
			if heads == 0 {
				linear++
			}
		}
		return linear
	}
	return a.realBlocks() - a.fullAttentionLayers()
}

// intervalHybridProjectable reports a hybrid whose complete cache the artifact
// determines: the layers that scale with context, and the fixed recurrent
// state held by the rest.
//
// A per-layer array is accepted rather than refused. It was refused when the
// only way to count attending layers was the interval, so an array meant an
// unknown scalar. It is now the better evidence of the two: the entries say
// which layers attend and with how many heads, where the interval only
// describes the repeating pattern. Current artifacts publish both.
func (a Arch) intervalHybridProjectable() bool {
	if a.FullAttentionInterval <= 0 {
		return false
	}
	// llama_hparams::set_recr_pattern in llama.cpp
	// 4a89937354190cef5a97baf8eeb17336105eb72d has two dense-first conventions.
	// They disagree by one attention layer when the final interval is partial.
	if len(a.KVHeadsPerLayer) == 0 && a.realBlocks()%a.FullAttentionInterval != 0 {
		return false
	}
	if !a.kvClassifiable() || a.totalKVHeads() <= 0 {
		return false
	}
	if len(a.KVHeadsPerLayer) == 0 && (a.KVHeads <= 0 || a.fullAttentionLayers() <= 0) {
		return false
	}
	_, ok := a.recurrentStateBytes()
	return ok
}

// slidingWindowPresent reports an artifact whose cache length is not uniform
// across layers.
func (a Arch) slidingWindowPresent() bool {
	return a.SlidingWindow > 0 || a.SlidingWindowPattern > 0 ||
		a.KeyLengthSWA > 0 || a.ValLengthSWA > 0
}

// kvClassifiable reports whether every layer's cache length is known to be the
// full context. Sliding-window layers hold a bounded cache instead, and the
// artifact does not say which layers those are, so a projection would be
// either an overstatement or an invention.
func (a Arch) kvClassifiable() bool { return !a.slidingWindowPresent() }

// totalKVHeads is the KV head count summed across layers. It is the one place
// the per-layer and uniform cases converge, so the projection does not grow a
// second formula.
func (a Arch) totalKVHeads() int {
	if len(a.KVHeadsPerLayer) > 0 {
		total := 0
		for _, heads := range a.KVHeadsPerLayer {
			total += heads
		}
		return total
	}
	// In an interval hybrid only the full-attention layers hold a cache that
	// grows with context. Charging every layer would report several times the
	// cache the model actually allocates.
	if a.FullAttentionInterval > 0 {
		return a.fullAttentionLayers() * a.KVHeads
	}
	return a.realBlocks() * a.KVHeads
}

// KVReady is whether the KV cache can be sized without guessing head dim
// from a name. A missing key_length falls back to embed/heads, which is
// correct for Llama and wrong for Qwen3 (128, not 64) - that fallback is
// disclosed, not hidden.
func (a Arch) KVReady() bool {
	if a.InvalidMetadata || a.RecurrentPatternUnsupported {
		return false
	}
	if a.Hybrid && !a.intervalHybridProjectable() {
		return false
	}
	return a.Blocks > 0 && a.totalKVHeads() > 0 && a.kvClassifiable() &&
		a.headDimK() > 0 && a.headDimV() > 0
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
	// llama.cpp 4a89937354190cef5a97baf8eeb17336105eb72d load_hparams
	// defaults V independently to embed/heads. An explicit K override does
	// not establish V, and missing inputs do not permit borrowing that width.
	if a.Heads > 0 && a.Embed > 0 {
		return a.Embed / a.Heads
	}
	return 0
}

func (a Arch) kvBytesPerToken(elem float64) float64 {
	if !a.KVReady() || elem <= 0 {
		return 0
	}
	// Accumulate in float64. The same product in int arithmetic wraps to a
	// negative on absurd dimensions, and a negative cost per token turns the
	// fit comparison inside out.
	// Summing heads across layers rather than multiplying one layer's count by
	// block_count keeps the uniform and per-layer cases on one formula. They
	// agree exactly when every layer carries the same count.
	heads := float64(a.totalKVHeads())
	per := heads * float64(a.headDimK()+a.headDimV()) * elem
	if math.IsNaN(per) || math.IsInf(per, 0) || per <= 0 {
		return 0
	}
	return per
}

// cacheFixedBytes is the part of the cache that does not grow with the
// requested context. It is zero for conventional attention and the recurrent
// state for an interval hybrid. Every caller that sizes a cache adds it, so a
// projection is the complete allocation rather than only its scaling half.
func (a Arch) cacheFixedBytes() float64 {
	if a.FullAttentionInterval <= 0 {
		return 0
	}
	fixed, ok := a.recurrentStateBytes()
	if !ok {
		return 0
	}
	return fixed
}

// ProjectKVBytes returns the cache projection for one declared context and
// element size: the context-scaling KV plus any fixed recurrent state.
//
// A hybrid is projected only when the artifact determines both halves. Where it
// does not, the conventional arithmetic is not this model's allocation model
// and the projection stays unavailable.
func ProjectKVBytes(arch Arch, contextTokens int, elementBytes float64) (int64, bool) {
	if contextTokens <= 0 || !arch.KVReady() {
		return 0, false
	}
	perToken := arch.kvBytesPerToken(elementBytes)
	projected := perToken*float64(contextTokens) + arch.cacheFixedBytes()
	// MaxInt64 rounds to 2^63 in float64, so equality is already outside the
	// signed byte range and must be refused before conversion.
	if projected <= 0 || math.IsNaN(projected) || math.IsInf(projected, 0) || projected >= float64(math.MaxInt64) {
		return 0, false
	}
	return int64(math.Ceil(projected)), true
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

// ArchShape is the parsed architecture, as read. Absent fields are absent from
// the metadata rather than zero, so each is omitted rather than reported as 0.
type ArchShape = analysis.SourceArchitectureShape

// Shape reports the architecture as parsed, including the derived figures a
// reader would otherwise have to recompute to check a verdict.
func (a Arch) Shape() *ArchShape {
	s := &ArchShape{
		Name: a.Name, Blocks: a.Blocks, RealBlocks: a.realBlocks(),
		Heads: a.Heads, KVHeads: a.KVHeads, KVHeadsPerLayer: a.KVHeadsPerLayer,
		KeyLength: a.KeyLength, ValLength: a.ValLength, MaxCtx: a.MaxCtx,
		Experts: a.Experts, ExpertUsed: a.ExpertUsed,
		FullAttentionInterval: a.FullAttentionInterval,
		FullAttentionLayers:   a.fullAttentionLayers(),
		RecurrentLayers:       a.RecurrentLayers, NextNPredictLayers: a.NextNPredictLayers,
		SlidingWindow: a.SlidingWindow, SlidingWindowPattern: a.SlidingWindowPattern,
		KeyLengthSWA: a.KeyLengthSWA, ValLengthSWA: a.ValLengthSWA,
		SSMInnerSize: a.SSMInnerSize, SSMStateSize: a.SSMStateSize,
		SSMConvKernel: a.SSMConvKernel, Hybrid: a.Hybrid,
		KVSizable: a.KVReady(),
	}
	if a.SSMGroupCountKnown {
		s.SSMGroupCount = &a.SSMGroupCount
	}
	if s.KVSizable {
		s.KVBytesPerToken = int64(a.kvBytesPerToken(2))
		s.FixedCacheBytes = int64(a.cacheFixedBytes())
		return s
	}
	s.UnsizableReason, _ = a.UnsizableReason()
	return s
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
	a.Blocks = a.dimension(kvs, p+"block_count")
	a.Embed = a.dimension(kvs, p+"embedding_length")
	a.Heads = a.dimension(kvs, p+"attention.head_count")
	// Read before the KV heads: the per-layer array covers attention layers,
	// while block_count may also count a prediction head, so the accepted
	// lengths depend on this.
	a.NextNPredictLayers = a.dimension(kvs, p+"nextn_predict_layers")
	kvHeads, kvHeadsPresent := kvs[p+"attention.head_count_kv"]
	a.KVHeads = archDim(kvHeads)
	a.PerLayerKVHeads = perLayerDimension(kvHeads)
	if a.PerLayerKVHeads {
		a.KVHeadsPerLayer = perLayerDimensions(kvHeads, a.Blocks, a.realBlocks())
	}
	a.KeyLength = a.positiveDimension(kvs, p+"attention.key_length")
	a.ValLength = a.positiveDimension(kvs, p+"attention.value_length")
	a.MaxCtx = a.dimension(kvs, p+"context_length")
	a.Experts = archDim(first(kvs, p+"expert_count"))
	a.ExpertUsed = archDim(first(kvs, p+"expert_used_count"))
	a.FFN = archDim(first(kvs, p+"expert_feed_forward_length", p+"feed_forward_length"))
	a.SSMInnerSize = a.dimension(kvs, p+"ssm.inner_size")
	a.SSMStateSize = a.dimension(kvs, p+"ssm.state_size")
	a.SSMConvKernel = a.dimension(kvs, p+"ssm.conv_kernel")
	a.SSMGroupCount = a.dimension(kvs, p+"ssm.group_count")
	_, a.SSMGroupCountKnown = kvs[p+"ssm.group_count"]
	a.SlidingWindow = a.dimension(kvs, p+"attention.sliding_window")
	a.SlidingWindowPattern = a.dimension(kvs, p+"attention.sliding_window_pattern")
	a.KeyLengthSWA = a.dimension(kvs, p+"attention.key_length_swa")
	a.ValLengthSWA = a.dimension(kvs, p+"attention.value_length_swa")
	a.FullAttentionInterval = a.dimension(kvs, p+"full_attention_interval")
	recurrentPresent := a.readRecurrentPattern(kvs, p+"attention.recurrent_layers", "attention.recurrent_layers")
	a.Hybrid = a.FullAttentionInterval > 0 || recurrentPresent || inherentHybridArchitecture(arch)
	if !kvHeadsPresent && !a.modernKVLayout() {
		// The pre-GQA default applies only to an absent key, after every layout
		// discriminator is read. Explicit zero, null or malformed values are
		// observations the default cannot repair; modern layouts need real KV
		// heads instead of borrowing the query-head count.
		a.KVHeads = a.Heads
	}
	return a
}

func (a *Arch) readRecurrentPattern(kvs map[string]any, keys ...string) bool {
	// llama.cpp 4a89937354190cef5a97baf8eeb17336105eb72d qwen35.cpp
	// loads attention.recurrent_layers with get_key_or_arr into a boolean
	// is_recr_impl array. The spelling is not a scalar count contract.
	for _, key := range keys {
		value, present := kvs[key]
		if !present {
			continue
		}
		a.RecurrentPatternUnsupported = true
		if !validRecurrentPattern(value, a.Blocks, a.realBlocks()) {
			a.InvalidMetadata = true
		}
		return true
	}
	return false
}

func validRecurrentPattern(value any, blocks, realBlocks int) bool {
	if _, ok := value.(bool); ok {
		return true
	}
	entries, ok := value.([]any)
	if !ok || len(entries) == 0 || (len(entries) != blocks && len(entries) != realBlocks) {
		return false
	}
	for _, entry := range entries {
		if _, ok := entry.(bool); !ok {
			return false
		}
	}
	return true
}

func (a *Arch) dimension(kvs map[string]any, keys ...string) int {
	for _, key := range keys {
		value, present := kvs[key]
		if !present {
			continue
		}
		n, ok := architectureInteger(value)
		if !ok || n < 0 || n > maxArchDim {
			a.InvalidMetadata = true
			return 0
		}
		return int(n)
	}
	return 0
}

func (a *Arch) positiveDimension(kvs map[string]any, key string) int {
	value := a.dimension(kvs, key)
	// llama.cpp 4a89937354190cef5a97baf8eeb17336105eb72d load_hparams
	// initializes attention widths before reading optional overrides. An
	// explicit zero overrides that default; it is not an absent declaration
	// that permits fitr to substitute embed/heads or the key width.
	if _, present := kvs[key]; present && value == 0 {
		a.InvalidMetadata = true
	}
	return value
}

// inherentHybridArchitecture is a GGUF architecture-string gate for families
// whose conventional KV formula is the wrong physics even when
// full_attention_interval is omitted from the header. It matches decoder
// architecture names, never serving tags like "qwen3.8:27b", and must not be
// grown from marketing names or a single-family quant study.
func inherentHybridArchitecture(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "qwen35", "qwen3.5", "qwen35moe", "qwen3next":
		return true
	default:
		return false
	}
}

// ShapeClass is a compact architecture label from GGUF keys only. Empty means
// the artifact did not supply enough metadata to name a class.
func (a Arch) ShapeClass() string {
	if a.Hybrid {
		if a.Experts > 0 {
			return "hybrid-linear+moe"
		}
		if a.FullAttentionInterval > 0 || inherentHybridArchitecture(a.Name) {
			return "hybrid-linear"
		}
		return "recurrent"
	}
	if a.Experts > 0 {
		return "moe"
	}
	if a.KVReady() {
		return "dense"
	}
	return ""
}

// KVStrategy names how context memory grows, only when metadata supports it.
// A missing full_attention_interval is never defaulted to 4.
func (a Arch) KVStrategy() string {
	if a.FullAttentionInterval > 0 {
		return fmt.Sprintf("hybrid-interval-%d", a.FullAttentionInterval)
	}
	if a.Hybrid {
		if a.RecurrentLayers > 0 && !inherentHybridArchitecture(a.Name) {
			return "recurrent-state"
		}
		return "hybrid-linear"
	}
	if a.KVReady() {
		return "full-kv"
	}
	return ""
}

// CompactLabel is the inventory architecture/KV-strategy line. Empty when the
// installed artifact did not supply the keys.
func (a Arch) CompactLabel() string {
	shape := a.ShapeClass()
	if shape == "" {
		return ""
	}
	if a.Experts > 0 && a.ExpertUsed > 0 {
		shape = fmt.Sprintf("%s %d/%d", shape, a.ExpertUsed, a.Experts)
	}
	if strategy := a.KVStrategy(); strategy != "" && strategy != shape {
		return shape + "  " + strategy
	}
	return shape
}

// archDim reads a dimension and treats an implausible one as unmeasured.
// Zero already means "not known" everywhere downstream, which routes the
// verdict to SKIP -- the correct answer for metadata that cannot be believed.
// perLayerDimension reports a GGUF value that carries one entry per layer.
// Such a key is present and readable but is not a model-wide scalar, which is
// a different state from the key being absent.
func perLayerDimension(v any) bool {
	_, ok := v.([]any)
	return ok
}

// perLayerDimensions reads a per-layer array as layer dimensions. It returns
// nil unless the array has exactly one entry per block and every entry is
// believable, because a length that disagrees with block_count means the two
// keys describe different models and neither can be trusted to size a cache.
func perLayerDimensions(v any, blocks, attentionBlocks int) []int {
	entries, ok := v.([]any)
	if !ok || blocks <= 0 {
		return nil
	}
	// One entry per block is the ordinary case. An artifact that counts a
	// multi-token-prediction head inside block_count describes one fewer
	// attention layer than it declares blocks, and its array covers the
	// attention layers. Any other length means the two keys describe
	// different models and neither can size a cache.
	if len(entries) != blocks && len(entries) != attentionBlocks {
		return nil
	}
	dimensions := make([]int, 0, len(entries))
	for _, entry := range entries {
		n, valid := architectureInteger(entry)
		if !valid || n < 0 || n > maxArchDim {
			return nil
		}
		dimensions = append(dimensions, int(n))
	}
	return dimensions
}

func archDim(v any) int {
	n, ok := architectureInteger(v)
	if !ok || n < 0 || n > maxArchDim {
		return 0
	}
	return int(n)
}

// GGUF dimensions and JSON model_info numbers are numeric scalars. Integer
// strings and singleton arrays remain supported by the general conversion
// helper, but are not the numeric shape an architecture dimension declares.
func architectureInteger(value any) (int64, bool) {
	switch value.(type) {
	case string, []any:
		return 0, false
	default:
		return integerScalar(value)
	}
}

func first(kvs map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := kvs[k]; ok && v != nil {
			return v
		}
	}
	return nil
}

func asInt(v any) int { return int(asInt64(v)) }

func asInt64(v any) int64 {
	if entries, ok := v.([]any); ok {
		if len(entries) != 1 {
			return 0
		}
		return asInt64(entries[0])
	}
	n, _ := integerScalar(v)
	return n
}

func integerScalar(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case uint32:
		return int64(n), true
	case uint64:
		if n > 1<<63-1 {
			return 0, false
		}
		return int64(n), true
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) || n < math.MinInt64 || n >= math.MaxInt64 || math.Trunc(n) != n {
			return 0, false
		}
		return int64(n), true
	case float32:
		return integerScalar(float64(n))
	case string:
		parsed, err := strconv.ParseInt(n, 10, 64)
		return parsed, err == nil
	}
	return 0, false
}
