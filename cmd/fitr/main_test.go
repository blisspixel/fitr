package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/calibration"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/score"
	"github.com/blisspixel/fitr/internal/stats"
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

// The golden corpus: a frozen, realistic full-run result. measure() -> Score()
// -> Result rendering is the pipeline every run ends with, and before this
// test none of it was covered - a scoring regression would have shipped
// silently inside plausible-looking scorecards.
func golden(t *testing.T) *Result {
	t.Helper()
	b, err := os.ReadFile("testdata/golden_result.json")
	if err != nil {
		t.Fatal(err)
	}
	var r Result
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	if r.SchemaVersion != 4 {
		t.Fatalf("golden fixture is schema %d; regenerate it when the schema changes", r.SchemaVersion)
	}
	return &r
}

func sealCurrentResult(t *testing.T, records ...*Result) {
	t.Helper()
	for _, result := range records {
		if err := prepareMockEvidence(result); err != nil {
			t.Fatal(err)
		}
	}
}

func saveCurrentResults(t *testing.T, records ...*Result) {
	t.Helper()
	for _, result := range records {
		if _, err := save(result); err != nil {
			t.Fatal(err)
		}
	}
}

func lappyProfile(t *testing.T) device.Profile {
	t.Helper()
	p, err := device.SelectProfile("lappy", device.Fingerprint{})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGoldenResultScoresExactly(t *testing.T) {
	r := golden(t)
	sc := score.Score(measure(r), lappyProfile(t))

	want := map[string]score.State{
		"fast_and_decent":       score.Pass,
		"coding":                score.Pass,
		"structured_output":     score.Inconclusive, // point estimate clears the gate, interval does not
		"instruction_precision": score.Inconclusive, // 4/4 is still thin evidence against a 0.70 gate
		"uncensored":            score.Pass,
		"unattended_agentic":    score.Pass,
		"tool_restraint":        score.Pass,
		"low_footprint":         score.Pass, // 20.34 under 22
		"vision":                score.NA,   // never claimed; not a deficiency
		"output_health":         score.Pass,
	}
	for need, state := range want {
		got, ok := sc.Needs[need]
		if !ok {
			t.Errorf("need %s missing from scorecard", need)
			continue
		}
		if got.State != state {
			t.Errorf("%s = %v (%s), want %v", need, got.State, got.Why, state)
		}
	}
	if _, ok := sc.Needs["user_tasks"]; ok {
		t.Error("user_tasks must not appear - the golden run had none")
	}
	if sc.Fails != 0 {
		t.Errorf("fails = %d, want 0", sc.Fails)
	}
	// The coding interval pools executed code trials with generated reasoning
	// checks: 6/6 code + 4/5 reasoning.
	if why := sc.Needs["coding"].Why; !strings.Contains(why, "10/11") {
		t.Errorf("coding why = %q, want the pooled 10/11 sample", why)
	}
}

func TestGoldenResultRendersCleanly(t *testing.T) {
	r := golden(t)
	sc := score.Score(measure(r), lappyProfile(t))

	trials := len(r.CodeWrite) + len(r.CodeFix) + len(r.Tools) + len(r.Checks) + 1
	meta := render.Meta{
		ParamSize: "30.5B", Quant: "Q4_K_M", Family: "qwen3moe",
		GPU: r.Device.GPU, Driver: r.Device.GPUDriver, Device: "GPU 100%",
		Profile: "lappy", NumCtx: resultNumCtx(r), Repeats: 3,
		DecodeMean: r.DecodeSum.Mean, DecodeSD: r.DecodeSum.SD,
		DecodeMin: r.DecodeSum.Min, DecodeMax: r.DecodeSum.Max, DecodeN: r.DecodeSum.N,
		PrefillMean: r.PrefillSum.Mean, PrefillSD: r.PrefillSum.SD, PrefillN: r.PrefillSum.N,
		Trials: trials, MDEpp: 100 * stats.MinDetectableEffect(trials, 1),
	}

	old := os.Stdout
	pr, pw, _ := os.Pipe()
	os.Stdout = pw
	disp := render.New("plain")
	disp.Result(sc, meta)
	pw.Close()
	os.Stdout = old
	out, _ := io.ReadAll(pr)
	got := string(out)

	for _, want := range []string{
		"[PASS]", "[n/a ]",
		"ctx      8192",
		"emits valid structured output",
		"follows exact instructions",
		"min detectable effect",
		"separates broken from working",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered scorecard missing %q\n%s", want, got)
		}
	}
	for _, never := range []string{"+/- 0.00", "not recommended", "[FAIL]"} {
		if strings.Contains(got, never) {
			t.Errorf("rendered scorecard must not contain %q\n%s", never, got)
		}
	}
}

// A degraded variant of the golden run with broken structured output and a
// looping longest-sample. This pins the FAIL paths end to end, not just the
// happy ones.
func TestDegradedResultFailsTheRightNeeds(t *testing.T) {
	r := golden(t)
	for i := range r.Checks {
		if r.Checks[i].Need == "structured_output" {
			r.Checks[i].Pass = false
		}
	}
	r.Rep.DupLineRatio = 0.91
	r.Rep.GzipRatio = 9.2
	sc := score.Score(measure(r), lappyProfile(t))

	if sc.Needs["structured_output"].State != score.Fail {
		t.Errorf("structured_output = %v, want FAIL at 0/7", sc.Needs["structured_output"].State)
	}
	if sc.Needs["output_health"].State != score.Fail {
		t.Errorf("output_health = %v, want FAIL on a looping sample", sc.Needs["output_health"].State)
	}
	// Independent needs stay independent: broken JSON must not drag down chat.
	if sc.Needs["fast_and_decent"].State != score.Pass {
		t.Errorf("fast_and_decent = %v, want PASS regardless of structured output", sc.Needs["fast_and_decent"].State)
	}
	if !strings.Contains(sc.UseFor, "AVOID") {
		t.Errorf("use_for = %q, want the degenerate-output warning", sc.UseFor)
	}
}

func TestQuantDamageLineIsInconclusiveWithoutSameBaseLineage(t *testing.T) {
	base := golden(t)
	hi, lo := *base, *base
	lo.Checks = append([]eval.CheckOutcome(nil), base.Checks...)
	hi.Model, lo.Model = "m:q8", "m:q4"
	hi.ModelMeta.Details.QuantizationLevel = "Q8_0"
	lo.ModelMeta.Details.QuantizationLevel = "Q4_K_M"
	for i := range lo.Checks {
		if lo.Checks[i].Need == "structured_output" && lo.Checks[i].Pass {
			lo.Checks[i].Pass = false
			break
		}
	}
	flips := eval.PairFlips(hi.Checks, lo.Checks)
	line, ok := quantDamageLine(&hi, &lo, flips)
	if !ok || !strings.Contains(line, "INCONCLUSIVE") || !strings.Contains(line, "same-base revision lineage") {
		t.Fatalf("got %q ok=%v, want explicit unverified-lineage result", line, ok)
	}
	if strings.Contains(line, "Q4_K_M lost") || strings.Contains(line, "quant damage:") {
		t.Fatalf("unverified lineage produced directional attribution: %q", line)
	}
	lo.ModelMeta.Details.QuantizationLevel = "IQ4_XS"
	if _, ok := quantDamageLine(&hi, &lo, flips); ok {
		t.Fatal("IQ must not invent a directional rank")
	}
	lo.ModelMeta.Details.QuantizationLevel = "Q4_K_M"
	lo.ModelMeta.Details.Family = "llama"
	if _, ok := quantDamageLine(&hi, &lo, flips); ok {
		t.Fatal("different families are not a quant pair")
	}
	lo.ModelMeta.Details.Family = hi.ModelMeta.Details.Family
	lo.ModelMeta.Details.ParameterSize = "different"
	if _, ok := quantDamageLine(&hi, &lo, flips); ok {
		t.Fatal("different parameter sizes are not a quant pair")
	}
}

func TestBoardDoesNotRankAcrossFingerprints(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	a, b := golden(t), golden(t)
	a.Model, b.Model = "aa", "bb"
	b.Device.GPU = a.Device.GPU + " other"
	b.Device.GPUDriver = "other-driver"
	sealCurrentResult(t, a, b)
	saveCurrentResults(t, a, b)
	oldOut, oldErr := os.Stdout, os.Stderr
	or, ow, _ := os.Pipe()
	er, ew, _ := os.Pipe()
	os.Stdout, os.Stderr = ow, ew
	code := cmdBoard(context.Background(), nil)
	ow.Close()
	ew.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	io.ReadAll(er)
	out, _ := io.ReadAll(or)
	got := string(out)
	if code != exitOK {
		t.Fatalf("code = %d, output:\n%s", code, got)
	}
	if !strings.Contains(got, "not comparable") {
		t.Fatalf("board must refuse to rank across fingerprints:\n%s", got)
	}
	if !strings.Contains(got, "aa") || !strings.Contains(got, "bb") {
		t.Fatalf("both groups must still be shown:\n%s", got)
	}
}

func TestBoardExcludesContaminatedResultsFromRankingAndClaims(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	clean, contaminated := golden(t), golden(t)
	clean.Model, clean.Scorecard.Model = "clean", "clean"
	contaminated.Model, contaminated.Scorecard.Model = "contaminated", "contaminated"
	contaminated.DecodeSum.Mean = clean.DecodeSum.Mean * 10
	contaminated.Scorecard.Serves = []string{"coding", "structured_output"}
	contaminated.Contamination = []string{"leftover:7b"}
	sealCurrentResult(t, clean, contaminated)
	saveCurrentResults(t, clean, contaminated)

	var stdout string
	stderr, code := captureTopStderr(t, func() int {
		var inner int
		stdout, inner = captureTopStdout(t, func() int {
			return cmdBoard(context.Background(), []string{"--display", "plain"})
		})
		return inner
	})
	if code != exitOK {
		t.Fatalf("board exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "clean") || strings.Contains(stdout, "contaminated") {
		t.Fatalf("board retained a contaminated ranking row:\n%s", stdout)
	}
	if !strings.Contains(stderr, "INCONCLUSIVE") || !strings.Contains(stderr, "excluded 1") {
		t.Fatalf("board did not disclose the excluded result:\n%s", stderr)
	}
}

func TestViewDefaultsToNewestSavedResult(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	older, newer := golden(t), golden(t)
	older.Model, older.Scorecard.Model, older.StartedAt = "older", "older", "2026-01-01T00:00:00Z"
	newer.Model, newer.Scorecard.Model, newer.StartedAt = "newer", "newer", "2026-01-02T00:00:00Z"
	newer.Profile = "deleted-local-profile"
	for _, result := range []*Result{older, newer} {
		if _, err := save(result); err != nil {
			t.Fatal(err)
		}
	}

	old := os.Stdout
	reader, writer, _ := os.Pipe()
	os.Stdout = writer
	code := cmdView(context.Background(), []string{"--display", "plain"})
	writer.Close()
	os.Stdout = old
	out, _ := io.ReadAll(reader)
	got := string(out)
	if code != exitOK {
		t.Fatalf("view exited %d:\n%s", code, got)
	}
	if !strings.Contains(got, "model    newer") || strings.Contains(got, "model    older") {
		t.Fatalf("view did not select the newest result:\n%s", got)
	}
	for _, want := range []string{"performance", "decode", "prefill", "graphs show repeat shape"} {
		if !strings.Contains(got, want) {
			t.Fatalf("view missing %q:\n%s", want, got)
		}
	}
}

func TestViewReadsResultPathAndCanEmitRawJSON(t *testing.T) {
	dir := t.TempDir()
	r := golden(t)
	path := filepath.Join(dir, "result.json")
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}

	output, err := os.CreateTemp(dir, "view-output-*.json")
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = output
	code := cmdView(context.Background(), []string{path, "--display", "json"})
	output.Close()
	os.Stdout = old
	out, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	if code != exitOK {
		t.Fatalf("view exited %d: %s", code, out)
	}
	var got Result
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("view JSON is invalid: %v\n%s", err, out)
	}
	if got.Model != r.Model || got.SchemaVersion != r.SchemaVersion {
		t.Fatalf("view JSON changed the saved result: got %q schema %d", got.Model, got.SchemaVersion)
	}
}

func TestCompareRefusesDifferentFingerprints(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	a, b := golden(t), golden(t)
	a.Model, b.Model = "aa", "bb"
	b.Device.GPU = a.Device.GPU + " other"
	sealCurrentResult(t, a, b)
	saveCurrentResults(t, a, b)
	old := os.Stderr
	er, ew, _ := os.Pipe()
	os.Stderr = ew
	code := cmdCompare(context.Background(), []string{"aa", "bb"})
	ew.Close()
	os.Stderr = old
	io.ReadAll(er)
	if code != exitError {
		t.Fatalf("code = %d, want error (never rank across fingerprints)", code)
	}
}

func TestCompareIsInconclusiveWhenEitherRunIsContaminated(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	a, b := golden(t), golden(t)
	a.Model, b.Model = "aa", "bb"
	b.Contamination = []string{"leftover:7b"}
	sealCurrentResult(t, a, b)
	saveCurrentResults(t, a, b)

	output, code := captureTopStdout(t, func() int {
		return cmdCompare(context.Background(), []string{"aa", "bb"})
	})
	if code != exitError {
		t.Fatalf("compare exit=%d, want error for invalid evidence", code)
	}
	if !strings.Contains(output, "INCONCLUSIVE") || !strings.Contains(output, "leftover:7b") {
		t.Fatalf("comparison did not disclose contamination:\n%s", output)
	}
	for _, forbidden := range []string{"first is faster", "second is faster", "wins the flips", "decode tok/s"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("comparison retained claim %q:\n%s", forbidden, output)
		}
	}
}

func TestMDEIsSaidOutLoud(t *testing.T) {
	// The ROADMAP claims fitr states its minimum detectable effect out loud.
	// This pins that the number exists and lands in the meta the renderer prints.
	mde := 100 * stats.MinDetectableEffect(23, 1)
	if mde < 20 || mde > 40 {
		t.Fatalf("MDE at 23 trials = %.1fpp - expected roughly 29pp; the formula changed", mde)
	}
}

func TestBareFitrIsStatusNotUsage(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	code := cmdStatus(context.Background())
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	got := string(out)
	if !strings.Contains(got, "fitr "+version) || !strings.Contains(got, "next") {
		t.Fatalf("bare fitr must be a status page:\n%s", got)
	}
	if code == exitUsage {
		t.Fatal("bare fitr is not a usage error")
	}
}

func TestUsageMentionsAdvise(t *testing.T) {
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	usage()
	w.Close()
	os.Stderr = old
	out, _ := io.ReadAll(r)
	got := string(out)
	if !strings.Contains(got, "fitr advise") {
		t.Fatalf("usage must list advise:\n%s", got)
	}
	if !strings.Contains(got, "fitr export") {
		t.Fatalf("usage must list export:\n%s", got)
	}
	if !strings.Contains(got, "fitr tune") {
		t.Fatalf("usage must list tune:\n%s", got)
	}
	if !strings.Contains(got, "fitr calibrate") {
		t.Fatalf("usage must list calibrate:\n%s", got)
	}
	if !strings.Contains(got, "fitr apply") {
		t.Fatalf("usage must list apply:\n%s", got)
	}
	if !strings.Contains(got, "--retonr") {
		t.Fatalf("usage must mention the optional retonr export:\n%s", got)
	}
	if !strings.Contains(got, "--checks-only") || !strings.Contains(got, "calibrate merge") {
		t.Fatalf("usage must include the calibration workflow:\n%s", got)
	}
}

func TestChecksOnlyRequiresFixedPairing(t *testing.T) {
	if code := cmdRun(context.Background(), []string{"m", "--checks-only"}); code != exitUsage {
		t.Fatalf("code = %d, want usage without a seedset", code)
	}
	if code := cmdRun(context.Background(), []string{"m", "--checks-only", "--seedset", "s", "--adaptive"}); code != exitUsage {
		t.Fatalf("code = %d, want usage for adaptive paired calibration", code)
	}
	if code := cmdRun(context.Background(), []string{"m", "--checks-only", "--seedset", "s", "--html"}); code != exitUsage {
		t.Fatalf("code = %d, want usage for standalone calibration HTML", code)
	}
}

func TestCalibrateReportsNeverFlippedItems(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	hi, lo := golden(t), golden(t)
	spec, err := eval.LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	hi.SchemaVersion, lo.SchemaVersion = spec.Version.ResultSchemaVersion, spec.Version.ResultSchemaVersion
	hi.Model, lo.Model = "m-q8", "m-q4"
	hi.SeedSet, lo.SeedSet = "night", "night"
	hi.ModelMeta.Details.QuantizationLevel = "Q8_0"
	lo.ModelMeta.Details.QuantizationLevel = "Q4_K_M"
	hi.Checks = []eval.CheckOutcome{
		{TaskID: "json_object", Need: "structured_output", Seed: 1, Pass: true, Outcome: eval.OutcomePass},
		{TaskID: "date_math", Need: "instruction_precision", Seed: 1, Pass: true, Outcome: eval.OutcomePass},
	}
	lo.Checks = []eval.CheckOutcome{
		{TaskID: "json_object", Need: "structured_output", Seed: 1, Pass: false, Outcome: eval.OutcomeFail},
		{TaskID: "date_math", Need: "instruction_precision", Seed: 1, Pass: true, Outcome: eval.OutcomePass},
	}
	sealCurrentResult(t, hi, lo)
	saveCurrentResults(t, hi, lo)
	old := os.Stdout
	pr, pw, _ := os.Pipe()
	os.Stdout = pw
	pairPath := filepath.Join(dir, "pair.json")
	code := cmdCalibrate(context.Background(), []string{"m-q8", "m-q4", "--out", pairPath})
	pw.Close()
	os.Stdout = old
	out, _ := io.ReadAll(pr)
	got := string(out)
	if code != exitOK {
		t.Fatalf("code = %d\n%s", code, got)
	}
	if !strings.Contains(got, "json_object") || !strings.Contains(got, "never flipped") {
		t.Fatalf("must name the discriminator and the inert item:\n%s", got)
	}
	if !strings.Contains(got, "exploratory only") || !strings.Contains(got, "fewer than 10") {
		t.Fatalf("must distinguish an exploratory pair from decision-grade evidence:\n%s", got)
	}
	if strings.Contains(got, "rewrites spec") {
		t.Fatal("must not claim to rewrite the spec")
	}
	report, err := calibration.ReadPair(pairPath)
	if err != nil {
		t.Fatal(err)
	}
	if report.Reference.Model != "m-q8" || report.Candidate.Model != "m-q4" {
		t.Fatalf("wrong calibration direction: %+v", report)
	}
	raw, err := os.ReadFile(pairPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{hi.Device.Host, "CODE_WRITE_OK", ".fitr"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("pair report leaked %q: %s", forbidden, raw)
		}
	}
}

func TestCalibrateRejectsUnpairedSeedsets(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	a, b := golden(t), golden(t)
	a.Model, b.Model = "aa", "bb"
	a.SeedSet, b.SeedSet = "one", "two"
	for _, r := range []*Result{a, b} {
		raw, _ := json.Marshal(r)
		os.WriteFile(filepath.Join(dir, safeName(r.Model)+".json"), raw, 0o644)
	}
	if code := cmdCalibrate(context.Background(), []string{"aa", "bb"}); code != exitUsage {
		t.Fatalf("code = %d, want usage when seedsets differ", code)
	}
}

func TestCalibrateRejectsDifferentDeviceConfiguration(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	a, b := golden(t), golden(t)
	a.Model, b.Model = "aa", "bb"
	a.SeedSet, b.SeedSet = "same", "same"
	b.Device.GPUDriver = "different"
	b.DeviceKey = b.Device.Key()
	sealCurrentResult(t, a, b)
	saveCurrentResults(t, a, b)
	if code := cmdCalibrate(context.Background(), []string{"aa", "bb"}); code != exitUsage {
		t.Fatalf("code = %d, want usage when device configuration differs", code)
	}
}

func TestCalibrationPairRejectsContaminatedAndUnreconciledEvidence(t *testing.T) {
	makePair := func() (*Result, *Result) {
		a, b := golden(t), golden(t)
		a.Model, b.Model = "m-q8", "m-q4"
		a.SeedSet, b.SeedSet = "shared", "shared"
		a.ModelMeta.Details.QuantizationLevel = "Q8_0"
		b.ModelMeta.Details.QuantizationLevel = "Q4_K_M"
		return a, b
	}

	a, b := makePair()
	b.Contamination = []string{"leftover:7b"}
	sealCurrentResult(t, a, b)
	if _, err := calibrationPair(a, b, eval.ItemStats(a.Checks, b.Checks)); err == nil ||
		!strings.Contains(err.Error(), "contaminated") {
		t.Fatalf("contaminated calibration pair error = %v", err)
	}

	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	a, b = makePair()
	sealCurrentResult(t, a, b)
	saveCurrentResults(t, a, b)
	if err := os.RemoveAll(record.NewStore(dir).HistoryDir()); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadResults()
	if err != nil {
		t.Fatal(err)
	}
	a, b = latestNamed(loaded, "m-q8"), latestNamed(loaded, "m-q4")
	if a == nil || b == nil {
		t.Fatalf("reconciled pair was not loaded: %+v", loaded)
	}
	if _, err := calibrationPair(a, b, eval.ItemStats(a.Checks, b.Checks)); err == nil ||
		!strings.Contains(err.Error(), "unverified") {
		t.Fatalf("unreconciled calibration pair error = %v", err)
	}
}

func TestCalibrateAttachesVerifiedLineageFromConversionManifest(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	a, b := golden(t), golden(t)
	a.Model, b.Model = "m-q8", "m-q4"
	a.SeedSet, b.SeedSet = "shared", "shared"
	a.ModelMeta.Details.QuantizationLevel = "Q8_0"
	b.ModelMeta.Details.QuantizationLevel = "Q4_K_M"
	sealCurrentResult(t, a, b)
	saveCurrentResults(t, a, b)
	base := "sha256:" + strings.Repeat("aa", 32)
	manifest := calibration.ConversionManifest{
		Schema: calibration.ConversionSchema, BaseRevision: base,
		Artifacts: []calibration.ConversionArtifact{
			{Digest: base, Role: "base", Quant: "F16"},
			{Digest: a.Manifest.Model.Value, Role: "derived", Quant: "Q8_0"},
			{Digest: b.Manifest.Model.Value, Role: "derived", Quant: "Q4_K_M"},
		},
	}
	manPath := filepath.Join(dir, "conversion.json")
	if err := calibration.WriteJSON(manPath, manifest); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "pair.json")
	printed, code := captureTopStdout(t, func() int {
		return cmdCalibrate(context.Background(), []string{"m-q8", "m-q4", "--out", out, "--lineage", manPath})
	})
	if code != exitOK {
		t.Fatalf("code = %d\n%s", code, printed)
	}
	if !strings.Contains(printed, "lineage    verified") || strings.Contains(printed, "READY FOR ITEM REVIEW") {
		t.Fatalf("verified unsigned lineage print:\n%s", printed)
	}
	got, err := calibration.ReadPair(out)
	if err != nil {
		t.Fatal(err)
	}
	if got.Lineage == nil || got.Lineage.Validate() != nil || !calibration.AssessPair(got).SameBaseLineageVerified {
		t.Fatalf("exported pair lineage = %+v", got.Lineage)
	}
	if calibration.AssessPair(got).DecisionGrade {
		t.Fatal("unsigned lineage pair became decision-grade")
	}
}

func TestCalibrateLineageRejectsManifestThatOmitsAnArtifact(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	a, b := golden(t), golden(t)
	a.Model, b.Model = "m-q8", "m-q4"
	a.SeedSet, b.SeedSet = "shared", "shared"
	a.ModelMeta.Details.QuantizationLevel = "Q8_0"
	b.ModelMeta.Details.QuantizationLevel = "Q4_K_M"
	sealCurrentResult(t, a, b)
	saveCurrentResults(t, a, b)
	base := "sha256:" + strings.Repeat("bb", 32)
	manifest := calibration.ConversionManifest{
		Schema: calibration.ConversionSchema, BaseRevision: base,
		Artifacts: []calibration.ConversionArtifact{
			{Digest: base, Role: "base"},
			{Digest: a.Manifest.Model.Value, Role: "derived"},
			{Digest: "sha256:" + strings.Repeat("cc", 32), Role: "derived"},
		},
	}
	manPath := filepath.Join(dir, "conversion.json")
	if err := calibration.WriteJSON(manPath, manifest); err != nil {
		t.Fatal(err)
	}
	if code := cmdCalibrate(context.Background(), []string{"m-q8", "m-q4", "--lineage", manPath}); code != exitError {
		t.Fatalf("code = %d, want error when the manifest omits a pair artifact", code)
	}
}

func TestCalibrateMergeWritesMultiDeviceSummary(t *testing.T) {
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")}
	for i, path := range paths {
		family := []string{"fam-a", "fam-b"}[i]
		report := calibration.NewPair(version, 2, []string{"seed-a", "seed-b"}[i],
			calibration.Device{ID: []string{"1111111111111111", "2222222222222222"}[i], GPU: "gpu"},
			calibration.Run{Model: family + "-q8", Quant: "Q8_0", Family: family, ParameterSize: "8B", ResultSchemaVersion: 4},
			calibration.Run{Model: family + "-q4", Quant: "Q4_K_M", Family: family, ParameterSize: "8B", ResultSchemaVersion: 4},
			[]eval.ItemStat{
				{TaskID: "json", Family: "json", Need: "structured_output", Shared: 10, Flips: 1, APass: 10, BPass: 9},
				{TaskID: "stable", Family: "reasoning", Need: "instruction_precision", Shared: 10, APass: 10, BPass: 10},
			})
		if err := calibration.WriteJSON(path, report); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(dir, "summary.json")
	printed, code := captureTopStdout(t, func() int {
		return cmdCalibrateMerge([]string{paths[0], paths[1], "--out", out})
	})
	if code != exitOK {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(printed, "UNVERIFIED") || !strings.Contains(printed, "0/2 report") ||
		strings.Contains(printed, "READY FOR ITEM REVIEW") || strings.Contains(printed, "eligible for written review") {
		t.Fatalf("unsigned imports must remain exploratory:\n%s", printed)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var summary calibration.Summary
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Devices != 2 || summary.Reports != 2 || summary.Readiness.ReadyForReview ||
		summary.Readiness.ModelFamilies != 0 || summary.Readiness.DecisionGradeReports != 0 {
		t.Fatalf("bad merged summary: %+v", summary)
	}
	var reviewCandidates int
	for _, item := range summary.Items {
		if item.ReviewCandidate {
			reviewCandidates++
		}
	}
	if reviewCandidates != 0 {
		t.Fatalf("unsigned imports produced %d review candidate(s): %+v", reviewCandidates, summary.Items)
	}
}

func TestProfilesNewWritesUncalibratedFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_PROFILES", dir)
	old := os.Stdout
	pr, pw, _ := os.Pipe()
	os.Stdout = pw
	code := cmdProfiles(context.Background(), []string{"new", "TestBox"})
	pw.Close()
	os.Stdout = old
	out, _ := io.ReadAll(pr)
	if code != exitOK {
		t.Fatalf("code = %d\n%s", code, out)
	}
	b, err := os.ReadFile(filepath.Join(dir, "testbox.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "UNCALIBRATED") {
		t.Fatalf("file must say uncalibrated:\n%s", b)
	}
}

func TestScreenshotsWriteDemoSVGs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_PROFILES", dir)
	t.Setenv("FITR_RESULTS", t.TempDir())
	t.Setenv("NO_COLOR", "1")
	if code := cmdScreenshots(context.Background(), []string{dir}); code != exitOK {
		t.Fatalf("screenshots exited %d", code)
	}
	if got := os.Getenv("NO_COLOR"); got != "1" {
		t.Fatalf("screenshot generation did not restore NO_COLOR: %q", got)
	}
	for _, name := range []string{"advise.svg", "run.svg", "apply.svg", "board.svg", "top.svg"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		got := string(b)
		if !strings.Contains(got, "<svg") || !strings.Contains(got, "$ fitr") {
			t.Fatalf("%s is not a terminal screenshot:\n%.200s", name, got)
		}
		if name == "board.svg" && !strings.Contains(got, "#d2a8ff") {
			t.Fatalf("board screenshot lost its rich terminal color:\n%.400s", got)
		}
		if name == "top.svg" && !strings.Contains(got, "#79c0ff") {
			t.Fatalf("top screenshot lost its rich terminal color:\n%.400s", got)
		}
	}
}

func TestTunePrintsProtocolWithoutSweeping(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	code := cmdTune(context.Background(), nil)
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	got := string(out)
	if code != exitOK {
		t.Fatalf("code = %d", code)
	}
	for _, want := range []string{"num_ctx", "OLLAMA_FLASH_ATTENTION", "llama-bench", "quality + degeneracy + throughput"} {
		if !strings.Contains(got, want) {
			t.Fatalf("tune protocol missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "sweeping") && strings.Contains(got, "please wait") {
		t.Fatal("tune must not pretend to run a sweep")
	}
}

func TestExportGoldenHTMLCarriesFingerprintAndEscapes(t *testing.T) {
	r := golden(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "scorecard.html")
	path, err := writeHTMLArtifact(r, out, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{
		r.DeviceKey,
		r.Device.GPU,
		"A number without its device is meaningless",
		"num_ctx",
		"8192",
		"PASS",
		"n/a",
		"min detectable effect",
		"Written only because you asked",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("HTML missing %q", want)
		}
	}
	// Raw model output lives in the JSON; the shareable page must not.
	if strings.Contains(got, "parse_duration") || strings.Contains(got, "discounts.py") {
		t.Fatal("HTML must not include raw model output")
	}
	if strings.Contains(got, ".fitr") {
		t.Fatal("HTML must not leak the local results path")
	}
}

func TestWriteHTMLArtifactIsOptIn(t *testing.T) {
	r := golden(t)
	path, err := writeHTMLArtifact(r, "", "ignored.json")
	if err != nil {
		t.Fatal(err)
	}
	if path != "" {
		t.Fatal("empty dest must not write a file")
	}
}

func TestExportRetonrEvidenceIsNotAQualification(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	r := golden(t)
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, safeName(r.Model)+".json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	oldErr := os.Stderr
	er, ew, _ := os.Pipe()
	os.Stderr = ew
	code := cmdExport(context.Background(), []string{"--retonr", r.Model})
	ew.Close()
	os.Stderr = oldErr
	io.ReadAll(er)
	if code != exitOK {
		t.Fatalf("code = %d", code)
	}
	b, err := os.ReadFile(filepath.Join(dir, record.ArtifactStem(r.Model)+".retonr.json"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{
		`"schema": "fitr.retonr.evidence.v1"`,
		`"kind": "device_measurement"`,
		"not a retonr qualification",
		"https://github.com/blisspixel/retonr",
		r.DeviceKey,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("evidence missing %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, safeName(r.Model)+".html")); err == nil {
		t.Fatal("--retonr alone must not also write HTML")
	}
}

func TestDefaultShareArtifactsDoNotCollideAfterNameSanitization(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	models := []string{"org/model", "org:model"}
	results := make([]*Result, 0, len(models))
	for _, model := range models {
		r := golden(t)
		r.Model = model
		results = append(results, r)
	}
	sealCurrentResult(t, results...)
	saveCurrentResults(t, results...)

	htmlPaths := map[string]bool{}
	for _, result := range results {
		path, err := writeHTMLArtifact(result, "auto", "")
		if err != nil {
			t.Fatal(err)
		}
		htmlPaths[path] = true
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
		stderr, code := captureTopStderr(t, func() int {
			return cmdExport(context.Background(), []string{"--retonr", result.Model})
		})
		if code != exitOK {
			t.Fatalf("export %q exit=%d stderr=%s", result.Model, code, stderr)
		}
		retPath := filepath.Join(dir, record.ArtifactStem(result.Model)+".retonr.json")
		if _, err := os.Stat(retPath); err != nil {
			t.Fatal(err)
		}
	}
	if len(htmlPaths) != len(results) {
		t.Fatalf("default HTML paths collided: %v", htmlPaths)
	}
	if record.ArtifactStem(models[0]) == record.ArtifactStem(models[1]) {
		t.Fatal("default retonr paths collided")
	}
}

func TestAdviseMissingModelIsUsage(t *testing.T) {
	if code := cmdAdvise(context.Background(), nil); code != exitUsage {
		t.Fatalf("code = %d, want usage", code)
	}
}

func TestAdviseLocalGGUFDoesNotNeedAServer(t *testing.T) {
	// A metadata-only fixture is tiny; with --vram-gb the weights fit and KV
	// is sized. The point is the command runs without a serving runtime.
	path := writeMiniGGUF(t)
	oldOut, oldErr := os.Stdout, os.Stderr
	or, ow, _ := os.Pipe()
	er, ew, _ := os.Pipe()
	os.Stdout, os.Stderr = ow, ew
	code := cmdAdvise(context.Background(), []string{"--vram-gb=8", "--ctx=4096", "--display=json", path})
	ow.Close()
	ew.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	io.ReadAll(er)
	out, _ := io.ReadAll(or)
	if code != exitOK && code != exitGates {
		t.Fatalf("code = %d, output:\n%s", code, out)
	}
	if !strings.Contains(string(out), `"tier"`) {
		t.Fatalf("json report missing:\n%s", out)
	}
	if strings.Contains(string(out), `"tier": "skip"`) && strings.Contains(string(out), "GPU memory was not measured") {
		t.Fatalf(" --vram-gb was ignored:\n%s", out)
	}
}

func writeMiniGGUF(t *testing.T) string {
	t.Helper()
	// Minimal valid GGUF with the fields Evaluate needs to size KV.
	// File size is the "weights"; that's honest for a fixture.
	buf := new(bytes.Buffer)
	buf.WriteString("GGUF")
	binary.Write(buf, binary.LittleEndian, uint32(3))
	binary.Write(buf, binary.LittleEndian, uint64(0))
	kvs := []struct {
		k string
		v uint64
	}{
		{"llama.block_count", 32},
		{"llama.embedding_length", 4096},
		{"llama.attention.head_count", 32},
		{"llama.attention.head_count_kv", 8},
		{"llama.attention.key_length", 128},
		{"llama.attention.value_length", 128},
		{"llama.context_length", 8192},
	}
	// general.architecture is a string, written separately.
	binary.Write(buf, binary.LittleEndian, uint64(len(kvs)+1))
	writeGGUFString(buf, "general.architecture")
	binary.Write(buf, binary.LittleEndian, uint32(8)) // string
	writeGGUFString(buf, "llama")
	for _, kv := range kvs {
		writeGGUFString(buf, kv.k)
		binary.Write(buf, binary.LittleEndian, uint32(10)) // uint64
		binary.Write(buf, binary.LittleEndian, kv.v)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "mini-Q4_K_M.gguf")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeGGUFString(buf *bytes.Buffer, s string) {
	binary.Write(buf, binary.LittleEndian, uint64(len(s)))
	buf.WriteString(s)
}

func TestSameServedModelDoesNotGuess(t *testing.T) {
	if !sameServedModel("qwen3:30b", "qwen3:30b") || !sameServedModel("qwen3:30b", "qwen3:30b:latest") {
		t.Fatal("tag and :latest must match")
	}
	if sameServedModel("qwen3:30b", "qwen3:8b") || sameServedModel("qwen3:30b", "llama3:8b") {
		t.Fatal("different tags must not match")
	}
}

func TestSelectResolvedModelUsesCanonicalRuntimeIdentity(t *testing.T) {
	models := []ollama.ModelInfo{{Name: "qwen:latest"}}
	got, err := selectResolvedModel("ollama", "qwen", models)
	if err != nil || got.Name != "qwen:latest" {
		t.Fatalf("latest resolution = %+v, %v", got, err)
	}
	served, err := selectResolvedModel("llama-server", "user-alias", []ollama.ModelInfo{{Name: "actual.gguf"}})
	if err != nil || served.Name != "actual.gguf" {
		t.Fatalf("single served model = %+v, %v", served, err)
	}
}

func TestSelectResolvedModelRejectsMissingAndAmbiguousIdentity(t *testing.T) {
	if _, err := selectResolvedModel("openai", "missing", []ollama.ModelInfo{{Name: "actual"}}); err == nil {
		t.Fatal("a missing OpenAI model was accepted")
	}
	duplicate := []ollama.ModelInfo{{Name: "qwen"}, {Name: "qwen:latest"}}
	if _, err := selectResolvedModel("ollama", "qwen", duplicate); err == nil {
		t.Fatal("an ambiguous alias was accepted")
	}
}

func TestPersistenceFailureHasNonzeroPriority(t *testing.T) {
	passing := &Result{}
	if got := runResultExitCode(passing, "checks", errors.New("disk full"), nil); got != exitError {
		t.Fatalf("checks save failure exit = %d, want %d", got, exitError)
	}
	failing := &Result{Scorecard: score.Scorecard{Fails: 1}}
	if got := runResultExitCode(failing, "full", errors.New("permission denied"), nil); got != exitError {
		t.Fatalf("failed-gate save failure exit = %d, want persistence error %d", got, exitError)
	}
	if got := runResultExitCode(failing, "full", nil, nil); got != exitGates {
		t.Fatalf("saved failed-gate exit = %d, want %d", got, exitGates)
	}
}

func TestRequestedHTMLFailureHasNonzeroPriority(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	r := golden(t)
	r.Model = "html-failure"
	sealCurrentResult(t, r)
	jsonPath, err := save(r)
	if err != nil {
		t.Fatal(err)
	}
	htmlPath := strings.TrimSuffix(jsonPath, ".json") + ".html"
	if err := os.Mkdir(htmlPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := writeHTMLArtifact(r, "auto", jsonPath); err == nil {
		t.Fatal("requested HTML unexpectedly succeeded when its destination was a directory")
	} else if got := runResultExitCode(r, "full", nil, err); got != exitError {
		t.Fatalf("HTML failure exit = %d, want %d", got, exitError)
	}
	if info, err := os.Stat(jsonPath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("saved result was not preserved: info=%v err=%v", info, err)
	}
}

func TestQuietFlagCanBeRepeated(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "repeated", args: []string{"model", "-q", "-q"}},
		{name: "explicit count", args: []string{"model", "-q=2"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fs := flag.NewFlagSet("quiet", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			var quiet countFlag
			fs.Var(&quiet, "q", "quiet level")
			if err := fs.Parse(permute(test.args)); err != nil {
				t.Fatal(err)
			}
			if quiet != 2 {
				t.Fatalf("quiet level = %d, want 2", quiet)
			}
			if fs.NArg() != 1 || fs.Arg(0) != "model" {
				t.Fatalf("positional arguments = %q, want model", fs.Args())
			}
		})
	}
}

func TestArtifactRejectsStoredScorecardTampering(t *testing.T) {
	r := golden(t)
	r.Model = "tamper-check"
	sealCurrentResult(t, r)
	r.Scorecard = score.Scorecard{
		Model: r.Model,
		Needs: map[string]score.Verdict{
			"fabricated": {State: score.Pass, Why: "stored claim"},
		},
		Serves: []string{"fabricated"}, Passes: 1,
	}
	if _, err := artifactFrom(r); err == nil {
		t.Fatalf("tampered stored scorecard was accepted: %v", err)
	}
}

type runIntegrationBackend struct {
	stopCalls     int
	generateCalls int
	stopErrAt     map[int]error
	generateErrAt map[int]error
	digest        string
	effectiveCtx  int
	contextErr    error
}

type digestVerifyingBackend struct {
	*runIntegrationBackend
	reported string
	called   bool
}

func (b *digestVerifyingBackend) Tags(context.Context) ([]ollama.ModelInfo, error) {
	return []ollama.ModelInfo{{Name: "model", ReportedDigest: b.reported, Size: 1024}}, nil
}

func (b *digestVerifyingBackend) VerifyModelDigest(model, reported string) (string, error) {
	b.called = true
	if model != "model" || reported != b.reported {
		return "", errors.New("unexpected model identity input")
	}
	return integrationDigest(), nil
}

var _ llm.Backend = (*runIntegrationBackend)(nil)
var _ llm.EffectiveContextObserver = (*runIntegrationBackend)(nil)

func (b *runIntegrationBackend) Name() string                   { return "fake" }
func (b *runIntegrationBackend) URL() string                    { return "fake://local" }
func (b *runIntegrationBackend) Version(context.Context) string { return "fake-runtime-v1" }
func (b *runIntegrationBackend) Reachable(context.Context) bool { return true }
func (b *runIntegrationBackend) StopAll(context.Context) ([]string, error) {
	b.stopCalls++
	if err := b.stopErrAt[b.stopCalls]; err != nil {
		return nil, err
	}
	return nil, nil
}
func (b *runIntegrationBackend) Tags(context.Context) ([]ollama.ModelInfo, error) {
	return []ollama.ModelInfo{{Name: "model", Digest: b.digest, Size: 1024}}, nil
}
func (b *runIntegrationBackend) Show(context.Context, string) (ollama.ModelInfo, error) {
	return ollama.ModelInfo{Name: "model", Capabilities: []string{"completion"}}, nil
}
func (b *runIntegrationBackend) PS(context.Context) ([]ollama.RunningModel, error) {
	return []ollama.RunningModel{{Name: "model", Size: 1024, SizeVRAM: 1024}}, nil
}
func (b *runIntegrationBackend) EffectiveContext(context.Context, string) (int, bool, error) {
	if b.contextErr != nil {
		return 0, false, b.contextErr
	}
	return b.effectiveCtx, b.effectiveCtx > 0, nil
}
func (b *runIntegrationBackend) Generate(context.Context, string, string, ollama.Sampling) (string, ollama.Metrics, error) {
	b.generateCalls++
	if err := b.generateErrAt[b.generateCalls]; err != nil {
		return "", ollama.Metrics{}, err
	}
	return "OK", ollama.Metrics{
		DecodeTPS: 12, PrefillTPS: 100, TTFTSeconds: 0.2,
		PromptTokens: 64, EvalCount: 8,
	}, nil
}
func (b *runIntegrationBackend) Chat(context.Context, string, []ollama.Message, []ollama.Tool, ollama.Sampling) (ollama.Message, ollama.Metrics, error) {
	return ollama.Message{Role: "assistant", Content: "DONE"}, ollama.Metrics{}, nil
}

func integrationDigest() string {
	return "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
}

func TestDiscoveredOpenAIBackendNeverReceivesEnvironmentCredential(t *testing.T) {
	receivedAuth := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer server.Close()

	t.Setenv("FITR_OPENAI_URL", "https://api.openai.com")
	t.Setenv("OPENAI_API_KEY", "cloud-secret")
	t.Setenv("FITR_OPENAI_API_KEY", "compatible-secret")
	b, err := backendAt("openai", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !b.Reachable(context.Background()) {
		t.Fatal("discovered backend was not reachable")
	}
	if receivedAuth != "" {
		t.Fatalf("auto-discovery sent Authorization: %q", receivedAuth)
	}
}

func TestResolveRunModelPromotesOnlyVerifierApprovedDigest(t *testing.T) {
	b := &digestVerifyingBackend{
		runIntegrationBackend: &runIntegrationBackend{},
		reported:              integrationDigest(),
	}
	resolved, err := resolveRunModel(context.Background(), b, "model")
	if err != nil {
		t.Fatal(err)
	}
	if !b.called {
		t.Fatal("model digest verifier was not called")
	}
	if resolved.Info.Digest != integrationDigest() || resolved.Identity.Value != integrationDigest() {
		t.Fatalf("verified identity was not promoted: %+v", resolved)
	}
}

func TestExecuteFakeBackendProducesCompleteSealedQuickRun(t *testing.T) {
	backend := &runIntegrationBackend{digest: integrationDigest(), effectiveCtx: eval.NumCtx}
	display := render.New("none")
	defer display.Close()
	result, err := execute(context.Background(), backend, "model", runOpts{
		level: "quick", profile: "default", reps: 1, checksReps: 1,
	}, display)
	if err != nil {
		t.Fatal(err)
	}
	if result.Manifest == nil || result.Manifest.Schema != record.RunManifestSchema ||
		result.Manifest.Model.Value != integrationDigest() || result.Manifest.Provenance == nil {
		t.Fatalf("manifest identity = %+v", result.Manifest)
	}
	if result.DeviceV2 == nil || result.DeviceV2.Context.State() != device.ContextVerified {
		t.Fatalf("context receipt = %+v", result.DeviceV2)
	}
	if _, err := result.ComparableDeviceKey(); err != nil {
		t.Fatalf("verified run is not comparable: %v", err)
	}
	if err := result.ValidateEvidenceContract(); err != nil {
		t.Fatalf("evidence contract = %v", err)
	}
	for _, phase := range []string{"coding", "tools", "agentic"} {
		if result.EvidenceCounts[phase].Scorable != 0 {
			t.Fatalf("%s executable evidence became scoreable: %+v", phase, result.EvidenceCounts[phase])
		}
	}
}

func TestExecuteFakeBackendInfrastructureFaultsCannotReturnAResult(t *testing.T) {
	for _, test := range []struct {
		name    string
		backend *runIntegrationBackend
	}{
		{name: "mutable identity", backend: &runIntegrationBackend{}},
		{name: "generation transport", backend: &runIntegrationBackend{
			digest: integrationDigest(), generateErrAt: map[int]error{2: errors.New("injected transport fault")},
		}},
		{name: "context receipt", backend: &runIntegrationBackend{
			digest: integrationDigest(), contextErr: errors.New("injected context fault"),
		}},
		{name: "final unload", backend: &runIntegrationBackend{
			digest: integrationDigest(), stopErrAt: map[int]error{3: errors.New("injected unload fault")},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			display := render.New("none")
			defer display.Close()
			result, err := execute(context.Background(), test.backend, "model", runOpts{
				level: "quick", profile: "default", reps: 1, checksReps: 1,
			}, display)
			if err == nil || result != nil {
				t.Fatalf("result=%+v error=%v", result, err)
			}
		})
	}
}

func TestMeasureExcludesUnavailableTaskOutcomes(t *testing.T) {
	r := &Result{
		Repeats:   1,
		CodeWrite: []eval.ExecResult{{Pass: true, Outcome: eval.OutcomeInconclusive}},
		CodeFix:   []eval.ExecResult{{Pass: false, Outcome: eval.OutcomeSkipped}},
		Tools:     []eval.ToolLoopResult{{Pass: true, Outcome: eval.OutcomeInconclusive}},
		Refusal: map[string]eval.RefusalVerdict{
			"political": {Verdict: "answered", Outcome: eval.OutcomePass},
			"fiction":   {Verdict: "error", Outcome: eval.OutcomeError},
		},
	}
	m := measure(r)
	if m.CodeKnown || m.CodeRepeats != 0 {
		t.Fatalf("unverified code entered scoring: known=%v repeats=%d", m.CodeKnown, m.CodeRepeats)
	}
	if m.ToolsRan {
		t.Fatal("unverified tool observation entered scoring")
	}
	if m.RefusalKnown {
		t.Fatal("partial refusal evidence became a known refusal result")
	}
	if p := poolOf(r, "coding"); p.N != 0 {
		t.Fatalf("comparison denominator includes unavailable code: %+v", p)
	}
}

func TestRunTaskPlanAndEvidenceCountsKeepImmutableDenominators(t *testing.T) {
	plan := runTaskPlan("full", 3, 1, false, 16, 3)
	if plan.CodeTrials != 6 || plan.CheckTrialsLimit != 16 || plan.ToolTrials != 3 ||
		!plan.Withdrawal || plan.RefusalTrials != 3 || plan.AgenticTrials != 1 {
		t.Fatalf("full plan = %+v", plan)
	}
	adaptive := runTaskPlan("default", 3, 1, true, 16, 3)
	if adaptive.CheckTrialsLimit != 96 || !adaptive.AdaptiveChecks {
		t.Fatalf("adaptive check limit = %d, want 96", adaptive.CheckTrialsLimit)
	}
	checks := runTaskPlan("checks", 5, 5, false, 16, 3)
	if checks.CheckTrialsLimit != 80 || checks.CodeTrials != 0 || checks.Plumbing {
		t.Fatalf("checks-only plan = %+v", checks)
	}

	r := &Result{SchemaVersion: 5, TaskPlan: record.TaskPlan{CodeTrials: 2}}
	r.CodeWrite = []eval.ExecResult{{Outcome: eval.OutcomeSkipped}}
	counts := buildEvidenceCounts(r)
	if counts["coding"].Complete() {
		t.Fatalf("one of two planned coding trials disappeared: %+v", counts["coding"])
	}
	r.CodeFix = []eval.ExecResult{{Outcome: eval.OutcomeSkipped}}
	if got := buildEvidenceCounts(r)["coding"]; !got.Complete() || got.Skipped != 2 || got.Scorable != 0 {
		t.Fatalf("explicitly skipped denominator = %+v", got)
	}
}

func TestNormalizeModelRefAcceptsPastedHFLinks(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"qwen3-coder:30b", "qwen3-coder:30b"},
		{"hf.co/bartowski/Foo-GGUF", "hf.co/bartowski/Foo-GGUF"},
		{"hf.co/bartowski/Foo-GGUF:Q4_K_M", "hf.co/bartowski/Foo-GGUF:Q4_K_M"},
		{"https://huggingface.co/bartowski/Qwen2.5-Coder-7B-Instruct-GGUF",
			"hf.co/bartowski/Qwen2.5-Coder-7B-Instruct-GGUF"},
		{"https://huggingface.co/bartowski/Qwen2.5-Coder-7B-Instruct-GGUF/",
			"hf.co/bartowski/Qwen2.5-Coder-7B-Instruct-GGUF"},
		{"https://huggingface.co/bartowski/Foo-GGUF:Q8_0",
			"hf.co/bartowski/Foo-GGUF:Q8_0"},
		{"https://huggingface.co/bartowski/Foo-GGUF/tree/main",
			"hf.co/bartowski/Foo-GGUF"},
		{"https://huggingface.co/bartowski/Foo-GGUF/blob/main/Foo-Q4_K_M.gguf",
			"hf.co/bartowski/Foo-GGUF:Q4_K_M"},
		{"https://huggingface.co/bartowski/Foo-GGUF/resolve/main/Foo.Q8_0.gguf?download=true",
			"hf.co/bartowski/Foo-GGUF:Q8_0"},
		{"http://hf.co/org/model", "hf.co/org/model"},
		{"huggingface.co/org/model", "hf.co/org/model"},
	}
	for _, tc := range cases {
		if got := normalizeModelRef(tc.in); got != tc.want {
			t.Errorf("normalizeModelRef(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if !isHFRef(normalizeModelRef("https://huggingface.co/a/b")) {
		t.Fatal("a pasted HF URL must become an hf.co/ ref")
	}
	if isHFRef("qwen3-coder:30b") {
		t.Fatal("an Ollama tag is not an HF ref")
	}
}

func TestApplyPrintsWithoutMutating(t *testing.T) {
	oldOut, oldErr := os.Stdout, os.Stderr
	or, ow, _ := os.Pipe()
	er, ew, _ := os.Pipe()
	os.Stdout, os.Stderr = ow, ew
	code := cmdApply(context.Background(), []string{"--backend=ollama", "--ctx=4096", "qwen3:30b"})
	ow.Close()
	ew.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	io.ReadAll(er)
	out, _ := io.ReadAll(or)
	got := string(out)
	if code != exitOK {
		t.Fatalf("code = %d\n%s", code, got)
	}
	for _, want := range []string{"does not restart", "num_ctx 4096", "ollama create qwen3:30b-ctx4096"} {
		if !strings.Contains(got, want) {
			t.Fatalf("apply missing %q:\n%s", want, got)
		}
	}
}

func TestApplyJSONSaysMutatesFalse(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	code := cmdApply(context.Background(), []string{"--display=json", "--backend=llama-server", "--ctx=2048"})
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	if code != exitOK {
		t.Fatalf("code = %d\n%s", code, out)
	}
	var plan struct {
		Mutates bool     `json:"mutates"`
		Ctx     int      `json:"ctx"`
		Steps   []string `json:"steps"`
	}
	if err := json.Unmarshal(out, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Mutates || plan.Ctx != 2048 {
		t.Fatalf("plan = %+v", plan)
	}
	joined := strings.Join(plan.Steps, "\n")
	if !strings.Contains(joined, "--ctx-size 2048") {
		t.Fatalf("steps = %s", joined)
	}
}

func TestApplyHumanOutputNeutralizesTerminalControls(t *testing.T) {
	hostile := "safe\x1b[2J\x1b]0;forged title\a\x1bPforged payload\x1b\\\r\n\tleft\u202eright"
	out, code := captureTopStdout(t, func() int {
		return cmdApply(context.Background(), []string{hostile, "--ctx", "4096", "--backend", "ollama", "--display", "plain"})
	})
	if code != exitOK {
		t.Fatalf("apply exit=%d output=%q", code, out)
	}
	if strings.ContainsAny(out, "\x1b\a\r\t\u202e") || strings.Contains(out, "forged title") ||
		strings.Contains(out, "forged payload") {
		t.Fatalf("apply leaked terminal controls: %q", out)
	}
	if !strings.Contains(out, "safe") || !strings.Contains(out, "leftright") {
		t.Fatalf("apply lost ordinary model text: %q", out)
	}
}

func TestBoardLabelsCtxSplitAsThisMachine(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	cur := device.Detect(context.Background(), probeBackend(context.Background()))
	a := golden(t)
	a.Model = "aa-default"
	a.NumCtx = eval.NumCtx
	a.Device, a.DeviceKey = cur, cur.Key()
	b := golden(t)
	b.Model = "bb-ctx4k"
	b.NumCtx = 4096
	b.Device = cur
	b.DeviceKey = cur.Key() + eval.CtxKeySuffix(4096)
	sealCurrentResult(t, a, b)
	saveCurrentResults(t, a, b)
	oldOut, oldErr := os.Stdout, os.Stderr
	or, ow, _ := os.Pipe()
	er, ew, _ := os.Pipe()
	os.Stdout, os.Stderr = ow, ew
	code := cmdBoard(context.Background(), nil)
	ow.Close()
	ew.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	io.ReadAll(er)
	out, _ := io.ReadAll(or)
	got := string(out)
	if code != exitOK {
		t.Fatalf("code = %d\n%s", code, got)
	}
	if !strings.Contains(got, "num_ctx=4096") {
		t.Fatalf("ctx split must be labeled as context, not a different box:\n%s", got)
	}
	if strings.Count(got, "different hardware/config - not comparable to other blocks") != 0 {
		t.Fatalf("a ctx-only split on this machine must not read as different hardware:\n%s", got)
	}
	if !strings.Contains(got, "ctx 4096") || !strings.Contains(got, "ctx 8192") {
		t.Fatalf("board header must show ctx:\n%s", got)
	}
}

func TestCompareCtxSplitNamesTheKnob(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	a, b := golden(t), golden(t)
	a.Model, b.Model = "aa", "bb"
	b.NumCtx = 4096
	b.DeviceKey = a.DeviceKey + eval.CtxKeySuffix(4096)
	sealCurrentResult(t, a, b)
	saveCurrentResults(t, a, b)
	old := os.Stderr
	er, ew, _ := os.Pipe()
	os.Stderr = ew
	code := cmdCompare(context.Background(), []string{"aa", "bb"})
	ew.Close()
	os.Stderr = old
	errOut, _ := io.ReadAll(er)
	got := string(errOut)
	if code != exitError {
		t.Fatalf("code = %d, want error", code)
	}
	if !strings.Contains(got, "different request context") {
		t.Fatalf("compare must name the ctx split:\n%s", got)
	}
}

func TestTuneDiffsNumCtx(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	a, b := golden(t), golden(t)
	a.Model, b.Model = "aa", "bb"
	a.NumCtx, b.NumCtx = eval.NumCtx, 4096
	for _, r := range []*Result{a, b} {
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, safeName(r.Model)+".json"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := os.Stdout
	or, ow, _ := os.Pipe()
	os.Stdout = ow
	code := cmdTune(context.Background(), []string{"aa", "bb"})
	ow.Close()
	os.Stdout = old
	out, _ := io.ReadAll(or)
	got := string(out)
	if code != exitOK {
		t.Fatalf("code = %d\n%s", code, got)
	}
	if !strings.Contains(got, "8192") || !strings.Contains(got, "->") || !strings.Contains(got, "4096") {
		t.Fatalf("tune must show the ctx delta:\n%s", got)
	}
}

func TestResultNumCtxFallsBackToDefault(t *testing.T) {
	if resultNumCtx(&Result{}) != eval.NumCtx {
		t.Fatal("empty result is the default ctx")
	}
	if resultNumCtx(&Result{NumCtx: 4096}) != 4096 {
		t.Fatal("recorded num_ctx wins")
	}
	if resultNumCtx(&Result{DeviceKey: "host|gpu|ctx=2048"}) != 2048 {
		t.Fatal("legacy key suffix must still parse")
	}
}
