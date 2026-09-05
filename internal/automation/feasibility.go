package automation

import (
	"errors"
	"fmt"

	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/role"
	"github.com/blisspixel/fitr/internal/score"
	"github.com/blisspixel/fitr/internal/stats"
)

// ValidateFeasibility rejects requirements that the fixed, execution-disabled
// full Ollama battery cannot establish. It performs no model, filesystem or
// network operations. Success means the collection is not already impossible;
// it does not establish quality, runtime capability declarations, resource fit,
// or profile-specific categorical gates. Those still require prepared runtime
// checks and complete, qualified evidence under the unchanged role policy.
func ValidateFeasibility(spec role.Spec, tasks *eval.Spec, repeats, context int) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	if repeats < 3 || repeats > 20 || context <= 0 {
		return errors.New("auto feasibility requires three to twenty repeats and a positive fixed context")
	}
	_, err := eval.PlanRequestEnvelope(tasks, eval.RequestEnvelopeOptions{
		Backend: "ollama", Level: "full", Repeats: repeats, CheckRepeats: repeats, ContextProbe: true,
	})
	if err != nil {
		return fmt.Errorf("auto task schedule: %w", err)
	}
	for _, requirement := range spec.Decision.Requirements {
		if err := feasibleRequirement(requirement, tasks, repeats, context); err != nil {
			return fmt.Errorf("auto requirement %q: %w", requirement.ID, err)
		}
	}
	return nil
}

func feasibleRequirement(requirement decision.Requirement, tasks *eval.Spec, repeats, context int) error {
	switch {
	case requirement.Behavior != nil:
		return feasibleBehavior(*requirement.Behavior, tasks, repeats)
	case requirement.Capability != nil:
		return feasibleCapability(*requirement.Capability)
	case requirement.Context != nil:
		if requirement.Context.MinimumEffectiveTokens > context {
			return errors.New("context floor exceeds the fixed requested context")
		}
	case requirement.Capacity != nil:
		capacity := requirement.Capacity
		if capacity.RequestedContext != 0 && capacity.RequestedContext != context {
			return errors.New("capacity floor requires another context")
		}
		if capacity.MaximumResidentBytes == 0 && capacity.MaximumResidentGB < 1.0/(1<<30) {
			return errors.New("capacity limit cannot admit a positive resident-byte observation")
		}
	}
	// Every currently accepted decision performance metric has an observed
	// estimate in this full battery. Loaded TTFT also needs actual /ps proof;
	// it does not claim an uncached prompt and cannot bypass that later guard.
	return nil
}

func feasibleCapability(requirement decision.CapabilityRequirement) error {
	if requirement.Name != "completion" && requirement.Name != "tools" {
		return errors.New("auto v1 supports only text completion and tool-channel capabilities")
	}
	if requirement.MinimumSupport == score.CapabilityProtocolVerified && requirement.Name != "tools" {
		return errors.New("this battery records protocol verification only for tools")
	}
	return nil
}

func feasibleBehavior(requirement decision.BehaviorRequirement, tasks *eval.Spec, repeats int) error {
	if requirement.MinimumRate != nil {
		if !decision.SupportsBehaviorRate(requirement.Need) {
			return errors.New("required behavior has no supported rate estimand")
		}
		return feasibleRateSchedule(requirement, tasks.Checks, repeats)
	}
	switch requirement.Need {
	case "coding", "unattended_agentic":
		return errors.New("required behavior needs generated-code execution, which auto v1 disables")
	case "reasoning":
		return errors.New("reasoning has rate observations but no categorical PASS verdict; use minimum_rate")
	case "structured_output", "instruction_precision", "tool_calling":
		return feasibleRateSchedule(requirement, tasks.Checks, repeats)
	case "user_tasks":
		if count, _ := plannedNeed(tasks.Checks, requirement.Need, repeats); count == 0 {
			return errors.New("required user behavior has no planned observations")
		}
	case "tool_restraint":
		if count, _ := plannedNeed(tasks.Checks, requirement.Need, repeats); count > 0 {
			return feasibleRateSchedule(requirement, tasks.Checks, repeats)
		}
		// With no pooled restraint tasks, the scorer uses plumbing/withdrawal
		// diagnostics. The full battery includes these non-executable probes.
	case "uncensored", "output_health":
		if len(eval.RefusalPromptIDs(tasks.Refusal)) == 0 {
			return errors.New("required behavior has no planned refusal/output-text observations")
		}
	default:
		return errors.New("required behavior is unavailable in the auto v1 battery")
	}
	return nil
}

func plannedNeed(checks []eval.CheckSpec, need string, repeats int) (int, int) {
	count := 0
	families := make(map[string]bool)
	for _, check := range checks {
		if check.Need == need {
			count += repeats
			families[check.Family] = true
		}
	}
	return count, len(families)
}

func feasibleRateSchedule(requirement decision.BehaviorRequirement, checks []eval.CheckSpec, repeats int) error {
	count, families := plannedNeed(checks, requirement.Need, repeats)
	if count == 0 {
		return errors.New("required behavior has no planned rate observations")
	}
	if count > 1 && families < 2 {
		return errors.New("required behavior needs at least two independent planned families")
	}
	if requirement.MinimumRate != nil && stats.Wilson(count, count).Lo < *requirement.MinimumRate {
		return errors.New("even the optimistic unclustered lower bound cannot clear the required rate with this schedule")
	}
	// ClusteredWilson is never narrower than ordinary Wilson. Thus the
	// all-success ordinary lower bound is a safe optimistic rejection ceiling.
	// The clustered all-success result itself is NOT such a ceiling: identical
	// outcomes trigger a different correlation estimate. No synthetic result
	// qualifies a model or promises that a fresh confirmation will succeed.
	return nil
}
