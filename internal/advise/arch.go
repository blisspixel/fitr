package advise

import (
	"fmt"
	"math"
	"strings"
)

// Architecture metadata and the cache arithmetic derived from it.
//
// Split from the verdict logic because they answer different questions: this
// file is what the artifact says about its own shape and what that implies for
// a cache, and advise.go is what those figures mean against one machine's
// budget. The fit verdict is arithmetic over the values here, so keeping them
// together makes the arithmetic auditable in one place.

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
}

// modernKVLayout reports an architecture that declares grouped, windowed or
// interval attention. Such an artifact is never pre-GQA, so an absent KV head
// count is a gap in whichever source was read rather than a statement that
// every head carries its own cache.
func (a Arch) modernKVLayout() bool {
	return a.FullAttentionInterval > 0 || a.RecurrentLayers > 0 ||
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
// full_attention_interval layers whichever way its dense_first argument falls,
// so the count is the same under both conventions. That is what makes this
// computable where a sliding-window layout is not.
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
	if linear <= 0 || a.SSMInnerSize <= 0 || a.SSMStateSize <= 0 || a.SSMConvKernel <= 1 {
		return 0, false
	}
	const fp32 = 4
	state := float64(a.SSMInnerSize) * float64(a.SSMStateSize) * fp32
	conv := float64(a.SSMInnerSize) * float64(a.SSMConvKernel-1) * fp32
	total := float64(linear) * (state + conv)
	if math.IsNaN(total) || math.IsInf(total, 0) || total <= 0 {
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
	return a.headDimK()
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
	if contextTokens <= 0 || (arch.Hybrid && !arch.intervalHybridProjectable()) {
		return 0, false
	}
	perToken := arch.kvBytesPerToken(elementBytes)
	projected := perToken*float64(contextTokens) + arch.cacheFixedBytes()
	if projected <= 0 || math.IsNaN(projected) || math.IsInf(projected, 0) || projected > math.MaxInt64 {
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
type ArchShape struct {
	Name                  string `json:"name,omitempty"`
	Blocks                int    `json:"block_count,omitempty"`
	RealBlocks            int    `json:"attention_blocks,omitempty"`
	Heads                 int    `json:"head_count,omitempty"`
	KVHeads               int    `json:"head_count_kv,omitempty"`
	KVHeadsPerLayer       []int  `json:"head_count_kv_per_layer,omitempty"`
	KeyLength             int    `json:"key_length,omitempty"`
	ValLength             int    `json:"value_length,omitempty"`
	MaxCtx                int    `json:"context_length,omitempty"`
	Experts               int    `json:"expert_count,omitempty"`
	ExpertUsed            int    `json:"expert_used_count,omitempty"`
	FullAttentionInterval int    `json:"full_attention_interval,omitempty"`
	FullAttentionLayers   int    `json:"full_attention_layers,omitempty"`
	RecurrentLayers       int    `json:"recurrent_layers,omitempty"`
	NextNPredictLayers    int    `json:"nextn_predict_layers,omitempty"`
	SlidingWindow         int    `json:"sliding_window,omitempty"`
	SlidingWindowPattern  int    `json:"sliding_window_pattern,omitempty"`
	KeyLengthSWA          int    `json:"key_length_swa,omitempty"`
	ValLengthSWA          int    `json:"value_length_swa,omitempty"`
	SSMInnerSize          int    `json:"ssm_inner_size,omitempty"`
	SSMStateSize          int    `json:"ssm_state_size,omitempty"`
	SSMConvKernel         int    `json:"ssm_conv_kernel,omitempty"`
	Hybrid                bool   `json:"hybrid,omitempty"`
	KVSizable             bool   `json:"kv_sizable"`
	KVBytesPerToken       int64  `json:"kv_bytes_per_token,omitempty"`
	FixedCacheBytes       int64  `json:"fixed_cache_bytes,omitempty"`
	UnsizableReason       string `json:"unsizable_reason,omitempty"`
}

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
	a.Blocks = archDim(first(kvs, p+"block_count"))
	a.Embed = archDim(first(kvs, p+"embedding_length"))
	a.Heads = archDim(first(kvs, p+"attention.head_count"))
	// Read before the KV heads: the per-layer array covers attention layers,
	// while block_count may also count a prediction head, so the accepted
	// lengths depend on this.
	a.NextNPredictLayers = archDim(first(kvs, p+"nextn_predict_layers"))
	kvHeads := first(kvs, p+"attention.head_count_kv")
	a.KVHeads = archDim(kvHeads)
	a.PerLayerKVHeads = perLayerDimension(kvHeads)
	if a.PerLayerKVHeads {
		a.KVHeadsPerLayer = perLayerDimensions(kvHeads, a.Blocks, a.realBlocks())
	}
	if a.KVHeads == 0 && !a.PerLayerKVHeads && !a.modernKVLayout() {
		// On a pre-GQA artifact an absent key really does mean every head
		// carries its own KV, so the head count is the right substitute.
		//
		// It is the wrong substitute when the source simply did not report the
		// key. Ollama's /api/show returns a null head_count_kv for current
		// hybrid artifacts whose file carries the real value, and taking the
		// full head count there charged 24 heads where the model uses 4: a six
		// times over-projection that told the operator to cut their context.
		// An architecture that declares a modern KV layout is not pre-GQA, so
		// an absent count is unmeasured rather than equal to the head count.
		a.KVHeads = a.Heads
	}
	a.KeyLength = archDim(first(kvs, p+"attention.key_length"))
	a.ValLength = archDim(first(kvs, p+"attention.value_length"))
	a.MaxCtx = archDim(first(kvs, p+"context_length"))
	a.Experts = archDim(first(kvs, p+"expert_count"))
	a.ExpertUsed = archDim(first(kvs, p+"expert_used_count"))
	a.FFN = archDim(first(kvs, p+"expert_feed_forward_length", p+"feed_forward_length"))
	a.SSMInnerSize = archDim(first(kvs, p+"ssm.inner_size"))
	a.SSMStateSize = archDim(first(kvs, p+"ssm.state_size"))
	a.SSMConvKernel = archDim(first(kvs, p+"ssm.conv_kernel"))
	a.SlidingWindow = archDim(first(kvs, p+"attention.sliding_window"))
	a.SlidingWindowPattern = archDim(first(kvs, p+"attention.sliding_window_pattern"))
	a.KeyLengthSWA = archDim(first(kvs, p+"attention.key_length_swa"))
	a.ValLengthSWA = archDim(first(kvs, p+"attention.value_length_swa"))
	a.FullAttentionInterval = archDim(first(kvs, p+"full_attention_interval"))
	// Keys.Attention.RECURRENT_LAYERS in llama.cpp gguf-py/gguf/constants.py,
	// checked against master on 2026-09-11. An earlier spelling of this name
	// was emitted by nothing, so the hybrid branch behind it never fired.
	a.RecurrentLayers = archDim(first(kvs, p+"attention.recurrent_layers", "attention.recurrent_layers"))
	a.Hybrid = a.FullAttentionInterval > 0 || a.RecurrentLayers > 0 || inherentHybridArchitecture(arch)
	return a
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
	n, ok := v.([]any)
	return ok && len(n) > 1
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
		n := asInt64(entry)
		if n < 0 || n > maxArchDim {
			return nil
		}
		dimensions = append(dimensions, int(n))
	}
	return dimensions
}

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
		// A one-element array is an unambiguous scalar. A longer one is a
		// per-layer array, and its first element is not the value for every
		// layer: collapsing it silently reports one layer's dimension as the
		// whole model's. Unmeasured is the honest answer, and it routes to
		// SKIP rather than to a fabricated projection.
		if len(n) != 1 {
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
