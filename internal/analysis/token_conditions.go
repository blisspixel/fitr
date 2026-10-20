package analysis

import (
	"github.com/blisspixel/fitr/internal/record"
)

// TokenConditionCase is one sealed condition. Outcome is the observation's
// disposition. It is not a rate, and the condition name is part of the cell.
type TokenConditionCase struct {
	Condition string `json:"condition"`
	Family    string `json:"family"`
	Position  string `json:"position"`
	Seed      uint64 `json:"seed"`
	Outcome   string `json:"outcome"`
}

// TokenConditions is the projection of a derived matched-condition receipt.
//
// Every sentence is copied from the report the record re-derived. A renderer
// must not turn the counts into a rate or treat a v1 family/position/seed as
// this cell. Status descriptive-only means the record cannot certify the
// target, whatever the stored state said. This receipt is not submitted to a
// runtime.
type TokenConditions struct {
	Status      ObservationStatus    `json:"status"`
	TargetLabel string               `json:"target_label"`
	Semantics   string               `json:"semantics"`
	State       string               `json:"state"`
	Tone        string               `json:"tone"`
	Passed      int                  `json:"passed"`
	Failed      int                  `json:"failed"`
	Unresolved  int                  `json:"unresolved"`
	Uncertainty string               `json:"uncertainty"`
	Reason      string               `json:"reason"`
	NextAction  string               `json:"next_action"`
	Cases       []TokenConditionCase `json:"cases"`
}

func tokenConditionsFrom(result *record.Record, unclaimable bool) *TokenConditions {
	if result == nil || result.ContextTokenConditions == nil {
		return nil
	}
	report := result.ContextTokenConditions.Report
	status := StatusAvailable
	state := report.State
	reason := report.Reason
	next := report.NextAction
	if unclaimable {
		status = StatusDescriptiveOnly
		state = "descriptive_only"
		reason = "descriptive only: this record cannot certify a matched-condition token target"
		next = "repair the record identity before reading the condition receipt"
	}
	view := &TokenConditions{
		Status: status, TargetLabel: report.TargetLabel, Semantics: report.Semantics,
		State: state, Tone: tokenTone(state), Passed: report.Passed, Failed: report.Failed,
		Unresolved: report.Unresolved, Uncertainty: report.Uncertainty,
		Reason: reason, NextAction: next,
	}
	for _, item := range report.Cases {
		view.Cases = append(view.Cases, TokenConditionCase{
			Condition: item.Condition, Family: string(item.Family), Position: string(item.Position),
			Seed: item.Seed, Outcome: item.Outcome,
		})
	}
	return view
}
