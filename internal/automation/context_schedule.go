package automation

import (
	"errors"
	"math"
	"reflect"

	"github.com/blisspixel/fitr/internal/contextquality"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/role"
)

// FeasibilitySchedule is the operator-declared document-context collection
// that accompanies one fixed battery. An empty schedule means the battery
// alone. Tiers are payload sizes in bytes, never tokens.
type FeasibilitySchedule struct {
	Tiers      []int
	Candidates int
	Limits     Limits
}

// ContextPointBudget reserves one candidate's document-context phase: every
// cell generate, plus the preserving load probe that has to ride the same
// admission hook. The budget is zero when no tiers were declared.
func ContextPointBudget(tiers []int) (int64, int64, error) {
	if len(tiers) == 0 {
		return 0, 0, nil
	}
	if len(tiers) > math.MaxInt64/contextquality.CellsPerTier {
		return 0, 0, errors.New("context schedule request count overflows")
	}
	cells := int64(len(tiers) * contextquality.CellsPerTier)
	requests := cells + 1
	if cells > math.MaxInt64/int64(contextquality.OutputReserveTokens) {
		return 0, 0, errors.New("context schedule token count overflows")
	}
	tokens := cells*int64(contextquality.OutputReserveTokens) + int64(eval.ContextProbeOutputTokens)
	return requests, tokens, nil
}

// BindContextSchedule seals one context plan from the session seed before the
// auto plan digest exists. Every candidate shares that digest. The caller has
// already refused a runner that cannot prove the output reserve.
func (plan *Plan) BindContextSchedule(tiers []int) error {
	if plan == nil || plan.Runtime.NumCtx <= 0 || plan.SeedSet == "" {
		return errors.New("context schedule needs the fixed context and seed set")
	}
	policy, err := contextquality.NewPolicy(plan.Runtime.NumCtx, tiers)
	if err != nil {
		return err
	}
	seed, err := record.ContextTaskSeedSet(plan.SeedSet)
	if err != nil {
		return err
	}
	built, err := contextquality.NewPlan(policy, seed)
	if err != nil {
		return err
	}
	digest, err := built.Digest()
	if err != nil {
		return err
	}
	plan.ContextPolicy = &policy
	plan.ContextPlanSHA256 = digest
	plan.ContextCells = len(built.Cells)
	return nil
}

func (plan Plan) validateContextSchedule() error {
	floor, err := role.UsableContextFloor(plan.Spec)
	if err != nil {
		return err
	}
	if plan.ContextPolicy == nil {
		if floor != nil || plan.ContextPlanSHA256 != "" || plan.ContextCells != 0 {
			return errors.New("usable-context floor requires a sealed context schedule")
		}
		return nil
	}
	if floor == nil {
		return errors.New("context schedule requires a usable-context floor")
	}
	if plan.ContextPolicy.OperatingWindowTokens != plan.Runtime.NumCtx {
		return errors.New("context schedule window differs from the fixed context")
	}
	payloads := plan.ContextPolicy.PayloadUTF8Bytes
	if len(payloads) == 0 || payloads[len(payloads)-1] < *floor {
		return errors.New("largest context tier is below the usable-context floor")
	}
	seed, err := record.ContextTaskSeedSet(plan.SeedSet)
	if err != nil {
		return err
	}
	built, err := contextquality.NewPlan(*plan.ContextPolicy, seed)
	if err != nil {
		return err
	}
	digest, err := built.Digest()
	if err != nil || digest != plan.ContextPlanSHA256 || len(built.Cells) != plan.ContextCells {
		return errors.New("context schedule digest does not match the sealed plan")
	}
	requests, tokens, err := ContextPointBudget(payloads)
	if err != nil {
		return err
	}
	if plan.PointRequests < requests || plan.PointRequestedOutputTokens < tokens {
		return errors.New("auto point allowance does not fund the sealed context schedule")
	}
	return nil
}

func feasibleContextSchedule(spec role.Spec, context int, envelope eval.RequestEnvelope, schedule FeasibilitySchedule) error {
	floor, err := role.UsableContextFloor(spec)
	if err != nil {
		return err
	}
	if floor == nil {
		if len(schedule.Tiers) != 0 {
			return errors.New("context tiers require a usable-context floor")
		}
		return nil
	}
	if len(schedule.Tiers) == 0 {
		return errors.New("usable-context floor requires declared context tiers")
	}
	policy, err := contextquality.NewPolicy(context, schedule.Tiers)
	if err != nil {
		return err
	}
	payloads := policy.PayloadUTF8Bytes
	if payloads[len(payloads)-1] < *floor {
		return errors.New("largest context tier is below the usable-context floor")
	}
	if schedule.Candidates < 2 || schedule.Candidates > 4 {
		return errors.New("usable-context collection requires two to four candidates")
	}
	requests, tokens, err := ContextPointBudget(schedule.Tiers)
	if err != nil {
		return err
	}
	pointRequests := envelope.MaxRequests + requests
	pointTokens := envelope.MaxRequestedOutputTokens + tokens
	if pointRequests < envelope.MaxRequests || pointTokens < envelope.MaxRequestedOutputTokens {
		return errors.New("context schedule reservation overflows")
	}
	n := int64(schedule.Candidates)
	if schedule.Limits.MaxRequests < 2*n*pointRequests || schedule.Limits.MaxRequestedOutputTokens < 2*n*pointTokens {
		return errors.New("auto limits must fund the battery and context schedule for exploration and confirmation")
	}
	return nil
}

func (plan Plan) matchesConfirmationContext(confirmation role.ConfirmationPlan, completed []Event) error {
	if plan.ContextPolicy == nil {
		if confirmation.ContextPolicy != nil || confirmation.ContextPlanSHA256 != "" || confirmation.ContextCells != 0 {
			return errors.New("confirmation added a context schedule the session did not seal")
		}
		for _, candidate := range confirmation.Candidates {
			if candidate.ContextEvidenceSHA256 != "" {
				return errors.New("confirmation added context evidence the session did not seal")
			}
		}
		return nil
	}
	if confirmation.ContextPolicy == nil || confirmation.ContextPlanSHA256 == "" || confirmation.ContextPlanSHA256 == plan.ContextPlanSHA256 || !reflect.DeepEqual(*confirmation.ContextPolicy, *plan.ContextPolicy) {
		return errors.New("confirmation context schedule is missing, changed, or reused the exploration plan")
	}
	if len(completed) != len(confirmation.Candidates) {
		return errors.New("confirmation context evidence does not match the exploration schedule")
	}
	for index, candidate := range confirmation.Candidates {
		if candidate.ContextEvidenceSHA256 == "" || candidate.ContextEvidenceSHA256 != completed[index].ContextEvidenceSHA256 {
			return errors.New("confirmation replaced exploration context evidence")
		}
	}
	return nil
}

// ContextStoreID is the sibling managed store for one phase. It is derived
// from the session so a closed store can be reopened without a second journal
// field. The purpose stays the phase name; this is not a new evidence class.
func ContextStoreID(plan Plan, phase string) (string, error) {
	if plan.ContextPolicy == nil {
		return "", errors.New("context store requires a sealed context schedule")
	}
	suffix := "-ctx-explore"
	switch phase {
	case "exploration":
	case "confirmation":
		suffix = "-ctx-confirm"
	default:
		return "", errors.New("invalid auto evidence phase")
	}
	id := plan.ID + suffix
	if len(id) > 64 {
		return "", errors.New("context store id exceeds 64 characters")
	}
	return id, nil
}
