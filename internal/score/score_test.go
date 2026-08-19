package score

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/device"
)

func lappy(t *testing.T) device.Profile {
	t.Helper()
	profs, err := device.LoadProfiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range profs {
		if p.Name == "lappy" {
			return p
		}
	}
	t.Fatal("lappy profile not embedded")
	return device.Profile{}
}

const looping = "| Source | Claim | Evidence |\n|---|---|---|\n| A | cycle | yes |\n\n"

func repeated(s string, n int) string { return strings.Repeat(s, n) }

const clean = `The market split in two this August. Vintage cards climbed while overprinted modern sets slid badly.

Grading remains the liquidity gate, and PSA still clears fastest of all the graders.

Reprints are the risk nobody prices in until the day it actually lands on them.

Sealed product is where the scarcity genuinely bites hardest right now, by far.
`

func good() Measured {
	return Measured{
		Model: "test:1b", Capabilities: []string{"completion", "tools"},
		DecodeTPS: 30, TTFT: 0.5, PrefillTPS: 200, SpeedKnown: true,
		ResidentGB32K: 5, MemoryKnown: true,
		CodeWritePass: true, CodeFixPass: true, CodeKnown: true,
		Rep: RepetitionMetrics(clean),
	}
}

func TestDegeneracyCatchesLoopingTable(t *testing.T) {
	r := RepetitionMetrics(repeated(looping, 11))
	if r.DupLineRatio <= 0.5 {
		t.Fatalf("dup_line_ratio = %v, want >0.5", r.DupLineRatio)
	}
	if r.GzipRatio <= 4 {
		t.Fatalf("gzip ratio = %v, want >4 for a looping table", r.GzipRatio)
	}
}

func TestSingleMetricWouldHaveMissedIt(t *testing.T) {
	// Pins WHY the gate uses five signals: the paragraph metric has a blind
	// spot for short repeated blocks and scores this at 0.0.
	r := RepetitionMetrics(repeated(looping, 11))
	if r.DupParagraphRatio != 0 {
		t.Fatalf("expected the paragraph metric to miss this, got %v", r.DupParagraphRatio)
	}
	if r.DupLineRatio <= 0.5 {
		t.Fatal("the other signals must still catch it")
	}
}

func TestCleanProsePassesEverySignal(t *testing.T) {
	r := RepetitionMetrics(clean)
	if r.DupLineRatio != 0 || r.DupParagraphRatio != 0 {
		t.Fatalf("clean prose flagged: %+v", r)
	}
	if r.GzipRatio > 4 {
		t.Fatalf("gzip ratio = %v, want <4", r.GzipRatio)
	}
}

func TestVisionAbsenceIsNotAFailure(t *testing.T) {
	sc := Score(good(), lappy(t))
	if sc.Needs["vision"].State != NA {
		t.Fatalf("vision = %v, want n/a", sc.Needs["vision"].State)
	}
	if sc.Fails != 0 {
		t.Fatalf("fails = %d, want 0 (n/a must not count as failure)", sc.Fails)
	}
}

func TestUnmeasuredNeedIsSkip(t *testing.T) {
	sc := Score(good(), lappy(t))
	if sc.Needs["uncensored"].State != Skip {
		t.Fatalf("uncensored = %v, want SKIP when the battery did not run", sc.Needs["uncensored"].State)
	}
}

func TestBrokenPlumbingBlocksRatherThanFails(t *testing.T) {
	m := good()
	m.PlumbingRan, m.PlumbingHealthy = true, false
	m.PlumbingVerdict = "template/parser problem"
	sc := Score(m, lappy(t))
	if sc.Needs["unattended_agentic"].State != Blocked {
		t.Fatalf("got %v, want BLKD -- an untestable capability is not a failure",
			sc.Needs["unattended_agentic"].State)
	}
}

func TestToolRestraintFlagsSpuriousCalls(t *testing.T) {
	m := good()
	m.PlumbingRan, m.PlumbingHealthy = true, true
	m.IrrelevanceRan, m.IrrelevancePass, m.SpuriousCalls = true, false, 1
	sc := Score(m, lappy(t))
	if sc.Needs["tool_restraint"].State != Fail {
		t.Fatalf("got %v, want FAIL", sc.Needs["tool_restraint"].State)
	}
}

func TestNeverSaysNotRecommended(t *testing.T) {
	m := good()
	m.DecodeTPS, m.TTFT, m.PrefillTPS = 1, 9, 5
	m.CodeWritePass, m.CodeFixPass = false, false
	m.ResidentGB32K = 99
	sc := Score(m, lappy(t))
	if strings.Contains(strings.ToLower(sc.UseFor), "not recommended") {
		t.Fatalf("must never print a bare dismissal, got %q", sc.UseFor)
	}
}

func TestMissingGateSkipsRatherThanCrashes(t *testing.T) {
	empty := device.Profile{Name: "empty", Gates: map[string]device.Gate{}}
	sc := Score(good(), empty)
	if sc.Needs["fast_and_decent"].State != Skip {
		t.Fatalf("got %v, want SKIP for a missing gate", sc.Needs["fast_and_decent"].State)
	}
	if sc.Fails != 0 {
		t.Fatalf("fails = %d, want 0", sc.Fails)
	}
}

func TestPooledNeedsScoreAgainstRateGates(t *testing.T) {
	m := good()
	m.Structured = Pool{Passes: 7, N: 7}
	m.Precision = Pool{Passes: 2, N: 4} // 0.5 < 0.7 gate
	sc := Score(m, lappy(t))
	if sc.Needs["structured_output"].State != Pass {
		t.Fatalf("structured_output = %v, want PASS", sc.Needs["structured_output"].State)
	}
	if !strings.Contains(sc.Needs["structured_output"].Why, "[") {
		t.Fatalf("pooled verdict must carry its Wilson interval, got %q", sc.Needs["structured_output"].Why)
	}
	if sc.Needs["instruction_precision"].State != Fail {
		t.Fatalf("instruction_precision = %v, want FAIL at 2/4 against 0.7", sc.Needs["instruction_precision"].State)
	}
}

func TestUnmeasuredPoolsSkip(t *testing.T) {
	sc := Score(good(), lappy(t))
	for _, need := range []string{"structured_output", "instruction_precision"} {
		if sc.Needs[need].State != Skip {
			t.Fatalf("%s = %v, want SKIP when no checks ran", need, sc.Needs[need].State)
		}
	}
	if _, ok := sc.Needs["user_tasks"]; ok {
		t.Fatal("user_tasks must not appear on runs without user tasks")
	}
}

func TestUserTasksDefaultToAllMustPass(t *testing.T) {
	m := good()
	m.User = Pool{Passes: 3, N: 3}
	sc := Score(m, lappy(t))
	if sc.Needs["user_tasks"].State != Pass {
		t.Fatalf("3/3 user tasks = %v, want PASS", sc.Needs["user_tasks"].State)
	}
	m.User = Pool{Passes: 2, N: 3}
	sc = Score(m, lappy(t))
	if sc.Needs["user_tasks"].State != Fail {
		t.Fatalf("2/3 user tasks = %v, want FAIL under the all-must-pass default", sc.Needs["user_tasks"].State)
	}
	// user_tasks lives outside NeedOrder; the counters must still see it.
	if sc.Fails == 0 {
		t.Fatal("a failing user_tasks need must count as a failure")
	}
}

func TestReasoningPoolWidensTheCodingSample(t *testing.T) {
	m := good()
	m.CodePasses, m.CodeRepeats = 6, 6
	m.Reasoning = Pool{Passes: 4, N: 5}
	sc := Score(m, lappy(t))
	why := sc.Needs["coding"].Why
	if !strings.Contains(why, "10/11") || !strings.Contains(why, "reasoning 4/5") {
		t.Fatalf("coding why must pool reasoning trials into the interval, got %q", why)
	}
}

func TestProfilesAreEmbeddedAndParse(t *testing.T) {
	profs, err := device.LoadProfiles()
	if err != nil || len(profs) < 2 {
		t.Fatalf("profiles = %d, err = %v", len(profs), err)
	}
	for _, p := range profs {
		b, _ := json.Marshal(p)
		if len(b) < 50 {
			t.Fatalf("profile %s looks empty", p.Name)
		}
	}
}
