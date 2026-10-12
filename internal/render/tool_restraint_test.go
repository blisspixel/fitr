package render

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/score"
)

// This is the contradictory combination reported in #32: the ordinary
// irrelevance sample is clean but inconclusive, while withdrawal failed.
func withdrawalFailureVerdict() score.Verdict {
	return score.Verdict{
		State: score.Fail, Measure: "3/3 clean [0.21-1.00]", Gate: "need >=0.70",
		Detail: []string{"tool_irrelevance 3/3", "did not stop cleanly after a tool was withdrawn", "undecided: 7"},
		Note:   "undecided: 70% sits inside 21-100%, so this run fits both clearing and missing the bar. Not a fail",
	}
}

func TestWithdrawalFailureLeadsEveryResultSurface(t *testing.T) {
	verdict := withdrawalFailureVerdict()
	before, err := json.Marshal(verdict)
	if err != nil {
		t.Fatal(err)
	}
	card := score.Scorecard{Model: "model", Needs: map[string]score.Verdict{"tool_restraint": verdict}}
	for _, width := range []int{40, 80} {
		var output strings.Builder
		plainDisplay(&output).verdictRow(&output, "tool_restraint", score.NeedLabel["tool_restraint"], verdict, width)
		text := output.String()
		flattened := strings.Join(strings.Fields(text), " ")
		headline := strings.Split(text, "\n")[0]
		if width == 40 {
			headline = strings.Join(strings.Split(text, "\n")[:2], " ")
		}
		if !strings.Contains(headline, "[FAIL]") || !strings.Contains(headline, "withdrawal") || strings.Contains(headline, "3/3 clean") {
			t.Errorf("width %d hides deciding family: %s", width, text)
		}
		for _, want := range []string{"3/3 clean", "0.21-1.00", "need >=0.70", "did not stop cleanly", "ordinary irrelevance checks are undecided"} {
			if !strings.Contains(flattened, want) {
				t.Errorf("width %d lost evidence %q: %s", width, want, text)
			}
		}
	}
	why := PresentVerdictWhy("tool_restraint", verdict)
	if !strings.HasPrefix(why, "withdrawal;") || strings.Contains(why, "Not a fail") {
		t.Errorf("deciding summary is misleading: %s", why)
	}
	needs, _ := htmlNeeds(card)
	if len(needs) != 1 || needs[0].State != "FAIL" || needs[0].Why != why {
		t.Errorf("HTML disagrees: %+v", needs)
	}
	var output strings.Builder
	(&jsonDisplay{out: &output}).Result(card, Meta{})
	var result struct {
		Needs    map[string]string `json:"needs"`
		Verdicts map[string]struct {
			State          string `json:"state"`
			Why            string `json:"why"`
			DecidingFamily string `json:"deciding_family"`
		} `json:"verdicts"`
	}
	if err := json.Unmarshal([]byte(output.String()), &result); err != nil {
		t.Fatal(err)
	}
	projected := result.Verdicts["tool_restraint"]
	if result.Needs["tool_restraint"] != "FAIL" || projected.State != "FAIL" || projected.Why != why || projected.DecidingFamily != "tool_withdrawal" {
		t.Errorf("JSON hides deciding evidence: %s", output.String())
	}
	after, err := json.Marshal(verdict)
	if err != nil || string(before) != string(after) {
		t.Fatalf("presentation mutated sealed verdict: before=%s after=%s err=%v", before, after, err)
	}
}

func TestSuccessfulWithdrawalIsNeverBlamedForIrrelevanceFailure(t *testing.T) {
	for _, withdrawal := range []string{"never called a withdrawn tool", "one grace call to a withdrawn tool, then stopped cleanly"} {
		verdict := score.Verdict{State: score.Fail, Measure: "0/20 clean [0.00-0.17]", Gate: "need >=0.70",
			Detail: []string{"tool_irrelevance 0/20", withdrawal}}
		measure, gate, detail, note := PresentVerdictParts("tool_restraint", verdict)
		if measure != verdict.Measure || gate != verdict.Gate || !reflect.DeepEqual(detail, verdict.Detail) || note != verdict.Note {
			t.Errorf("successful withdrawal misattributed as failure: %s", PresentVerdictWhy("tool_restraint", verdict))
		}
	}
}

func TestUndecidedOrUnavailableRestraintStaysUnchanged(t *testing.T) {
	for _, state := range []score.State{score.Pass, score.Inconclusive, score.Skip, score.Blocked, score.NA} {
		verdict := withdrawalFailureVerdict()
		verdict.State = state
		measure, gate, detail, note := PresentVerdictParts("tool_restraint", verdict)
		if measure != verdict.Measure || gate != verdict.Gate || !reflect.DeepEqual(detail, verdict.Detail) || note != verdict.Note {
			t.Errorf("state %s was promoted or relabeled", state)
		}
	}
}
