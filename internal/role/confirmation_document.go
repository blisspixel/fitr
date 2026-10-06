package role

import (
	"errors"
	"reflect"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/contextquality"
	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/record"
)

// errContextWindow is unresolved evidence, not a disproof. A context run
// whose operating window differs from the battery must not be read as a
// smaller verified window.
var errContextWindow = errors.New("context evidence does not share the battery operating window")

func (plan *ConfirmationPlan) sealFreshContext(policy contextquality.Policy) error {
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
	plan.ContextPolicy.PayloadUTF8Bytes = append([]int(nil), policy.PayloadUTF8Bytes...)
	plan.ContextPlanSHA256 = digest
	plan.ContextCells = len(built.Cells)
	return nil
}

func (plan ConfirmationPlan) validateDocumentContext() error {
	floor, err := UsableContextFloor(plan.Spec)
	if err != nil {
		return err
	}
	if floor == nil {
		if plan.ContextPolicy != nil || plan.ContextPlanSHA256 != "" || plan.ContextCells != 0 {
			return errors.New("confirmation context schedule requires a usable-context floor")
		}
		for _, candidate := range plan.Candidates {
			if candidate.ContextEvidenceSHA256 != "" {
				return errors.New("confirmation context evidence requires a usable-context floor")
			}
		}
		return nil
	}
	if plan.ContextPolicy == nil || plan.ContextPlanSHA256 == "" || plan.ContextCells == 0 {
		return errors.New("usable-context confirmation requires a fresh context schedule")
	}
	if plan.ContextPolicy.OperatingWindowTokens != plan.Protocol.RequestedContext {
		return errors.New("confirmation context window differs from the battery protocol")
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
		return errors.New("confirmation context schedule does not match its fresh seed")
	}
	for _, candidate := range plan.Candidates {
		if !roleDigestValid(candidate.ContextEvidenceSHA256) {
			return errors.New("confirmation is missing exploration context evidence")
		}
	}
	return nil
}

func explorationContextSchedule(plan ConfirmationPlan, battery, contextPoints []*record.Record) (contextquality.Policy, string, error) {
	if len(contextPoints) != len(battery) {
		return contextquality.Policy{}, "", errors.New("usable-context confirmation requires one exploration context record per candidate")
	}
	var policy contextquality.Policy
	var digest string
	for index, contextRecord := range contextPoints {
		if err := contextCompanion(battery[index], contextRecord, ""); err != nil {
			return contextquality.Policy{}, "", err
		}
		if contextRecord.ContextQuality == nil {
			return contextquality.Policy{}, "", errors.New("exploration context record has no sealed phase")
		}
		current := contextRecord.ContextQuality.Plan.Policy
		current.PayloadUTF8Bytes = append([]int(nil), current.PayloadUTF8Bytes...)
		currentDigest := contextRecord.TaskPlan.ContextPlanSHA256
		if index == 0 {
			policy, digest = current, currentDigest
			if policy.OperatingWindowTokens != plan.Protocol.RequestedContext || currentDigest == "" {
				return contextquality.Policy{}, "", errors.New("exploration context schedule does not match the battery window")
			}
			continue
		}
		if currentDigest != digest || !reflect.DeepEqual(current, policy) {
			return contextquality.Policy{}, "", errors.New("exploration context records do not share one sealed plan")
		}
	}
	return policy, digest, nil
}

func contextCompanion(battery, context *record.Record, expectedPlan string) error {
	if battery == nil || context == nil || battery.Completion == nil || context.Completion == nil || battery.Manifest == nil || context.Manifest == nil {
		return errors.New("context evidence is missing")
	}
	if issue := context.EvidenceIntegrityIssue(); issue != "" {
		return errors.New(issue)
	}
	if context.Level != "context" {
		return errors.New("usable-context evidence must be a context-level run")
	}
	if context.Manifest.Model.RuntimeBoundDigest() == "" || context.Manifest.Model.RuntimeBoundDigest() != battery.Manifest.Model.RuntimeBoundDigest() {
		return errors.New("context evidence names a different artifact")
	}
	if context.DeviceV2 == nil || battery.DeviceV2 == nil || context.DeviceV2.Context.EffectiveTokens == nil || battery.DeviceV2.Context.EffectiveTokens == nil ||
		*context.DeviceV2.Context.EffectiveTokens != *battery.DeviceV2.Context.EffectiveTokens || context.ContextSize() != battery.ContextSize() {
		return errContextWindow
	}
	contextKey, err := context.DeviceV2.ComparabilityKey()
	batteryKey, batteryErr := battery.DeviceV2.ComparabilityKey()
	if err != nil || batteryErr != nil || contextKey == "" || contextKey != batteryKey {
		return errors.New("context evidence names a different device configuration")
	}
	if expectedPlan != "" && context.TaskPlan.ContextPlanSHA256 != expectedPlan {
		return errors.New("context evidence does not match the sealed context plan")
	}
	if context.StableRunID() == battery.StableRunID() || context.Completion.EvidenceSHA256 == battery.Completion.EvidenceSHA256 {
		return errors.New("context evidence reused the battery run")
	}
	return nil
}

func overlayUsableContext(candidate Candidate, spec Spec, battery, context *record.Record, expectedPlan string) (Candidate, error) {
	requirement, found, err := usableContextRequirement(spec)
	if err != nil {
		return candidate, err
	}
	if !found {
		if context != nil {
			return candidate, errors.New("context evidence requires a usable-context floor")
		}
		return candidate, nil
	}
	if candidate.Evaluation == nil || context == nil {
		return candidate, nil
	}
	if tokenFloorDisproven(candidate, requirement.ID) {
		return candidate, nil
	}
	if err := contextCompanion(battery, context, expectedPlan); err != nil {
		if errors.Is(err, errContextWindow) {
			return applyContextOverlay(candidate, spec, decision.RequirementResult{
				ID: requirement.ID, State: decision.RequirementUnresolved, Reason: err.Error(),
				Missing: []string{"context evidence at the battery operating window"},
			})
		}
		return candidate, err
	}
	outcome, err := decision.UsableContextResult(context, requirement)
	if err != nil {
		return candidate, err
	}
	if !declaredContextLength(outcome, context) {
		return applyContextOverlay(candidate, spec, decision.RequirementResult{
			ID: requirement.ID, State: decision.RequirementUnresolved, Reason: "verified usable context is not one of the declared tiers",
			Missing: []string{"a verified prefix at a declared tier"},
		})
	}
	return applyContextOverlay(candidate, spec, outcome)
}

func usableContextRequirement(spec Spec) (decision.Requirement, bool, error) {
	var requirement decision.Requirement
	found := false
	for _, item := range spec.Decision.Requirements {
		if item.Context == nil || item.Context.MinimumUsableContextBytes == nil {
			continue
		}
		if found {
			return decision.Requirement{}, false, errors.New("a role admits one usable-context floor")
		}
		requirement, found = item, true
	}
	return requirement, found, nil
}

func tokenFloorDisproven(candidate Candidate, id string) bool {
	if candidate.Evaluation == nil {
		return false
	}
	for _, result := range candidate.Evaluation.Requirements {
		if result.ID == id && result.State == decision.RequirementDisproven && result.Unit == analysis.UnitTokens {
			return true
		}
	}
	return false
}

func declaredContextLength(outcome decision.RequirementResult, context *record.Record) bool {
	if outcome.Observed == nil || context == nil || context.ContextQuality == nil {
		return true
	}
	observed := *outcome.Observed
	if observed == 0 {
		return true
	}
	for _, tier := range context.ContextQuality.Plan.Policy.PayloadUTF8Bytes {
		if float64(tier) == observed {
			return true
		}
	}
	return false
}

func applyContextOverlay(candidate Candidate, spec Spec, outcome decision.RequirementResult) (Candidate, error) {
	evaluation, err := candidate.Evaluation.WithRequirement(outcome)
	if err != nil {
		return candidate, err
	}
	candidate.Evaluation = &evaluation
	candidate.State = string(evaluation.Eligibility)
	candidate.Reasons = nil
	candidate.Preference = nil
	for _, requirement := range evaluation.Requirements {
		if requirement.State != decision.RequirementEstablished {
			candidate.Reasons = append(candidate.Reasons, requirement.ID+": "+requirement.Reason)
		}
	}
	if candidate.State == "eligible" {
		candidate.Preference, candidate.Reasons = preferenceResult(spec, evaluation.Requirements)
	}
	return candidate, nil
}
