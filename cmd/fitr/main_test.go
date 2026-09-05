package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/calibration"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/score"
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

func TestAnalysisActionCommandSubstitutesAndQuotesOnlyTheModel(t *testing.T) {
	action := analysis.Action{Argv: []string{
		"fitr", "run", analysis.CurrentModelPlaceholder, "-k", "3",
	}}
	if got := analysisActionCommand(action, "local model"); got != `fitr run 'local model' -k 3` {
		t.Fatalf("command = %q", got)
	}
	if action.Argv[2] != analysis.CurrentModelPlaceholder {
		t.Fatal("rendering mutated the semantic action template")
	}
}

func TestAnalysisActionCommandQuotesHostileModelLabels(t *testing.T) {
	action := analysis.Action{Argv: []string{"fitr", "run", analysis.CurrentModelPlaceholder}}
	for _, model := range []string{
		`$(Get-Content private).gguf`,
		`model; Remove-Item private`,
		`C:\private\model&whoami.gguf`,
	} {
		want := "fitr run '" + model + "'"
		if got := analysisActionCommand(action, model); got != want {
			t.Errorf("command for %q = %q, want %q", model, got, want)
		}
	}
}

func TestShellCommandArgEscapesEmbeddedQuotesForTargetShell(t *testing.T) {
	const arg = "model'$(command).gguf"
	if got := shellCommandArg(arg, "windows"); got != `'model''$(command).gguf'` {
		t.Fatalf("PowerShell argument = %q", got)
	}
	if got := shellCommandArg(arg, "linux"); got != `'model'"'"'$(command).gguf'` {
		t.Fatalf("POSIX argument = %q", got)
	}
}

func TestCurrentInvalidEvidenceDoesNotProjectRawPerformance(t *testing.T) {
	result := mockResult("tampered", 10, .1, 100, 1, 1, 1, 1, 1)
	result.DecodeSum.Mean = 999
	meta := resultMeta(result, result.Profile)
	if meta.Analysis != nil || meta.DecodeMean != 0 || meta.DecodeN != 0 || len(meta.DecodeSeries) != 0 {
		t.Fatalf("invalid current evidence reached presentation: %+v", meta)
	}
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
		"fast_and_decent":       score.Inconclusive, // legacy receipt did not preserve cache-known state
		"coding":                score.Pass,
		"structured_output":     score.Inconclusive, // point estimate clears the gate, interval does not
		"instruction_precision": score.Inconclusive, // 4/4 is still thin evidence against a 0.70 gate
		"uncensored":            score.Pass,
		"unattended_agentic":    score.Inconclusive, // legacy receipt did not preserve prefill cache state
		"tool_restraint":        score.Inconclusive, // one repeated family cannot establish a broader need
		"low_footprint":         score.Skip,         // legacy 32K request did not preserve effective context
		"vision":                score.NA,           // never claimed; not a deficiency
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
	// Executable coding and generated reasoning have different evidence
	// structures and remain separate.
	if why := sc.Needs["coding"].Why; !strings.Contains(why, "6/6 executable") ||
		!strings.Contains(why, "reasoning 4/5") || strings.Contains(why, "10/11") {
		t.Errorf("coding why = %q, want separated executable and reasoning evidence", why)
	}
}

func TestGoldenResultRendersCleanly(t *testing.T) {
	r := golden(t)
	sc := score.Score(measure(r), lappyProfile(t))

	meta := render.Meta{
		ParamSize: "30.5B", Quant: "Q4_K_M", Family: "qwen3moe",
		GPU: r.Device.GPU, Driver: r.Device.GPUDriver, Device: "GPU 100%",
		Profile: "lappy", NumCtx: resultNumCtx(r), Repeats: 3,
		DecodeMean: r.DecodeSum.Mean, DecodeSD: r.DecodeSum.SD,
		DecodeMin: r.DecodeSum.Min, DecodeMax: r.DecodeSum.Max, DecodeN: r.DecodeSum.N,
		PrefillMean: r.PrefillSum.Mean, PrefillSD: r.PrefillSum.SD, PrefillN: r.PrefillSum.N,
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

	// Prose wraps to the rule, so assertions on sentences run against the
	// reflowed text. Assertions on columns run against the raw output.
	flat := strings.Join(strings.Fields(got), " ")
	for _, want := range []string{
		"[PASS]", "[n/a ]",
		"valid structured output",
		"follows exact instructions",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("rendered scorecard missing %q\n%s", want, got)
		}
	}
	for _, never := range []string{"+/- 0.00", "not recommended", "[FAIL]", "resolves ~", "against a gate"} {
		if strings.Contains(flat, never) {
			t.Errorf("rendered scorecard must not contain %q\n%s", never, got)
		}
	}

	// The header keeps its aligned label column, which reflowing would erase.
	if !strings.Contains(got, "ctx      8192") {
		t.Errorf("header lost its aligned value column\n%s", got)
	}

	// The rule is the promise the report makes about its own width. Every row
	// used to be free to break it: 7 of 10 verdict rows did, the longest at 224
	// columns against a 78-column rule, which wrapped into unreadable text at
	// exactly the moment a reader was trying to find the failing need.
	width := render.DefaultWidth
	for _, line := range strings.Split(got, "\n") {
		if n := len([]rune(line)); n > width {
			t.Errorf("line is %d cols against a %d-col rule: %q", n, width, line)
		}
	}
}

// A degraded variant of the golden run with broken structured output and a
// looping longest-sample. This pins the FAIL paths end to end, not just the
// happy ones.
func TestDegradedResultFailsTheRightNeeds(t *testing.T) {
	r := golden(t)
	for i := range r.Speed {
		r.Speed[i].GatedCacheKnown = true
		r.Speed[i].GatedPromptTok = 32
		r.Speed[i].GatedLoadKnown = true
		r.Speed[i].GatedResidencyKnown = true
		r.Speed[i].GatedResident = true
	}
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
	older.Model, older.Scorecard.Model, older.StartedAt = "older", "older", "2026-01-01T17:30:00Z"
	newer.Model, newer.Scorecard.Model, newer.StartedAt = "newer", "newer", "2026-01-01T10:00:00-08:00"
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
	// The spread rides on the row it belongs to. The old caption named an axis
	// ("oldest to newest") without ever giving a scale, so the glyphs it
	// explained were still unreadable; min and max on the row are the scale.
	for _, want := range []string{"performance", "decode", "prefill", "min 22.71, max 23.60"} {
		if !strings.Contains(got, want) {
			t.Fatalf("view missing %q:\n%s", want, got)
		}
	}
}

func TestViewReadsResultPathAndEmitsBothJSONShapes(t *testing.T) {
	dir, path, result := writeViewResultFixture(t)
	// --full returns the sealed record unchanged.
	full := emitViewJSON(t, dir, path, "--display", "json", "--full")
	assertFullViewJSON(t, full, result)

	// The default is the scorecard the command prints. The record behind it is
	// an order of magnitude larger and carries per-trial evidence nobody
	// reading `view` asked for.
	brief := emitViewJSON(t, dir, path, "--display", "json")
	assertBriefViewJSON(t, brief, full, result)
}

func writeViewResultFixture(t *testing.T) (string, string, *Result) {
	t.Helper()
	dir := t.TempDir()
	result := golden(t)
	path := filepath.Join(dir, "result.json")
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, path, result
}

func emitViewJSON(t *testing.T, dir, path string, args ...string) []byte {
	t.Helper()
	out, err := os.CreateTemp(dir, "view-output-*.json")
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = out
	code := cmdView(context.Background(), append([]string{path}, args...))
	out.Close()
	os.Stdout = saved
	body, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	if code != exitOK {
		t.Fatalf("view exited %d: %s", code, body)
	}
	return body
}

func assertFullViewJSON(t *testing.T, full []byte, result *Result) {
	t.Helper()
	var got Result
	if err := json.Unmarshal(full, &got); err != nil {
		t.Fatalf("view --full JSON is invalid: %v", err)
	}
	if got.Model != result.Model || got.SchemaVersion != result.SchemaVersion {
		t.Fatalf("view --full changed the saved result: got %q schema %d", got.Model, got.SchemaVersion)
	}
}

func assertBriefViewJSON(t *testing.T, brief, full []byte, result *Result) {
	t.Helper()
	var card map[string]any
	if err := json.Unmarshal(brief, &card); err != nil {
		t.Fatalf("view JSON is invalid: %v", err)
	}
	if card["schema"] != "fitr.view.v1" {
		t.Fatalf("view JSON does not identify itself: %v", card["schema"])
	}
	if card["model"] != result.Model {
		t.Fatalf("view JSON names %v, want %q", card["model"], result.Model)
	}
	if card["scorecard"] == nil {
		t.Fatalf("view JSON carries no scorecard")
	}
	// A projection that approaches the size of the record it replaced has
	// stopped being a projection.
	if len(brief) >= len(full) {
		t.Fatalf("projection is %d bytes against a %d-byte record", len(brief), len(full))
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

func TestBareFitrIsStatusNotUsage(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	code := cmdStatus(context.Background(), nil)
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	got := string(out)
	if !strings.Contains(got, "fitr "+version) {
		t.Fatalf("bare fitr must be a status page:\n%s", got)
	}
	if !strings.Contains(got, "cpu") || !strings.Contains(got, "logical") {
		t.Fatalf("bare fitr must show logical CPUs as display-only:\n%s", got)
	}
	if !strings.Contains(got, "none reachable") && !strings.Contains(got, "STATE") && !strings.Contains(got, "no models") {
		t.Fatalf("bare fitr must be inventory or an empty-runtime page:\n%s", got)
	}
	if strings.Contains(got, "run <model> --full") {
		t.Fatalf("first-run next must be the default battery, not --full:\n%s", got)
	}
	if code == exitUsage {
		t.Fatal("bare fitr is not a usage error")
	}
}

func TestBareModelIsAdvise(t *testing.T) {
	path := writeMiniGGUF(t)
	oldArgs := os.Args
	os.Args = []string{"fitr", path, "--vram-gb=8", "--ctx=4096", "--display=json"}
	defer func() { os.Args = oldArgs }()
	oldOut, oldErr := os.Stdout, os.Stderr
	or, ow, _ := os.Pipe()
	er, ew, _ := os.Pipe()
	os.Stdout, os.Stderr = ow, ew
	code := run()
	ow.Close()
	ew.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	io.ReadAll(er)
	out, _ := io.ReadAll(or)
	if code != exitOK && code != exitGates {
		t.Fatalf("code = %d, output:\n%s", code, out)
	}
	if !strings.Contains(string(out), `"tier"`) {
		t.Fatalf("fitr <model> must be named advise:\n%s", out)
	}
}

func TestAdviseWithoutModelIsInventory(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	code := cmdAdvise(context.Background(), nil)
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	got := string(out)
	if code == exitUsage {
		t.Fatalf("advise with no model is inventory, not usage:\n%s", got)
	}
	if !strings.Contains(got, "fitr "+version) {
		t.Fatalf("advise inventory missing header:\n%s", got)
	}
}

func TestAdviseLoadWithoutModelIsUsage(t *testing.T) {
	if code := cmdAdvise(context.Background(), []string{"--load"}); code != exitUsage {
		t.Fatalf("advise --load with no model must be usage, got %d", code)
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
	if !strings.Contains(got, "fitr update") {
		t.Fatalf("usage must list update:\n%s", got)
	}
	if !strings.Contains(got, "--retonr") {
		t.Fatalf("usage must mention the optional retonr export:\n%s", got)
	}
	if !strings.Contains(got, "--checks-only") || !strings.Contains(got, "calibrate merge") {
		t.Fatalf("usage must include the calibration workflow:\n%s", got)
	}
}

func TestUpdateHelpAndFlagValidation(t *testing.T) {
	if commandHandler("update") == nil {
		t.Fatal("update command is not registered")
	}
	if code := cmdUpdate(context.Background(), []string{"--help"}); code != exitOK {
		t.Fatalf("update --help exit = %d", code)
	}
	if code := cmdUpdate(context.Background(), []string{"--check", "--reinstall"}); code != exitUsage {
		t.Fatalf("incompatible update flags exit = %d", code)
	}
	if code := cmdUpdate(context.Background(), []string{"--display", "invalid"}); code != exitUsage {
		t.Fatalf("invalid update display exit = %d", code)
	}
}

func TestChecksOnlyRequiresFixedPairing(t *testing.T) {
	if code := cmdRun(context.Background(), []string{"m", "--checks-only"}); code != exitUsage {
		t.Fatalf("code = %d, want usage without a seedset", code)
	}
	if code := cmdRun(context.Background(), []string{"m", "--adaptive"}); code != exitUsage {
		t.Fatalf("code = %d, want usage for removed adaptive mode", code)
	}
	if code := cmdRun(context.Background(), []string{"m", "--checks-only", "--seedset", "s", "--html"}); code != exitUsage {
		t.Fatalf("code = %d, want usage for standalone calibration HTML", code)
	}
}

func TestCalibrateReportsNeverFlippedItems(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	hi, pairPath := writeNeverFlippedCalibrationFixture(t, dir)
	got, code := captureCalibrationPair(t, pairPath)
	assertCalibrationPairOutput(t, got, code)
	assertCalibrationPairArtifact(t, pairPath, hi.Device.Host)
}

func writeNeverFlippedCalibrationFixture(t *testing.T, dir string) (*Result, string) {
	t.Helper()
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
	return hi, filepath.Join(dir, "pair.json")
}

func captureCalibrationPair(t *testing.T, pairPath string) (string, int) {
	t.Helper()
	old := os.Stdout
	pr, pw, _ := os.Pipe()
	os.Stdout = pw
	code := cmdCalibrate(context.Background(), []string{"m-q8", "m-q4", "--out", pairPath})
	pw.Close()
	os.Stdout = old
	out, _ := io.ReadAll(pr)
	return string(out), code
}

func assertCalibrationPairOutput(t *testing.T, got string, code int) {
	t.Helper()
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
}

func assertCalibrationPairArtifact(t *testing.T, pairPath, privateHost string) {
	t.Helper()
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
	for _, forbidden := range []string{privateHost, "CODE_WRITE_OK", ".fitr"} {
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
	for _, name := range []string{"advise.svg", "run.svg", "apply.svg", "board.svg", "top.svg", "discovery.svg"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		got := string(b)
		if !strings.Contains(got, "<svg") || (name != "top.svg" && !strings.Contains(got, "$ fitr")) {
			t.Fatalf("%s is not a terminal screenshot:\n%.200s", name, got)
		}
		if name == "top.svg" && strings.Contains(got, "$ fitr top") {
			t.Fatalf("top screenshot should show the full-screen surface, not a shell prompt:\n%.400s", got)
		}
		if name == "board.svg" && !strings.Contains(got, "#d2a8ff") {
			t.Fatalf("board screenshot lost its rich terminal color:\n%.400s", got)
		}
		if name == "top.svg" && !strings.Contains(got, "#56d4dd") {
			t.Fatalf("top screenshot lost its rich terminal color:\n%.400s", got)
		}
	}
}

func TestCaptureStdoutDrainsWhileTheProducerWrites(t *testing.T) {
	const size = 1 << 20
	want := strings.Repeat("x", size)
	got, err := captureStdout(context.Background(), func(context.Context) (string, error) {
		if _, writeErr := io.WriteString(os.Stdout, want); writeErr != nil {
			return "", writeErr
		}
		return "prefix", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len("prefix")+size || !strings.HasPrefix(got, "prefix") ||
		!strings.HasSuffix(got, "xxxx") {
		t.Fatalf("captured %d bytes, want %d", len(got), len("prefix")+size)
	}
}

// Every one-shot surface composes to the same width. The interactive top demo
// deliberately uses a wider desktop canvas so the README can show its real
// master-detail layout instead of a stretched single-column capture.
//
// It used to be worth checking and nobody was: the scorecard reached 224
// columns, the inventory 117, doctor 286, and the board drew a 104-column rule
// around 123-column rows.
func TestEveryDemoSurfaceComposesToTheWidth(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_PROFILES", dir)
	t.Setenv("FITR_RESULTS", t.TempDir())
	t.Setenv("NO_COLOR", "1")
	if code := cmdScreenshots(context.Background(), []string{dir}); code != exitOK {
		t.Fatalf("screenshots exited %d", code)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".svg") {
			continue
		}
		seen++
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		limit := render.DefaultWidth
		if e.Name() == "top.svg" {
			limit = 120
		}
		for _, line := range svgTextLines(string(b)) {
			if n := len([]rune(line)); n > limit {
				t.Errorf("%s: %d cols against %d: %q", e.Name(), n, limit, line)
			}
		}
	}
	if seen < 8 {
		t.Fatalf("only %d demo surfaces rendered; expected every one", seen)
	}
}

// svgTextLines recovers the terminal lines a demo SVG was built from.
func svgTextLines(svg string) []string {
	var out []string
	for _, block := range regexp.MustCompile(`(?s)<text[^>]*>(.*?)</text>`).FindAllStringSubmatch(svg, -1) {
		stripped := regexp.MustCompile(`<[^>]*>`).ReplaceAllString(block[1], "")
		out = append(out, html.UnescapeString(stripped))
	}
	return out
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
		"device-",
		r.Device.GPU,
		"A number without its device is meaningless",
		"num_ctx",
		"8192",
		// Every need in this fixture is excluded, so the page must show the
		// excluded state and say why. It previously asserted "PASS", which
		// appeared only inside the phrase "excluded from PASS/FAIL claims" --
		// the test was passing on a substring of an explanation, not on a
		// verdict, and it went red the moment that sentence was reworded.
		"INCONCLUSIVE",
		"does not count",
		"n/a",
		"Written only because you asked",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("HTML missing %q", want)
		}
	}
	if strings.Contains(got, "resolves ~") || strings.Contains(got, "against a gate") {
		t.Fatal("HTML invented a global resolution claim across heterogeneous needs")
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

func TestAdviseMissingModelIsInventory(t *testing.T) {
	if code := cmdAdvise(context.Background(), nil); code == exitUsage {
		t.Fatal("advise with no model is inventory, not usage")
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

func TestLatestNamedNeverGuessesAcrossSavedModelNames(t *testing.T) {
	results := []*Result{
		{Model: "qwen:latest", StartedAt: "2026-08-20T12:00:00Z"},
		{Model: "qwen-coder:latest", StartedAt: "2026-08-21T12:00:00Z"},
	}
	if got := latestNamed(results, "qwen"); got == nil || got.Model != "qwen:latest" {
		t.Fatalf("exact :latest alias resolved to %+v", got)
	}
	if got := latestNamed(results, "qwe"); got != nil {
		t.Fatalf("partial saved-model name selected %+v", got)
	}
}

func TestLatestNamedOrdersRFC3339InstantsNotText(t *testing.T) {
	results := []*Result{
		{Model: "qwen:latest", StartedAt: "2026-01-01T17:30:00Z"},
		{Model: "qwen", StartedAt: "2026-01-01T10:00:00-08:00"},
	}
	if got := latestNamed(results, "qwen"); got != results[1] {
		t.Fatalf("latest result = %+v, want timezone-offset instant %+v", got, results[1])
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
	psCalls       int
	stopErrAt     map[int]error
	generateErrAt map[int]error
	digest        string
	effectiveCtx  int
	contextErr    error
	psErr         error
	// flapPlacement makes /api/ps report a different resident split on later
	// calls, the way a real runtime does once a battery has finished and the
	// model's allocation has changed.
	flapPlacement bool
}

type digestVerifyingBackend struct {
	*runIntegrationBackend
	reported string
	called   bool
}

type inventoryBackend struct {
	*runIntegrationBackend
	name string
	tags []ollama.ModelInfo
	err  error
}

func (b *inventoryBackend) Name() string { return b.name }
func (b *inventoryBackend) Tags(context.Context) ([]ollama.ModelInfo, error) {
	return b.tags, b.err
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
	b.psCalls++
	if b.psErr != nil {
		return nil, b.psErr
	}
	vram := int64(1024)
	if b.flapPlacement {
		// Every call reports a distinct split, so any two placement probes
		// taken at different moments disagree. Keying off one call index
		// would depend on how many unrelated PS calls the run happens to make.
		if drop := int64(b.psCalls-1) * 64; drop < 960 {
			vram = 1024 - drop
		} else {
			vram = 64
		}
	}
	return []ollama.RunningModel{{Name: "model", Size: 1024, SizeVRAM: vram}}, nil
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

func TestBackendAtRejectsUnknownKind(t *testing.T) {
	if b, err := backendAt("typo", ""); err == nil || b != nil {
		t.Fatalf("unknown backend = %T, %v", b, err)
	}
}

func TestBackendAtUsesConfiguredOpenAIURLWhenExplicit(t *testing.T) {
	t.Setenv("FITR_OPENAI_URL", "http://127.0.0.1:32123")
	t.Setenv("FITR_OPENAI_API_KEY", "")
	b, err := backendAt("openai", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := b.URL(); got != "http://127.0.0.1:32123" {
		t.Fatalf("OpenAI-compatible URL = %q", got)
	}
}

func TestProbeBackendHonorsConfiguredRuntimeAheadOfDiscovery(t *testing.T) {
	t.Setenv("FITR_BACKEND", "llama-server")
	t.Setenv("LLAMA_SERVER_URL", "http://127.0.0.1:18080")
	t.Setenv("OLLAMA_BASE_URL", "http://127.0.0.1:11434")
	b := probeBackend(context.Background())
	if b.Name() != "llama-server" || b.URL() != "http://127.0.0.1:18080" {
		t.Fatalf("probe backend = %s at %s, want configured llama-server", b.Name(), b.URL())
	}
}

func TestNewBackendSelectsReachableExplicitRuntimes(t *testing.T) {
	for _, tc := range explicitRuntimeCases() {
		t.Run(tc.name, func(t *testing.T) {
			assertExplicitRuntime(t, tc)
		})
	}
}

type explicitRuntimeCase struct {
	name    string
	kind    string
	model   string
	env     string
	handler http.Handler
	backend string
}

func explicitRuntimeCases() []explicitRuntimeCase {
	return []explicitRuntimeCase{
		{
			name: "ollama", kind: "ollama", model: "model", env: "OLLAMA_BASE_URL", backend: "ollama",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"models":[{"name":"model:latest","size":1024}]}`)
			}),
		},
		{
			name: "openai compatible", kind: "openai", model: "model", env: "FITR_OPENAI_URL", backend: "openai",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"data":[{"id":"model"}]}`)
			}),
		},
		{
			name: "llama server", kind: "llama-server", model: "requested", env: "LLAMA_SERVER_URL", backend: "llama-server",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/health":
					_, _ = io.WriteString(w, `{"status":"ok"}`)
				case "/props":
					_, _ = io.WriteString(w, `{"build_info":"test build","model_path":"served.gguf"}`)
				default:
					http.NotFound(w, r)
				}
			}),
		},
	}
}

func assertExplicitRuntime(t *testing.T, tc explicitRuntimeCase) {
	t.Helper()
	server := httptest.NewServer(tc.handler)
	defer server.Close()
	t.Setenv(tc.env, server.URL)
	display := render.New("none")
	defer display.Close()
	var backend llm.Backend
	var code int
	if tc.kind == "llama-server" {
		backend, code = newBackendWithDisplay(context.Background(), tc.model, tc.kind, false, display)
	} else {
		backend, code = newBackend(context.Background(), tc.model, tc.kind, false)
	}
	if code != exitOK || backend == nil || backend.Name() != tc.backend {
		t.Fatalf("backend=%T name=%v code=%d", backend, backendName(backend), code)
	}
}

func backendName(backend llm.Backend) string {
	if backend == nil {
		return ""
	}
	return backend.Name()
}

func TestNewBackendRejectsUnknownAndInvalidDiscoveryConfiguration(t *testing.T) {
	display := render.New("none")
	defer display.Close()
	if backend, code := newBackendWithDisplay(context.Background(), "model", "unknown", false, display); backend != nil || code != exitUsage {
		t.Fatalf("unknown backend=%T code=%d", backend, code)
	}
	t.Setenv("FITR_DISCOVER_URLS", "http://127.0.0.1/"+strings.Repeat("x", 2048))
	if backend, code := newBackendWithDisplay(context.Background(), "model", "auto", false, display); backend != nil || code != exitUsage {
		t.Fatalf("invalid discovery backend=%T code=%d", backend, code)
	}
}

func TestAdviseTimingsRequireExactArtifactAndPlacement(t *testing.T) {
	t.Setenv("FITR_RESULTS", t.TempDir())
	result := mockResult("model:latest", 21, 0.5, 180, 3, 1, 1, 1, 1)
	saveCurrentResults(t, result)
	digest := result.Manifest.Model.RuntimeBoundDigest()
	if got := adviseTimings("model", digest, result.Device); len(got) != 1 || got[0].DecodeTPS != result.DecodeSum.Mean {
		t.Fatalf("matching timings = %+v", got)
	}
	if got := adviseTimings("model", integrationDigest(), result.Device); len(got) != 0 {
		t.Fatalf("changed artifact reused timings: %+v", got)
	}
	changedPlacement := result.Device
	changedPlacement.InferenceDevice = "CPU + GPU"
	if got := adviseTimings("model", digest, changedPlacement); len(got) != 0 {
		t.Fatalf("changed placement reused timings: %+v", got)
	}
	if got := adviseTimings("model", "", result.Device); len(got) != 0 {
		t.Fatalf("missing current digest reused timings: %+v", got)
	}
}

func TestCheckModelRejectsFailedAndEmptyInventory(t *testing.T) {
	for _, tc := range []struct {
		name    string
		backend *inventoryBackend
		want    int
	}{
		{
			name: "inventory error",
			backend: &inventoryBackend{runIntegrationBackend: &runIntegrationBackend{}, name: "openai",
				err: errors.New("malformed models response")},
			want: exitError,
		},
		{
			name: "empty single-model inventory",
			backend: &inventoryBackend{runIntegrationBackend: &runIntegrationBackend{}, name: "llama-server",
				tags: []ollama.ModelInfo{}},
			want: exitError,
		},
		{
			name: "empty ollama inventory",
			backend: &inventoryBackend{runIntegrationBackend: &runIntegrationBackend{}, name: "ollama",
				tags: []ollama.ModelInfo{}},
			want: exitUsage,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			display := render.New("none")
			defer display.Close()
			got, code := checkModelWithDisplay(context.Background(), tc.backend, "model", false, display)
			if code != tc.want || got != nil {
				t.Fatalf("backend=%T code=%d, want nil, %d", got, code, tc.want)
			}
		})
	}
}

func TestServingCtxFromRunningRejectsOnlyConflicts(t *testing.T) {
	running := []ollama.RunningModel{
		{Name: "qwen3:8b", ContextLength: 8192},
		{Name: "qwen3:8b:latest", ContextLength: 8192},
	}
	if n, ok := servingCtxFromRunning(running, "qwen3:8b"); !ok || n != 8192 {
		t.Fatalf("matching observations = %d, %v", n, ok)
	}
	running[1].ContextLength = 16384
	if n, ok := servingCtxFromRunning(running, "qwen3:8b"); ok || n != 0 {
		t.Fatalf("conflicting observations = %d, %v", n, ok)
	}
}

func TestResidentSizeFromRunningRejectsOnlyConflicts(t *testing.T) {
	running := []ollama.RunningModel{
		{Name: "qwen3:8b", Size: 8 << 30},
		{Name: "qwen3:8b:latest", Size: 8 << 30},
	}
	if n, ok := residentSizeFromRunning(running, "qwen3:8b"); !ok || n != 8<<30 {
		t.Fatalf("matching allocations = %d, %v", n, ok)
	}
	running[1].Size = 12 << 30
	if n, ok := residentSizeFromRunning(running, "qwen3:8b"); ok || n != 0 {
		t.Fatalf("conflicting allocations = %d, %v", n, ok)
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

func TestExecuteFakeBackendCompletesFullNonExecutableBattery(t *testing.T) {
	backend := &runIntegrationBackend{digest: integrationDigest(), effectiveCtx: eval.NumCtx}
	display := render.New("none")
	defer display.Close()
	result, err := execute(context.Background(), backend, "model", runOpts{
		level: "full", profile: "default", reps: 1, checksReps: 1,
	}, display)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Checks) == 0 || result.Refusal == nil || result.Plumbing == nil {
		t.Fatalf("full battery omitted non-executable evidence: checks=%d refusal=%v plumbing=%v",
			len(result.Checks), result.Refusal != nil, result.Plumbing != nil)
	}
	if result.EvidenceCounts["checks"].Expected == 0 || result.EvidenceCounts["refusal"].Expected == 0 {
		t.Fatalf("full battery did not seal denominator counts: %+v", result.EvidenceCounts)
	}
	for _, phase := range []string{"coding", "tools", "agentic"} {
		if result.EvidenceCounts[phase].Scorable != 0 {
			t.Fatalf("%s executable evidence became scoreable: %+v", phase, result.EvidenceCounts[phase])
		}
	}
	if err := result.ValidateEvidenceContract(); err != nil {
		t.Fatalf("full result evidence contract = %v", err)
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
}

func TestRunTaskPlanAndEvidenceCountsKeepImmutableDenominators(t *testing.T) {
	plan := runTaskPlan("full", 3, 1, 16, 3)
	if plan.CodeTrials != 6 || plan.CheckTrialsLimit != 16 || plan.ToolTrials != 3 ||
		!plan.Withdrawal || plan.RefusalTrials != 3 || plan.AgenticTrials != 1 {
		t.Fatalf("full plan = %+v", plan)
	}
	checks := runTaskPlan("checks", 5, 5, 16, 3)
	if checks.CheckTrialsLimit != 80 || checks.CodeTrials != 0 || checks.Plumbing {
		t.Fatalf("checks-only plan = %+v", checks)
	}

	r := &Result{SchemaVersion: 5, TaskPlan: record.TaskPlan{CodeTrials: 2}}
	r.CodeWrite = []eval.ExecResult{{Outcome: eval.OutcomeSkipped}}
	counts, err := r.DeriveEvidenceCounts()
	if err != nil {
		t.Fatal(err)
	}
	if counts["coding"].Complete() {
		t.Fatalf("one of two planned coding trials disappeared: %+v", counts["coding"])
	}
	r.CodeFix = []eval.ExecResult{{Outcome: eval.OutcomeSkipped}}
	counts, err = r.DeriveEvidenceCounts()
	if err != nil {
		t.Fatal(err)
	}
	if got := counts["coding"]; !got.Complete() || got.Skipped != 2 || got.Scorable != 0 {
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
		{"https://huggingface.co/bartowski/Foo-GGUF/blob/main/model.gguf",
			"hf.co/bartowski/Foo-GGUF"},
		{"https://huggingface.co/bartowski/Foo-GGUF/blob/main/Foo-Instruct.gguf",
			"hf.co/bartowski/Foo-GGUF"},
		{"https://huggingface.co/bartowski/Foo-GGUF/blob/main/Q4_K_M.gguf",
			"hf.co/bartowski/Foo-GGUF:Q4_K_M"},
		{"http://hf.co/org/model", "hf.co/org/model"},
		{"huggingface.co/org/model", "hf.co/org/model"},
		{`C:\models\foo#bar.gguf`, `C:\models\foo#bar.gguf`},
		{"/models/foo?bar.gguf", "/models/foo?bar.gguf"},
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

func TestInventoryDisclosesUnreadableSavedEvidence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	if err := os.WriteFile(filepath.Join(dir, "damaged.json"), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	table, warnings, err := joinInstalled(context.Background(), &runIntegrationBackend{}, device.Fingerprint{})
	if err != nil {
		t.Fatal(err)
	}
	if len(table.Rows) != 1 {
		t.Fatalf("healthy inventory disappeared after damaged evidence: %+v", table)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "could not be trusted") {
		t.Fatalf("evidence warnings = %v", warnings)
	}
}

func TestInventoryDisclosesUnknownRuntimeStatus(t *testing.T) {
	t.Setenv("FITR_RESULTS", t.TempDir())
	table, warnings, err := joinInstalled(context.Background(), &runIntegrationBackend{
		psErr: errors.New("status endpoint unavailable"),
	}, device.Fingerprint{})
	if err != nil {
		t.Fatal(err)
	}
	if len(table.Rows) != 1 || table.Rows[0].Loaded || table.Rows[0].ServingKnown {
		t.Fatalf("inventory status failure = %+v", table.Rows)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "loaded markers and serving contexts are unknown") {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestDeviceRejectsMalformedUserProfiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_PROFILES", dir)
	if err := os.WriteFile(filepath.Join(dir, "damaged.json"), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stderr, code := captureTopStderr(t, func() int {
		return cmdDevice(ctx, []string{"--display=none"})
	})
	if code != exitError || !strings.Contains(stderr, "could not select device profile") {
		t.Fatalf("device exit=%d stderr=%q", code, stderr)
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

// Ollama's /api/show returns architecture metadata but no size field, so
// advise must recover the weight bytes from the model list the way inventory
// always has. Without it, `fitr <model>` SKIPs with "model weights were not
// measured" while bare `fitr` prints that model's size on the same screen.
func TestWeightsFromTagsRecoversSizeWhenShowOmitsIt(t *testing.T) {
	ctx := context.Background()
	b := &inventoryBackend{runIntegrationBackend: &runIntegrationBackend{}, name: "ollama",
		tags: []ollama.ModelInfo{
			{Name: "gemma4:e2b", Size: 7 << 30},
			{Name: "qwen3:30b", Size: 18 << 30},
		}}

	if got := weightsFromTags(ctx, b, "qwen3:30b"); got != 18<<30 {
		t.Fatalf("weights = %d, want %d", got, int64(18)<<30)
	}
	// A bare name must resolve to the served tag the same way inventory joins.
	if got := weightsFromTags(ctx, b, "qwen3:30b"); got == 0 {
		t.Fatal("served model must not report zero weights")
	}
	if got := weightsFromTags(ctx, b, "not-installed:7b"); got != 0 {
		t.Fatalf("an unlisted model must stay unmeasured, got %d", got)
	}
}

func TestWeightsFromTagsStaysUnmeasuredOnFailure(t *testing.T) {
	ctx := context.Background()
	failing := &inventoryBackend{runIntegrationBackend: &runIntegrationBackend{}, name: "ollama",
		err: errors.New("malformed models response")}
	if got := weightsFromTags(ctx, failing, "qwen3:30b"); got != 0 {
		t.Fatalf("a failed model list must not become a weight reading, got %d", got)
	}

	zeroSize := &inventoryBackend{runIntegrationBackend: &runIntegrationBackend{}, name: "ollama",
		tags: []ollama.ModelInfo{{Name: "qwen3:30b", Size: 0}}}
	if got := weightsFromTags(ctx, zeroSize, "qwen3:30b"); got != 0 {
		t.Fatalf("size 0 is unmeasured, not a reading, got %d", got)
	}
}

// Inference placement is derived from the resident VRAM split, so it changes
// as a model's allocation changes. It is observed once while the model is
// resident for context verification, then sealed into DeviceV2 and the
// comparability key. A re-probe after the battery reads a different residency
// state, and if it overwrites the sealed value the record's legacy and v2
// device fields disagree -- the manifest check then rejects the save and a
// completed measurement is thrown away. Placement is sealed, not refreshed.
func TestExecuteSealsInferencePlacementAgainstALaterReprobe(t *testing.T) {
	backend := &runIntegrationBackend{
		digest: integrationDigest(), effectiveCtx: eval.NumCtx, flapPlacement: true,
	}
	display := render.New("none")
	defer display.Close()
	result, err := execute(context.Background(), backend, "model", runOpts{
		level: "quick", profile: "default", reps: 1, checksReps: 1,
	}, display)
	if err != nil {
		t.Fatal(err)
	}
	if backend.psCalls < 2 {
		t.Fatalf("placement was probed %d times; the flap is not exercised", backend.psCalls)
	}
	if result.DeviceV2 == nil {
		t.Fatal("run produced no v2 fingerprint")
	}
	if result.Device.InferenceDevice != result.DeviceV2.Device.InferenceDevice {
		t.Fatalf("placement diverged after sealing: legacy %q vs v2 %q",
			result.Device.InferenceDevice, result.DeviceV2.Device.InferenceDevice)
	}
	if !reflect.DeepEqual(result.Device, result.DeviceV2.Device) {
		t.Fatalf("legacy device fields differ from fingerprint v2:\n legacy=%+v\n v2=%+v",
			result.Device, result.DeviceV2.Device)
	}
	// The manifest check is what actually refuses the save.
	if err := result.ValidateEvidenceContract(); err != nil {
		t.Fatalf("evidence contract = %v", err)
	}
}
