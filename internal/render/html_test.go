package render

import (
	"bytes"
	"html"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/analysis"
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
			DecodeMin: 22.71, DecodeMax: 23.6,
			PrefillMean: 226.6, PrefillSD: 3.41, PrefillN: 3,
			TTFTMean: 0.89, TTFTSD: 0.03, TTFTN: 3,
			ResidentGB: 20.34,
		},
	}
}

func TestHTMLSeparatesPerformanceAndVerifiedCapacity(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteHTML(&buf, sampleArtifact()); err != nil {
		t.Fatal(err)
	}
	got := html.UnescapeString(buf.String())
	for _, want := range []string{
		"<h2>Performance</h2>",
		"decode</th><td>23.16 +/-0.44 (CV 1.9%, n=3) tok/s; min 22.71, max 23.60",
		"prefill</th><td>226.60 +/-3.41 (CV 1.5%, n=3) tok/s",
		"TTFT</th><td>0.89 +/-0.03 (CV 3.4%, n=3) s",
		"<h2>Capacity</h2>",
		"resident</th><td>20.34 GB after requested 32K load probe",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("HTML missing %q", want)
		}
	}
	if strings.Contains(got, "<h2>Sample</h2>") {
		t.Fatal("HTML retained the ambiguous sample heading")
	}
}

func TestHTMLMarksDescriptiveOnlyEvidenceAndSource(t *testing.T) {
	artifact := sampleArtifact()
	decode, ttft := artifact.Meta.DecodeMean, artifact.Meta.TTFTMean
	resident := int64(artifact.Meta.ResidentGB * 1024 * 1024 * 1024)
	artifact.Meta.Analysis = &analysis.Report{
		Performance: analysis.Performance{
			DecodeTPS: analysis.PerformanceObservation{Estimate: &decode, Status: analysis.StatusDescriptiveOnly,
				Acquisition: analysis.AcquisitionRuntimeReported, SampleCount: 1},
			TTFTSeconds: analysis.PerformanceObservation{Estimate: &ttft, Status: analysis.StatusDescriptiveOnly,
				Acquisition: analysis.AcquisitionClientWallClock, SampleCount: 1},
		},
		Capacity: analysis.Capacity{Resident: &analysis.ResidentObservation{
			Estimate: &resident, Status: analysis.StatusDescriptiveOnly,
			Acquisition: analysis.AcquisitionRuntimeAllocation,
		}},
	}
	var output bytes.Buffer
	if err := WriteHTML(&output, artifact); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"request TTFT", "descriptive only; source runtime reported",
		"descriptive only; source client wall clock", "descriptive only; source runtime allocation",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("HTML missing %q", want)
		}
	}
}

func TestHTMLPerformanceDoesNotRequireDecode(t *testing.T) {
	a := sampleArtifact()
	a.Meta.DecodeN = 0
	var buf bytes.Buffer
	if err := WriteHTML(&buf, a); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "<h2>Performance</h2>") ||
		!strings.Contains(got, "prefill</th>") || !strings.Contains(got, "TTFT</th>") {
		t.Fatal("prefill and TTFT observations must remain visible without decode")
	}
	if strings.Contains(got, "decode</th>") {
		t.Fatal("HTML rendered an unavailable decode observation")
	}
}

func TestHTMLCapacityAndPerformanceAreIndependent(t *testing.T) {
	a := sampleArtifact()
	a.Meta.DecodeN, a.Meta.PrefillN, a.Meta.TTFTN = 0, 0, 0
	var buf bytes.Buffer
	if err := WriteHTML(&buf, a); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if strings.Contains(got, "<h2>Performance</h2>") {
		t.Fatal("HTML rendered a performance section without observations")
	}
	if !strings.Contains(got, "<h2>Capacity</h2>") {
		t.Fatal("verified capacity must remain visible without performance observations")
	}

	a.Meta.ResidentGB = 0
	buf.Reset()
	if err := WriteHTML(&buf, a); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "<h2>Capacity</h2>") {
		t.Fatal("HTML rendered capacity without a verified resident observation")
	}
}

func TestHTMLRendersFriendlyCentralEvidenceLabels(t *testing.T) {
	a := sampleArtifact()
	a.Meta.Analysis = &analysis.Report{Gaps: []analysis.EvidenceGap{{
		Code:    analysis.GapCapacityPolicyUnsealed,
		Message: "resident bytes cannot establish headroom or fit",
	}}}
	var buf bytes.Buffer
	if err := WriteHTML(&buf, a); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, want := range []string{
		"<h2>Evidence limits</h2>",
		"usable capacity",
		"resident bytes cannot establish headroom or fit",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("HTML missing central evidence limit %q", want)
		}
	}
}

func TestHTMLRendersCentralLatencyPlacementAndDiagnosis(t *testing.T) {
	a := sampleArtifact()
	cachedTTFT, unloadedTTFT, runtimeLoad := 0.19, 4.82, 3.93
	resident := int64(20 * 1024 * 1024 * 1024)
	a.Meta.Analysis = &analysis.Report{
		Performance: analysis.Performance{
			LoadedCacheHitTTFTSeconds:  analysis.PerformanceObservation{Estimate: &cachedTTFT, SampleCount: 3},
			RuntimeUnloadedTTFTSeconds: analysis.PerformanceObservation{Estimate: &unloadedTTFT, SampleCount: 1},
			RuntimeLoadSeconds:         analysis.PerformanceObservation{Estimate: &runtimeLoad, SampleCount: 1},
		},
		Capacity: analysis.Capacity{
			Resident: &analysis.ResidentObservation{Estimate: &resident, RequestedContext: 32768},
			Placement: &analysis.PlacementObservation{
				AcceleratorBytes:    15 * 1024 * 1024 * 1024,
				NonAcceleratorBytes: 5 * 1024 * 1024 * 1024,
				AcceleratorPercent:  75,
				Boundary:            analysis.AllocationAttributionBoundary,
			},
		},
		Diagnoses: []analysis.Diagnosis{{
			Code:      analysis.DiagnosisPartialPlacement,
			Statement: "the runtime reported a partial accelerator share at the exact-context allocation point",
		}},
	}
	var buf bytes.Buffer
	if err := WriteHTML(&buf, a); err != nil {
		t.Fatal(err)
	}
	got := html.UnescapeString(buf.String())
	for _, want := range []string{
		"loaded TTFT</th>",
		"loaded cache-hit TTFT</th><td>0.19 (identical, n=3) s",
		"runtime-unloaded TTFT</th><td>4.82 (abs, n=1) s",
		"runtime load</th><td>3.93 (abs, n=1) s",
		"runtime-attributed accelerator</th><td>15.00 GB (75.0% of runtime allocation)",
		"non-accelerator</th><td>5.00 GB (derived remainder)",
		"not proof of exclusive physical pools, layer placement, or host traffic",
		"allocation attribution",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("HTML missing %q", want)
		}
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
