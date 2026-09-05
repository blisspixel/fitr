package automation

import (
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/role"
	"github.com/blisspixel/fitr/internal/score"
	"github.com/blisspixel/fitr/internal/stats"
)

func feasibleRole() role.Spec {
	rate := 0.5
	return role.Spec{Schema: role.SpecSchema, Name: "daily-driver", MaxAgeDays: 30,
		Decision: decision.DecisionSpec{Schema: decision.SpecSchema, Name: "Daily driver", Evidence: decision.EvidenceDecide,
			Requirements: []decision.Requirement{
				{ID: "quality", Behavior: &decision.BehaviorRequirement{Need: "structured_output", MinimumRate: &rate}},
				{ID: "context", Context: &decision.ContextRequirement{MinimumEffectiveTokens: 4096}},
				{ID: "memory", Capacity: &decision.CapacityRequirement{MaximumResidentBytes: 16 << 30, RequestedContext: 4096}},
			}}, Preferences: []role.Preference{{Requirement: "memory", Weight: 1, Worst: 16 << 30, Best: 0}}}
}

func feasibilityTasks(t *testing.T) *eval.Spec {
	t.Helper()
	tasks, err := eval.LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	return tasks
}

func TestFeasibilityAcceptsSupportedTextAndToolRoles(t *testing.T) {
	for _, need := range []string{"structured_output", "instruction_precision", "reasoning", "tool_calling"} {
		t.Run(need, func(t *testing.T) {
			spec := feasibleRole()
			spec.Decision.Requirements[0].Behavior.Need = need
			spec.Decision.Requirements = append(spec.Decision.Requirements,
				decision.Requirement{ID: "tool-protocol", Capability: &decision.CapabilityRequirement{
					Name: "tools", MinimumSupport: score.CapabilityProtocolVerified}})
			if err := ValidateFeasibility(spec, feasibilityTasks(t), 3, 4096); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFeasibilityRejectsUnavailableMandatoryProtocols(t *testing.T) {
	for _, need := range []string{"coding", "unattended_agentic", "reasoning"} {
		t.Run(need, func(t *testing.T) {
			spec := feasibleRole()
			spec.Decision.Requirements[0].Behavior = &decision.BehaviorRequirement{Need: need, RequiredState: score.Pass}
			if err := ValidateFeasibility(spec, feasibilityTasks(t), 3, 4096); err == nil {
				t.Fatal("unavailable required PASS accepted")
			}
		})
	}
	for _, capability := range []decision.CapabilityRequirement{
		{Name: "vision", MinimumSupport: score.CapabilityDeclared},
		{Name: "embedding", MinimumSupport: score.CapabilityProtocolVerified},
		{Name: "completion", MinimumSupport: score.CapabilityProtocolVerified},
	} {
		spec := feasibleRole()
		spec.Decision.Requirements = append(spec.Decision.Requirements,
			decision.Requirement{ID: "capability", Capability: &capability})
		if err := ValidateFeasibility(spec, feasibilityTasks(t), 3, 4096); err == nil {
			t.Fatalf("unavailable capability accepted: %+v", capability)
		}
	}
}

func TestFeasibilityRejectsMissingRateObservationsAndFamilies(t *testing.T) {
	for _, scenario := range []string{"missing", "one family", "unreachable floor", "builtin restraint"} {
		t.Run(scenario, func(t *testing.T) {
			spec, tasks := feasibleRole(), feasibilityTasks(t)
			switch scenario {
			case "missing":
				spec.Decision.Requirements[0].Behavior.Need = "user_tasks"
			case "one family":
				for index := range tasks.Checks {
					if tasks.Checks[index].Need == "structured_output" {
						tasks.Checks[index].Family = "json_object"
					}
				}
			case "unreachable floor":
				*spec.Decision.Requirements[0].Behavior.MinimumRate = 1
			case "builtin restraint":
				spec.Decision.Requirements[0].Behavior.Need = "tool_restraint"
			}
			if err := ValidateFeasibility(spec, tasks, 3, 4096); err == nil {
				t.Fatal("impossible rate schedule accepted")
			}
		})
	}
}

func TestFeasibilityDoesNotTreatClusteredAllSuccessAsAnUpperCeiling(t *testing.T) {
	spec, tasks := feasibleRole(), feasibilityTasks(t)
	threshold := 0.6
	spec.Decision.Requirements[0].Behavior = &decision.BehaviorRequirement{Need: "structured_output", MinimumRate: &threshold}
	count, families := plannedNeed(tasks.Checks, "structured_output", 3)
	if stats.Wilson(families, families).Lo >= threshold || stats.Wilson(count, count).Lo <= threshold {
		t.Fatal("fixture does not separate clustered identical outcomes from the optimistic ceiling")
	}
	if err := ValidateFeasibility(spec, tasks, 3, 4096); err != nil {
		t.Fatalf("a synthetic all-success outcome prematurely rejected collection: %v", err)
	}
}

func TestFeasibilityRejectsContradictoryContextAndInvalidSchedules(t *testing.T) {
	cases := map[string]func(*role.Spec, *eval.Spec){
		"role schema":      func(s *role.Spec, _ *eval.Spec) { s.Schema = "other" },
		"context floor":    func(s *role.Spec, _ *eval.Spec) { s.Decision.Requirements[1].Context.MinimumEffectiveTokens = 8192 },
		"capacity context": func(s *role.Spec, _ *eval.Spec) { s.Decision.Requirements[2].Capacity.RequestedContext = 8192 },
		"sub-byte ceiling": func(s *role.Spec, _ *eval.Spec) {
			s.Decision.Requirements[2].Capacity.MaximumResidentBytes = 0
			s.Decision.Requirements[2].Capacity.MaximumResidentGB = 1e-12
		},
		"unbounded task": func(_ *role.Spec, tasks *eval.Spec) { tasks.Speed.Decode.NumPredict = -1 },
		"no output health text": func(s *role.Spec, tasks *eval.Spec) {
			s.Decision.Requirements[0].Behavior = &decision.BehaviorRequirement{Need: "output_health", RequiredState: score.Pass}
			tasks.Refusal.Prompts = nil
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			spec, tasks := feasibleRole(), feasibilityTasks(t)
			mutate(&spec, tasks)
			if err := ValidateFeasibility(spec, tasks, 3, 4096); err == nil {
				t.Fatal("contradictory or invalid schedule accepted")
			}
		})
	}
	for _, counts := range [][2]int{{0, 4096}, {21, 4096}, {3, 0}} {
		if err := ValidateFeasibility(feasibleRole(), feasibilityTasks(t), counts[0], counts[1]); err == nil {
			t.Fatalf("invalid repeats/context accepted: %v", counts)
		}
	}
	if err := ValidateFeasibility(feasibleRole(), nil, 3, 4096); err == nil {
		t.Fatal("nil task schedule accepted")
	}
}

func TestFeasibilitySupportsExplicitUserClassifierAndObservedPerformance(t *testing.T) {
	spec, tasks := feasibleRole(), feasibilityTasks(t)
	tasks.Checks = tasks.Checks[:1]
	tasks.Checks[0].Need = "user_tasks"
	spec.Decision.Requirements[0].Behavior = &decision.BehaviorRequirement{Need: "user_tasks", RequiredState: score.Pass}
	for _, metric := range []decision.PerformanceMetric{decision.MetricDecodeTPS, decision.MetricPrefillTPS,
		decision.MetricRequestTTFT, decision.MetricLoadedTTFT} {
		bound := 100.0
		spec.Decision.Requirements = append(spec.Decision.Requirements,
			decision.Requirement{ID: string(metric), Performance: &decision.PerformanceRequirement{Metric: metric, AtMost: &bound}})
	}
	if err := ValidateFeasibility(spec, tasks, 3, 4096); err != nil {
		t.Fatal(err)
	}
	tasks.Checks = nil
	if err := ValidateFeasibility(spec, tasks, 3, 4096); err == nil || !strings.Contains(err.Error(), "no planned observations") {
		t.Fatalf("missing user classifier error = %v", err)
	}
}

func TestFeasibilityCategoricalRestraintUsesActualScorerDenominator(t *testing.T) {
	spec, tasks := feasibleRole(), feasibilityTasks(t)
	spec.Decision.Requirements[0].Behavior = &decision.BehaviorRequirement{Need: "tool_restraint", RequiredState: score.Pass}
	if err := ValidateFeasibility(spec, tasks, 3, 4096); err == nil {
		t.Fatal("single pooled restraint family was accepted as broad PASS")
	}
	kept := tasks.Checks[:0]
	for _, check := range tasks.Checks {
		if check.Need != "tool_restraint" {
			kept = append(kept, check)
		}
	}
	tasks.Checks = kept
	if err := ValidateFeasibility(spec, tasks, 3, 4096); err != nil {
		t.Fatalf("legacy diagnostic schedule was rejected: %v", err)
	}
}
