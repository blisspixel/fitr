package device

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fitr-profiles-")
	if err != nil {
		os.Exit(1)
	}
	os.Setenv("FITR_PROFILES", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestFormatCPUIsDisplayOnly(t *testing.T) {
	got := FormatCPU("Example CPU")
	want := fmt.Sprintf("(%d logical)", runtime.NumCPU())
	if !strings.Contains(got, "Example CPU") || !strings.Contains(got, want) {
		t.Fatalf("FormatCPU = %q, want name and %s", got, want)
	}
	if FormatCPU("") == "" {
		t.Fatal("empty name must still render")
	}
}

func TestProfilesEmbedAndParse(t *testing.T) {
	profs, err := LoadProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(profs) < 2 {
		t.Fatalf("expected default + at least one calibrated profile, got %d", len(profs))
	}
	var haveDefault bool
	for _, p := range profs {
		if p.Name == "default" {
			haveDefault = true
		}
		if len(p.Gates) == 0 {
			t.Fatalf("profile %q has no gates", p.Name)
		}
		// Every threshold must carry a `why` so it can be argued with rather
		// than cargo-culted onto a machine it was never measured on.
		for gname, g := range p.Gates {
			if _, ok := g["why"]; !ok {
				t.Fatalf("profile %q gate %q has no `why`", p.Name, gname)
			}
		}
	}
	if !haveDefault {
		t.Fatal("a `default` profile must always exist as the fallback")
	}
}

func TestUserProfileOverridesEmbeddedAndMalformedIsFatal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_PROFILES", dir)
	p := Profile{
		Name:        "lappy",
		Description: "user override",
		Match:       map[string]string{"gpu_contains": "780M"},
		Gates:       map[string]Gate{"fast_chat": {"decode_tps_min": 99.0, "why": "user's lived number"}},
	}
	b, _ := json.Marshal(p)
	if err := os.WriteFile(dir+"/lappy.json", b, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := SelectProfile("", Fingerprint{GPU: "AMD Radeon 780M"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != "user override" {
		t.Fatalf("user profile must win on name+match: %+v", got)
	}
	v, ok := got.Float("fast_chat", "decode_tps_min")
	if !ok || v != 99 {
		t.Fatalf("user gate = %v %v", v, ok)
	}

	if err := os.WriteFile(dir+"/bad.json", []byte(`{`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfiles(); err == nil {
		t.Fatal("malformed user profile must be a hard error")
	}
}

func TestUserProfileRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown":  `{"name":"local","gates":{},"typo":true}`,
		"trailing": `{"name":"local","gates":{}} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("FITR_PROFILES", dir)
			if err := os.WriteFile(dir+"/profile.json", []byte(raw), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadProfiles(); err == nil {
				t.Fatalf("accepted %s profile", name)
			}
		})
	}
}

func TestScaffoldProfileIsUncalibratedCopyOfDefault(t *testing.T) {
	p, err := ScaffoldProfile("My Box", Fingerprint{GPU: "NVIDIA GeForce RTX 4090", Host: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "my-box" {
		t.Fatalf("name = %q, want a slug", p.Name)
	}
	if p.Match["gpu_contains"] != "NVIDIA GeForce RTX 4090" {
		t.Fatalf("match = %v", p.Match)
	}
	if !strings.Contains(p.Notes[0], "UNCALIBRATED") {
		t.Fatalf("must say uncalibrated: %v", p.Notes)
	}
	if _, ok := p.Gates["fast_chat"]["why"]; !ok {
		t.Fatal("copied gates must keep why")
	}
}

func TestSelectProfileMatchesOnGPU(t *testing.T) {
	fp := Fingerprint{GPU: "AMD Radeon(TM) 780M", Host: "somebox"}
	p, err := SelectProfile("", fp)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "lappy" {
		t.Fatalf("gpu_contains match failed: got %q", p.Name)
	}
}

func TestSelectProfileFallsBackToDefault(t *testing.T) {
	fp := Fingerprint{GPU: "NVIDIA GeForce RTX 4090", Host: "blackbox"}
	p, err := SelectProfile("", fp)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "default" {
		t.Fatalf("unknown hardware should fall back to default, got %q", p.Name)
	}
}

func TestSelectProfileExplicitNameWins(t *testing.T) {
	fp := Fingerprint{GPU: "NVIDIA GeForce RTX 4090"}
	p, err := SelectProfile("lappy", fp)
	if err != nil || p.Name != "lappy" {
		t.Fatalf("explicit --profile must win: %v %q", err, p.Name)
	}
	if _, err := SelectProfile("nope", fp); err == nil {
		t.Fatal("an unknown profile name must be an error, not a silent default")
	}
}

func TestFingerprintKeyChangesWithConfig(t *testing.T) {
	// This is the whole comparability contract: change the driver or the KV
	// cache dtype and prior results are explicitly void.
	base := Fingerprint{Host: "h", GPU: "g", GPUDriver: "1.0", Runtime: "0.32",
		Config: map[string]string{"OLLAMA_FLASH_ATTENTION": "1", "OLLAMA_KV_CACHE_TYPE": "f16"}}
	same := base
	if base.Key() != same.Key() {
		t.Fatal("identical fingerprints must produce identical keys")
	}
	drv := base
	drv.GPUDriver = "2.0"
	if base.Key() == drv.Key() {
		t.Fatal("a driver change MUST invalidate comparability")
	}
	kv := base
	kv.Config = map[string]string{"OLLAMA_FLASH_ATTENTION": "1", "OLLAMA_KV_CACHE_TYPE": "q8_0"}
	if base.Key() == kv.Key() {
		t.Fatal("a KV cache dtype change MUST invalidate comparability")
	}
	accel := base
	accel.GPUBackend = "vulkan"
	if base.Key() == accel.Key() {
		t.Fatal("a GPU backend change (CUDA vs Vulkan) MUST invalidate comparability")
	}
}

func TestFingerprintDiffNamesTheKnob(t *testing.T) {
	a := Fingerprint{Host: "h", GPU: "g", GPUDriver: "1.0", Runtime: "0.32",
		GPUBackend: "cuda",
		Config:     map[string]string{"OLLAMA_KV_CACHE_TYPE": "f16"}}
	b := a
	b.Config = map[string]string{"OLLAMA_KV_CACHE_TYPE": "q8_0"}
	d := a.Diff(b)
	if len(d) != 1 || d[0][0] != "config.OLLAMA_KV_CACHE_TYPE" {
		t.Fatalf("diff = %v, want the KV dtype knob", d)
	}
	if d[0][1] != "f16" || d[0][2] != "q8_0" {
		t.Fatalf("diff values = %v", d)
	}
	if len(a.Diff(a)) != 0 {
		t.Fatal("identical fingerprints must have an empty diff")
	}
}

func TestNormalizeAccelPrefersGPUOverCPU(t *testing.T) {
	cases := []struct{ in, want string }{
		{"CUDA : ARCHS = 890 | CPU : AVX = 1", "cuda"},
		{"ggml_vulkan: 0 = AMD Radeon 780M", "vulkan"},
		{"library=Metal", "metal"},
		{"HIP/ROCm gfx1100", "rocm"},
		{"cpu only, AVX2", "cpu"},
		{"chipset listing, no gpu", ""}, // must not match hip inside chip
		{"", ""},
		{"something unknown", ""},
	}
	for _, tc := range cases {
		if got := NormalizeAccel(tc.in); got != tc.want {
			t.Errorf("NormalizeAccel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestGateLookupMissingIsNotZero(t *testing.T) {
	p := Profile{Gates: map[string]Gate{"fast_chat": {"decode_tps_min": 10.0}}}
	if v, ok := p.Float("fast_chat", "decode_tps_min"); !ok || v != 10 {
		t.Fatalf("got %v %v", v, ok)
	}
	// A missing gate must report "absent", never 0 -- 0 would silently pass
	// every model instead of skipping the need.
	if _, ok := p.Float("fast_chat", "nonexistent"); ok {
		t.Fatal("a missing key must report absent")
	}
	if _, ok := p.Float("nonexistent_gate", "x"); ok {
		t.Fatal("a missing gate must report absent")
	}
}

func TestParseNvidiaSMIMemoryTakesLargestCard(t *testing.T) {
	got := ParseNvidiaSMIMemory("128\n8192\n")
	if got != 8.0 {
		t.Fatalf("got %v, want 8.0 (the 8 GiB card, not the 128 MiB iGPU)", got)
	}
	if ParseNvidiaSMIMemory("") != 0 {
		t.Fatal("empty nvidia-smi output is unmeasured, not zero-as-a-reading")
	}
	if ParseNvidiaSMIMemory("[NVIDIA]\nfailed") != 0 {
		t.Fatal("garbage must not parse as a budget")
	}
}

func TestFormatVRAMDoesNotPrintZeroAsAReading(t *testing.T) {
	if got := FormatVRAM(0, ""); got != "unknown (not measured)" {
		t.Fatalf("got %q", got)
	}
	if got := FormatVRAM(8, ""); got != "unknown (not measured)" {
		t.Fatalf("a number without a source is unmeasured, got %q", got)
	}
	if got := FormatVRAM(8.0, "nvidia-smi"); got != "8.0 (nvidia-smi)" {
		t.Fatalf("got %q", got)
	}
}

func TestIsDenseAndBigIgnoresMoE(t *testing.T) {
	p := Profile{Hints: map[string]any{"dense_param_b_interactive_max": 20.0}}
	if !IsDenseAndBig("27B", "llama", p) {
		t.Fatal("a dense 27B should be flagged on a bandwidth-bound device")
	}
	// Decode tracks ACTIVE parameters: a 30B MoE (~3B active) outruns an 8B
	// dense model, so total size alone must not trigger the warning.
	if IsDenseAndBig("30.5B", "qwen3moe", p) {
		t.Fatal("MoE must not be flagged by total parameter count")
	}
	if IsDenseAndBig("8.0B", "llama", p) {
		t.Fatal("a small dense model is fine")
	}
	if IsDenseAndBig("27B", "llama", Profile{}) {
		t.Fatal("no hint in profile means no opinion")
	}
}
