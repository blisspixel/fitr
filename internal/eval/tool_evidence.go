package eval

import "fmt"

// ValidateTerminationEvidence checks the persisted clean-stop and loop facts
// without changing their established JSON representation.
func (r ToolLoopResult) ValidateTerminationEvidence() error {
	switch {
	case r.Ended == "":
		return fmt.Errorf("tool loop is missing its termination reason")
	case r.Calls < 0 || r.Malformed < 0 || r.Repeats < 0:
		return fmt.Errorf("tool loop counters cannot be negative")
	case r.Repeats > r.Calls:
		return fmt.Errorf("repeated identical calls exceed total calls")
	case r.Looped != (r.Repeats >= 3):
		return fmt.Errorf("loop flag disagrees with repeated identical calls")
	case r.Ended == "clean_stop" && (r.Outcome == OutcomeError || r.Outcome == OutcomeSkipped):
		return fmt.Errorf("clean stop cannot carry %q outcome", r.Outcome)
	case r.Outcome == OutcomePass && r.Ended != "clean_stop":
		return fmt.Errorf("passing tool loop did not end with a clean stop")
	}
	return nil
}
