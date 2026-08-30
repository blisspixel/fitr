package render

import (
	"bytes"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/score"
)

func sampleArtifact() Artifact {
	return Artifact{
		FitrVersion:   "0.2.0-dev",
		SchemaVersion: 4,
		Model:         `qwen3<script>alert(1)</script>`,
		StartedAt:     "2026-08-18T09:00:00Z",
		Level:         "full",
		Repeats:       3,
		WallSeconds:   812.4,
		Device: NewShareDevice(device.Fingerprint{
			Host: "lappy", OS: "windows", CPU: "AMD Ryzen 7 7840U",
			RAMGb: 62, GPU: "AMD Radeon(TM) 780M",
			GPUDriver: "32.0.31007.5012", GPUDriverDate: "2025-11-02",
			Runtime: "0.32.10", InferenceDevice: "GPU 100%",
			GPUBackend: "vulkan",
			Config: map[string]string{
				"OLLAMA_FLASH_ATTENTION": "1",
				"OLLAMA_KV_CACHE_TYPE":   "f16",
			},
		}),
		DeviceKey: ShareFingerprintID("lappy|AMD Radeon(TM) 780M|32.0.31007.5012|vulkan|0.32.10|1|f16"),
		Profile:   "lappy",
		Scorecard: score.Scorecard{
			Model:   "qwen3<script>alert(1)</script>",
			Profile: "lappy",
			UseFor:  "chat, coding",
			Needs: map[string]score.Verdict{
				"fast_and_decent": {State: score.Pass, Why: "23 tok/s"},
				"vision":          {State: score.NA, Why: "model never claimed vision"},
				"coding":          {State: score.Skip, Why: "not measured"},
			},
		},
		Meta: Meta{
			ParamSize: "30.5B", Quant: "Q4_K_M", Family: "qwen3moe",
			NumCtx:  4096,
			Repeats: 3, DecodeMean: 23.16, DecodeSD: 0.44, DecodeN: 3,
			DecodeMin: 22.71, DecodeMax: 23.6, PrefillMean: 226.6, PrefillN: 3,
		},
	}
}

func TestHTMLContainsFingerprintAndNeedEvidence(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteHTML(&buf, sampleArtifact()); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, want := range []string{
		"<!DOCTYPE html>",
		"device-",
		"A number without its device is meaningless",
		"Do not rank",
		"vulkan",
		"num_ctx",
		"4096",
		"OLLAMA_KV_CACHE_TYPE",
		"request context",
		"PASS",
		"n/a",
		"Not measured",
		"fitr 0.2.0-dev",
		"schema 4",
		"Written only because you asked",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("HTML missing %q", want)
		}
	}
	if strings.Contains(got, "resolves ~") || strings.Contains(got, "against a gate") {
		t.Fatal("HTML invented a global resolution claim across heterogeneous needs")
	}
}

func TestHTMLEscapesUntrustedStrings(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteHTML(&buf, sampleArtifact()); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if strings.Contains(got, "<script>") {
		t.Fatal("unescaped <script> in model name would execute in a browser")
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Fatal("model name must appear escaped")
	}
}

func TestHTMLDoesNotInventARankOrFailASkip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteHTML(&buf, sampleArtifact()); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, never := range []string{"not recommended", "leaderboard", "+/- 0.00"} {
		if strings.Contains(got, never) {
			t.Errorf("HTML must not contain %q", never)
		}
	}
	// SKIP/n/a live in the "Not measured" section, not as FAIL.
	if strings.Contains(got, `class="fail"`) {
		t.Fatal("sample had no FAIL; HTML invented one")
	}
	if !strings.Contains(got, `class="skip"`) {
		t.Fatal("n/a / SKIP must keep their own class")
	}
}

func TestHTMLOmitsHomePaths(t *testing.T) {
	a := sampleArtifact()
	a.Meta.SavedPath = `C:\Users\someone\.fitr\results\qwen3.json`
	a.Device = NewShareDevice(device.Fingerprint{
		Host: "private-host", OS: "windows", CPU: "cpu", GPU: "gpu",
		Config: map[string]string{"OLLAMA_MODELS": `C:\Users\someone\.ollama\models`},
	})
	var buf bytes.Buffer
	if err := WriteHTML(&buf, a); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if strings.Contains(got, `Users\someone`) || strings.Contains(got, ".fitr") ||
		strings.Contains(got, "lappy|") || strings.Contains(got, "private-host") ||
		strings.Contains(got, "OLLAMA_MODELS") {
		t.Fatal("shareable artifact leaked a hostname, raw fingerprint, or local path")
	}
}

func TestHTMLDefaultProfileSaysUncalibrated(t *testing.T) {
	a := sampleArtifact()
	a.Profile = "default"
	var buf bytes.Buffer
	WriteHTML(&buf, a)
	if !strings.Contains(buf.String(), "uncalibrated") {
		t.Fatal("default profile must be labeled uncalibrated")
	}
}
