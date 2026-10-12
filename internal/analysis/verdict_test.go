package analysis

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/score"
)

func TestWithdrawalPresentationRetainsLegacyCombinedEvidence(t *testing.T) {
	sealed := score.Verdict{State: score.Fail, Measure: "1/2 clean", Gate: "need 2/2",
		Detail: []string{"left tools alone on an unrelated question", "kept calling a withdrawn tool (4 dead calls)"}}
	view := PresentVerdict("tool_restraint", sealed)
	if view.State != score.Fail || view.DecidingFamily != "tool_withdrawal" ||
		!strings.HasPrefix(view.Why, "withdrawal; kept calling a withdrawn tool (4 dead calls)") ||
		!strings.Contains(view.Why, "combined checks: 1/2 clean (need 2/2)") ||
		strings.Contains(view.Why, "ordinary irrelevance: 1/2") {
		t.Fatalf("legacy combined denominator was lost or relabeled: %+v", view)
	}
}

func TestVerdictPresentationNeverGuessesMissingDecidingEvidence(t *testing.T) {
	for _, test := range []struct {
		name, need string
		verdict    score.Verdict
	}{
		{"why-only history", "tool_restraint", score.Verdict{State: score.Fail, Why: "kept calling a withdrawn tool (4 dead calls)"}},
		{"unknown historical wording", "tool_restraint", score.Verdict{State: score.Fail, Detail: []string{"withdrawal assessment was unavailable"}}},
		{"unavailable evidence", "tool_restraint", score.Verdict{State: score.Blocked, Note: "withdrawal could not be measured"}},
		{"different need", "tool_calling", score.Verdict{State: score.Fail, Detail: []string{"did not stop cleanly after a tool was withdrawn"}}},
		{"not a failed count", "tool_restraint", score.Verdict{State: score.Fail, Detail: []string{"kept calling a withdrawn tool (1 dead calls)"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			view := PresentVerdict(test.need, test.verdict)
			if view.DecidingFamily != "" || view.State != test.verdict.State {
				t.Fatalf("missing evidence acquired a deciding family or state: %+v", view)
			}
			if test.verdict.Why != "" && view.Why != test.verdict.Why {
				t.Fatalf("historical explanation changed: %q", view.Why)
			}
		})
	}
}

func TestVerdictProjectionCannotMutateSealedBreakdown(t *testing.T) {
	for _, need := range []string{"tool_restraint", "coding"} {
		sealed := score.Verdict{State: score.Fail, Measure: "3/3 clean", Gate: "need >=0.70",
			Detail: []string{"tool_irrelevance 3/3", "did not stop cleanly after a tool was withdrawn"}}
		before, err := json.Marshal(sealed)
		if err != nil {
			t.Fatal(err)
		}
		view := PresentVerdict(need, sealed)
		view.Detail[0] = "a consumer changed the projection"
		after, err := json.Marshal(sealed)
		if err != nil || string(before) != string(after) {
			t.Fatalf("projection aliases signed evidence: before=%s after=%s err=%v", before, after, err)
		}
	}
}
