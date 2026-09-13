package analysis

import (
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/record"
)

// Performance has disclosed its own thin sampling for several releases.
// Behavior never did, although it is the half a reader trusts more: a need
// measured once reports a rate over a denominator of one, which is consistent
// with almost any true rate.
func TestBehaviorMeasuredOnceIsNamed(t *testing.T) {
	once := singleTrialBehavior(record.TaskPlan{ToolTrials: 1, CheckTrialsLimit: 5})
	if len(once) != 1 || once[0] != "tool trials" {
		t.Fatalf("single-observation plans = %v, want only the tool trials", once)
	}
	every := singleTrialBehavior(record.TaskPlan{
		CheckTrialsLimit: 1, ToolTrials: 1, RefusalTrials: 1, CodeTrials: 1,
	})
	if len(every) != 4 {
		t.Fatalf("all four single-observation plans were not named: %v", every)
	}
	if !strings.Contains(strings.Join(every, ", "), "generated checks") {
		t.Fatalf("named plans do not read as plan names: %v", every)
	}
}

// Zero is a different state and already reads as SKIP: the plan was not run.
// Reporting it as thin evidence would describe evidence that does not exist.
func TestUnplannedBehaviorIsNotReportedAsThin(t *testing.T) {
	if once := singleTrialBehavior(record.TaskPlan{}); len(once) != 0 {
		t.Fatalf("an empty plan was reported as measured once: %v", once)
	}
	if once := singleTrialBehavior(record.TaskPlan{ToolTrials: 0, CheckTrialsLimit: 0}); len(once) != 0 {
		t.Fatalf("unplanned work was reported as measured once: %v", once)
	}
}

// Repeated measurement is the state this gap exists to distinguish from.
func TestRepeatedBehaviorIsNotFlagged(t *testing.T) {
	plan := record.TaskPlan{CheckTrialsLimit: 3, ToolTrials: 5, RefusalTrials: 3, CodeTrials: 2}
	if once := singleTrialBehavior(plan); len(once) != 0 {
		t.Fatalf("repeated plans were flagged as single observations: %v", once)
	}
}
