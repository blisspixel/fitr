package advise

import (
	"bytes"
	"strings"
	"testing"
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

func TestHybridArchitectureRequiresMeasuredAllocation(t *testing.T) {
	kvs := map[string]any{
		"general.architecture":                   "qwen35",
		"qwen35.block_count":                     uint64(64),
		"qwen35.attention.head_count_kv":         uint64(4),
		"qwen35.attention.key_length":            uint64(128),
		"qwen35.full_attention_interval":         uint64(4),
		"qwen35.attention.recurrent_layer_count": uint64(48),
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
