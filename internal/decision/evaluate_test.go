package decision

import (
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/score"
)

const decisionTestDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestDecisionSpecValidationKeepsRequirementsTyped(t *testing.T) {
	rate := 0.9
	valid := DecisionSpec{
		Schema: SpecSchema, Name: "coding", Evidence: EvidenceDecide,
		Requirements: []Requirement{{ID: "tools", Behavior: &BehaviorRequirement{
			Need: "tool_calling", MinimumRate: &rate,
		}}},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid decision spec: %v", err)
	}

	invalid := valid
	invalid.Requirements[0].Capability = &CapabilityRequirement{
		Name: "tools", MinimumSupport: score.CapabilityDeclared,
	}
	if err := invalid.Validate(); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("mixed requirement validation = %v", err)
	}
}

func TestEvaluateReappliesRequirementsWithoutRewritingScorecard(t *testing.T) {
	result := sealedDecisionRecord(t)
	if got := result.Scorecard.Needs["structured_output"].State; got != score.Inconclusive {
		t.Fatalf("screening verdict = %s, want original INCONCLUSIVE", got)
	}
	minimum := 0.4
	spec := DecisionSpec{
		Schema: SpecSchema, Name: "structured pipeline", Evidence: EvidenceDecide,
		Requirements: []Requirement{
			{ID: "json", Behavior: &BehaviorRequirement{Need: "structured_output", MinimumRate: &minimum}},
			{ID: "context", Context: &ContextRequirement{MinimumEffectiveTokens: 8192}},
			{ID: "tools-declared", Capability: &CapabilityRequirement{
				Name: "tools", MinimumSupport: score.CapabilityDeclared,
			}},
		},
	}
	evaluation, err := Evaluate(result, spec)
	if err != nil {
		t.Fatal(err)
	}
	if evaluation.State != DecisionEligible || evaluation.Eligibility != DecisionEligible {
		t.Fatalf("decision state = %s, requirements=%+v", evaluation.State, evaluation.Requirements)
	}
	if evaluation.SpecSHA256 == "" || evaluation.Subject.ID == "" || !evaluation.Source.Validated {
		t.Fatalf("decision receipts are incomplete: %+v", evaluation)
	}
	if evaluation.Subject.ArtifactDigest != decisionTestDigest ||
		evaluation.Subject.ArtifactBinding != record.IdentityBindingRuntime {
		t.Fatalf("configuration identity = %+v", evaluation.Subject)
	}
	if got := result.Scorecard.Needs["structured_output"].State; got != score.Inconclusive {
		t.Fatalf("decision evaluation rewrote source verdict to %s", got)
	}
}

func TestBehaviorRateReportsUncertaintyAndUnscorableEvidence(t *testing.T) {
	minimum := 0.9
	result := &record.Record{Scorecard: score.Scorecard{Needs: map[string]score.Verdict{}}}
	result.Checks = []eval.CheckOutcome{
		{Need: "structured_output", Family: "a", Pass: true, Outcome: eval.OutcomePass},
		{Need: "structured_output", Family: "b", Pass: true, Outcome: eval.OutcomePass},
		{Need: "structured_output", Family: "c", Pass: true, Outcome: eval.OutcomePass},
	}
	outcome := evaluateBehavior(result, "json", BehaviorRequirement{
		Need: "structured_output", MinimumRate: &minimum,
	})
	if outcome.State != RequirementUnresolved || outcome.IntervalLow == nil || outcome.IntervalHigh == nil ||
		!strings.Contains(outcome.Reason, "crosses") {
		t.Fatalf("thin evidence outcome = %+v", outcome)
	}

	result.Checks = append(result.Checks,
		eval.CheckOutcome{Need: "structured_output", Family: "d", Outcome: eval.OutcomeSkipped})
	outcome = evaluateBehavior(result, "json", BehaviorRequirement{
		Need: "structured_output", MinimumRate: &minimum,
	})
	if outcome.State != RequirementUnresolved || !strings.Contains(outcome.Reason, "scorable") {
		t.Fatalf("unscorable evidence outcome = %+v", outcome)
	}
}

func TestCapabilityDeclarationDoesNotVerifyProtocol(t *testing.T) {
	result := &record.Record{ModelMeta: ollama.ModelInfo{Capabilities: []string{"tools"}}}
	requirement := CapabilityRequirement{Name: "tools", MinimumSupport: score.CapabilityProtocolVerified}
	outcome := evaluateCapability(result, "tools", requirement)
	if outcome.State != RequirementUnresolved || !strings.Contains(outcome.Reason, "declared") {
		t.Fatalf("declared capability outcome = %+v", outcome)
	}

	result.Scorecard.Capabilities = map[string]score.CapabilityEvidence{
		"tools": {Support: score.CapabilityProtocolVerified, Source: "tool plumbing"},
	}
	outcome = evaluateCapability(result, "tools", requirement)
	if outcome.State != RequirementEstablished {
		t.Fatalf("verified capability outcome = %+v", outcome)
	}
}

func TestPerformanceRequirementNamesTheExactLatencyState(t *testing.T) {
	value := 0.5
	report := analysis.Report{Performance: analysis.Performance{TTFTSeconds: analysis.PerformanceObservation{
		Estimate: &value, Unit: analysis.UnitSeconds, Status: analysis.StatusAvailable,
		Supports: []analysis.SupportClaim{analysis.ClaimObservedRequestTTFT},
	}}}
	maximum := 1.0
	loaded := evaluatePerformance(report, "loaded", PerformanceRequirement{
		Metric: MetricLoadedTTFT, AtMost: &maximum,
	})
	if loaded.State != RequirementUnresolved || !strings.Contains(loaded.Reason, "requested latency") {
		t.Fatalf("loaded TTFT outcome = %+v", loaded)
	}
	request := evaluatePerformance(report, "request", PerformanceRequirement{
		Metric: MetricRequestTTFT, AtMost: &maximum,
	})
	if request.State != RequirementEstablished {
		t.Fatalf("request TTFT outcome = %+v", request)
	}
}

func TestPerformanceRequirementUsesRepeatedMeasurementUncertainty(t *testing.T) {
	mean, deviation, maximum := 0.9, 0.1, 1.0
	report := analysis.Report{Performance: analysis.Performance{TTFTSeconds: analysis.PerformanceObservation{
		Estimate: &mean, SD: &deviation, SampleCount: 3, Unit: analysis.UnitSeconds,
		Status: analysis.StatusAvailable, Supports: []analysis.SupportClaim{analysis.ClaimObservedRequestTTFT},
	}}}
	outcome := evaluatePerformance(report, "request", PerformanceRequirement{
		Metric: MetricRequestTTFT, AtMost: &maximum,
	})
	if outcome.State != RequirementUnresolved || outcome.IntervalLow == nil || outcome.IntervalHigh == nil ||
		!strings.Contains(outcome.Reason, "crosses") || outcome.NextAction == nil {
		t.Fatalf("uncertain repeated performance outcome = %+v", outcome)
	}
}

func TestCapacityRequirementRequiresExactContext(t *testing.T) {
	effective := 16384
	resident := int64(12 * device.GB)
	report := analysis.Report{Capacity: analysis.Capacity{Resident: &analysis.ResidentObservation{
		Estimate: &resident, Unit: analysis.UnitBytes, Status: analysis.StatusAvailable,
		RequestedContext: 16384, EffectiveContext: &effective,
		Supports: []analysis.SupportClaim{analysis.ClaimExactContextResidentBytes},
	}}}
	requirement := CapacityRequirement{MaximumResidentBytes: 16 * device.GB, RequestedContext: 32768}
	outcome := evaluateCapacity(report, "memory", requirement)
	if outcome.State != RequirementUnresolved || !strings.Contains(outcome.Reason, "not required") {
		t.Fatalf("wrong-context capacity outcome = %+v", outcome)
	}
	requirement.RequestedContext = 16384
	outcome = evaluateCapacity(report, "memory", requirement)
	if outcome.State != RequirementEstablished {
		t.Fatalf("exact-context capacity outcome = %+v", outcome)
	}
}

func TestConfirmationCannotReuseOrdinaryRunAsFreshEvidence(t *testing.T) {
	result := sealedDecisionRecord(t)
	spec := DecisionSpec{
		Schema: SpecSchema, Name: "confirm", Evidence: EvidenceConfirm,
		Requirements: []Requirement{{ID: "context", Context: &ContextRequirement{MinimumEffectiveTokens: 4096}}},
	}
	evaluation, err := Evaluate(result, spec)
	if err != nil {
		t.Fatal(err)
	}
	if evaluation.State != DecisionUnresolved || len(evaluation.Gaps) == 0 {
		t.Fatalf("confirmation evaluation = %+v", evaluation)
	}
}

func TestConfirmedEvaluationRequiresSealedExperimentBinding(t *testing.T) {
	result := sealedDecisionRecord(t)
	spec := DecisionSpec{
		Schema: SpecSchema, Name: "confirm", Evidence: EvidenceConfirm,
		Requirements: []Requirement{{ID: "context", Context: &ContextRequirement{MinimumEffectiveTokens: 4096}}},
	}
	if _, err := EvaluateConfirmed(result, spec); err == nil {
		t.Fatal("ordinary run was accepted as fresh confirmation")
	}
	binding := &record.ExperimentBinding{
		Schema: record.ExperimentBindingSchema, Kind: "configuration", Stage: "confirm",
		PlanSHA256: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		PointIndex: 1, PointCount: 2,
	}
	result = sealedDecisionRecordWithExperiment(t, binding, "fresh-confirmation")
	evaluation, err := EvaluateConfirmed(result, spec)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, requirement := range evaluation.Requirements {
		if requirement.ID == "confirmation_lineage" {
			found = true
			if requirement.State != RequirementEstablished {
				t.Fatalf("confirmation lineage = %+v", requirement)
			}
		}
	}
	if !found {
		t.Fatal("confirmed evaluation omitted confirmation lineage")
	}
	if evaluation.NextAction != nil {
		t.Fatalf("confirmed evaluation retained stale confirmation action = %+v", evaluation.NextAction)
	}

	spec.Evidence = EvidenceDecide
	if _, err := EvaluateConfirmed(result, spec); err == nil || !strings.Contains(err.Error(), "evidence_level") {
		t.Fatalf("decide-level spec entered confirmed path: %v", err)
	}
}

func sealedDecisionRecord(t *testing.T) *record.Record {
	t.Helper()
	return sealedDecisionRecordWithExperiment(t, nil, "")
}

func sealedDecisionRecordWithExperiment(t *testing.T, binding *record.ExperimentBinding, seedSet string) *record.Record {
	t.Helper()
	profile := decisionTestProfile()
	checks := decisionTestChecks()
	planDigest, err := record.ObservedCheckPlanSHA256(checks)
	if err != nil {
		t.Fatal(err)
	}
	result := newDecisionTestRecord(checks, planDigest)
	result.Experiment = binding
	if seedSet != "" {
		result.SeedSet = seedSet
	}
	attachDecisionTestDevice(t, result)
	sealDecisionTestRecord(t, result, profile)
	return result
}

func decisionTestProfile() device.Profile {
	return device.Profile{
		Name: "default", Description: "decision test", Match: map[string]string{},
		Gates: map[string]device.Gate{
			"structured_output": {"pass_rate_min": 0.7, "why": "test screening threshold"},
		}, Hints: map[string]any{},
	}
}

func decisionTestChecks() []eval.CheckOutcome {
	return []eval.CheckOutcome{
		{TaskID: "json-a", Family: "a", Need: "structured_output", Origin: "builtin", Seed: 1, Pass: true, Outcome: eval.OutcomePass},
		{TaskID: "json-b", Family: "b", Need: "structured_output", Origin: "builtin", Seed: 2, Pass: true, Outcome: eval.OutcomePass},
		{TaskID: "json-c", Family: "c", Need: "structured_output", Origin: "builtin", Seed: 3, Pass: true, Outcome: eval.OutcomePass},
	}
}

func newDecisionTestRecord(checks []eval.CheckOutcome, planDigest string) *record.Record {
	result := &record.Record{
		SchemaVersion: record.EvidenceSchemaVersion,
		Model:         "model", StartedAt: "2026-08-31T12:00:00Z", Level: "standard",
		ExecutionPolicy: record.ExecutionDisabled, SeedSet: "decision-a", Repeats: 1, NumCtx: 8192,
		Profile: "default", Checks: checks,
		TaskPlan:  record.TaskPlan{CheckTrialsLimit: len(checks), CheckPlanSHA256: planDigest},
		ModelMeta: ollama.ModelInfo{Name: "model", Capabilities: []string{"completion", "tools"}},
		Rep:       score.RepetitionMetrics(""), Density: score.InformationDensity(""),
	}
	result.ModelMeta.Details.QuantizationLevel = "Q5_K_M"
	result.ModelMeta.Details.Family = "qwen"
	result.ModelMeta.Details.ParameterSize = "30B"
	return result
}

func attachDecisionTestDevice(t *testing.T, result *record.Record) {
	t.Helper()
	result.Device = device.Fingerprint{
		Host: "host", OS: "linux", CPU: "cpu", GPU: "gpu", Runtime: "ollama 1.2.3",
		InferenceDevice: "GPU 100%", Config: map[string]string{},
	}
	effective := result.NumCtx
	fingerprint, err := device.NewFingerprintV2(result.Device, device.ContextVerification{
		RequestedTokens: result.NumCtx, EffectiveTokens: &effective,
		EffectiveSource: device.ContextSourceRuntimeReport,
	})
	if err != nil {
		t.Fatal(err)
	}
	result.DeviceV2 = &fingerprint
	result.DeviceKey, err = fingerprint.ComparabilityKey()
	if err != nil {
		t.Fatal(err)
	}
}

func sealDecisionTestRecord(t *testing.T, result *record.Record, profile device.Profile) {
	t.Helper()
	var err error
	result.EvidenceCounts, err = result.DeriveEvidenceCounts()
	if err != nil {
		t.Fatal(err)
	}
	result.Scorecard = score.Score(result.Measured(), profile)
	hashes, err := eval.BuiltinHashes()
	if err != nil {
		t.Fatal(err)
	}
	provenance, err := record.NewRunProvenance(hashes.TaskSetSHA256, hashes.SpecSHA256, profile,
		record.CurrentScoringPolicy(), record.SoftwareReceipt{
			FitrVersion: "decision-test", SoftwareBuildSHA256: decisionTestDigest,
			BackendProtocol: record.BackendProtocolOllama,
		})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := record.NewModelIdentity("model", "model", "ollama", "ollama 1.2.3",
		decisionTestDigest, "", 1234)
	if err != nil {
		t.Fatal(err)
	}
	if err := result.AttachManifest(identity, provenance); err != nil {
		t.Fatal(err)
	}
	if err := result.CompleteEvidence(profile); err != nil {
		t.Fatal(err)
	}
}
