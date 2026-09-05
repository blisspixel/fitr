package record

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/blisspixel/fitr/internal/contextquality"
)

// ContextQuality is one finite document-task phase attached to a run.
//
// Report is never taken from a caller. AttachContextQuality derives it from
// the plan and the observations, and every loader derives it again before the
// evidence is accepted, so a stored report the analyzer does not reproduce is
// rejected rather than believed. The phase establishes a finite task-set
// result at one operating window; it is not a context capacity, a statistical
// interval or proof that the runtime honored the requested controls.
type ContextQuality struct {
	Plan         contextquality.Plan          `json:"plan"`
	Observations []contextquality.Observation `json:"observations"`
	Report       contextquality.Report        `json:"report"`
}

// PlanContextQuality seals a context task plan into the task plan before the
// manifest exists. The digest binds the exact policy, seed set and ordered
// cells, so a finished phase cannot present an easier plan than the one the
// run committed to.
func (r *Record) PlanContextQuality(plan contextquality.Plan) error {
	if r == nil {
		return errors.New("cannot plan context tasks on a nil record")
	}
	if r.Manifest != nil {
		return errors.New("context task plan must be sealed before the run manifest")
	}
	if r.TaskPlan.ContextCells != 0 || r.TaskPlan.ContextPlanSHA256 != "" {
		return errors.New("context task plan is already sealed")
	}
	digest, err := plan.Digest()
	if err != nil {
		return err
	}
	r.TaskPlan.ContextCells = len(plan.Cells)
	r.TaskPlan.ContextPlanSHA256 = digest
	return nil
}

// AttachContextQuality records one phase's observations against the sealed
// plan. The caller supplies what it observed, never a verdict.
func (r *Record) AttachContextQuality(plan contextquality.Plan, observations []contextquality.Observation) error {
	if r == nil {
		return errors.New("cannot attach context tasks to a nil record")
	}
	if r.ContextQuality != nil {
		return errors.New("context task evidence is already attached")
	}
	report, err := contextquality.Analyze(plan, observations)
	if err != nil {
		return err
	}
	r.ContextQuality = &ContextQuality{
		Plan:         plan.Clone(),
		Observations: append([]contextquality.Observation(nil), observations...),
		Report:       report,
	}
	if err := r.validateContextQuality(); err != nil {
		r.ContextQuality = nil
		return err
	}
	return nil
}

// validateContextQuality rejects observations that arrive without their sealed
// plan, a plan that differs from the one sealed before inference, and a report
// the analyzer does not reproduce from the stored observations. A run that
// sealed a context plan and finished without one is equally invalid: the
// phase's absence would otherwise read as an absent requirement.
func (r *Record) validateContextQuality() error {
	if r.ContextQuality == nil {
		if r.TaskPlan.ContextCells != 0 || r.TaskPlan.ContextPlanSHA256 != "" {
			return errors.New("sealed context task plan has no attached observations")
		}
		return nil
	}
	evidence := r.ContextQuality
	if err := evidence.Plan.Validate(); err != nil {
		return err
	}
	digest, err := evidence.Plan.Digest()
	if err != nil {
		return err
	}
	if !sha256Digest.MatchString(r.TaskPlan.ContextPlanSHA256) || digest != r.TaskPlan.ContextPlanSHA256 {
		return errors.New("context task observations do not match the sealed plan")
	}
	if len(evidence.Plan.Cells) != r.TaskPlan.ContextCells {
		return fmt.Errorf("context task cells %d do not match planned %d",
			len(evidence.Plan.Cells), r.TaskPlan.ContextCells)
	}
	report, err := contextquality.Analyze(evidence.Plan, evidence.Observations)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(report, evidence.Report) {
		return errors.New("persisted context task report does not match its observations")
	}
	return nil
}

func cloneContextQuality(evidence *ContextQuality) *ContextQuality {
	if evidence == nil {
		return nil
	}
	dup := *evidence
	dup.Plan = evidence.Plan.Clone()
	dup.Observations = append([]contextquality.Observation(nil), evidence.Observations...)
	return &dup
}
