package experiment

import (
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/calibration"
	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/record"
)

const (
	quantDigestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	quantDigestB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestQuantExperimentKeepsChoiceSeparateFromCausalAttribution(t *testing.T) {
	first := sealedExperimentRecord(t, 8192, nil, "2026-08-31T10:00:00Z", quantDigestA, "Q8_0", 18)
	second := sealedExperimentRecord(t, 8192, nil, "2026-08-31T11:00:00Z", quantDigestB, "Q4_K_M", 24)
	spec := quantDecisionSpec()
	conversion := calibration.ConversionManifest{
		Schema: calibration.ConversionSchema, BaseRevision: quantDigestA,
		Artifacts: []calibration.ConversionArtifact{
			{Digest: quantDigestA, Role: "base", Quant: "Q8_0"},
			{Digest: quantDigestB, Role: "derived", Quant: "Q4_K_M"},
		},
	}
	report, err := AnalyzeQuant([]*record.Record{first, second}, spec, &conversion)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Comparison.Ready || !report.Lineage.Verified || report.Lineage.Scope != "configuration_comparison" {
		t.Fatalf("quant evidence boundaries = %+v", report)
	}
	if len(report.Candidates) != 2 || len(report.Frontier.Nondominated) != 2 {
		t.Fatalf("quant frontier = %+v", report.Frontier)
	}
	if report.Objective == nil || report.Objective.State != "unresolved" ||
		!strings.Contains(report.Objective.Reason, "metric") {
		t.Fatalf("single-sample objective = %+v", report.Objective)
	}
}

func TestQuantExperimentAllowsConfigurationChoiceWithoutInventingLineage(t *testing.T) {
	first := sealedExperimentRecord(t, 8192, nil, "2026-08-31T10:00:00Z", quantDigestA, "Q8_0", 18)
	second := sealedExperimentRecord(t, 8192, nil, "2026-08-31T11:00:00Z", quantDigestB, "Q4_K_M", 24)
	report, err := AnalyzeQuant([]*record.Record{first, second}, quantDecisionSpec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Comparison.Ready || report.Lineage.Verified || len(report.Gaps) == 0 ||
		!strings.Contains(strings.Join(report.Gaps, " "), "configuration-level") {
		t.Fatalf("unverified lineage report = %+v", report)
	}
	if argv := strings.Join(confirmationArgv(report.Candidates), " "); !strings.Contains(argv, "fitr experiment confirm") || !strings.Contains(argv, "--spec") {
		t.Fatalf("confirmation argv = %q", argv)
	}
}

func TestQuantExperimentRejectsChangedRequiredEqualFactors(t *testing.T) {
	first := sealedExperimentRecord(t, 4096, nil, "2026-08-31T10:00:00Z", quantDigestA, "Q8_0", 18)
	second := sealedExperimentRecord(t, 8192, nil, "2026-08-31T11:00:00Z", quantDigestB, "Q4_K_M", 24)
	report, err := AnalyzeQuant([]*record.Record{first, second}, quantDecisionSpec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Comparison.Ready || report.NextAction == nil ||
		report.NextAction.Code != "align_configuration_experiment_factors" {
		t.Fatalf("changed context comparison = %+v", report)
	}
}

func TestQuantDominanceRequiresSeparatedBoundsOnEveryDimension(t *testing.T) {
	better := frontierCandidate("better", 30, 0.4, 4)
	worse := frontierCandidate("worse", 20, 0.8, 8)
	metrics, ok := establishedDominance(better, worse)
	if !ok || len(metrics) != 3 {
		t.Fatalf("established dominance = %v, %v", metrics, ok)
	}
	frontier := quantFrontier([]QuantCandidate{better, worse}, true)
	if len(frontier.Nondominated) != 1 || frontier.Nondominated[0] != "better" || len(frontier.Dominance) != 1 {
		t.Fatalf("frontier = %+v", frontier)
	}
	if winner := objectiveWinner([]QuantCandidate{better, worse}, "decode_tps", decision.Maximize); winner != "better" {
		t.Fatalf("objective winner = %q", winner)
	}
	confirmed := evaluateQuantObjective([]QuantCandidate{better, worse}, Comparison{Ready: true},
		&decision.Objective{Metric: "decode_tps", Direction: decision.Maximize}, StageConfirm)
	if confirmed.State != "confirmed" || confirmed.Candidate != "better" {
		t.Fatalf("confirmed objective = %+v", confirmed)
	}
	better.Metrics["request_ttft_seconds"] = boundedTestMetric("request_ttft_seconds", 0.9, 0.3, 1.1, analysis.UnitSeconds)
	if _, ok := establishedDominance(better, worse); ok {
		t.Fatal("overlapping latency intervals established dominance")
	}
}

func TestQuantObjectiveWaitsForEveryDeclaredCandidateEligibility(t *testing.T) {
	better := frontierCandidate("better", 30, 0.4, 4)
	worse := frontierCandidate("worse", 20, 0.8, 8)
	unresolved := frontierCandidate("unresolved", 10, 1.2, 12)
	unresolved.Evaluation.Eligibility = decision.DecisionUnresolved

	objective := evaluateQuantObjective([]QuantCandidate{better, worse, unresolved}, Comparison{Ready: true},
		&decision.Objective{Metric: "decode_tps", Direction: decision.Maximize}, StageConfirm)
	if objective.State != "unresolved" || !strings.Contains(objective.Reason, "candidate eligibility") {
		t.Fatalf("objective ignored unresolved declared candidate = %+v", objective)
	}
}

func TestQuantObjectiveDoesNotIgnoreEligibleCandidateWithoutMetric(t *testing.T) {
	better := frontierCandidate("better", 30, 0.4, 4)
	worse := frontierCandidate("worse", 20, 0.8, 8)
	missing := frontierCandidate("missing", 10, 1.2, 12)
	delete(missing.Metrics, "decode_tps")

	objective := evaluateQuantObjective([]QuantCandidate{better, worse, missing}, Comparison{Ready: true},
		&decision.Objective{Metric: "decode_tps", Direction: decision.Maximize}, StageConfirm)
	if objective.State != "unresolved" || !strings.Contains(objective.Reason, "lack a bounded") {
		t.Fatalf("objective ignored eligible candidate without metric = %+v", objective)
	}
}

func TestQuantObjectiveSelectsOnlyEligibleCandidate(t *testing.T) {
	eligible := frontierCandidate("eligible", 20, 0.8, 8)
	ineligible := frontierCandidate("ineligible", 30, 0.4, 4)
	ineligible.Evaluation.Eligibility = decision.DecisionIneligible

	objective := evaluateQuantObjective([]QuantCandidate{eligible, ineligible}, Comparison{Ready: true},
		&decision.Objective{Metric: "decode_tps", Direction: decision.Maximize}, StageConfirm)
	if objective.State != "confirmed" || objective.Candidate != "eligible" ||
		!strings.Contains(objective.Reason, "only declared configuration") {
		t.Fatalf("sole eligible candidate objective = %+v", objective)
	}
}

func TestQuantFrontierWithholdsDominanceWhenFactorsAreIncomparable(t *testing.T) {
	better := frontierCandidate("better", 30, 0.4, 4)
	worse := frontierCandidate("worse", 20, 0.8, 8)
	frontier := quantFrontier([]QuantCandidate{better, worse}, false)
	if len(frontier.Nondominated) != 0 || len(frontier.Dominance) != 0 {
		t.Fatalf("incomparable frontier made cross-configuration claims = %+v", frontier)
	}
}

func TestQuantMetricsDoNotUpgradeDescriptiveResidentEvidence(t *testing.T) {
	resident := int64(6 * 1024 * 1024 * 1024)
	report := analysis.Report{Capacity: analysis.Capacity{Resident: &analysis.ResidentObservation{
		Estimate: &resident, Unit: analysis.UnitBytes, Status: analysis.StatusDescriptiveOnly,
		Supports: []analysis.SupportClaim{analysis.ClaimExactContextResidentBytes},
	}}}
	metric := quantMetrics(&record.Record{}, report)["resident_bytes"]
	if metric.Status != analysis.StatusDescriptiveOnly || metric.Estimate == nil || metric.Low != nil || metric.High != nil {
		t.Fatalf("descriptive resident metric was upgraded = %+v", metric)
	}
}

func TestQuantExperimentRejectsUnknownObjectiveMetric(t *testing.T) {
	first := sealedExperimentRecord(t, 8192, nil, "2026-08-31T10:00:00Z", quantDigestA, "Q8_0", 18)
	second := sealedExperimentRecord(t, 8192, nil, "2026-08-31T11:00:00Z", quantDigestB, "Q4_K_M", 24)
	spec := quantDecisionSpec()
	spec.Objective.Metric = "deocde_tps"
	if _, err := AnalyzeQuant([]*record.Record{first, second}, spec, nil); err == nil ||
		!strings.Contains(err.Error(), "unsupported decision objective") {
		t.Fatalf("unknown objective metric error = %v", err)
	}
}

func quantDecisionSpec() decision.DecisionSpec {
	return decision.DecisionSpec{
		Schema: decision.SpecSchema, Name: "quant test", Evidence: decision.EvidenceDecide,
		Requirements: []decision.Requirement{{
			ID: "context", Context: &decision.ContextRequirement{MinimumEffectiveTokens: 4096},
		}},
		Objective: &decision.Objective{Metric: "decode_tps", Direction: decision.Maximize},
	}
}

func frontierCandidate(id string, decode, ttft, resident float64) QuantCandidate {
	return QuantCandidate{
		Subject:    decision.ConfigurationUnderTest{ID: id},
		Evaluation: decision.Evaluation{Eligibility: decision.DecisionEligible},
		Metrics: map[string]MetricEstimate{
			"decode_tps":           boundedTestMetric("decode_tps", decode, decode-1, decode+1, analysis.UnitTokensPerSecond),
			"request_ttft_seconds": boundedTestMetric("request_ttft_seconds", ttft, ttft-0.05, ttft+0.05, analysis.UnitSeconds),
			"resident_bytes":       boundedTestMetric("resident_bytes", resident, resident, resident, analysis.UnitBytes),
		},
	}
}

func boundedTestMetric(code string, estimate, low, high float64, unit analysis.Unit) MetricEstimate {
	return MetricEstimate{
		Code: code, Estimate: &estimate, Low: &low, High: &high,
		Unit: unit, Status: analysis.StatusAvailable,
	}
}
