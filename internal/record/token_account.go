//nolint:dupl // field binder only; phase_receipt.go owns the seal so the schemas cannot be swapped
package record

import (
	"github.com/blisspixel/fitr/internal/contextquality"
)

// ContextTokenAccount is one usable-input token receipt attached to a run.
//
// Report is never taken from a caller. Attach derives it, and every loader
// derives it again. A byte-pack phase is not this receipt: 65536 UTF-8 bytes
// do not certify 64000 usable-input tokens.
type ContextTokenAccount struct {
	Plan     contextquality.TokenPlan           `json:"plan"`
	Observed contextquality.TokenObservationSet `json:"observed"`
	Report   contextquality.TokenReport         `json:"report"`
}

// PlanContextTokenAccount seals the token schedule before the run manifest
// exists. The digest binds the target, identity and ordered cases.
func (r *Record) PlanContextTokenAccount(plan contextquality.TokenPlan) error {
	return r.tokenAccountReceipt().plan(len(plan.Cases), plan.Digest)
}

// AttachContextTokenAccount records observations against the sealed plan.
// The caller supplies what it observed, never a verdict.
func (r *Record) AttachContextTokenAccount(plan contextquality.TokenPlan, observed contextquality.TokenObservationSet) error {
	return r.tokenAccountReceipt().attach(plan, observed)
}

func (r *Record) validateContextTokenAccount() error {
	return r.tokenAccountReceipt().validate()
}

func (r *Record) tokenAccountReceipt() receiptBinder[contextquality.TokenPlan, contextquality.TokenObservationSet, contextquality.TokenReport] {
	binder := receiptBinder[contextquality.TokenPlan, contextquality.TokenObservationSet, contextquality.TokenReport]{
		r: r, noun: "a token account", claim: "token account", qualify: contextquality.Qualify,
		present: func() bool { return r != nil && r.ContextTokenAccount != nil },
	}
	if r == nil {
		return binder
	}
	binder.cases = &r.TaskPlan.TokenAccountCases
	binder.digest = &r.TaskPlan.TokenAccountPlanSHA256
	binder.put = func(plan contextquality.TokenPlan, observed contextquality.TokenObservationSet, report contextquality.TokenReport) {
		r.ContextTokenAccount = &ContextTokenAccount{Plan: plan, Observed: observed, Report: report}
	}
	binder.clear = func() { r.ContextTokenAccount = nil }
	binder.snapshot = func() (contextquality.TokenPlan, contextquality.TokenObservationSet, contextquality.TokenReport, int) {
		evidence := r.ContextTokenAccount
		return evidence.Plan, evidence.Observed, evidence.Report, len(evidence.Plan.Cases)
	}
	return binder
}

func cloneContextTokenAccount(evidence *ContextTokenAccount) *ContextTokenAccount {
	if evidence == nil {
		return nil
	}
	dup := *evidence
	dup.Plan = evidence.Plan.Clone()
	dup.Observed = evidence.Observed.Clone()
	dup.Report = evidence.Report.Clone()
	return &dup
}
