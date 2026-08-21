package score

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/device"
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
	m.Structured = Pool{Passes: 20, N: 20}
	m.Precision = Pool{Passes: 0, N: 20}
	sc := Score(m, lappy(t))
	if sc.Needs["structured_output"].State != Pass {
		t.Fatalf("structured_output = %v, want PASS", sc.Needs["structured_output"].State)
	}
	if !strings.Contains(sc.Needs["structured_output"].Why, "[") {
		t.Fatalf("pooled verdict must carry its Wilson interval, got %q", sc.Needs["structured_output"].Why)
	}
	if sc.Needs["instruction_precision"].State != Fail {
		t.Fatalf("instruction_precision = %v, want FAIL at 0/20 against 0.7", sc.Needs["instruction_precision"].State)
	}
}

func TestReasoningObservedWhenCodingSkipped(t *testing.T) {
	m := good()
	m.CodeKnown = false
	m.Reasoning = Pool{Passes: 4, N: 5}
	sc := Score(m, lappy(t))
	verdict := sc.Needs["coding"]
	if verdict.State != Skip {
		t.Fatalf("coding = %s, want SKIP", verdict.State)
	}
	if !strings.Contains(verdict.Why, "reasoning checks 4/5") || !strings.Contains(verdict.Why, "not a coding verdict") {
		t.Fatalf("coding skip hid observed reasoning: %q", verdict.Why)
	}
	if slices.Contains(sc.Serves, "coding") {
		t.Fatalf("unrun coding became a served need: %+v", sc)
	}
}

func TestClusteredPoolCannotHideADeadFamily(t *testing.T) {
	m := good()
	m.Structured = Pool{Passes: 60, N: 70}
	for i, name := range []string{"json_object", "json_schema", "json_extract", "csv_strict", "tool_args", "json_object_nested"} {
		m.Structured.Families = append(m.Structured.Families, FamilyPool{Family: name, Passes: 10, N: 10})
		_ = i
	}
	m.Structured.Families = append(m.Structured.Families, FamilyPool{Family: "json_extract_noise", Passes: 0, N: 10})
	sc := Score(m, lappy(t))
	verdict := sc.Needs["structured_output"]
	if verdict.State == Pass {
		t.Fatalf("structured_output = PASS with a 0/10 family: %s", verdict.Why)
	}
	if !strings.Contains(verdict.Why, "json_extract_noise 0/10") {
		t.Fatalf("dead family was not named: %q", verdict.Why)
	}
	if slices.Contains(sc.Serves, "structured_output") {
		t.Fatalf("dead-family pool became a product claim: %+v", sc)
	}
}

func TestPooledGateInsideWilsonIntervalIsInconclusive(t *testing.T) {
	m := good()
	// The point estimate clears 0.75, but 7 trials cannot establish that the
	// underlying rate does. The interval, not the point estimate, decides.
	m.Structured = Pool{Passes: 7, N: 7}
	sc := Score(m, lappy(t))
	verdict := sc.Needs["structured_output"]
	if verdict.State != Inconclusive {
		t.Fatalf("structured_output = %s, want INCONCLUSIVE: %s", verdict.State, verdict.Why)
	}
	if !strings.Contains(verdict.Why, "gate inside the 95% interval") {
		t.Fatalf("inconclusive verdict does not explain the uncertainty: %q", verdict.Why)
	}
	if slices.Contains(sc.Serves, "structured_output") || sc.Fails != 0 {
		t.Fatalf("inconclusive pool became a product claim: %+v", sc)
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

func TestConfiguredUserTaskRateUsesWilsonInconclusive(t *testing.T) {
	m := Measured{Model: "test", User: Pool{Passes: 7, N: 7}}
	profile := device.Profile{Name: "test", Gates: map[string]device.Gate{
		"user_tasks": {"pass_rate_min": 0.75},
	}}
	sc := Score(m, profile)
	if verdict := sc.Needs["user_tasks"]; verdict.State != Inconclusive ||
		!strings.Contains(verdict.Why, "gate inside the 95% interval") {
		t.Fatalf("user_tasks = %+v, want explicit Wilson INCONCLUSIVE", verdict)
	}
	if !strings.Contains(sc.UseFor, "unmeasured/blocked") {
		t.Fatalf("use_for did not count INCONCLUSIVE as unproven: %q", sc.UseFor)
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

func TestToolRestraintCoversWithdrawal(t *testing.T) {
	m := good()
	m.PlumbingRan, m.PlumbingHealthy = true, true
	m.IrrelevanceRan, m.IrrelevancePass = true, true
	m.WithdrawRan, m.WithdrawDeadCalls, m.WithdrawClean = true, 1, true
	sc := Score(m, lappy(t))
	if sc.Needs["tool_restraint"].State != Pass {
		t.Fatalf("grace call + clean stop = %v, want PASS: %s",
			sc.Needs["tool_restraint"].State, sc.Needs["tool_restraint"].Why)
	}
	if !strings.Contains(sc.Needs["tool_restraint"].Why, "grace") {
		t.Fatalf("why must disclose the grace call: %q", sc.Needs["tool_restraint"].Why)
	}
	m.WithdrawDeadCalls = 3
	sc = Score(m, lappy(t))
	if sc.Needs["tool_restraint"].State != Fail {
		t.Fatal("persisting past the withdrawal error must fail the need")
	}
	if !strings.Contains(sc.Needs["tool_restraint"].Why, "withdrawn") {
		t.Fatalf("why must name the withdrawn-tool failure: %q", sc.Needs["tool_restraint"].Why)
	}
}

func TestContextCeilingWithoutCompactionFailsUnattended(t *testing.T) {
	m := good()
	m.PlumbingRan, m.PlumbingHealthy = true, true
	m.AgenticRan, m.AgenticPass, m.AgenticTurns = true, true, 22
	m.AgenticCtxCeiling, m.AgenticMaxPrompt, m.AgenticCompacted = true, 7100, false
	sc := Score(m, lappy(t))
	v := sc.Needs["unattended_agentic"]
	if v.State != Fail {
		t.Fatalf("filling the window with no compaction must FAIL unattended: %v %s", v.State, v.Why)
	}
	if !strings.Contains(v.Why, "no compaction") {
		t.Fatalf("why must name the compaction failure: %q", v.Why)
	}
}

func TestContextCeilingWithCompactionIsDisclosedNotFailed(t *testing.T) {
	m := good()
	m.PlumbingRan, m.PlumbingHealthy = true, true
	m.AgenticRan, m.AgenticPass, m.AgenticTurns = true, true, 22
	m.AgenticCtxCeiling, m.AgenticMaxPrompt, m.AgenticCompacted = true, 7100, true
	sc := Score(m, lappy(t))
	v := sc.Needs["unattended_agentic"]
	if v.State != Pass {
		t.Fatalf("a model that compacted after peaking is not a FAIL: %v %s", v.State, v.Why)
	}
	if !strings.Contains(v.Why, "compacted") {
		t.Fatalf("why must disclose the peak-then-compact: %q", v.Why)
	}
}

func TestColdAndWarmTTFTAreDisclosedInFastChat(t *testing.T) {
	m := good()
	m.TTFTCold, m.TTFTWarm = 9.1, 0.18
	sc := Score(m, lappy(t))
	why := sc.Needs["fast_and_decent"].Why
	if !strings.Contains(why, "loaded/uncached") || !strings.Contains(why, "cold start 9.1s") {
		t.Fatalf("why must label the gated figure and disclose cold: %q", why)
	}
	if !strings.Contains(why, "cached prefix 0.18s") {
		t.Fatalf("why must disclose the warm-prefix receipt: %q", why)
	}
	if sc.Needs["fast_and_decent"].State != Pass {
		t.Fatal("the gate judges the loaded/uncached figure only")
	}
}

func TestContaminatedTTFTIsExcludedFromTheGate(t *testing.T) {
	m := good()
	// Cache-hit TTFT looks excellent (0.05s) but is not a new-question
	// number. Counting it would PASS a model whose real loaded/uncached
	// TTFT was never measured.
	m.TTFT, m.TTFTCacheContaminated = 0.05, true
	m.TTFTWarm = 0.05
	sc := Score(m, lappy(t))
	why := sc.Needs["fast_and_decent"].Why
	if !strings.Contains(why, "cache hit") || !strings.Contains(why, "excluded") {
		t.Fatalf("contaminated TTFT must be labeled and excluded: %q", why)
	}
	if sc.Needs["fast_and_decent"].State != Pass {
		t.Fatal("decode still passed; excluding TTFT must not invent a FAIL")
	}
	m.DecodeTPS = 1 // below the lappy fast_chat gate
	sc = Score(m, lappy(t))
	if sc.Needs["fast_and_decent"].State != Fail {
		t.Fatal("decode still has to clear its own gate")
	}
}

func TestClientDerivedTimingsAreLabeledNotSkipped(t *testing.T) {
	m := good()
	m.TimingsClientDerived = true
	sc := Score(m, lappy(t))
	why := sc.Needs["fast_and_decent"].Why
	if !strings.Contains(why, "client-derived") {
		t.Fatalf("OpenAI-compat wall-clock must be labeled: %q", why)
	}
	if sc.Needs["fast_and_decent"].State != Pass {
		t.Fatal("labeling is not a SKIP; decode and TTFT were still measured")
	}
}

func TestContaminationExcludesMeasuredScoreClaims(t *testing.T) {
	m := good()
	m.Capabilities = append(m.Capabilities, "vision")
	m.Structured = Pool{Passes: 0, N: 7}
	m.Rep.Words, m.Rep.GzipRatio = 200, 9
	m.Contamination = []string{"other:7b", "other:7b"}

	sc := Score(m, lappy(t))
	for _, need := range []string{"fast_and_decent", "coding", "structured_output", "vision", "output_health"} {
		verdict := sc.Needs[need]
		if verdict.State != Inconclusive {
			t.Fatalf("%s = %s, want INCONCLUSIVE: %s", need, verdict.State, verdict.Why)
		}
		if !strings.Contains(verdict.Why, "other:7b") || !strings.Contains(verdict.Why, "excluded") {
			t.Fatalf("%s must disclose the exclusion and resident model: %q", need, verdict.Why)
		}
	}
	if sc.Needs["uncensored"].State != Skip {
		t.Fatalf("an unmeasured need remains SKIP, got %s", sc.Needs["uncensored"].State)
	}
	if sc.Passes != 0 || sc.Fails != 0 || len(sc.Serves) != 0 {
		t.Fatalf("contaminated scorecard still claims outcomes: %+v", sc)
	}
	if sc.Unproven != len(sc.Needs) || !strings.Contains(sc.UseFor, "INCONCLUSIVE") {
		t.Fatalf("contaminated scorecard summary is not explicit: %+v", sc)
	}
}

func TestExcludeContaminationDoesNotMutateStoredScorecard(t *testing.T) {
	original := Scorecard{
		Needs:  map[string]Verdict{"coding": {State: Pass, Why: "passed"}},
		Serves: []string{"coding"}, Passes: 1, UseFor: "coding",
	}
	excluded := ExcludeContamination(original, []string{"resident"})
	if original.Needs["coding"].State != Pass || len(original.Serves) != 1 {
		t.Fatalf("legacy scorecard was mutated: %+v", original)
	}
	if excluded.Needs["coding"].State != Inconclusive || len(excluded.Serves) != 0 {
		t.Fatalf("excluded scorecard retained a claim: %+v", excluded)
	}
}
