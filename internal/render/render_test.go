package render

import (
	"os"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/score"
)

func TestPresentationLabelsClarifyLegacyAndIndependentEvidence(t *testing.T) {
	if got := UseForLabel("chat, (small footprint)"); got != "chat; small footprint" {
		t.Fatalf("UseForLabel = %q", got)
	}
	verdict := score.Verdict{
		State: score.Fail, Measure: "9/9 clean [0.21-1.00]", Gate: "need >=0.70",
		Detail: []string{"tool_irrelevance 9/9", "did not stop cleanly after a tool was withdrawn"},
		Note:   "undecided: the interval crosses the bar. Not a fail",
	}
	why := PresentVerdictWhy("tool_restraint", verdict)
	for _, want := range []string{
		"ordinary irrelevance checks are undecided",
		"that interval alone is not a fail",
		"overall FAIL comes from the separate withdrawal scenario above",
	} {
		if !strings.Contains(why, want) {
			t.Fatalf("presented verdict missing %q: %s", want, why)
		}
	}
	if verdict.Note != "undecided: the interval crosses the bar. Not a fail" {
		t.Fatal("presentation mutated sealed verdict")
	}
}

func TestResultPrintsDiagnosisSupportClassNotObserved(t *testing.T) {
	report := &analysis.Report{
		Artifact: analysis.ArtifactIdentity{
			Digest: "sha256:abcdef0123456789ffff", Quant: "Q4_K_M", SizeBytes: 17 << 30,
		},
		Diagnoses: []analysis.Diagnosis{{
			Code: analysis.DiagnosisPartialPlacement, Support: analysis.DiagnosisDirect,
			Statement:      "the runtime reported a partial accelerator share at the exact-context allocation point",
			NextExperiment: &analysis.Action{Argv: []string{"fitr", "board"}, Reason: "compare compatible receipts"},
		}},
		Gaps: []analysis.EvidenceGap{{
			Code: analysis.GapCapacityPolicyUnsealed, Message: "usable budget was not sealed",
		}},
		NextActions: []analysis.Action{{Argv: []string{"fitr", "board"}, Reason: "compare compatible receipts"}},
	}
	var output strings.Builder
	display := plainDisplay(&output)
	display.Result(score.Scorecard{Model: "m", Needs: map[string]score.Verdict{}}, Meta{Analysis: report})
	text := output.String()
	for _, want := range []string{
		"explain", "direct · allocation attribution", "next experiment fitr board",
		"limit", "usable capacity", "sha256:abcdef012345", "recipe label",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("result missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "observed   allocation") || strings.Contains(text, "\nobserved ") {
		t.Fatalf("diagnosis support collapsed to observed:\n%s", text)
	}
}

func TestResultMarksDescriptiveOnlyEvidenceAndItsSource(t *testing.T) {
	decode, ttft := 12.0, 0.2
	resident := int64(5 * 1024 * 1024 * 1024)
	report := &analysis.Report{
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
	var output strings.Builder
	display := plainDisplay(&output)
	display.Result(score.Scorecard{Model: "m", Needs: map[string]score.Verdict{}}, Meta{
		DecodeMean: 12, DecodeN: 1, TTFTMean: 0.2, TTFTN: 1,
		ResidentGB: 5, ResidentContext: 8192, Analysis: report,
	})
	text := output.String()
	for _, want := range []string{
		"request TTFT", "descriptive only; source runtime reported",
		"descriptive only; source client wall clock", "descriptive only; source runtime allocation",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("result missing %q:\n%s", want, text)
		}
	}
}

func TestResultRendersCapacityPolicyProjectionAndObservedBudgetResult(t *testing.T) {
	addressable, available := int64(24<<30), int64(23<<30)
	reserve, budget, components := int64(3<<30), int64(20<<30), int64(18<<30)
	resident, headroom := int64(19<<30), int64(1<<30)
	report := &analysis.Report{Capacity: analysis.Capacity{
		Policy: &analysis.CapacityPolicyObservation{
			ResourceDomain: "accelerator_memory", AddressableBytes: &addressable,
			AddressableSource: "nvidia-smi", CurrentAvailableBytes: &available,
			CurrentAvailableSource: "nvidia-smi memory.free", CurrentAvailableAt: "2026-09-01T12:00:00Z",
			OperatorReserveBytes: &reserve,
			UsableBudgetBytes:    &budget, Formula: "current_available-operator_reserve",
			SwapPolicy: "excluded", Status: analysis.StatusAvailable,
		},
		Prediction: &analysis.CapacityPredictionObservation{
			RequestedContext: 32768, KnownComponentBytes: &components,
			ArtifactBytes: int64TestPointer(17 << 30), KVBytes: int64TestPointer(1 << 30), KVDataType: "q8_0",
			Excluded: []string{"in-flight peaks", "runtime buffers"}, Status: analysis.StatusAvailable,
		},
		Resident: &analysis.ResidentObservation{
			Estimate: &resident, RequestedContext: 32768, Status: analysis.StatusAvailable,
			Acquisition: analysis.AcquisitionRuntimeAllocation,
		},
		Budget: &analysis.CapacityBudgetObservation{
			State: analysis.CapacityBudgetFit, BudgetBytes: budget, ObservedBytes: &resident,
			HeadroomBytes: &headroom, Status: analysis.StatusAvailable, Acquisition: analysis.AcquisitionMixed,
		},
	}}
	var output strings.Builder
	display := plainDisplay(&output)
	display.Result(score.Scorecard{Model: "m", Needs: map[string]score.Verdict{}}, Meta{Analysis: report})
	text := output.String()
	for _, want := range []string{
		"resource domain", "accelerator memory", "addressable", "available now",
		"availability observed 2026-09-01T12:00:00Z", "operator reserve", "safe budget", "swap excluded", "pre-load components",
		"component projection only", "excludes in-flight peaks; runtime buffers",
		"resident", "safe-budget result", "FIT", "headroom +1.00 GiB",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("capacity result missing %q:\n%s", want, text)
		}
	}
}

func TestJSONResultIncludesRendererNeutralAnalysis(t *testing.T) {
	budget := int64(20 << 30)
	report := &analysis.Report{
		Schema: analysis.ReportSchema,
		Capacity: analysis.Capacity{Policy: &analysis.CapacityPolicyObservation{
			ResourceDomain: "unified_memory", UsableBudgetBytes: &budget,
			Formula: "operator_budget", SwapPolicy: "excluded", Status: analysis.StatusAvailable,
		}},
	}
	var output strings.Builder
	display := &jsonDisplay{out: &output}
	display.Result(score.Scorecard{Model: "m", Needs: map[string]score.Verdict{}}, Meta{Analysis: report})
	text := output.String()
	for _, want := range []string{`"analysis"`, `"capacity"`, `"usable_budget_bytes":21474836480`, `"unified_memory"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("JSON result missing %q: %s", want, text)
		}
	}
}

func int64TestPointer(value int64) *int64 { return &value }

func TestNoColorEmptyStringMeansUnset(t *testing.T) {
	// no-color.org: present AND NOT EMPTY disables colour. NO_COLOR="" must NOT
	// disable it -- the classic off-by-one in this spec.
	t.Setenv("NO_COLOR", "1")
	if !noColor() {
		t.Fatal(`NO_COLOR=1 must disable colour`)
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "1")
	if noColor() {
		t.Fatal(`NO_COLOR="" means UNSET; FORCE_COLOR should win`)
	}
}

func TestNoColorZeroStillDisables(t *testing.T) {
	// "0" is non-empty, so per spec it DISABLES. This surprises people coming
	// from npm/chalk where FORCE_COLOR=0 means off -- documented, not fixed.
	t.Setenv("NO_COLOR", "0")
	if !noColor() {
		t.Fatal(`NO_COLOR=0 is non-empty and must disable colour`)
	}
}

func TestUnicodeFallsBackWhenNotUTF(t *testing.T) {
	t.Setenv("FITR_ASCII", "")
	t.Setenv("FITR_UNICODE", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "C")
	t.Setenv("WT_SESSION", "")
	t.Setenv("WT_PROFILE_ID", "")
	if unicodeOK() {
		t.Fatal("a non-UTF locale must fall back to ASCII glyphs")
	}
	t.Setenv("LANG", "en_US.UTF-8")
	if !unicodeOK() {
		t.Fatal("a UTF locale should allow unicode glyphs")
	}
}

func TestUnicodeOnWindowsTerminal(t *testing.T) {
	t.Setenv("FITR_ASCII", "")
	t.Setenv("FITR_UNICODE", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "")
	t.Setenv("OS", "Windows_NT")
	t.Setenv("WT_SESSION", "")
	t.Setenv("WT_PROFILE_ID", "")
	if unicodeOK() {
		t.Fatal("legacy Windows console without WT_SESSION must stay ASCII")
	}
	t.Setenv("WT_SESSION", "a1b2c3d4-session")
	if !unicodeOK() {
		t.Fatal("Windows Terminal (WT_SESSION) must get Unicode glyphs")
	}
	t.Setenv("WT_SESSION", "")
	t.Setenv("WT_PROFILE_ID", "a1b2c3d4-profile")
	if !unicodeOK() {
		t.Fatal("Windows Terminal (WT_PROFILE_ID) must get Unicode glyphs")
	}
}

func TestAsciiOverrideAlwaysWins(t *testing.T) {
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("FITR_ASCII", "1")
	if unicodeOK() {
		t.Fatal("FITR_ASCII must force ASCII even on a UTF locale")
	}
	g := glyphs{" | ", "-", "+/-", "..."}
	if strings.ContainsAny(g.PM+g.Dot+g.Dash, "\u00b1\u00b7\u2014") {
		t.Fatal("ASCII glyph set must contain no multi-byte characters")
	}
}

func TestSanitizeStripsTerminalEscapes(t *testing.T) {
	// Model output is untrusted input to the terminal: ANSI can clear the
	// screen, reposition the cursor, or spoof a prompt.
	nasty := "hello\x1b[2J\x1b[1;1Hspoofed\x07 and \x1b[31mred"
	got := Sanitize(nasty)
	if strings.ContainsRune(got, 0x1b) || strings.ContainsRune(got, 0x07) {
		t.Fatalf("escapes survived sanitisation: %q", got)
	}
	if !strings.Contains(got, "hello") || !strings.Contains(got, "spoofed") {
		t.Fatalf("sanitiser destroyed legitimate text: %q", got)
	}
}

func TestSingleLineNeutralizesTerminalAndLayoutControls(t *testing.T) {
	nasty := "café 模型\tready\r\n" +
		"\x1b[2Jvisible\x1b[31mred\x1b[0m " +
		"\x1b]0;forged title\a" +
		"\x1bPforged payload\x1b\\" +
		"\u009b32mgreen\u009b0m " +
		"\u009d0;forged c1 title\u009c" +
		"left\u202eright\u2066isolate\u2069\u0000\u0085end"
	got := SingleLine(nasty)
	if got != "café 模型 ready visiblered green leftrightisolate end" {
		t.Fatalf("single-line encoding = %q", got)
	}
	for _, r := range got {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			t.Fatalf("terminal or format control survived: %U in %q", r, got)
		}
	}
}

func TestSingleLinePreservesOrdinaryUnicode(t *testing.T) {
	want := "cafe\u0301 | 模型 | Ελληνικά | العربية"
	if got := SingleLine(want); got != want {
		t.Fatalf("ordinary Unicode changed: got %q, want %q", got, want)
	}
}

func TestSingleLineConsumesUnterminatedStringControl(t *testing.T) {
	if got := SingleLine("safe \x1b]0;forged remainder"); got != "safe" {
		t.Fatalf("unterminated OSC payload survived: %q", got)
	}
}

func TestSanitizeKeepsExistingMultilineBehavior(t *testing.T) {
	want := "first\n\tsecond\rthird"
	if got := Sanitize(want); got != want {
		t.Fatalf("multiline sanitization changed: got %q, want %q", got, want)
	}
}

func TestStatNeverPrintsFabricatedZeroSigma(t *testing.T) {
	g := glyphs{" | ", "-", "+/-", "..."}
	// A single observation is not an estimate. hyperfine relabels rather than
	// printing "+/- 0.00", which would be a claim of precision we do not have.
	single := stat(23.16, 0, 1, g)
	if strings.Contains(single, "+/-") {
		t.Fatalf("n=1 must not carry an error bar, got %q", single)
	}
	if !strings.Contains(single, "abs") {
		t.Fatalf("n=1 should be labelled, got %q", single)
	}
	multi := stat(23.16, 0.44, 3, g)
	if !strings.Contains(multi, "+/-0.44") {
		t.Fatalf("n=3 should carry its spread, got %q", multi)
	}
	identical := stat(23.16, 0, 3, g)
	if identical != "23.16 (identical, n=3)" {
		t.Fatalf("zero spread across repeats must retain its denominator, got %q", identical)
	}
}

func TestStatHandlesNoData(t *testing.T) {
	if got := stat(0, 0, 0, glyphs{}); got != "n/a" {
		t.Fatalf("got %q, want n/a", got)
	}
}

func TestFitTruncatesOnWordBoundary(t *testing.T) {
	got := fit("daily driver coding and agents plus more", 24, "...")
	if len(got) > 24 {
		t.Fatalf("fit overflowed: %q (%d)", got, len(got))
	}
	if strings.Contains(got, " ...") {
		t.Fatalf("trailing space before ellipsis reads as a bug: %q", got)
	}
}

func TestFitNeverSplitsUTF8(t *testing.T) {
	got := fit("模型名称很长", 5, "…")
	if !utf8.ValidString(got) {
		t.Fatalf("fit produced invalid UTF-8: %q", got)
	}
	if utf8.RuneCountInString(got) > 5 {
		t.Fatalf("fit overflowed rune budget: %q", got)
	}
	if got := fit("long", 1, "..."); got != "." {
		t.Fatalf("one-cell fit = %q", got)
	}
	if got := fit("long", 0, "..."); got != "" {
		t.Fatalf("zero-cell fit = %q", got)
	}
}

func TestResolveModeFallsBackForNonTTY(t *testing.T) {
	if got := Resolve("json"); got != "json" {
		t.Fatalf("explicit mode must win, got %q", got)
	}
	// Test binaries never have a TTY on stdout, so auto must resolve to plain.
	if got := Resolve("auto"); got != "plain" {
		t.Fatalf("auto on a non-TTY = %q, want plain", got)
	}
}

func TestValidModeRejectsTypos(t *testing.T) {
	for _, mode := range []string{"", "auto", "rich", "plain", "json", "none"} {
		if !ValidMode(mode) {
			t.Fatalf("valid mode %q was rejected", mode)
		}
	}
	if ValidMode("colourful") {
		t.Fatal("unknown display mode was accepted")
	}
}

func TestJSONModeEmitsOnlyRecordsOnStdout(t *testing.T) {
	// Diagnostics must go to stderr even under --json; nobody wraps errors in
	// JSON, and stdout has to stay a clean record stream.
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	d := &jsonDisplay{out: w}
	d.Note("this is a diagnostic", "warn")
	d.Result(score.Scorecard{Model: "m", Needs: map[string]score.Verdict{}}, Meta{})
	w.Close()
	os.Stdout = old

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	out := string(buf[:n])
	if strings.Contains(out, "this is a diagnostic") {
		t.Fatalf("a note leaked into the JSON stdout stream: %q", out)
	}
	if !strings.Contains(out, `"event":"result"`) {
		t.Fatalf("result record missing from stdout: %q", out)
	}
}

func TestNoneDisplayIsSilent(t *testing.T) {
	d := New("none")
	d.Phase("x", "y")
	d.Note("z", "warn")
	d.Result(score.Scorecard{}, Meta{})
	d.Close() // must not panic
}

func TestScorecardPrintsNumCtx(t *testing.T) {
	var buf strings.Builder
	d := &textDisplay{out: &buf, err: &buf, pal: palette{}, g: glyphs{" | ", "-", "+/-", "..."}}
	d.Result(score.Scorecard{Model: "qwen3:30b", UseFor: "chat"}, Meta{
		ParamSize: "30.5B", Quant: "Q4_K_M", Family: "qwen3moe",
		GPU: "demo-gpu", Driver: "1", Device: "GPU", Profile: "lappy",
		NumCtx: 4096,
	})
	got := buf.String()
	if !strings.Contains(got, "ctx      4096") {
		t.Fatalf("scorecard must print the request context:\n%s", got)
	}
}

func TestScorecardSeparatesRequestedAndEffectiveContext(t *testing.T) {
	var buf strings.Builder
	d := &textDisplay{out: &buf, err: &buf, pal: palette{}, g: glyphs{" | ", "-", "+/-", "..."}}
	d.Result(score.Scorecard{Model: "m", UseFor: "unproven"}, Meta{
		NumCtx: 8192, EffectiveCtx: 4096, ContextState: "adjusted",
	})
	if got := buf.String(); !strings.Contains(got, "8192 requested -> 4096 effective (adjusted)") {
		t.Fatalf("effective context is hidden:\n%s", got)
	}
}

func TestScorecardProjectsValidatedAnalysisInsteadOfLegacyFields(t *testing.T) {
	decode, prefill, ttft := 23.16, 226.6, 0.89
	cachedTTFT, unloadedTTFT, runtimeLoad := 0.19, 4.82, 3.93
	sd, minValue, maxValue := 0.44, 22.71, 23.6
	effective, resident := 4096, int64(20*1024*1024*1024)
	meta := Meta{
		DecodeMean: 999,
		Analysis: &analysis.Report{
			Context: analysis.Context{Requested: 8192, Effective: &effective, State: "adjusted"},
			Performance: analysis.Performance{
				DecodeTPS: analysis.PerformanceObservation{Estimate: &decode, SD: &sd, Min: &minValue, Max: &maxValue,
					SampleCount: 3, Samples: []float64{22.71, 23.17, 23.6}},
				PrefillTPS:                 analysis.PerformanceObservation{Estimate: &prefill, SampleCount: 3},
				TTFTSeconds:                analysis.PerformanceObservation{Estimate: &ttft, SampleCount: 3},
				LoadedCacheHitTTFTSeconds:  analysis.PerformanceObservation{Estimate: &cachedTTFT, SampleCount: 3},
				RuntimeUnloadedTTFTSeconds: analysis.PerformanceObservation{Estimate: &unloadedTTFT, SampleCount: 1},
				RuntimeLoadSeconds:         analysis.PerformanceObservation{Estimate: &runtimeLoad, SampleCount: 1},
			},
			Capacity: analysis.Capacity{Resident: &analysis.ResidentObservation{
				Estimate: &resident, RequestedContext: 32768,
			}, Placement: &analysis.PlacementObservation{
				AcceleratorBytes:    15 * 1024 * 1024 * 1024,
				NonAcceleratorBytes: 5 * 1024 * 1024 * 1024,
				AcceleratorPercent:  75,
				Boundary:            analysis.AllocationAttributionBoundary,
			}},
		},
	}
	var buf strings.Builder
	d := &textDisplay{out: &buf, err: &buf, pal: palette{}, g: glyphs{" | ", "-", "+/-", "..."}}
	d.Result(score.Scorecard{Model: "m"}, meta)
	got := buf.String()
	for _, want := range []string{
		"8192 requested -> 4096 effective (adjusted)",
		"23.16 +/-0.44 (CV 1.9%, n=3) tok/s",
		"226.60 (identical, n=3) tok/s",
		"0.89 (identical, n=3) s",
		"loaded cache-hit TTFT 0.19 (identical, n=3) s",
		"runtime-unloaded TTFT 4.82 (abs, n=1) s",
		"runtime load 3.93 (abs, n=1) s",
		"20.00 GB after requested 32K load probe",
		"runtime accelerator 15.00 GB (75.0% of runtime allocation)",
		"non-accelerator    5.00 GB (derived remainder)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("analysis-backed scorecard missing %q:\n%s", want, got)
		}
	}
	if flat := reflow(got); !strings.Contains(flat,
		"not proof of exclusive physical pools, layer placement, or host traffic") {
		t.Errorf("analysis-backed scorecard lost the placement boundary:\n%s", got)
	}
	if strings.Contains(got, "999") {
		t.Fatal("renderer trusted a stale compatibility field over validated analysis")
	}
}

func TestScorecardSanitizesMetadata(t *testing.T) {
	var buf strings.Builder
	d := &textDisplay{out: &buf, err: &buf, pal: palette{}, g: glyphs{" | ", "-", "+/-", "..."}}
	d.Result(score.Scorecard{Model: "m", Needs: map[string]score.Verdict{}}, Meta{
		ParamSize: "8B\x1b[2J\u202e", GPU: "gpu\x07\r\nspoof", Driver: "driver\x1b[31m",
		Profile: "profile\x1b]0;forged title\a\x1bPforged payload\x1b\\",
	})
	got := buf.String()
	if strings.ContainsAny(got, "\x1b\x07\r\u202e") || strings.Contains(got, "forged title") ||
		strings.Contains(got, "forged payload") {
		t.Fatalf("scorecard leaked terminal control bytes: %q", got)
	}
}

func TestCalibrationScorecardDoesNotPretendToBeAProductVerdict(t *testing.T) {
	var buf strings.Builder
	d := &textDisplay{out: &buf, err: &buf, pal: palette{}, g: glyphs{" | ", "-", "+/-", "..."}}
	d.Result(score.Scorecard{Model: "m", UseFor: "no need passed yet"}, Meta{
		GPU: "gpu", Driver: "driver", Device: "GPU", Profile: "default",
		Repeats: 1, Calibration: true,
	})
	got := buf.String()
	// Prose is wrapped to the rule now, so a sentence can straddle a line
	// break. Assert on the reflowed text, not on where the wrap happened to
	// land: pinning the line breaks would make every width change a test
	// change and tell us nothing about the words.
	flat := reflow(got)
	if !strings.Contains(flat, "calibration evidence only") || !strings.Contains(flat, "-k 10 for decision evidence") {
		t.Fatalf("calibration framing missing:\n%s", got)
	}
	if strings.Contains(flat, "before ranking") {
		t.Fatalf("calibration output gave product-ranking advice:\n%s", got)
	}
}

// reflow collapses the wrapping so prose assertions test the words rather than
// the column they broke at.
func reflow(s string) string { return strings.Join(strings.Fields(s), " ") }

func TestJSONResultIncludesNumCtx(t *testing.T) {
	var buf strings.Builder
	d := &jsonDisplay{out: &buf}
	d.Result(score.Scorecard{Model: "m", Needs: map[string]score.Verdict{}}, Meta{NumCtx: 4096})
	got := buf.String()
	if !strings.Contains(got, `"num_ctx":4096`) {
		t.Fatalf("json result missing num_ctx:\n%s", got)
	}
}

func TestJSONResultIncludesContextReceiptWhenAvailable(t *testing.T) {
	var buf strings.Builder
	d := &jsonDisplay{out: &buf}
	d.Result(score.Scorecard{Model: "m", Needs: map[string]score.Verdict{}}, Meta{
		NumCtx: 8192, EffectiveCtx: 4096, ContextState: "adjusted",
	})
	got := buf.String()
	for _, want := range []string{`"num_ctx":8192`, `"effective_ctx":4096`, `"context_state":"adjusted"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("json result missing %s:\n%s", want, got)
		}
	}
}
