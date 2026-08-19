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
		Profile: "lappy", Repeats: 3,
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
