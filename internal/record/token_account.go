package record

import (
	"errors"
	"reflect"

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
	if r == nil {
		return errors.New("cannot plan a token account on a nil record")
	}
	if r.Manifest != nil {
		return errors.New("token account plan must be sealed before the run manifest")
	}
	if r.TaskPlan.TokenAccountCases != 0 || r.TaskPlan.TokenAccountPlanSHA256 != "" {
		return errors.New("token account plan is already sealed")
	}
	digest, err := plan.Digest()
	if err != nil {
		return err
	}
	r.TaskPlan.TokenAccountCases = len(plan.Cases)
	r.TaskPlan.TokenAccountPlanSHA256 = digest
	return nil
}

// AttachContextTokenAccount records observations against the sealed plan.
// The caller supplies what it observed, never a verdict.
func (r *Record) AttachContextTokenAccount(plan contextquality.TokenPlan, observed contextquality.TokenObservationSet) error {
	if r == nil {
		return errors.New("cannot attach a token account to a nil record")
	}
	if r.ContextTokenAccount != nil {
		return errors.New("token account evidence is already attached")
	}
	report, err := contextquality.Qualify(plan, observed)
	if err != nil {
		return err
	}
	r.ContextTokenAccount = &ContextTokenAccount{
		Plan: plan.Clone(), Observed: observed.Clone(), Report: report.Clone(),
	}
	if err := r.validateContextTokenAccount(); err != nil {
		r.ContextTokenAccount = nil
		return err
	}
	return nil
}

func (r *Record) validateContextTokenAccount() error {
	planned := r.TaskPlan.TokenAccountCases != 0 || r.TaskPlan.TokenAccountPlanSHA256 != ""
	if r.ContextTokenAccount == nil {
		if planned {
			return errors.New("sealed token account has no attached observations")
		}
		return nil
	}
	if !planned {
		return errors.New("token account observations have no sealed plan")
	}
	evidence := r.ContextTokenAccount
	digest, err := evidence.Plan.Digest()
	if err != nil {
		return err
	}
	if !sha256Digest.MatchString(r.TaskPlan.TokenAccountPlanSHA256) || digest != r.TaskPlan.TokenAccountPlanSHA256 {
		return errors.New("token account observations do not match the sealed plan")
	}
	if len(evidence.Plan.Cases) != r.TaskPlan.TokenAccountCases {
		return errors.New("token account cases do not match the sealed plan")
	}
	report, err := contextquality.Qualify(evidence.Plan, evidence.Observed)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(report, evidence.Report) {
		return errors.New("persisted token account report does not match its observations")
	}
	return nil
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
