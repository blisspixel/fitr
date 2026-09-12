package advise

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/device"
)

// Qwen3-30B-A3B architecture as stored in GGUF / Ollama model_info.
// The lappy notes pin the field observation: 30B MoE (~3B active) at 24.8
// tok/s beat an 8B dense at 14.6. Active params, not total, is the point.
func qwen3MoE() Arch {
	return Arch{
		Name: "qwen3moe", Blocks: 48, Embed: 2048, Heads: 32, KVHeads: 4,
		KeyLength: 128, ValLength: 128, MaxCtx: 40960,
		Experts: 128, ExpertUsed: 8, FFN: 768, Vocab: 151936,
		Params: 30532132864,
	}
}

func llama8B() Arch {
	return Arch{
		Name: "llama", Blocks: 32, Embed: 4096, Heads: 32, KVHeads: 8,
		KeyLength: 128, ValLength: 128, MaxCtx: 131072,
		FFN: 14336, Vocab: 128256, Params: 8030261248,
	}
}

func TestMoEActiveParamsNotTotal(t *testing.T) {
	n, ok := qwen3MoE().ActiveParams()
	if !ok {
		t.Fatal("Qwen3-MoE active params must be computable from architecture")
	}
	// ~3.3B active, never the 30.5B total.
	if n < 2_500_000_000 || n > 4_000_000_000 {
		t.Fatalf("active = %d (%.2fB), want ~3B, not 30B", n, float64(n)/1e9)
	}
	dense, ok := llama8B().ActiveParams()
	if !ok || dense != 8030261248 {
		t.Fatalf("dense active must equal total, got %d ok=%v", dense, ok)
	}
	// The whole point of tracking active: a 30B MoE is closer to 3B than to 8B.
	if n > dense {
		t.Fatalf("30B MoE active (%.2fB) should be below the 8B dense (%.2fB); naive total-param math ranks them backwards",
			float64(n)/1e9, float64(dense)/1e9)
	}
}

func TestMoEActiveUnknownWithoutFFN(t *testing.T) {
	a := qwen3MoE()
	a.FFN = 0
	if _, ok := a.ActiveParams(); ok {
		t.Fatal("must not invent active params when expert FFN length is missing")
	}
}

func TestObservedResidentBeatsEstimate(t *testing.T) {
	r := Evaluate(Input{
		Model: "qwen3:30b", HaveGB: 24, HaveSrc: "nvidia-smi",
		ResidentB: 19 * GiB, ResidentSrc: "ollama /api/ps",
	})
	if r.Tier != Compatible {
		t.Fatalf("tier = %s (%s), want compatible from a live resident", r.Tier, r.Why)
	}
	if r.ObservedGB != 19.0 || !strings.Contains(r.Why, "measured") {
		t.Fatalf("must say the GB was measured: %+v", r)
	}
	if r.NeedGB != 19.0 {
		t.Fatalf("need_gb = %v, want the observed 19, not an invented KV", r.NeedGB)
	}
	if strings.Contains(r.Why, "weights + KV") {
		t.Fatal("observed path must not sell the estimate as the number")
	}
}

func TestObservedExceedingBudgetIsSkipNotIncompatible(t *testing.T) {
	r := Evaluate(Input{
		HaveGB: 8, HaveSrc: "--vram-gb",
		ResidentB: 19 * GiB, ResidentSrc: "ollama /api/ps",
	})
	if r.Tier != Skip {
		t.Fatalf("tier = %s, want skip - the process is running", r.Tier)
	}
	if r.Remedy != "" {
		t.Fatalf("must not invent a ctx flag when the budget reading is the suspect: %q", r.Remedy)
	}
	if !strings.Contains(r.Why, "suspect") {
		t.Fatalf("why = %q, want the budget called out", r.Why)
	}
	if r.ExitCode() != 0 {
		t.Fatal("SKIP is not a failed measurement")
	}
}

func TestEvaluateCorePreservesEvidencePrecedence(t *testing.T) {
	tests := []struct {
		name   string
		input  Input
		tier   string
		why    string
		source string
		needGB float64
	}{
		{
			name:  "unknown budget precedes load failure",
			input: Input{LoadErr: "out of memory", ResidentB: 6 * GiB},
			tier:  Skip, why: "GPU memory was not measured; model is resident at 6.0 GB",
		},
		{
			name:  "load failure precedes resident allocation",
			input: Input{HaveGB: 8, LoadErr: "out of memory", ResidentB: 9 * GiB},
			tier:  Incompatible, why: "server refused the load: out of memory", source: "observed load failure",
		},
		{
			name:  "resident allocation precedes hybrid projection refusal",
			input: Input{HaveGB: 8, ResidentB: 6 * GiB, Arch: Arch{Hybrid: true}},
			tier:  Compatible, why: "resident 6.0 GB of 8.0 GB available (measured)", source: "observed resident", needGB: 6,
		},
		{
			name:  "hybrid projection refusal precedes missing weights",
			input: Input{HaveGB: 8, Arch: Arch{Hybrid: true}},
			tier:  Skip, why: "hybrid recurrent architecture cannot be safely projected from weights plus a conventional KV cache",
		},
		{
			name:   "allocator fit precedes weight arithmetic",
			input:  Input{HaveGB: 8, FitB: 6 * GiB, WeightsB: 9 * GiB},
			tier:   Skip,
			why:    "llama-fit-params projected 6.0 GB of device memory, but its effective context and placement were not captured",
			needGB: 6,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := evaluateCore(test.input)
			if got.Tier != test.tier || got.Why != test.why || got.Source != test.source || got.NeedGB != test.needGB {
				t.Fatalf("evaluateCore() = %+v", got)
			}
		})
	}
}

func TestSkipWhenVRAMUnknownMentionsResident(t *testing.T) {
	r := Evaluate(Input{ResidentB: 19 * GiB})
	if r.Tier != Skip || !strings.Contains(r.Why, "19.0 GB") {
		t.Fatalf("unmeasured VRAM should still disclose the resident size: %q", r.Why)
	}
}

func TestSkipWhenVRAMUnknown(t *testing.T) {
	r := Evaluate(Input{Model: "x", WeightsB: 5 * GiB, Arch: llama8B()})
	if r.Tier != Skip {
		t.Fatalf("tier = %s, want skip", r.Tier)
	}
	if r.HaveGB != 0 {
		t.Fatalf("unmeasured VRAM leaked into have_gb=%v", r.HaveGB)
	}
	if strings.Contains(r.Why, "0.0") {
		t.Fatalf("why printed a fabricated 0.0: %q", r.Why)
	}
	var buf bytes.Buffer
	Write(&buf, r)
	out := buf.String()
	if !strings.Contains(out, "[SKIP") {
		t.Fatalf("printed report must say SKIP:\n%s", out)
	}
	if strings.Contains(out, "0.0 GB available") || strings.Contains(out, "memory         0.0") {
		t.Fatalf("SKIP must not print 0.0 GB as a budget:\n%s", out)
	}
	if r.ExitCode() != 0 {
		t.Fatal("SKIP is not a failed measurement")
	}
}

func TestSkipWhenWeightsUnknown(t *testing.T) {
	r := Evaluate(Input{HaveGB: 8, HaveSrc: "--vram-gb", Arch: llama8B()})
	if r.Tier != Skip {
		t.Fatalf("tier = %s, want skip", r.Tier)
	}
	if strings.Contains(r.Why, "8.0 GB") && strings.Contains(r.Why, "needs") {
		t.Fatalf("must not size a model with no weights: %q", r.Why)
	}
}

func TestSkipWhenArchitectureMissingButWeightsFit(t *testing.T) {
	r := Evaluate(Input{WeightsB: 5 * GiB, HaveGB: 8, HaveSrc: "nvidia-smi"})
	if r.Tier != Skip {
		t.Fatalf("tier = %s, want skip (KV not sized)", r.Tier)
	}
	if r.NeedGB != 0 {
		t.Fatalf("need_gb = %v; KV was not measured", r.NeedGB)
	}
}

func TestIncompatibleWhenWeightsExceedVRAM(t *testing.T) {
	r := Evaluate(Input{
		Model: "qwen3:30b", WeightsB: 19 * GiB, HaveGB: 8, HaveSrc: "--vram-gb",
		Arch: qwen3MoE(),
	})
	if r.Tier != Incompatible {
		t.Fatalf("tier = %s, want incompatible", r.Tier)
	}
	if r.Remedy == "" {
		t.Fatal("incompatible must carry a remedy")
	}
	if strings.Contains(strings.ToLower(r.Remedy), "num_ctx") {
		t.Fatalf("shrinking context cannot fix weights that do not fit: %q", r.Remedy)
	}
	if r.ExitCode() != 3 {
		t.Fatalf("exit = %d, want 3", r.ExitCode())
	}
}

func TestLowMemoryNamesFlagAndNumber(t *testing.T) {
	// 5 GiB weights, 8 GiB budget, Llama-8B GQA.
	// KV per token = 32 * (8*128 + 8*128) * 2 = 131072 bytes.
	// At 131072 ctx: 16 GiB KV + 5 GiB weights = 21 GiB → does not fit.
	// Remaining for KV = 3 GiB → 24576 tokens exactly.
	r := Evaluate(Input{
		Model: "llama3.1:8b", Quant: "Q4_K_M",
		WeightsB: 5 * GiB, HaveGB: 8, HaveSrc: "--vram-gb",
		Ctx: 131072, Arch: llama8B(), Backend: "ollama",
	})
	if r.Tier != LowMemory {
		t.Fatalf("tier = %s (%s), want low_memory", r.Tier, r.Why)
	}
	if r.Flag != "num_ctx" || r.FlagValue != 24576 {
		t.Fatalf("remedy flag = %s=%d, want num_ctx=24576", r.Flag, r.FlagValue)
	}
	if r.FitsGB != 8.0 {
		t.Fatalf("fits_gb = %v, want 8.0", r.FitsGB)
	}
	if !strings.Contains(r.Remedy, "num_ctx=24576") || !strings.Contains(r.Remedy, "8.0 GB") {
		t.Fatalf("remedy = %q, want flag and resulting GB", r.Remedy)
	}
	var buf bytes.Buffer
	Write(&buf, r)
	out := buf.String()
	if !strings.Contains(out, "low memory") || !strings.Contains(out, "try") {
		t.Fatalf("printed report missing the remedy:\n%s", out)
	}
}

func TestCompatibleAtShortContext(t *testing.T) {
	r := Evaluate(Input{
		WeightsB: 5 * GiB, HaveGB: 8, HaveSrc: "--vram-gb",
		Ctx: 8192, Arch: llama8B(),
	})
	if r.Tier != Compatible {
		t.Fatalf("tier = %s (%s), want compatible", r.Tier, r.Why)
	}
	if r.Remedy != "" {
		t.Fatalf("compatible must not invent a remedy, got %q", r.Remedy)
	}
	// 5 GiB weights + 1 GiB KV = 6 GiB
	if r.NeedGB != 6.0 {
		t.Fatalf("need_gb = %v, want 6.0", r.NeedGB)
	}
}

func TestLlamaServerFlagName(t *testing.T) {
	r := Evaluate(Input{
		WeightsB: 5 * GiB, HaveGB: 8, HaveSrc: "x",
		Ctx: 131072, Arch: llama8B(), Backend: "llama-server",
	})
	if r.Flag != "--ctx-size" || !strings.Contains(r.Remedy, "--ctx-size=24576") {
		t.Fatalf("llama-server remedy = %q flag=%s", r.Remedy, r.Flag)
	}
}

func TestOpenAICompatDoesNotGuessAFlag(t *testing.T) {
	r := Evaluate(Input{
		WeightsB: 5 * GiB, HaveGB: 8, HaveSrc: "x",
		Ctx: 131072, Arch: llama8B(), Backend: "openai",
	})
	if r.Flag != "" {
		t.Fatalf("unknown OpenAI-compat flag must stay empty, got %q", r.Flag)
	}
	if !strings.Contains(r.Remedy, "shorter context") || !strings.Contains(r.Remedy, "24576") {
		t.Fatalf("remedy = %q, want a context number without a guessed flag", r.Remedy)
	}
}

func TestQwen3MoEFits24GBAtFullCtx(t *testing.T) {
	// KV per token = 48 * (4*128 + 4*128) * 2 = 98304
	// at 40960: 3.75 GiB KV. 19 + 3.75 = 22.75 < 24 → compatible.
	r := Evaluate(Input{
		WeightsB: 19 * GiB, HaveGB: 24, HaveSrc: "nvidia-smi",
		Arch: qwen3MoE(),
	})
	if r.Tier != Compatible {
		t.Fatalf("tier = %s (%s), want compatible", r.Tier, r.Why)
	}
	if !r.ActiveKnown || r.ActiveParamsB < 2.5 || r.ActiveParamsB > 4 {
		t.Fatalf("active_params_b = %v, want ~3", r.ActiveParamsB)
	}
}

func TestNeverPrintsFabricatedPrecisionOnSkip(t *testing.T) {
	r := Evaluate(Input{Model: "mystery", WeightsB: 7 * GiB})
	var buf bytes.Buffer
	Write(&buf, r)
	out := buf.String()
	for _, bad := range []string{"±", "+/-", "compatible", "low memory"} {
		if strings.Contains(strings.ToLower(out), bad) && !strings.Contains(out, "SKIP") {
			t.Fatalf("skip report contains %q:\n%s", bad, out)
		}
	}
	if strings.Contains(out, "try num_ctx") {
		t.Fatalf("SKIP must not invent a context remedy:\n%s", out)
	}
}

func TestArchFromKVsQwen3MoE(t *testing.T) {
	kvs := map[string]any{
		"general.architecture":                "qwen3moe",
		"general.parameter_count":             float64(30532132864), // JSON number
		"qwen3moe.block_count":                48,
		"qwen3moe.embedding_length":           2048,
		"qwen3moe.attention.head_count":       32,
		"qwen3moe.attention.head_count_kv":    4,
		"qwen3moe.attention.key_length":       128,
		"qwen3moe.attention.value_length":     128,
		"qwen3moe.context_length":             40960,
		"qwen3moe.expert_count":               128,
		"qwen3moe.expert_used_count":          8,
		"qwen3moe.expert_feed_forward_length": 768,
	}
	a := ArchFromKVs(kvs)
	want := qwen3MoE()
	if a.Blocks != want.Blocks || a.KVHeads != want.KVHeads || a.KeyLength != want.KeyLength ||
		a.Experts != want.Experts || a.ExpertUsed != want.ExpertUsed || a.FFN != want.FFN ||
		a.Params != want.Params {
		t.Fatalf("ArchFromKVs = %+v, want %+v", a, want)
	}
	if !a.KVReady() {
		t.Fatal("Qwen3-MoE metadata must be enough to size KV")
	}
}

func TestArchFromKVsEmptyIsNotReady(t *testing.T) {
	if ArchFromKVs(nil).KVReady() || ArchFromKVs(map[string]any{}).KVReady() {
		t.Fatal("empty metadata must not look KV-ready")
	}
}

func TestProjectKVBytesUsesDeclaredArchitectureAndContext(t *testing.T) {
	arch := Arch{Blocks: 2, KVHeads: 4, KeyLength: 8, ValLength: 8}
	got, ok := ProjectKVBytes(arch, 1024, 2)
	if !ok {
		t.Fatal("ProjectKVBytes unexpectedly unavailable")
	}
	const want = int64(2 * (4*8 + 4*8) * 2 * 1024)
	if got != want {
		t.Fatalf("ProjectKVBytes = %d, want %d", got, want)
	}
}

func TestProjectKVBytesRefusesIncompleteOrUnsupportedInputs(t *testing.T) {
	ready := Arch{Blocks: 2, KVHeads: 4, KeyLength: 8, ValLength: 8}
	tests := []struct {
		name string
		arch Arch
		ctx  int
		elem float64
	}{
		{name: "hybrid", arch: Arch{Hybrid: true, Blocks: 2, KVHeads: 4, KeyLength: 8}, ctx: 1024, elem: 2},
		{name: "missing metadata", arch: Arch{}, ctx: 1024, elem: 2},
		{name: "zero context", arch: ready, ctx: 0, elem: 2},
		{name: "unknown element size", arch: ready, ctx: 1024, elem: 0},
		{name: "overflow", arch: Arch{Blocks: math.MaxInt, KVHeads: math.MaxInt, KeyLength: math.MaxInt}, ctx: math.MaxInt, elem: math.MaxFloat64},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := ProjectKVBytes(tc.arch, tc.ctx, tc.elem); ok || got != 0 {
				t.Fatalf("ProjectKVBytes = %d, %v; want unavailable", got, ok)
			}
		})
	}
}

// Per-layer head_count_kv is what current sliding-window and hybrid artifacts
// publish. Reading element 0 as the whole model's KV head count produced a
// projection tens of times the truth, and KVReady still passed, so advise
// printed an incompatible verdict for a model that fits comfortably.
func TestPerLayerKVHeadsAreUnmeasuredRatherThanTheFirstLayer(t *testing.T) {
	perLayer := make([]any, 48)
	for i := range perLayer {
		perLayer[i] = uint64(8)
	}
	perLayer[5] = uint64(1)
	kvs := map[string]any{
		"general.architecture":            "gemma4",
		"gemma4.block_count":              uint64(48),
		"gemma4.attention.head_count":     uint64(16),
		"gemma4.attention.head_count_kv":  perLayer,
		"gemma4.attention.key_length":     uint64(256),
		"gemma4.attention.value_length":   uint64(256),
		"gemma4.attention.sliding_window": uint64(1024),
	}
	arch := ArchFromKVs(kvs)
	if arch.KVHeads != 0 || !arch.PerLayerKVHeads {
		t.Fatalf("per-layer head_count_kv must not become a scalar: %+v", arch)
	}
	// The per-layer heads are readable here, so the refusal has to come from
	// the sliding window rather than from a missing count. Which layers slide
	// is not in the artifact, so the cache length per layer is unknown.
	if len(arch.KVHeadsPerLayer) != 48 {
		t.Fatalf("per-layer heads = %v, want one entry per block", arch.KVHeadsPerLayer)
	}
	if arch.kvClassifiable() {
		t.Fatal("an artifact declaring a sliding window must not be treated as uniform full attention")
	}
	if arch.KVReady() {
		t.Fatal("KVReady must be false while layer cache lengths are unknown")
	}
	if _, ok := ProjectKVBytes(arch, 131072, 2); ok {
		t.Fatal("a sliding-window artifact must not be projected as full attention")
	}
	if arch.ShapeClass() == "dense" {
		t.Fatal("a per-layer KV layout must not be labeled dense")
	}
	report := Evaluate(Input{
		Model: "gemma", WeightsB: 7 * GiB, HaveGB: 24, HaveSrc: "nvidia-smi",
		Ctx: 131072, Arch: arch,
	})
	if report.Tier != Skip {
		t.Fatalf("tier = %s (%s), want skip rather than a projected verdict", report.Tier, report.Why)
	}
}

// The zero-fallback exists for pre-GQA metadata that omits head_count_kv. A
// per-layer array whose first entries are zero -- layers that do not attend --
// must not reach it, or the model is charged the full head count on every
// layer: the largest cache its shape could ever need.
func TestLeadingZeroInAPerLayerArrayDoesNotBecomeTheFullHeadCount(t *testing.T) {
	perLayer := make([]any, 42)
	for i := range perLayer {
		perLayer[i] = uint64(0)
	}
	perLayer[9], perLayer[19], perLayer[29], perLayer[39] = uint64(8), uint64(8), uint64(8), uint64(8)
	kvs := map[string]any{
		"general.architecture":               "nemotron_h",
		"nemotron_h.block_count":             uint64(42),
		"nemotron_h.attention.head_count":    uint64(40),
		"nemotron_h.attention.head_count_kv": perLayer,
		"nemotron_h.attention.key_length":    uint64(128),
		"nemotron_h.attention.value_length":  uint64(128),
	}
	arch := ArchFromKVs(kvs)
	if arch.KVHeads == 40 {
		t.Fatal("a per-layer array must not fall back to the full head count")
	}
	// Only four of the 42 layers attend, so the cache is sized from 32 KV
	// heads, not from 42 layers of 40. The naive figure is 52 times larger.
	if got, want := arch.totalKVHeads(), 4*8; got != want {
		t.Fatalf("total KV heads = %d, want %d summed across layers", got, want)
	}
	projected, ok := ProjectKVBytes(arch, 8192, 2)
	if !ok {
		t.Fatal("a per-layer array with no sliding window is exactly projectable")
	}
	if want := int64(32 * (128 + 128) * 2 * 8192); projected != want {
		t.Fatalf("KV projection = %d, want %d", projected, want)
	}
	naive := int64(42 * 40 * (128 + 128) * 2 * 8192)
	if projected >= naive {
		t.Fatalf("projection %d did not improve on the full-head-count figure %d", projected, naive)
	}
}

// inherentHybridArchitecture is a deliberately short name list. A hybrid model
// that declares its recurrent layers must be recognized from that declaration,
// so new families do not depend on the list being grown for each one.
func TestRecurrentLayersKeyMarksAnUnlistedHybrid(t *testing.T) {
	kvs := map[string]any{
		"general.architecture":                  "nemotron_h",
		"nemotron_h.block_count":                uint64(42),
		"nemotron_h.attention.head_count":       uint64(40),
		"nemotron_h.attention.head_count_kv":    uint64(8),
		"nemotron_h.attention.key_length":       uint64(128),
		"nemotron_h.attention.value_length":     uint64(128),
		"nemotron_h.attention.recurrent_layers": uint64(38),
	}
	if inherentHybridArchitecture("nemotron_h") {
		t.Fatal("this test is meaningless if the architecture is already on the name list")
	}
	arch := ArchFromKVs(kvs)
	if arch.RecurrentLayers != 38 || !arch.Hybrid {
		t.Fatalf("recurrent_layers must mark the model hybrid: %+v", arch)
	}
	report := Evaluate(Input{
		Model: "nemotron", WeightsB: 3 * GiB, HaveGB: 24, HaveSrc: "nvidia-smi",
		Ctx: 1 << 20, Arch: arch,
	})
	if report.Tier != Skip {
		t.Fatalf("tier = %s (%s), want skip for a hybrid without a load receipt", report.Tier, report.Why)
	}
}

// The header below is the published Qwen3.8-27B UD-Q4_K_XL GGUF, whose
// layer layout is confirmed by the model's own config.json: linear attention
// three layers out of every four, so 16 of its 64 real layers attend. A
// blanket hybrid refusal made advise silent about one of the most widely run
// local models when its arithmetic is fully determined here.
func qwen38KVs() map[string]any {
	return map[string]any{
		"general.architecture":           "qwen35",
		"qwen35.block_count":             uint64(65), // 64 real plus one MTP head
		"qwen35.context_length":          uint64(262144),
		"qwen35.embedding_length":        uint64(5120),
		"qwen35.attention.head_count":    uint64(24),
		"qwen35.attention.head_count_kv": uint64(4),
		"qwen35.attention.key_length":    uint64(256),
		"qwen35.attention.value_length":  uint64(256),
		"qwen35.full_attention_interval": uint64(4),
		"qwen35.nextn_predict_layers":    uint64(1),
		"qwen35.ssm.conv_kernel":         uint64(4),
		"qwen35.ssm.state_size":          uint64(128),
		"qwen35.ssm.inner_size":          uint64(6144),
	}
}

func TestIntervalHybridChargesOnlyItsFullAttentionLayers(t *testing.T) {
	arch := ArchFromKVs(qwen38KVs())
	if !arch.Hybrid {
		t.Fatal("an interval hybrid is still hybrid")
	}
	if got := arch.realBlocks(); got != 64 {
		t.Fatalf("real blocks = %d, want block_count less the MTP head", got)
	}
	if got := arch.fullAttentionLayers(); got != 16 {
		t.Fatalf("full-attention layers = %d, want 64/4", got)
	}
	if !arch.KVReady() {
		t.Fatalf("a fully determined interval hybrid is sizable: %+v", arch)
	}
	// 16 attending layers x 4 KV heads x (256+256) x 2 bytes = 64 KiB/token.
	if got := arch.kvBytesPerToken(2); got != 65536 {
		t.Fatalf("KV bytes per token = %v, want 65536", got)
	}
	// Charging all 64 layers would have reported four times the cache.
	projected, ok := ProjectKVBytes(arch, 32768, 2)
	if !ok {
		t.Fatal("a fully determined interval hybrid projects")
	}
	fixed, ok := arch.recurrentStateBytes()
	if !ok {
		t.Fatal("the recurrent state is determined by these keys")
	}
	if want := int64(65536*32768) + int64(fixed); projected != want {
		t.Fatalf("projection = %d, want %d including the recurrent state", projected, want)
	}
	// The recurrent half does not grow with context; the scaling half does.
	larger, _ := ProjectKVBytes(arch, 65536, 2)
	if larger-projected != 65536*32768 {
		t.Fatalf("the fixed state scaled with context: %d then %d", projected, larger)
	}
}

// The recurrent state is a real allocation, so a hybrid whose shape the
// artifact does not carry stays unsizable rather than being projected from its
// attention layers alone.
func TestIntervalHybridWithoutItsRecurrentShapeStaysUnsizable(t *testing.T) {
	for _, missing := range []string{
		"qwen35.ssm.inner_size", "qwen35.ssm.state_size", "qwen35.ssm.conv_kernel",
	} {
		t.Run(missing, func(t *testing.T) {
			kvs := qwen38KVs()
			delete(kvs, missing)
			arch := ArchFromKVs(kvs)
			if arch.KVReady() {
				t.Fatalf("sized a hybrid with no %s", missing)
			}
			if _, ok := ProjectKVBytes(arch, 32768, 2); ok {
				t.Fatalf("projected a hybrid with no %s", missing)
			}
		})
	}
}

func TestHybridArchitectureRequiresMeasuredAllocation(t *testing.T) {
	kvs := map[string]any{
		"general.architecture":           "qwen35",
		"qwen35.block_count":             uint64(64),
		"qwen35.attention.head_count_kv": uint64(4),
		"qwen35.attention.key_length":    uint64(128),
		"qwen35.full_attention_interval": uint64(4),
		// The key llama.cpp writes is attention.recurrent_layers. fitr read
		// attention.recurrent_layer_count, which nothing emits, and this test
		// asserted the same invented name -- so the branch could never fire in
		// production and CI could never notice.
		"qwen35.attention.recurrent_layers": uint64(48),
	}
	arch := ArchFromKVs(kvs)
	if !arch.Hybrid || arch.FullAttentionInterval != 4 || arch.RecurrentLayers != 48 {
		t.Fatalf("hybrid metadata = %+v", arch)
	}
	report := Evaluate(Input{
		Model: "qwen", WeightsB: 20 * GiB, HaveGB: 32, HaveSrc: "test",
		Ctx: 8192, Arch: arch,
	})
	if report.Tier != Skip || !strings.Contains(report.Why, "hybrid recurrent") {
		t.Fatalf("unmeasured hybrid estimate = %+v", report)
	}

	report = Evaluate(Input{
		Model: "qwen", WeightsB: 20 * GiB, HaveGB: 32, HaveSrc: "test",
		Ctx: 8192, Arch: arch, ResidentB: 24 * GiB, ResidentCtx: 8192,
		ResidentSrc: "runtime",
	})
	if report.Tier != Compatible || report.NeedGB != 24 {
		t.Fatalf("measured hybrid allocation = %+v", report)
	}
}

func TestInherentHybridArchitectureDoesNotInventInterval(t *testing.T) {
	arch := ArchFromKVs(map[string]any{"general.architecture": "qwen35moe"})
	if !arch.Hybrid || arch.FullAttentionInterval != 0 {
		t.Fatalf("name-only hybrid = %+v", arch)
	}
	if arch.ShapeClass() != "hybrid-linear" {
		t.Fatalf("shape = %q", arch.ShapeClass())
	}
	if arch.KVStrategy() != "hybrid-linear" {
		t.Fatalf("must not invent interval-4: %q", arch.KVStrategy())
	}
}

func TestCompactLabelUsesMetadataOnly(t *testing.T) {
	moe := ArchFromKVs(map[string]any{
		"general.architecture": "qwen3moe", "qwen3moe.block_count": uint64(48),
		"qwen3moe.embedding_length": uint64(2048), "qwen3moe.attention.head_count": uint64(32),
		"qwen3moe.attention.head_count_kv": uint64(4), "qwen3moe.attention.key_length": uint64(128),
		"qwen3moe.attention.value_length": uint64(128), "qwen3moe.expert_count": uint64(128),
		"qwen3moe.expert_used_count": uint64(8), "qwen3moe.expert_feed_forward_length": uint64(768),
	})
	if got := moe.CompactLabel(); got != "moe 8/128  full-kv" {
		t.Fatalf("moe label = %q", got)
	}
	dense := llama8B()
	if got := dense.CompactLabel(); got != "dense  full-kv" {
		t.Fatalf("dense label = %q", got)
	}
	if got := (Arch{}).CompactLabel(); got != "" {
		t.Fatalf("empty arch invented a label: %q", got)
	}
}

func TestKVElemBytesUnknownIsNotInvented(t *testing.T) {
	if _, ok := KVElemBytes("mystery"); ok {
		t.Fatal("unknown dtype must not invent a packing")
	}
	if n, ok := KVElemBytes("q8_0"); !ok || n != 1 {
		t.Fatalf("q8_0 = %v %v", n, ok)
	}
}

func TestContextFlag(t *testing.T) {
	if ContextFlag("ollama") != "num_ctx" {
		t.Fatal(ContextFlag("ollama"))
	}
	if ContextFlag("llama-server") != "--ctx-size" {
		t.Fatal(ContextFlag("llama-server"))
	}
	if ContextFlag("openai") != "" {
		t.Fatal("openai must not guess a flag")
	}
}

func TestPlanApplyNeverMutates(t *testing.T) {
	p := PlanApply("ollama", "qwen3:30b", 4096)
	if p.Mutates {
		t.Fatal("apply must not claim to mutate the server")
	}
	if !strings.Contains(p.Note, "does not restart") {
		t.Fatalf("note = %q", p.Note)
	}
	joined := strings.Join(p.Steps, "\n")
	for _, want := range []string{"num_ctx 4096", "ollama create qwen3:30b-ctx4096"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("ollama plan missing %q:\n%s", want, joined)
		}
	}
	llama := PlanApply("llama-server", "", 4096)
	if llama.Mutates || !strings.Contains(strings.Join(llama.Steps, "\n"), "--ctx-size 4096") {
		t.Fatalf("llama-server plan: %+v", llama)
	}
	if strings.Contains(strings.Join(llama.Steps, "\n"), "ollama create") {
		t.Fatal("a llama-server plan must not print Ollama commands")
	}
	open := PlanApply("openai", "", 4096)
	if !strings.Contains(strings.Join(open.Steps, "\n"), "--max-model-len 4096") {
		t.Fatalf("openai plan: %+v", open)
	}
	all := PlanApply("", "m", 2048)
	if all.Backend != "" || all.Mutates {
		t.Fatal("unknown backend must print every recipe, still without mutating")
	}
	var buf strings.Builder
	WriteApply(&buf, p)
	out := buf.String()
	if !strings.Contains(out, "does not restart") || !strings.Contains(out, "qwen3:30b") {
		t.Fatalf("WriteApply:\n%s", out)
	}
	p.ServingKnown, p.ServingCtx = true, 4096
	buf.Reset()
	WriteApply(&buf, p)
	if !strings.Contains(buf.String(), "already has this window") {
		t.Fatalf("matching serving ctx:\n%s", buf.String())
	}
}

func TestHumanWritersNeutralizeTerminalControls(t *testing.T) {
	hostile := "safe\x1b[2J\x1b]0;forged title\a\x1bPforged payload\x1b\\\r\n\tleft\u202eright"
	var reportOut strings.Builder
	Write(&reportOut, Report{
		Model: hostile, Quant: hostile, Tier: Skip, Why: hostile, Remedy: hostile,
		Hint: hostile, Gaps: []string{hostile}, Source: hostile, HaveSource: hostile,
	})
	var applyOut strings.Builder
	WriteApply(&applyOut, PlanApply("ollama", hostile, 4096))
	for name, got := range map[string]string{"report": reportOut.String(), "apply": applyOut.String()} {
		if strings.ContainsAny(got, "\x1b\a\r\t\u202e") || strings.Contains(got, "forged title") ||
			strings.Contains(got, "forged payload") {
			t.Fatalf("%s leaked terminal controls: %q", name, got)
		}
		if !strings.Contains(got, "safe") || !strings.Contains(got, "leftright") {
			t.Fatalf("%s lost ordinary text: %q", name, got)
		}
	}
}

// The contract says every negative fit verdict carries a remedy. When the KV
// cache is what overflowed, dtype is a third lever next to "shorter window"
// and "smaller quant", and advise already computes with it -- q8_0 halves
// bytes per element, so the window it buys back is exactly double.
func TestKVCacheRemedyOffersTheDtypeLever(t *testing.T) {
	// Same fixture as TestLowMemoryNamesFlagAndNumber: 3 GiB of room for KV,
	// 131072 bytes per token at f16 -> 24576. At q8_0, 65536 -> 49152.
	r := Evaluate(Input{
		Model: "llama3.1:8b", Quant: "Q4_K_M",
		WeightsB: 5 * GiB, HaveGB: 8, HaveSrc: "--vram-gb",
		Ctx: 131072, Arch: llama8B(), Backend: "ollama",
	})
	if r.Tier != LowMemory {
		t.Fatalf("tier = %s (%s), want low_memory", r.Tier, r.Why)
	}
	if !strings.Contains(r.KVRemedy, "OLLAMA_KV_CACHE_TYPE=q8_0") {
		t.Fatalf("kv remedy = %q, want the Ollama dtype knob", r.KVRemedy)
	}
	if !strings.Contains(r.KVRemedy, "49152") {
		t.Fatalf("kv remedy = %q, want the doubled window 49152", r.KVRemedy)
	}
	// Cache dtype is part of the device fingerprint. Offering it without
	// saying so would invite an incomparable measurement.
	if !strings.Contains(r.KVRemedy, "fingerprint") {
		t.Fatalf("kv remedy = %q, must say the fingerprint changes", r.KVRemedy)
	}
	var buf bytes.Buffer
	Write(&buf, r)
	if out := buf.String(); !strings.Contains(out, "or ") {
		t.Fatalf("printed report did not offer the alternative:\n%s", out)
	}
}

func TestKVCacheRemedyKeepsTheRequestedWindowWhenItFits(t *testing.T) {
	// 32768 ctx costs 4 GiB at f16 and 2 GiB at q8_0; 3 GiB of room means the
	// requested window survives the dtype change intact.
	r := Evaluate(Input{
		Model: "llama3.1:8b", WeightsB: 5 * GiB, HaveGB: 8, HaveSrc: "--vram-gb",
		Ctx: 32768, Arch: llama8B(), Backend: "ollama",
	})
	if r.Tier != LowMemory {
		t.Fatalf("tier = %s (%s), want low_memory", r.Tier, r.Why)
	}
	if !strings.Contains(r.KVRemedy, "keeps 32768 ctx") {
		t.Fatalf("kv remedy = %q, want the requested window kept", r.KVRemedy)
	}
	if !strings.Contains(r.KVRemedy, "7.0 GB") {
		t.Fatalf("kv remedy = %q, want the resulting 7.0 GB", r.KVRemedy)
	}
}

func TestKVCacheRemedyStaysSilentWhenItCannotHelp(t *testing.T) {
	base := Input{
		Model: "llama3.1:8b", WeightsB: 5 * GiB, HaveGB: 8, HaveSrc: "--vram-gb",
		Ctx: 131072, Arch: llama8B(), Backend: "ollama",
	}
	t.Run("already quantized", func(t *testing.T) {
		in := base
		in.KVBytes, in.KVSrc = 1, "OLLAMA_KV_CACHE_TYPE=q8_0"
		if r := Evaluate(in); r.KVRemedy != "" {
			t.Fatalf("kv remedy = %q; q8_0 is already the remedy", r.KVRemedy)
		}
	})
	t.Run("no knob on the runtime", func(t *testing.T) {
		in := base
		in.Backend = "openai"
		if r := Evaluate(in); r.KVRemedy != "" {
			t.Fatalf("kv remedy = %q; must not name a flag the user cannot set", r.KVRemedy)
		}
	})
	t.Run("weights alone overflow", func(t *testing.T) {
		in := base
		in.WeightsB = 9 * GiB
		if r := Evaluate(in); r.KVRemedy != "" {
			t.Fatalf("kv remedy = %q; no cache dtype saves weights that do not fit", r.KVRemedy)
		}
	})
}

func TestKVCacheFlagNamesTheRuntimeKnob(t *testing.T) {
	for backend, want := range map[string]string{
		"ollama":       "OLLAMA_KV_CACHE_TYPE",
		"":             "OLLAMA_KV_CACHE_TYPE",
		"llama-server": "--cache-type-k/--cache-type-v",
		"openai":       "",
		"unknown":      "",
	} {
		if got := KVCacheFlag(backend); got != want {
			t.Fatalf("KVCacheFlag(%q) = %q, want %q", backend, got, want)
		}
	}
}

func TestAllocatorProjectionAtDefaultContextCannotEmitKVRemedy(t *testing.T) {
	r := Evaluate(Input{
		Model: "llama3.1:8b", WeightsB: 5 * GiB, HaveGB: 8, HaveSrc: "--vram-gb",
		Ctx: 0, Arch: llama8B(), Backend: "ollama",
		FitB: 9 * GiB, FitSrc: "llama-fit-params",
	})
	if r.Tier != Skip {
		t.Fatalf("tier = %s (%s), want skip", r.Tier, r.Why)
	}
	if r.KVRemedy != "" || r.FlagValue != 0 {
		t.Fatalf("unbound projection emitted a point-specific remedy: %+v", r)
	}
}

// The memory-source label is a trust decision keyed by exact string. Correcting
// the Apple Silicon budget changed that label, and without this test every
// macOS model would have silently become "unproven" instead of getting a fit
// verdict, on a platform with no CI hardware to notice.
func TestEveryMemorySourceFitrEmitsIsTrusted(t *testing.T) {
	for _, src := range []string{
		"nvidia-smi",
		"drm sysfs",
		device.AppleWiredLimitSource,
		device.AppleAssumedShareSource,
		device.AppleLegacyRAMSource, // saved results predating the correction
		device.NVIDIAUnifiedMemorySource,
		device.NVIDIAUnifiedProbeSource,
		"--vram-gb 24",
	} {
		if !memoryBudgetTrusted(src) {
			t.Errorf("fitr emits %q as a memory source but does not trust it, so "+
				"every model on that platform reads as unproven", src)
		}
	}
	for _, src := range []string{"", "guess", "unknown (not measured)"} {
		if memoryBudgetTrusted(src) {
			t.Errorf("%q must not be trusted enough to declare a model incompatible", src)
		}
	}
}

func TestNVIDIAUnifiedBudgetDisclosesUnknownCurrentAvailability(t *testing.T) {
	r := Evaluate(Input{
		Model: "model", WeightsB: 5 * GiB,
		HaveGB: 121.7, HaveSrc: device.NVIDIAUnifiedMemorySource,
		Ctx: 4096, Arch: llama8B(), Backend: "ollama",
	})
	if r.Tier != Skip {
		t.Fatalf("tier = %s (%s), want skip from addressable-only capacity", r.Tier, r.Why)
	}
	if r.FlagValue != 0 || r.FitsGB != 0 || r.KVRemedy != "" {
		t.Fatalf("addressable capacity emitted a fit claim: %+v", r)
	}
	sawAvailability, sawBudget := false, false
	for _, gap := range r.Gaps {
		if strings.Contains(gap, "whole-pool available memory was not measured") {
			sawAvailability = true
		}
		if strings.Contains(gap, "not a safe planning budget") {
			sawBudget = true
		}
	}
	if !sawAvailability || !sawBudget {
		t.Fatalf("unified addressable capacity gaps = %+v", r.Gaps)
	}
}

func TestResidentAtFallbackLoadContextDoesNotProveModelMaximum(t *testing.T) {
	r := Evaluate(Input{
		HaveGB: 24, HaveSrc: "nvidia-smi", WeightsB: 5 * GiB,
		Arch: llama8B(), ResidentB: 7 * GiB, ResidentCtx: 2048,
		ResidentSrc: "ollama /api/ps after --load",
	})
	if r.Source == "ollama /api/ps after --load" || strings.Contains(r.Why, "resident") {
		t.Fatalf("2K receipt proved the default max-context question: %+v", r)
	}
	if !strings.Contains(strings.Join(r.Gaps, " "), "does not prove") {
		t.Fatalf("missing context-mismatch gap: %+v", r.Gaps)
	}
}

func TestNVIDIAUnifiedCapacityDoesNotCallFullResidencyProjectionIncompatible(t *testing.T) {
	r := Evaluate(Input{
		Model: "model", WeightsB: 5 * GiB,
		HaveGB: 5.5, HaveSrc: device.NVIDIAUnifiedMemorySource,
		Ctx: 8192, Arch: llama8B(), Backend: "ollama",
	})
	if r.Tier != Skip {
		t.Fatalf("tier = %s (%s), want skip without resolved mmap or placement", r.Tier, r.Why)
	}
	if r.Flag != "" || r.FlagValue != 0 || r.FitsGB != 0 || r.KVRemedy != "" {
		t.Fatalf("physical lower-bound failure certified a shorter fit: %+v", r)
	}
	if !strings.Contains(r.Why, "mmap") || r.Hint == "" {
		t.Fatalf("projection gap did not explain its missing runtime evidence: %+v", r)
	}
}

func TestNVIDIAUnifiedBudgetAcceptsExactResidentReceipt(t *testing.T) {
	r := Evaluate(Input{
		Model: "model", WeightsB: 5 * GiB,
		HaveGB: 121.7, HaveSrc: device.NVIDIAUnifiedMemorySource,
		Ctx: 8192, Arch: llama8B(), Backend: "ollama",
		ResidentB: 7 * GiB, ResidentCtx: 8192, ResidentSrc: "ollama /api/ps after --load",
	})
	if r.Tier != Compatible || r.NeedGB != 7 || r.Ctx != 8192 {
		t.Fatalf("exact resident receipt = %+v, want measured compatible", r)
	}
}

func TestWholeSystemUnifiedBudgetFailsClosedAndAcceptsExactReceipt(t *testing.T) {
	for _, gpu := range []string{"AMD Radeon 780M", "Intel UHD Graphics 770"} {
		t.Run(gpu+" projection", func(t *testing.T) {
			r := Evaluate(Input{
				Model: gpu, WeightsB: 5 * GiB,
				HaveGB: 32, HaveSrc: device.AppleLegacyRAMSource,
				Ctx: 8192, Arch: llama8B(), Backend: "ollama",
			})
			if r.Tier != Skip || !strings.Contains(r.Why, "safe available shared memory was not measured") {
				t.Fatalf("automatic whole-system capacity = %+v, want fail-closed skip", r)
			}
			if r.FlagValue != 0 || r.FitsGB != 0 || r.KVRemedy != "" {
				t.Fatalf("automatic whole-system capacity emitted a fit claim: %+v", r)
			}
		})

		t.Run(gpu+" exact receipt", func(t *testing.T) {
			r := Evaluate(Input{
				Model: gpu, WeightsB: 5 * GiB,
				HaveGB: 32, HaveSrc: device.AppleLegacyRAMSource,
				Ctx: 8192, Arch: llama8B(), Backend: "ollama",
				ResidentB: 7 * GiB, ResidentCtx: 8192, ResidentSrc: "runtime status after load",
			})
			if r.Tier != Compatible || r.NeedGB != 7 || r.Source != "runtime status after load" {
				t.Fatalf("exact resident receipt = %+v, want measured compatible", r)
			}
		})
	}
}

func TestExplicitBudgetOverridesAutomaticWholeSystemCapacity(t *testing.T) {
	r := Evaluate(Input{
		WeightsB: 5 * GiB, HaveGB: 8, HaveSrc: "--vram-gb 8",
		Ctx: 8192, Arch: llama8B(), Backend: "ollama",
	})
	if r.Tier != Compatible || !strings.Contains(r.Why, "8.0 GB available") {
		t.Fatalf("declared planning budget = %+v, want compatible projection", r)
	}
}

func TestSharedNVIDIAUserBudgetSeparatesProjectionKinds(t *testing.T) {
	ordinary := Evaluate(Input{
		WeightsB: 5 * GiB, HaveGB: 8, HaveSrc: "--vram-gb",
		NVIDIAUnifiedMemory: true, Ctx: 8192, Arch: llama8B(),
	})
	if ordinary.Tier != Compatible {
		t.Fatalf("ordinary declared-budget projection = %+v, want compatible", ordinary)
	}
	withFit := Evaluate(Input{
		WeightsB: 5 * GiB, HaveGB: 8, HaveSrc: "--vram-gb",
		NVIDIAUnifiedMemory: true, Ctx: 8192, Arch: llama8B(),
		FitB: 7 * GiB, FitSrc: "llama-fit-params",
	})
	if withFit.Tier != Skip || !strings.Contains(withFit.Why, "effective context and placement were not captured") {
		t.Fatalf("device-only shared-memory fit = %+v, want skip", withFit)
	}
}

func TestNVIDIAUnifiedIdentityStaysAutomaticWithDedicatedProbeValue(t *testing.T) {
	for _, tc := range []struct {
		name    string
		unified bool
		want    string
	}{
		{name: "GB10 shared pool", unified: true, want: Skip},
		{name: "discrete NVIDIA VRAM", unified: false, want: Compatible},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Evaluate(Input{
				WeightsB: 5 * GiB, HaveGB: 121.7, HaveSrc: "nvidia-smi",
				NVIDIAUnifiedMemory: tc.unified, Ctx: 8192, Arch: llama8B(),
			})
			if r.Tier != tc.want {
				t.Fatalf("tier = %s (%s), want %s", r.Tier, r.Why, tc.want)
			}
			if tc.unified && !strings.Contains(r.Why, "safe available shared memory was not measured") {
				t.Fatalf("shared identity with nonzero probe = %+v, want automatic-capacity skip", r)
			}
		})
	}
}

func TestSharedNVIDIAResidentNeedsReportedRequestedContext(t *testing.T) {
	r := Evaluate(Input{
		HaveGB: 96, HaveSrc: "--vram-gb", NVIDIAUnifiedMemory: true,
		Ctx: 8192, ResidentB: 7 * GiB, ResidentSrc: "runtime status",
	})
	if r.Tier != Skip || r.Source == "runtime status" || !strings.Contains(strings.Join(r.Gaps, " "), "does not prove") {
		t.Fatalf("contextless shared resident = %+v, want unproven", r)
	}
}

// A refusal has to name the cause the reader can act on. Every case here was
// previously reported as missing metadata, which sent the reader looking for a
// better file when the file was already complete.
func TestUnsizableArchitectureNamesItsActualCause(t *testing.T) {
	perLayer := make([]any, 30)
	for i := range perLayer {
		perLayer[i] = uint64(8)
	}
	for _, test := range []struct {
		name string
		kvs  map[string]any
		want string
	}{
		{
			name: "sliding window",
			kvs: map[string]any{
				"general.architecture": "gemma4", "gemma4.block_count": uint64(30),
				"gemma4.attention.head_count": uint64(16), "gemma4.attention.head_count_kv": perLayer,
				"gemma4.attention.key_length": uint64(512), "gemma4.attention.value_length": uint64(512),
				"gemma4.attention.sliding_window": uint64(1024),
			},
			want: "sliding-window",
		},
		{
			name: "hybrid without its recurrent shape",
			kvs: map[string]any{
				"general.architecture": "qwen35", "qwen35.block_count": uint64(64),
				"qwen35.attention.head_count": uint64(24), "qwen35.attention.head_count_kv": uint64(4),
				"qwen35.attention.key_length": uint64(256), "qwen35.attention.value_length": uint64(256),
				"qwen35.full_attention_interval": uint64(4),
			},
			want: "recurrent state shape",
		},
		{
			name: "genuinely absent metadata",
			kvs:  map[string]any{"general.architecture": "llama"},
			want: "missing or not believable",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			arch := ArchFromKVs(test.kvs)
			if arch.KVReady() {
				t.Fatalf("this case is only meaningful when the cache is unsizable: %+v", arch)
			}
			note, hint := arch.UnsizableReason()
			if !strings.Contains(note, test.want) {
				t.Fatalf("note = %q, want it to name %q", note, test.want)
			}
			if hint == "" {
				t.Fatal("a refusal must carry a next action")
			}
			// The hint must not send the reader back for a file they supplied.
			if strings.Contains(note, "sliding-window") && strings.Contains(hint, "pass a GGUF") {
				t.Fatalf("hint %q asks for a better file where the file is already complete", hint)
			}
		})
	}
}
