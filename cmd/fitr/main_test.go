package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/device"
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

func TestMDEIsSaidOutLoud(t *testing.T) {
	// The ROADMAP claims fitr states its minimum detectable effect out loud.
	// This pins that the number exists and lands in the meta the renderer prints.
	mde := 100 * stats.MinDetectableEffect(23, 1)
	if mde < 20 || mde > 40 {
		t.Fatalf("MDE at 23 trials = %.1fpp - expected roughly 29pp; the formula changed", mde)
	}
}
