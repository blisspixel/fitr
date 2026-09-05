package experiment

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/calibration"
	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/stats"
)

const QuantReportSchema = "fitr.experiment.quant.analysis.v1"

type MetricEstimate struct {
	Code         string                     `json:"code"`
	Estimate     *float64                   `json:"estimate,omitempty"`
	Low          *float64                   `json:"low,omitempty"`
	High         *float64                   `json:"high,omitempty"`
	Unit         analysis.Unit              `json:"unit,omitempty"`
	Status       analysis.ObservationStatus `json:"status"`
	EvidenceRefs []string                   `json:"evidence_refs,omitempty"`
}

type QuantCandidate struct {
	Source     SourceReference                 `json:"source"`
	Subject    decision.ConfigurationUnderTest `json:"subject"`
	Quant      string                          `json:"quant,omitempty"`
	Evaluation decision.Evaluation             `json:"evaluation"`
	Metrics    map[string]MetricEstimate       `json:"metrics"`
}

type QuantLineage struct {
	Verified       bool   `json:"verified"`
	Method         string `json:"method,omitempty"`
	BaseRevision   string `json:"base_revision,omitempty"`
	EvidenceSHA256 string `json:"evidence_sha256,omitempty"`
	Scope          string `json:"scope"`
	Reason         string `json:"reason"`
}

type Dominance struct {
	Dominant  string   `json:"dominant"`
	Dominated string   `json:"dominated"`
	Metrics   []string `json:"metrics"`
}

type QuantFrontier struct {
	Nondominated []string    `json:"nondominated,omitempty"`
	Dominance    []Dominance `json:"dominance,omitempty"`
}

type QuantObjective struct {
	Metric    string                      `json:"metric"`
	Direction decision.ObjectiveDirection `json:"direction"`
	State     string                      `json:"state"`
	Candidate string                      `json:"candidate,omitempty"`
	Reason    string                      `json:"reason"`
}

type QuantReport struct {
	Schema     string           `json:"schema"`
	Stage      Stage            `json:"stage"`
	SpecName   string           `json:"spec_name"`
	Comparison Comparison       `json:"comparison"`
	Lineage    QuantLineage     `json:"lineage"`
	Candidates []QuantCandidate `json:"candidates"`
	Frontier   QuantFrontier    `json:"frontier"`
	Objective  *QuantObjective  `json:"objective,omitempty"`
	Gaps       []string         `json:"gaps,omitempty"`
	NextAction *Action          `json:"next_action,omitempty"`
}

// AnalyzeQuant compares already measured configurations under one decision
// specification. It may verify same-base lineage, but reports configuration
// outcomes rather than claiming that quantization alone caused a difference.
func AnalyzeQuant(results []*record.Record, spec decision.DecisionSpec,
	conversion *calibration.ConversionManifest) (QuantReport, error) {
	return analyzeQuant(results, spec, conversion, StageExplore)
}

func analyzeConfirmedQuant(results []*record.Record, spec decision.DecisionSpec,
	conversion *calibration.ConversionManifest) (QuantReport, error) {
	return analyzeQuant(results, spec, conversion, StageConfirm)
}

func analyzeQuant(results []*record.Record, spec decision.DecisionSpec,
	conversion *calibration.ConversionManifest, stage Stage) (QuantReport, error) {
	if len(results) < 2 {
		return QuantReport{}, errors.New("quant experiment requires at least two sealed runs")
	}
	if err := spec.Validate(); err != nil {
		return QuantReport{}, fmt.Errorf("quant decision spec: %w", err)
	}
	report := QuantReport{
		Schema: QuantReportSchema, Stage: stage, SpecName: spec.Name,
		Comparison: quantComparison(results),
	}
	for index, result := range results {
		candidate, err := quantCandidate(result, spec, conversion, stage)
		if err != nil {
			return QuantReport{}, fmt.Errorf("quant experiment source %d: %w", index+1, err)
		}
		report.Candidates = append(report.Candidates, candidate)
	}
	report.Lineage = assessQuantLineage(report.Candidates, conversion)
	report.Frontier = quantFrontier(report.Candidates, report.Comparison.Ready)
	report.Objective = evaluateQuantObjective(report.Candidates, report.Comparison, spec.Objective, stage)
	report.Gaps, report.NextAction = quantGapsAndAction(report)
	return report, nil
}

func quantCandidate(result *record.Record, spec decision.DecisionSpec,
	conversion *calibration.ConversionManifest, stage Stage) (QuantCandidate, error) {
	derived, err := analysis.FromRecord(result)
	if err != nil {
		return QuantCandidate{}, err
	}
	var evaluation decision.Evaluation
	if stage == StageConfirm {
		evaluation, err = decision.EvaluateConfirmed(result, spec)
	} else {
		evaluation, err = decision.Evaluate(result, spec)
	}
	if err != nil {
		return QuantCandidate{}, err
	}
	quant := strings.TrimSpace(evaluation.Subject.Quant)
	if quant == "" {
		quant = conversionQuant(conversion, evaluation.Subject.ArtifactDigest)
	}
	return QuantCandidate{
		Source: SourceReference{
			RunID: result.StableRunID(), EvidenceSHA256: derived.Source.EvidenceSHA256, Source: derived.Source,
		},
		Subject: evaluation.Subject, Quant: quant, Evaluation: evaluation,
		Metrics: quantMetrics(result, derived),
	}, nil
}

func quantComparison(results []*record.Record) Comparison {
	factors := []FactorComparison{
		compareFactor(results, "backend_runtime", backendRuntimeFactor),
		compareFactor(results, "device_configuration", deviceFactor),
		compareFactor(results, "context", quantContextFactor),
		compareFactor(results, "measurement_and_grading_protocol", quantProtocolFactor),
	}
	comparison := Comparison{
		Schema: ComparisonSchema, Treatment: "configuration_artifact", Ready: true, Factors: factors,
	}
	for _, factor := range factors {
		if factor.State != FactorEqual {
			comparison.Ready = false
			comparison.Missing = append(comparison.Missing, factor.Code+": "+factor.Reason)
		}
	}
	if distinctArtifacts(results) < 2 {
		comparison.Ready = false
		comparison.Missing = append(comparison.Missing, "at least two distinct runtime-bound artifacts")
	}
	sort.Strings(comparison.Missing)
	return comparison
}

func quantContextFactor(result *record.Record) (any, bool) {
	if result == nil || result.DeviceV2 == nil || result.DeviceV2.Context.EffectiveTokens == nil {
		return nil, false
	}
	return struct {
		Requested int `json:"requested"`
		Effective int `json:"effective"`
	}{result.ContextSize(), *result.DeviceV2.Context.EffectiveTokens}, true
}

func quantProtocolFactor(result *record.Record) (any, bool) {
	protocol, err := decision.ProtocolFromRecord(result)
	if err != nil || result.Manifest.Provenance == nil {
		return nil, false
	}
	provenance := result.Manifest.Provenance
	return struct {
		Level         string          `json:"level"`
		Execution     string          `json:"execution"`
		TaskPlan      record.TaskPlan `json:"task_plan"`
		SeedSet       string          `json:"seedset"`
		Repeats       int             `json:"repeats"`
		TaskSet       string          `json:"task_set"`
		Spec          string          `json:"spec"`
		ScoringPolicy string          `json:"scoring_policy"`
		Profile       string          `json:"profile"`
	}{
		protocol.Level, protocol.ExecutionPolicy, protocol.TaskPlan, protocol.SeedSet, protocol.Repeats,
		provenance.TaskSetSHA256, provenance.SpecSHA256, provenance.ScoringPolicySHA256, provenance.ProfileSHA256,
	}, true
}

func distinctArtifacts(results []*record.Record) int {
	values := make(map[string]bool)
	for _, result := range results {
		if result != nil && result.Manifest != nil {
			if digest := result.Manifest.Model.RuntimeBoundDigest(); digest != "" {
				values[digest] = true
			}
		}
	}
	return len(values)
}

func assessQuantLineage(candidates []QuantCandidate, conversion *calibration.ConversionManifest) QuantLineage {
	lineage := QuantLineage{
		Scope:  "configuration_comparison",
		Reason: "no same-base conversion manifest was supplied; results are configuration-level only",
	}
	if conversion == nil {
		return lineage
	}
	manifest := *conversion
	manifest.Artifacts = append([]calibration.ConversionArtifact(nil), conversion.Artifacts...)
	if err := (&manifest).Validate(); err != nil {
		lineage.Reason = "conversion manifest is invalid: " + err.Error()
		return lineage
	}
	known := make(map[string]bool, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		known[strings.ToLower(strings.TrimSpace(artifact.Digest))] = true
	}
	for _, candidate := range candidates {
		digest := strings.ToLower(strings.TrimSpace(candidate.Subject.ArtifactDigest))
		if !known[digest] {
			lineage.Reason = "conversion manifest does not bind every measured artifact"
			return lineage
		}
	}
	digest, err := factorDigest("quant_conversion_manifest", manifest)
	if err != nil {
		lineage.Reason = "conversion manifest could not be hashed"
		return lineage
	}
	lineage.Verified = true
	lineage.Method = calibration.LineageConversion
	lineage.BaseRevision = manifest.BaseRevision
	lineage.EvidenceSHA256 = digest
	lineage.Reason = "every runtime-bound artifact is named by one validated base-revision conversion manifest"
	return lineage
}

func conversionQuant(conversion *calibration.ConversionManifest, digest string) string {
	if conversion == nil {
		return ""
	}
	for _, artifact := range conversion.Artifacts {
		if strings.EqualFold(artifact.Digest, digest) {
			return strings.TrimSpace(artifact.Quant)
		}
	}
	return ""
}

func quantMetrics(result *record.Record, report analysis.Report) map[string]MetricEstimate {
	metrics := map[string]MetricEstimate{
		"decode_tps":           metricFromPerformance("decode_tps", report.Performance.DecodeTPS),
		"prefill_tps":          metricFromPerformance("prefill_tps", report.Performance.PrefillTPS),
		"request_ttft_seconds": metricFromPerformance("request_ttft_seconds", report.Performance.TTFTSeconds),
	}
	if analysis.Supports(report.Performance.TTFTSeconds, analysis.ClaimObservedLoadedTTFT) {
		metrics["loaded_ttft_seconds"] = metricFromPerformance("loaded_ttft_seconds", report.Performance.TTFTSeconds)
	}
	if report.Capacity.Resident != nil && report.Capacity.Resident.Estimate != nil {
		metrics["resident_bytes"] = metricFromResident(report.Capacity.Resident)
	}
	if result.Manifest != nil && result.Manifest.Model.SizeBytes > 0 {
		metrics["artifact_bytes"] = exactMetric("artifact_bytes", float64(result.Manifest.Model.SizeBytes),
			analysis.UnitBytes, "manifest.model.size_bytes")
	}
	return metrics
}

func metricFromPerformance(code string, observation analysis.PerformanceObservation) MetricEstimate {
	metric := MetricEstimate{
		Code: code, Estimate: cloneMetricFloat(observation.Estimate), Unit: observation.Unit,
		Status: observation.Status, EvidenceRefs: []string{"analysis.performance." + code},
	}
	if observation.Estimate == nil || observation.Status != analysis.StatusAvailable {
		return metric
	}
	if observation.SD == nil || observation.SampleCount < 2 {
		return metric
	}
	margin := stats.TCrit975(float64(observation.SampleCount-1)) * *observation.SD /
		math.Sqrt(float64(observation.SampleCount))
	low, high := math.Max(0, *observation.Estimate-margin), *observation.Estimate+margin
	metric.Low, metric.High = &low, &high
	return metric
}

func exactMetric(code string, value float64, unit analysis.Unit, evidenceRef string) MetricEstimate {
	low, high, estimate := value, value, value
	return MetricEstimate{Code: code, Estimate: &estimate, Low: &low, High: &high,
		Unit: unit, Status: analysis.StatusAvailable, EvidenceRefs: []string{evidenceRef}}
}

func metricFromResident(observation *analysis.ResidentObservation) MetricEstimate {
	value := float64(*observation.Estimate)
	metric := MetricEstimate{
		Code: "resident_bytes", Estimate: &value, Unit: analysis.UnitBytes,
		Status: observation.Status, EvidenceRefs: []string{"analysis.capacity.resident"},
	}
	if !residentClaimable(observation) {
		if metric.Status == analysis.StatusAvailable {
			metric.Status = analysis.StatusDescriptiveOnly
		}
		return metric
	}
	low, high := value, value
	metric.Low, metric.High = &low, &high
	return metric
}

func quantFrontier(candidates []QuantCandidate, comparisonReady bool) QuantFrontier {
	frontier := QuantFrontier{}
	if !comparisonReady {
		return frontier
	}
	for _, candidate := range candidates {
		if candidate.Evaluation.Eligibility == decision.DecisionEligible {
			frontier.Nondominated = append(frontier.Nondominated, candidate.Subject.ID)
		}
	}
	for _, dominant := range candidates {
		for _, dominated := range candidates {
			if dominant.Subject.ID == dominated.Subject.ID ||
				dominant.Evaluation.Eligibility != decision.DecisionEligible ||
				dominated.Evaluation.Eligibility != decision.DecisionEligible {
				continue
			}
			metrics, ok := establishedDominance(dominant, dominated)
			if !ok {
				continue
			}
			frontier.Dominance = append(frontier.Dominance, Dominance{
				Dominant: dominant.Subject.ID, Dominated: dominated.Subject.ID, Metrics: metrics,
			})
			frontier.Nondominated = removeString(frontier.Nondominated, dominated.Subject.ID)
		}
	}
	sort.Strings(frontier.Nondominated)
	return frontier
}

func establishedDominance(a, b QuantCandidate) ([]string, bool) {
	dimensions := []struct {
		code      string
		direction decision.ObjectiveDirection
	}{
		{"decode_tps", decision.Maximize},
		{"request_ttft_seconds", decision.Minimize},
		{"resident_bytes", decision.Minimize},
	}
	strict := false
	used := make([]string, 0, len(dimensions))
	for _, dimension := range dimensions {
		left, leftOK := boundedMetric(a.Metrics[dimension.code])
		right, rightOK := boundedMetric(b.Metrics[dimension.code])
		if !leftOK || !rightOK || !establishedNoWorse(left, right, dimension.direction) {
			return nil, false
		}
		strict = strict || establishedBetter(left, right, dimension.direction)
		used = append(used, dimension.code)
	}
	return used, strict
}

type metricBounds struct{ low, high float64 }

func boundedMetric(metric MetricEstimate) (metricBounds, bool) {
	if metric.Estimate == nil || metric.Status != analysis.StatusAvailable || metric.Low == nil || metric.High == nil {
		return metricBounds{}, false
	}
	return metricBounds{low: *metric.Low, high: *metric.High}, true
}

func establishedNoWorse(a, b metricBounds, direction decision.ObjectiveDirection) bool {
	if direction == decision.Maximize {
		return a.low >= b.high
	}
	return a.high <= b.low
}

func establishedBetter(a, b metricBounds, direction decision.ObjectiveDirection) bool {
	if direction == decision.Maximize {
		return a.low > b.high
	}
	return a.high < b.low
}

func evaluateQuantObjective(candidates []QuantCandidate, comparison Comparison,
	objective *decision.Objective, stage Stage) *QuantObjective {
	if objective == nil {
		return nil
	}
	result := &QuantObjective{Metric: objective.Metric, Direction: objective.Direction, State: "unresolved"}
	if !comparison.Ready {
		result.Reason = "required-equal comparison factors are unresolved"
		return result
	}
	for _, candidate := range candidates {
		if candidate.Evaluation.Eligibility == decision.DecisionUnresolved {
			result.Reason = "one or more declared candidate eligibility decisions remain unresolved"
			return result
		}
	}
	eligible := eligibleCandidates(candidates)
	if len(eligible) == 0 {
		result.State = "no_eligible_candidate"
		result.Reason = "every declared candidate fails at least one required constraint"
		return result
	}
	if len(eligible) == 1 {
		return resolvedQuantObjective(result, eligible[0].Subject.ID, stage,
			"the candidate is the only declared configuration that clears every requirement")
	}
	eligibleWithBounds := eligibleWithMetric(eligible, objective.Metric)
	if len(eligibleWithBounds) != len(eligible) {
		result.Reason = "one or more eligible candidates lack a bounded objective metric"
		return result
	}
	winner := objectiveWinner(eligibleWithBounds, objective.Metric, objective.Direction)
	if winner == "" {
		result.State = "ambiguous"
		result.Reason = "95% metric intervals do not establish one candidate as better than every other eligible candidate"
		return result
	}
	return resolvedQuantObjective(result, winner, stage,
		"the candidate clears every requirement and its 95% metric interval is better than every eligible alternative")
}

func resolvedQuantObjective(result *QuantObjective, candidate string, stage Stage, reason string) *QuantObjective {
	result.State, result.Candidate, result.Reason = "exploratory", candidate, reason
	if stage == StageConfirm {
		result.State = "confirmed"
		result.Reason = "fresh predeclared evidence confirms that " + reason
	}
	return result
}

func eligibleCandidates(candidates []QuantCandidate) []QuantCandidate {
	eligible := make([]QuantCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Evaluation.Eligibility == decision.DecisionEligible {
			eligible = append(eligible, candidate)
		}
	}
	return eligible
}

func eligibleWithMetric(candidates []QuantCandidate, metric string) []QuantCandidate {
	eligible := make([]QuantCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Evaluation.Eligibility != decision.DecisionEligible {
			continue
		}
		if _, ok := boundedMetric(candidate.Metrics[metric]); ok {
			eligible = append(eligible, candidate)
		}
	}
	return eligible
}

func objectiveWinner(candidates []QuantCandidate, metric string, direction decision.ObjectiveDirection) string {
	for _, candidate := range candidates {
		candidateBounds, _ := boundedMetric(candidate.Metrics[metric])
		winner := true
		for _, other := range candidates {
			if candidate.Subject.ID == other.Subject.ID {
				continue
			}
			otherBounds, _ := boundedMetric(other.Metrics[metric])
			if !establishedBetter(candidateBounds, otherBounds, direction) {
				winner = false
				break
			}
		}
		if winner {
			return candidate.Subject.ID
		}
	}
	return ""
}

func quantGapsAndAction(report QuantReport) ([]string, *Action) {
	var gaps []string
	if !report.Comparison.Ready {
		gaps = append(gaps, report.Comparison.Missing...)
	}
	if !report.Lineage.Verified {
		gaps = append(gaps, "quant attribution: "+report.Lineage.Reason)
	}
	for _, candidate := range report.Candidates {
		if candidate.Evaluation.Eligibility != decision.DecisionEligible {
			gaps = append(gaps, candidate.Subject.ID+": "+string(candidate.Evaluation.Eligibility))
		}
	}
	gaps = uniqueSorted(gaps)
	switch {
	case !report.Comparison.Ready:
		return gaps, &Action{Code: "align_configuration_experiment_factors",
			Reason: "repeat candidates under the same runtime, device, context, task plan, grading policy, and seed set"}
	case report.Objective == nil:
		return gaps, &Action{Code: "declare_configuration_objective",
			Reason: "add one narrow objective to the decision specification"}
	case report.Objective.State == "exploratory":
		return gaps, &Action{Code: "confirm_candidate", Argv: confirmationArgv(report.Candidates),
			Reason: "collect fresh evidence after sealing the exploratory candidate"}
	case report.Objective.State == "confirmed":
		return gaps, nil
	case report.Objective.State == "no_eligible_candidate":
		return gaps, &Action{Code: "expand_configuration_candidates",
			Reason: "add a configuration that can satisfy the disproven requirements"}
	default:
		return gaps, &Action{Code: "resolve_configuration_objective",
			Reason: "collect more comparable evidence for the ambiguous or unavailable objective"}
	}
}

func confirmationArgv(candidates []QuantCandidate) []string {
	argv := []string{"fitr", "experiment", "confirm"}
	seen := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		model := strings.TrimSpace(candidate.Subject.ResolvedModel)
		if model != "" && !seen[model] {
			argv = append(argv, model)
			seen[model] = true
		}
	}
	return append(argv, "--spec", "<confirm-decision.json>")
}

func removeString(values []string, target string) []string {
	result := values[:0]
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}
	return result
}

func cloneMetricFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
