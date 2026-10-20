//nolint:dupl // field binder only; phase_receipt.go owns the seal so the schemas cannot be swapped
package record

import (
	"github.com/blisspixel/fitr/internal/contextquality"
)

// ContextTokenConditions is the matched-condition receipt. It is not a
// reading of fitr.context-token-account.v1 and not a reading of the byte
// pack. Report is derived on attach and again on load.
type ContextTokenConditions struct {
	Plan     contextquality.TokenConditionPlan      `json:"plan"`
	Observed contextquality.ConditionObservationSet `json:"observed"`
	Report   contextquality.ConditionReport         `json:"report"`
}

// PlanContextTokenConditions seals the condition schedule before the run
// manifest exists.
func (r *Record) PlanContextTokenConditions(plan contextquality.TokenConditionPlan) error {
	return r.tokenConditionReceipt().plan(len(plan.Cases), plan.Digest)
}

// AttachContextTokenConditions records observations against the sealed plan.
// The caller supplies what it observed, never a verdict.
func (r *Record) AttachContextTokenConditions(plan contextquality.TokenConditionPlan, observed contextquality.ConditionObservationSet) error {
	return r.tokenConditionReceipt().attach(plan, observed)
}

func (r *Record) validateContextTokenConditions() error {
	return r.tokenConditionReceipt().validate()
}

func (r *Record) tokenConditionReceipt() receiptBinder[contextquality.TokenConditionPlan, contextquality.ConditionObservationSet, contextquality.ConditionReport] {
	binder := receiptBinder[contextquality.TokenConditionPlan, contextquality.ConditionObservationSet, contextquality.ConditionReport]{
		r: r, noun: "token conditions", claim: "token conditions", qualify: contextquality.QualifyConditions,
		present: func() bool { return r != nil && r.ContextTokenConditions != nil },
	}
	if r == nil {
		return binder
	}
	binder.cases = &r.TaskPlan.TokenConditionCases
	binder.digest = &r.TaskPlan.TokenConditionPlanSHA256
	binder.put = func(plan contextquality.TokenConditionPlan, observed contextquality.ConditionObservationSet, report contextquality.ConditionReport) {
		r.ContextTokenConditions = &ContextTokenConditions{Plan: plan, Observed: observed, Report: report}
	}
	binder.clear = func() { r.ContextTokenConditions = nil }
	binder.snapshot = func() (contextquality.TokenConditionPlan, contextquality.ConditionObservationSet, contextquality.ConditionReport, int) {
		evidence := r.ContextTokenConditions
		return evidence.Plan, evidence.Observed, evidence.Report, len(evidence.Plan.Cases)
	}
	return binder
}

func cloneContextTokenConditions(evidence *ContextTokenConditions) *ContextTokenConditions {
	if evidence == nil {
		return nil
	}
	dup := *evidence
	dup.Plan = evidence.Plan.Clone()
	dup.Observed = evidence.Observed.Clone()
	dup.Report = evidence.Report.Clone()
	return &dup
}
