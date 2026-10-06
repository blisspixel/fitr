package decision

import (
	"errors"
	"fmt"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/record"
)

// UsableContextResult derives only the usable-context byte floor from one
// sealed record. An effective-token floor stays on the record that measured
// the operating window; this result must not replace that observation.
func UsableContextResult(result *record.Record, requirement Requirement) (RequirementResult, error) {
	if result == nil || requirement.Context == nil || requirement.Context.MinimumUsableContextBytes == nil {
		return RequirementResult{}, errors.New("usable-context result requires a sealed record and a byte floor")
	}
	report, err := analysis.FromRecord(result)
	if err != nil {
		return RequirementResult{}, err
	}
	return evaluateUsableContextBytes(report, requirement.ID, *requirement.Context), nil
}

// WithRequirement substitutes one requirement result that already belongs to
// the evaluation and recomputes eligibility with the same aggregation Evaluate
// uses. It does not reread observations and does not add confirmation lineage.
func (evaluation Evaluation) WithRequirement(result RequirementResult) (Evaluation, error) {
	found := false
	for index := range evaluation.Requirements {
		if evaluation.Requirements[index].ID != result.ID {
			continue
		}
		evaluation.Requirements[index] = result
		found = true
		break
	}
	if !found {
		return Evaluation{}, fmt.Errorf("requirement %q is not part of this evaluation", result.ID)
	}
	evaluation.Eligibility = aggregateState(evaluation.Requirements)
	evaluation.State = evaluation.Eligibility
	if evaluation.Objective != nil && evaluation.State == DecisionEligible {
		evaluation.State = DecisionUnresolved
	}
	evaluation.Gaps = nil
	if evaluation.Objective != nil && evaluation.Eligibility == DecisionEligible {
		evaluation.Gaps = append(evaluation.Gaps, "objective: "+evaluation.Objective.Reason)
	}
	for _, item := range evaluation.Requirements {
		if item.State == RequirementUnresolved || item.State == RequirementBlocked {
			evaluation.Gaps = append(evaluation.Gaps, item.ID+": "+item.Reason)
		}
	}
	evaluation.Gaps = uniqueStrings(evaluation.Gaps)
	evaluation.NextAction = nil
	if evaluation.State == DecisionUnresolved {
		evaluation.NextAction = selectNextAction(evaluation.Requirements)
	}
	return evaluation, nil
}
