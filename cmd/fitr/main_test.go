package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
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
		"structured_output":     score.Pass, // 6/7 = 0.857 over the 0.75 gate
		"instruction_precision": score.Pass, // 4/4
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

// A degraded variant of the golden run: the same model with quant damage in
// its structured output and a looping longest-sample. This pins the FAIL paths
// end to end, not just the happy ones.
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

func TestQuantDamageLineIsDirectionalAndSkipsUnknown(t *testing.T) {
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
	if !ok || !strings.Contains(line, "Q4_K_M lost") || !strings.Contains(line, "Q8_0") {
		t.Fatalf("got %q ok=%v, want directional Q4 vs Q8", line, ok)
	}
	if strings.Contains(line, "accuracy") && !strings.Contains(line, "flips") {
		t.Fatal("must say flips, not sell an accuracy delta")
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
}

func TestBoardDoesNotRankAcrossFingerprints(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	a, b := golden(t), golden(t)
	a.Model, b.Model = "aa", "bb"
	b.DeviceKey = a.DeviceKey + "|other"
	b.Device.GPUDriver = "other-driver"
	for _, r := range []*Result{a, b} {
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, safeName(r.Model)+".json"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
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

func TestCompareRefusesDifferentFingerprints(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	a, b := golden(t), golden(t)
	a.Model, b.Model = "aa", "bb"
	b.DeviceKey = a.DeviceKey + "|other"
	for _, r := range []*Result{a, b} {
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, safeName(r.Model)+".json"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
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
}

func TestCalibrateReportsNeverFlippedItems(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	hi, lo := golden(t), golden(t)
	hi.Model, lo.Model = "m-q8", "m-q4"
	hi.SeedSet, lo.SeedSet = "night", "night"
	hi.ModelMeta.Details.QuantizationLevel = "Q8_0"
	lo.ModelMeta.Details.QuantizationLevel = "Q4_K_M"
	hi.Checks = []eval.CheckOutcome{
		{TaskID: "json_object", Need: "structured_output", Seed: 1, Pass: true},
		{TaskID: "date_math", Need: "instruction_precision", Seed: 1, Pass: true},
	}
	lo.Checks = []eval.CheckOutcome{
		{TaskID: "json_object", Need: "structured_output", Seed: 1, Pass: false},
		{TaskID: "date_math", Need: "instruction_precision", Seed: 1, Pass: true},
	}
	for _, r := range []*Result{hi, lo} {
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, safeName(r.Model)+".json"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := os.Stdout
	pr, pw, _ := os.Pipe()
	os.Stdout = pw
	code := cmdCalibrate(context.Background(), []string{"m-q8", "m-q4"})
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
	if strings.Contains(got, "rewrites spec") {
		t.Fatal("must not claim to rewrite the spec")
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
	if code := cmdScreenshots(context.Background(), []string{dir}); code != exitOK {
		t.Fatalf("screenshots exited %d", code)
	}
	for _, name := range []string{"advise.svg", "run.svg", "apply.svg", "board.svg"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		got := string(b)
		if !strings.Contains(got, "<svg") || !strings.Contains(got, "$ fitr") {
			t.Fatalf("%s is not a terminal screenshot:\n%.200s", name, got)
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
	b, err := os.ReadFile(filepath.Join(dir, safeName(r.Model)+".retonr.json"))
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
	for _, r := range []*Result{a, b} {
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, safeName(r.Model)+".json"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
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
	for _, r := range []*Result{a, b} {
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, safeName(r.Model)+".json"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
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
