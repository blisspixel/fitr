package decision

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/score"
	"github.com/blisspixel/fitr/internal/stats"
)

const currentModelPlaceholder = "<current-model>"

// Evaluate applies a user-declared decision specification to one sealed
// result. The returned evaluation is derived analysis. It never replaces the
// result's original profile-bound scorecard or becomes evidence for that run.
func Evaluate(result *record.Record, spec DecisionSpec) (Evaluation, error) {
	if err := spec.Validate(); err != nil {
		return Evaluation{}, fmt.Errorf("evaluate decision: %w", err)
	}
	report, err := analysis.FromRecord(result)
	if err != nil {
		return Evaluation{}, fmt.Errorf("evaluate decision: %w", err)
	}
	specDigest, err := digest("fitr.decision.spec.v1", spec)
	if err != nil {
		return Evaluation{}, fmt.Errorf("evaluate decision: hash spec: %w", err)
	}
	subject, err := configurationFrom(result, report)
	if err != nil {
		return Evaluation{}, fmt.Errorf("evaluate decision: configuration: %w", err)
	}

	evaluation := evaluateValidated(result, report, subject, spec)
	evaluation.SpecSHA256 = specDigest
	return evaluation, nil
}

// EvaluateConfirmed recognizes fresh evidence only when the sealed run itself
// is bound to a predeclared confirmation plan. Experiment analysis still owns
// candidate-set and fresh-task validation; an ordinary run cannot opt itself
// into this path through a decision-spec flag.
func EvaluateConfirmed(result *record.Record, spec DecisionSpec) (Evaluation, error) {
	evaluation, err := Evaluate(result, spec)
	if err != nil {
		return Evaluation{}, err
	}
	if spec.Evidence != EvidenceConfirm {
		return Evaluation{}, errors.New("evaluate confirmed decision: evidence_level must be confirm")
	}
	if result.Experiment == nil || result.Experiment.Kind != "configuration" ||
		result.Experiment.Stage != "confirm" || result.Experiment.PointCount < 2 ||
		result.Experiment.PlanSHA256 == "" || strings.TrimSpace(result.SeedSet) == "" {
		return Evaluation{}, errors.New("evaluate confirmed decision: sealed confirmation binding is missing")
	}
	for index := range evaluation.Requirements {
		if evaluation.Requirements[index].ID != "confirmation_lineage" {
			continue
		}
		evaluation.Requirements[index] = RequirementResult{
			ID: "confirmation_lineage", State: RequirementEstablished,
			Reason:       "the run is bound to a predeclared confirmation plan and fresh task seed set",
			EvidenceRefs: []string{"experiment.plan_sha256", "run.seedset"},
		}
	}
	evaluation.Gaps = removeConfirmationGaps(evaluation.Gaps)
	evaluation.State = aggregateState(evaluation.Requirements)
	if evaluation.Objective != nil && evaluation.State == DecisionEligible {
		evaluation.State = DecisionUnresolved
	}
	evaluation.NextAction = nil
	if evaluation.State == DecisionUnresolved {
		evaluation.NextAction = selectNextAction(evaluation.Requirements)
	}
	return evaluation, nil
}

func removeConfirmationGaps(gaps []string) []string {
	filtered := gaps[:0]
	for _, gap := range gaps {
		if strings.Contains(gap, "fresh confirmation lineage") ||
			strings.HasPrefix(gap, "confirmation_lineage:") {
			continue
		}
		filtered = append(filtered, gap)
	}
	return filtered
}

// ConfigurationFromRecord derives the typed configuration identity from one
// validated sealed result. Experiment packages use this instead of growing a
// second interpretation of model, runtime, context, and placement identity.
func ConfigurationFromRecord(result *record.Record) (ConfigurationUnderTest, error) {
	report, err := analysis.FromRecord(result)
	if err != nil {
		return ConfigurationUnderTest{}, err
	}
	return configurationFrom(result, report)
}

func evaluateValidated(result *record.Record, report analysis.Report, subject ConfigurationUnderTest,
	spec DecisionSpec) Evaluation {
	evaluation := Evaluation{
		Schema: EvaluationSchema, Source: report.Source, Subject: subject,
		SpecName: spec.Name, Evidence: spec.Evidence,
		State: DecisionEligible, Requirements: make([]RequirementResult, 0, len(spec.Requirements)),
	}
	integrityIssue := result.EvidenceIntegrityIssue()
	for _, requirement := range spec.Requirements {
		var outcome RequirementResult
		if integrityIssue != "" {
			outcome = blockedRequirement(requirement.ID, integrityIssue, "sealed_result.integrity")
		} else {
			outcome = evaluateRequirement(result, report, requirement)
		}
		evaluation.Requirements = append(evaluation.Requirements, outcome)
	}
	evaluation.Eligibility = aggregateState(evaluation.Requirements)

	if spec.Evidence == EvidenceConfirm || spec.Confirmation.FreshEvidence || spec.Confirmation.FreshTasks {
		const gap = "fresh confirmation lineage is not present in an ordinary run receipt"
		evaluation.Gaps = append(evaluation.Gaps, gap)
		evaluation.Requirements = append(evaluation.Requirements, RequirementResult{
			ID: "confirmation_lineage", State: RequirementUnresolved, Reason: gap,
			Missing: []string{"predeclared candidate", "fresh sealed observations"},
			NextAction: &Action{Code: "confirm_candidate", Experiment: "confirmation",
				Reason: "collect fresh evidence after sealing the selected candidate"},
		})
	}

	evaluation.State = aggregateState(evaluation.Requirements)
	if spec.Objective != nil {
		evaluation.Objective = &ObjectiveResult{
			Metric: spec.Objective.Metric, Direction: spec.Objective.Direction,
			State:   RequirementUnresolved,
			Reason:  "one eligible configuration cannot establish a comparative objective winner",
			Missing: []string{"a comparable eligible candidate set", "a declared comparison policy"},
		}
		if evaluation.State == DecisionEligible {
			evaluation.State = DecisionUnresolved
		}
		evaluation.Gaps = append(evaluation.Gaps, "objective: "+evaluation.Objective.Reason)
	}
	for _, result := range evaluation.Requirements {
		if result.State == RequirementUnresolved || result.State == RequirementBlocked {
			evaluation.Gaps = append(evaluation.Gaps, result.ID+": "+result.Reason)
		}
	}
	evaluation.Gaps = uniqueStrings(evaluation.Gaps)
	if evaluation.State == DecisionUnresolved {
		evaluation.NextAction = selectNextAction(evaluation.Requirements)
	}
	return evaluation
}

func evaluateRequirement(result *record.Record, report analysis.Report, requirement Requirement) RequirementResult {
	switch {
	case requirement.Behavior != nil:
		return evaluateBehavior(result, requirement.ID, *requirement.Behavior)
	case requirement.Capability != nil:
		return evaluateCapability(result, requirement.ID, *requirement.Capability)
	case requirement.Context != nil:
		return evaluateContext(report, requirement.ID, *requirement.Context)
	case requirement.Performance != nil:
		return evaluatePerformance(report, requirement.ID, *requirement.Performance)
	case requirement.Capacity != nil:
		return evaluateCapacity(report, requirement.ID, *requirement.Capacity)
	default:
		return blockedRequirement(requirement.ID, "typed requirement is unavailable", "decision_spec.requirements")
	}
}

func evaluateBehavior(result *record.Record, id string, requirement BehaviorRequirement) RequirementResult {
	verdict, found := result.Scorecard.Needs[requirement.Need]
	if requirement.MinimumRate == nil {
		return evaluateBehaviorState(id, requirement.Need, verdict, found)
	}
	return evaluateBehaviorRate(result, id, requirement, verdict, found)
}

func evaluateBehaviorState(id, need string, verdict score.Verdict, found bool) RequirementResult {
	if !found {
		return unresolvedRequirement(id, "the sealed scorecard has no verdict for this need",
			[]string{"behavior verdict"}, behaviorAction(need))
	}
	switch verdict.State {
	case score.Pass:
		return RequirementResult{ID: id, State: RequirementEstablished,
			Reason: "the sealed behavioral verdict is PASS", EvidenceRefs: []string{"scorecard.needs." + need}}
	case score.Fail:
		return RequirementResult{ID: id, State: RequirementDisproven,
			Reason: "the sealed behavioral verdict is FAIL", EvidenceRefs: []string{"scorecard.needs." + need}}
	case score.Blocked:
		return blockedRequirement(id, verdict.Why, "scorecard.needs."+need)
	default:
		return unresolvedRequirement(id, "the sealed behavioral verdict is "+string(verdict.State),
			[]string{"decision-bearing behavioral evidence"}, behaviorAction(need))
	}
}

func evaluateBehaviorRate(result *record.Record, id string, requirement BehaviorRequirement,
	verdict score.Verdict, found bool) RequirementResult {
	if found && verdict.State == score.Blocked {
		return blockedRequirement(id, verdict.Why, "scorecard.needs."+requirement.Need)
	}
	if requirement.Need == "tool_restraint" && found && verdict.State == score.Fail {
		return RequirementResult{ID: id, State: RequirementDisproven,
			Reason:       "the sealed tool-restraint verdict failed a required protocol condition",
			EvidenceRefs: []string{"scorecard.needs.tool_restraint"}}
	}

	pool, ok := behaviorPool(result.Measured(), requirement.Need)
	if !ok {
		return unresolvedRequirement(id, "this need has no rate estimand in the current evidence contract",
			[]string{"typed rate observations"}, behaviorAction(requirement.Need))
	}
	observed, measured := pool.Rate()
	if !measured {
		return unresolvedRequirement(id, "no scorable observation entered the rate denominator",
			[]string{"scorable observations"}, behaviorAction(requirement.Need))
	}
	interval := pool.Interval()
	independent := pool.IndependentFamilies()
	outcome := RequirementResult{
		ID: id, Observed: float64Pointer(observed), Unit: analysis.UnitFraction,
		IntervalLow: float64Pointer(interval.Lo), IntervalHigh: float64Pointer(interval.Hi),
		IndependentUnits: independent,
		EvidenceRefs:     []string{"sealed_measurements." + requirement.Need, "scoring_policy.rate_interval"},
	}
	planned := pool.Planned
	if planned == 0 {
		planned = pool.N
	}
	return evaluateRateOutcome(outcome, pool, planned, independent, *requirement.MinimumRate, requirement.Need)
}

func evaluateRateOutcome(outcome RequirementResult, pool score.Pool, planned, independent int,
	minimum float64, need string) RequirementResult {
	intervalLow, intervalHigh := *outcome.IntervalLow, *outcome.IntervalHigh
	switch {
	case pool.N < planned:
		outcome.State = RequirementUnresolved
		outcome.Reason = fmt.Sprintf("%d of %d planned observations were scorable", pool.N, planned)
		outcome.Missing = []string{"all planned terminal observations"}
		outcome.NextAction = behaviorAction(need)
	case independent < 2 && planned > 1:
		outcome.State = RequirementUnresolved
		outcome.Reason = "one scenario family establishes only within-family behavior"
		outcome.Missing = []string{"independent scenario families"}
		outcome.NextAction = behaviorAction(need)
	case intervalLow >= minimum:
		outcome.State = RequirementEstablished
		outcome.Reason = fmt.Sprintf("clustered 95%% lower bound %.2f clears %.2f", intervalLow, minimum)
	case intervalHigh < minimum:
		outcome.State = RequirementDisproven
		outcome.Reason = fmt.Sprintf("clustered 95%% upper bound %.2f is below %.2f", intervalHigh, minimum)
	default:
		outcome.State = RequirementUnresolved
		outcome.Reason = fmt.Sprintf("clustered 95%% interval [%.2f, %.2f] crosses %.2f", intervalLow, intervalHigh, minimum)
		outcome.Missing = []string{"enough independent evidence to resolve the declared rate"}
		outcome.NextAction = behaviorAction(need)
	}
	return outcome
}

func behaviorPool(measured score.Measured, need string) (score.Pool, bool) {
	switch need {
	case "structured_output":
		return measured.Structured, true
	case "instruction_precision":
		return measured.Precision, true
	case "reasoning":
		return measured.Reasoning, true
	case "tool_calling":
		return measured.ToolCalling, true
	case "tool_restraint":
		return measured.ToolRestraintPool, true
	case "user_tasks":
		return measured.User, true
	default:
		return score.Pool{}, false
	}
}

func evaluateCapability(result *record.Record, id string, requirement CapabilityRequirement) RequirementResult {
	evidence, found := result.Scorecard.Capabilities[requirement.Name]
	evidenceRef := "scorecard.capabilities." + requirement.Name
	if !found && stringInSlice(result.ModelMeta.Capabilities, requirement.Name) {
		evidence = score.CapabilityEvidence{Support: score.CapabilityDeclared, Source: "runtime model metadata"}
		found = true
		evidenceRef = "model_meta.capabilities"
	}
	if !found {
		return unresolvedRequirement(id, "the capability was not declared or verified",
			[]string{"capability evidence"}, &Action{Code: "probe_capability", Experiment: "capability",
				Reason: "run a protocol-specific capability probe"})
	}
	if capabilityRank(evidence.Support) >= capabilityRank(requirement.MinimumSupport) {
		return RequirementResult{ID: id, State: RequirementEstablished,
			Reason:       fmt.Sprintf("%s support clears required %s support", evidence.Support, requirement.MinimumSupport),
			EvidenceRefs: []string{evidenceRef}}
	}
	return unresolvedRequirement(id,
		fmt.Sprintf("%s support does not establish required %s support", evidence.Support, requirement.MinimumSupport),
		[]string{string(requirement.MinimumSupport) + " capability evidence"},
		&Action{Code: "verify_capability_protocol", Experiment: "capability",
			Reason: "run a protocol-specific capability verification"})
}

func evaluateContext(report analysis.Report, id string, requirement ContextRequirement) RequirementResult {
	if report.Context.Effective == nil {
		return unresolvedRequirement(id, "effective context was not runtime verified",
			[]string{"runtime-verified effective context"}, &Action{Code: "verify_effective_context", Experiment: "context",
				Reason: "run a context probe that returns a positive effective-context receipt"})
	}
	observed := float64(*report.Context.Effective)
	if *report.Context.Effective >= requirement.MinimumEffectiveTokens {
		return RequirementResult{ID: id, State: RequirementEstablished, Observed: &observed,
			Unit: analysis.UnitTokens, Reason: "verified effective context clears the declared minimum",
			EvidenceRefs: []string{"device_fingerprint_v2.context"}}
	}
	return RequirementResult{ID: id, State: RequirementDisproven, Observed: &observed,
		Unit: analysis.UnitTokens, Reason: "verified effective context is below the declared minimum",
		EvidenceRefs: []string{"device_fingerprint_v2.context"}}
}

func evaluatePerformance(report analysis.Report, id string, requirement PerformanceRequirement) RequirementResult {
	observation, claim := performanceObservation(report.Performance, requirement.Metric)
	if observation.Estimate == nil {
		return unresolvedRequirement(id, "the performance observation is unavailable",
			[]string{string(requirement.Metric)}, performanceAction(requirement.Metric))
	}
	if observation.Status == analysis.StatusDescriptiveOnly {
		return blockedRequirement(id, "the performance observation is descriptive only", "analysis.performance."+string(requirement.Metric))
	}
	if !analysis.Supports(observation, claim) {
		return unresolvedRequirement(id, "the observation does not support the requested latency or throughput state",
			[]string{string(claim)}, performanceAction(requirement.Metric))
	}
	if low, high, ok := performanceInterval(observation); ok {
		state, reason := compareInterval(low, high, requirement.AtLeast, requirement.AtMost)
		outcome := RequirementResult{ID: id, State: state, Observed: cloneFloat64(observation.Estimate),
			Unit: observation.Unit, IntervalLow: &low, IntervalHigh: &high, Reason: reason,
			EvidenceRefs: []string{"analysis.performance." + string(requirement.Metric)}}
		if state == RequirementUnresolved {
			outcome.Missing = []string{"enough performance evidence to resolve the declared bound"}
			outcome.NextAction = performanceAction(requirement.Metric)
		}
		return outcome
	}
	state, reason := compareBound(*observation.Estimate, requirement.AtLeast, requirement.AtMost)
	return RequirementResult{ID: id, State: state, Observed: cloneFloat64(observation.Estimate),
		Unit: observation.Unit, Reason: reason,
		EvidenceRefs: []string{"analysis.performance." + string(requirement.Metric)}}
}

func performanceInterval(observation analysis.PerformanceObservation) (float64, float64, bool) {
	if observation.Estimate == nil || observation.SD == nil || observation.SampleCount < 2 {
		return 0, 0, false
	}
	margin := stats.TCrit975(float64(observation.SampleCount-1)) * *observation.SD /
		math.Sqrt(float64(observation.SampleCount))
	return math.Max(0, *observation.Estimate-margin), *observation.Estimate + margin, true
}

func compareInterval(low, high float64, atLeast, atMost *float64) (RequirementState, string) {
	if atLeast != nil {
		switch {
		case low >= *atLeast:
			return RequirementEstablished,
				fmt.Sprintf("95%% lower bound %.4g clears minimum %.4g", low, *atLeast)
		case high < *atLeast:
			return RequirementDisproven,
				fmt.Sprintf("95%% upper bound %.4g is below minimum %.4g", high, *atLeast)
		default:
			return RequirementUnresolved,
				fmt.Sprintf("95%% interval [%.4g, %.4g] crosses minimum %.4g", low, high, *atLeast)
		}
	}
	switch {
	case high <= *atMost:
		return RequirementEstablished,
			fmt.Sprintf("95%% upper bound %.4g is within maximum %.4g", high, *atMost)
	case low > *atMost:
		return RequirementDisproven,
			fmt.Sprintf("95%% lower bound %.4g exceeds maximum %.4g", low, *atMost)
	default:
		return RequirementUnresolved,
			fmt.Sprintf("95%% interval [%.4g, %.4g] crosses maximum %.4g", low, high, *atMost)
	}
}

func performanceObservation(performance analysis.Performance, metric PerformanceMetric) (analysis.PerformanceObservation, analysis.SupportClaim) {
	switch metric {
	case MetricDecodeTPS:
		return performance.DecodeTPS, analysis.ClaimObservedDecode
	case MetricPrefillTPS:
		return performance.PrefillTPS, analysis.ClaimObservedPrefill
	case MetricLoadedTTFT:
		return performance.TTFTSeconds, analysis.ClaimObservedLoadedTTFT
	default:
		return performance.TTFTSeconds, analysis.ClaimObservedRequestTTFT
	}
}

func evaluateCapacity(report analysis.Report, id string, requirement CapacityRequirement) RequirementResult {
	resident := report.Capacity.Resident
	if resident == nil || resident.Estimate == nil {
		return unresolvedRequirement(id, "exact-context resident allocation is unavailable",
			[]string{"runtime allocation receipt"}, capacityAction(requirement.RequestedContext))
	}
	if resident.Status == analysis.StatusDescriptiveOnly {
		return blockedRequirement(id, "resident allocation is descriptive only", "analysis.capacity.resident")
	}
	if !residentSupports(resident, analysis.ClaimExactContextResidentBytes) {
		return unresolvedRequirement(id, "resident allocation does not support an exact-context claim",
			[]string{string(analysis.ClaimExactContextResidentBytes)}, capacityAction(requirement.RequestedContext))
	}
	if requirement.RequestedContext > 0 && resident.RequestedContext != requirement.RequestedContext {
		return unresolvedRequirement(id,
			fmt.Sprintf("resident allocation was measured at %d tokens, not required %d", resident.RequestedContext, requirement.RequestedContext),
			[]string{"exact-context allocation at the required context"}, capacityAction(requirement.RequestedContext))
	}
	observed := float64(*resident.Estimate)
	maximum := maximumResidentBytes(requirement)
	if *resident.Estimate <= maximum {
		return RequirementResult{ID: id, State: RequirementEstablished, Observed: &observed,
			Unit: analysis.UnitBytes, Reason: "exact-context resident allocation is within the declared maximum",
			EvidenceRefs: []string{"analysis.capacity.resident"}}
	}
	return RequirementResult{ID: id, State: RequirementDisproven, Observed: &observed,
		Unit: analysis.UnitBytes, Reason: "exact-context resident allocation exceeds the declared maximum",
		EvidenceRefs: []string{"analysis.capacity.resident"}}
}

func configurationFrom(result *record.Record, report analysis.Report) (ConfigurationUnderTest, error) {
	if result == nil || result.Manifest == nil {
		return ConfigurationUnderTest{}, errors.New("sealed manifest is unavailable")
	}
	identity := result.Manifest.Model
	configuration := ConfigurationUnderTest{
		Schema: ConfigurationSchema, RequestedModel: identity.Requested, ResolvedModel: identity.Resolved,
		ArtifactKind: identity.Kind, ArtifactBinding: identity.Binding,
		Backend: identity.Backend, Runtime: identity.Runtime,
		Quant: result.ModelMeta.Details.QuantizationLevel, Family: result.ModelMeta.Details.Family,
		ParameterSize:    result.ModelMeta.Details.ParameterSize,
		RequestedContext: report.Context.Requested, EffectiveContext: cloneInt(report.Context.Effective),
		ContextState: report.Context.State,
	}
	if identity.ContentAddressed {
		configuration.ArtifactDigest = identity.Value
	}
	if key, err := result.ComparableDeviceKey(); err == nil {
		configuration.ComparabilityKey = key
	}
	if report.Capacity.Placement != nil {
		placement := *report.Capacity.Placement
		placement.EffectiveContext = cloneInt(report.Capacity.Placement.EffectiveContext)
		placement.Supports = append([]analysis.SupportClaim(nil), report.Capacity.Placement.Supports...)
		configuration.Placement = &placement
	}
	digest, err := digest("fitr.configuration.v1", configuration)
	if err != nil {
		return ConfigurationUnderTest{}, err
	}
	configuration.ID = digest
	return configuration, nil
}

func compareBound(observed float64, atLeast, atMost *float64) (RequirementState, string) {
	if atLeast != nil {
		if observed >= *atLeast {
			return RequirementEstablished, fmt.Sprintf("observed %.4g clears minimum %.4g", observed, *atLeast)
		}
		return RequirementDisproven, fmt.Sprintf("observed %.4g is below minimum %.4g", observed, *atLeast)
	}
	if observed <= *atMost {
		return RequirementEstablished, fmt.Sprintf("observed %.4g is within maximum %.4g", observed, *atMost)
	}
	return RequirementDisproven, fmt.Sprintf("observed %.4g exceeds maximum %.4g", observed, *atMost)
}

func aggregateState(results []RequirementResult) EvaluationState {
	state := DecisionEligible
	for _, result := range results {
		switch result.State {
		case RequirementDisproven:
			return DecisionIneligible
		case RequirementUnresolved, RequirementBlocked:
			state = DecisionUnresolved
		}
	}
	return state
}

func selectNextAction(results []RequirementResult) *Action {
	bestPriority := int(^uint(0) >> 1)
	var best *Action
	for _, result := range results {
		if (result.State != RequirementUnresolved && result.State != RequirementBlocked) || result.NextAction == nil {
			continue
		}
		priority := actionPriority(result.NextAction.Code)
		if priority < bestPriority {
			actionCopy := *result.NextAction
			actionCopy.Argv = append([]string(nil), result.NextAction.Argv...)
			best, bestPriority = &actionCopy, priority
		}
	}
	return best
}

func actionPriority(code string) int {
	switch code {
	case "verify_effective_context", "measure_exact_context_capacity":
		return 10
	case "probe_capability", "verify_capability_protocol", "collect_behavior_evidence":
		return 20
	case "measure_performance_state":
		return 30
	case "confirm_candidate":
		return 40
	default:
		return 100
	}
}

func blockedRequirement(id, reason, evidenceRef string) RequirementResult {
	return RequirementResult{ID: id, State: RequirementBlocked, Reason: reason,
		EvidenceRefs: []string{evidenceRef}, Missing: []string{"claimable evidence"}}
}

func unresolvedRequirement(id, reason string, missing []string, action *Action) RequirementResult {
	return RequirementResult{ID: id, State: RequirementUnresolved, Reason: reason,
		Missing: append([]string(nil), missing...), NextAction: action}
}

func behaviorAction(need string) *Action {
	return &Action{Code: "collect_behavior_evidence", Experiment: "behavior",
		Argv:   []string{"fitr", "run", currentModelPlaceholder},
		Reason: "collect planned observations for " + need + " under the same configuration"}
}

func performanceAction(metric PerformanceMetric) *Action {
	return &Action{Code: "measure_performance_state", Experiment: "performance",
		Argv:   []string{"fitr", "run", currentModelPlaceholder, "--level", "standard"},
		Reason: "measure the exact performance state required by " + string(metric)}
}

func capacityAction(context int) *Action {
	action := &Action{Code: "measure_exact_context_capacity", Experiment: "context",
		Argv:   []string{"fitr", "run", currentModelPlaceholder, "--level", "standard"},
		Reason: "measure runtime allocation at the required context"}
	if context > 0 {
		action.Argv = append(action.Argv, "--ctx", strconv.Itoa(context))
	}
	return action
}

func maximumResidentBytes(requirement CapacityRequirement) int64 {
	if requirement.MaximumResidentBytes > 0 {
		return requirement.MaximumResidentBytes
	}
	return int64(requirement.MaximumResidentGB * 1024 * 1024 * 1024)
}

func capabilityRank(support score.CapabilitySupport) int {
	switch support {
	case score.CapabilityProtocolVerified:
		return 2
	case score.CapabilityDeclared:
		return 1
	default:
		return 0
	}
}

func residentSupports(observation *analysis.ResidentObservation, claim analysis.SupportClaim) bool {
	if observation == nil {
		return false
	}
	for _, supported := range observation.Supports {
		if supported == claim {
			return true
		}
	}
	return false
}

func digest(domain string, value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(domain))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(encoded)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func stringInSlice(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneFloat64(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func float64Pointer(value float64) *float64 { return &value }
