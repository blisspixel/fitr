package device

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func ptr(n int) *int { return &n }

func validFingerprint() Fingerprint {
	return Fingerprint{
		Host: "workstation", OS: "linux", CPU: "cpu", RAMGb: 64,
		GPU: "gpu", GPUDriver: "550.1", GPUDriverDate: "2026-08-01",
		Runtime: "ollama 1.2.3", InferenceDevice: "GPU 100%",
		GPUBackend: "cuda", VRAMGb: 24, VRAMSource: "nvidia-smi",
		Config: map[string]string{
			"OLLAMA_FLASH_ATTENTION": "1",
			"OLLAMA_KV_CACHE_TYPE":   "q8_0",
			"OLLAMA_NUM_PARALLEL":    "1",
			"UNSET_VALUE":            "",
		},
	}
}

func verifiedContext(requested, effective int) ContextVerification {
	return ContextVerification{
		RequestedTokens: requested,
		EffectiveTokens: ptr(effective),
		EffectiveSource: ContextSourceRuntimeReport,
	}
}

func TestLegacyFingerprintKeyContractIsUnchanged(t *testing.T) {
	fp := validFingerprint()
	want := "workstation|gpu|550.1|cuda|ollama 1.2.3|1|q8_0"
	if got := fp.Key(); got != want {
		t.Fatalf("legacy key = %q, want %q", got, want)
	}
	v2, err := NewFingerprintV2(fp, verifiedContext(8192, 8192))
	if err != nil {
		t.Fatal(err)
	}
	if got := v2.LegacyKey(); got != want {
		t.Fatalf("v2 legacy key = %q, want %q", got, want)
	}
}

func TestFingerprintV2KeyIsStableAndSeparatesMissingLegacyDimensions(t *testing.T) {
	a, err := NewFingerprintV2(validFingerprint(), verifiedContext(8192, 8192))
	if err != nil {
		t.Fatal(err)
	}
	bfp := validFingerprint()
	bfp.Config = map[string]string{
		"OLLAMA_NUM_PARALLEL":    "1",
		"OLLAMA_KV_CACHE_TYPE":   "q8_0",
		"OLLAMA_FLASH_ATTENTION": "1",
	}
	b, err := NewFingerprintV2(bfp, verifiedContext(8192, 8192))
	if err != nil {
		t.Fatal(err)
	}
	ka, err := a.ComparabilityKey()
	if err != nil {
		t.Fatal(err)
	}
	kb, err := b.ComparabilityKey()
	if err != nil {
		t.Fatal(err)
	}
	if ka != kb || !strings.HasPrefix(ka, "fp2:") || len(ka) != len("fp2:")+64 {
		t.Fatalf("canonical keys = %q and %q", ka, kb)
	}

	changes := []struct {
		name string
		edit func(*FingerprintV2)
	}{
		{"OS", func(v *FingerprintV2) { v.Device.OS = "windows" }},
		{"CPU", func(v *FingerprintV2) { v.Device.CPU = "different" }},
		{"placement", func(v *FingerprintV2) { v.Device.InferenceDevice = "GPU 50%" }},
		{"runtime config", func(v *FingerprintV2) { v.Device.Config["OLLAMA_NUM_PARALLEL"] = "2" }},
		{"requested context", func(v *FingerprintV2) { v.Context.RequestedTokens = 4096 }},
		{"effective context", func(v *FingerprintV2) { *v.Context.EffectiveTokens = 4096 }},
	}
	for _, tc := range changes {
		changed, err := NewFingerprintV2(validFingerprint(), verifiedContext(8192, 8192))
		if err != nil {
			t.Fatal(err)
		}
		tc.edit(&changed)
		got, err := changed.ComparabilityKey()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got == ka {
			t.Errorf("%s did not change the v2 key", tc.name)
		}
	}
}

func TestContextVerificationDoesNotPromoteProbeToEffectiveContext(t *testing.T) {
	probe := &ContextProbe{
		PromptTokens: 2400, CachedTokens: 300, MinimumExpectedTokens: 2000,
		Source: ContextSourceGeneration,
	}
	context := ContextVerification{RequestedTokens: 8192, Probe: probe}
	if err := context.Validate(); err != nil {
		t.Fatal(err)
	}
	if context.State() != ContextLowerBound || context.EffectiveKnown() {
		t.Fatalf("probe-only context state = %q, known=%v", context.State(), context.EffectiveKnown())
	}
	if probe.ServedTokens() != 2700 || !probe.MeetsMinimum() {
		t.Fatalf("probe receipt = %d, passes=%v", probe.ServedTokens(), probe.MeetsMinimum())
	}
	v2, err := NewFingerprintV2(validFingerprint(), context)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v2.ComparabilityKey(); err == nil || !strings.Contains(err.Error(), "unverified") {
		t.Fatalf("probe-only comparability error = %v", err)
	}
	b, err := json.Marshal(v2)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "effective_tokens") {
		t.Fatalf("unknown effective context was fabricated: %s", b)
	}
}

func TestContextVerificationStatesRequestedAndEffectiveSeparately(t *testing.T) {
	matched := verifiedContext(8192, 8192)
	if matched.State() != ContextVerified {
		t.Fatalf("matching state = %q", matched.State())
	}
	adjusted := verifiedContext(8192, 4096)
	if adjusted.State() != ContextAdjusted || adjusted.RequestedTokens != 8192 || *adjusted.EffectiveTokens != 4096 {
		t.Fatalf("adjusted context = %+v", adjusted)
	}
	unknown := ContextVerification{RequestedTokens: 8192}
	if unknown.State() != ContextUnverified || unknown.EffectiveKnown() {
		t.Fatalf("unknown context state = %q", unknown.State())
	}
	short := verifiedContext(8192, 8192)
	short.Probe = &ContextProbe{PromptTokens: 1000, MinimumExpectedTokens: 2000, Source: ContextSourceGeneration}
	if short.State() != ContextProbeShort {
		t.Fatalf("short receipt state = %q", short.State())
	}
	v2, err := NewFingerprintV2(validFingerprint(), short)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v2.ComparabilityKey(); err == nil || !strings.Contains(err.Error(), "minimum") {
		t.Fatalf("short receipt comparability error = %v", err)
	}
}

func TestFingerprintV2ValidationRejectsContradictoryOrFabricatedEvidence(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*FingerprintV2)
		wantErr string
	}{
		{"schema", func(v *FingerprintV2) { v.Schema = "v3" }, "schema"},
		{"missing CPU", func(v *FingerprintV2) { v.Device.CPU = "" }, "CPU"},
		{"missing config", func(v *FingerprintV2) { v.Device.Config = nil }, "configuration"},
		{"NaN RAM", func(v *FingerprintV2) { v.Device.RAMGb = math.NaN() }, "RAM"},
		{"VRAM without source", func(v *FingerprintV2) { v.Device.VRAMSource = "" }, "VRAM"},
		{"nonpositive request", func(v *FingerprintV2) { v.Context.RequestedTokens = 0 }, "requested context"},
		{"source without effective", func(v *FingerprintV2) {
			v.Context.EffectiveTokens = nil
		}, "source is present"},
		{"effective without source", func(v *FingerprintV2) { v.Context.EffectiveSource = "" }, "requires"},
		{"invalid effective source", func(v *FingerprintV2) {
			v.Context.EffectiveSource = ContextSourceGeneration
		}, "requires"},
		{"receipt exceeds context", func(v *FingerprintV2) {
			v.Context.Probe = &ContextProbe{PromptTokens: 9000, MinimumExpectedTokens: 2000, Source: ContextSourceGeneration}
		}, "exceeds"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v2, err := NewFingerprintV2(validFingerprint(), verifiedContext(8192, 8192))
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(&v2)
			if err := v2.Validate(); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validation error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestNewFingerprintV2OwnsMutableInput(t *testing.T) {
	fp := validFingerprint()
	effective := 8192
	context := ContextVerification{
		RequestedTokens: 8192, EffectiveTokens: &effective,
		EffectiveSource: ContextSourceRuntimeReport,
		Probe:           &ContextProbe{PromptTokens: 2700, MinimumExpectedTokens: 2000, Source: ContextSourceGeneration},
	}
	v2, err := NewFingerprintV2(fp, context)
	if err != nil {
		t.Fatal(err)
	}
	fp.Config["OLLAMA_KV_CACHE_TYPE"] = "mutated"
	effective = 1
	context.Probe.PromptTokens = 0
	if v2.Device.Config["OLLAMA_KV_CACHE_TYPE"] != "q8_0" || *v2.Context.EffectiveTokens != 8192 || v2.Context.Probe.PromptTokens != 2700 {
		t.Fatalf("constructor retained mutable input: %+v", v2)
	}
}
