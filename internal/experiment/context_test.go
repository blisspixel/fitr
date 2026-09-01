package experiment

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/score"
	"github.com/blisspixel/fitr/internal/stats"
)

func TestContextPlanIsFiniteFreshAndBound(t *testing.T) {
	contexts := []int{4096, 8192, 16384}
	plan, digest, err := NewContextPlan("model", contexts, 3)
	if err != nil {
		t.Fatal(err)
	}
	contexts[0] = 1
	if plan.RequestedContexts[0] != 4096 || !strings.HasPrefix(digest, "sha256:") ||
		!strings.HasPrefix(plan.SeedSet, "context-") || plan.Level != "quick" {
		t.Fatalf("sealed plan = %+v digest=%q", plan, digest)
	}
	derivedDigest, err := contextPlanDigest(plan)
	if err != nil || derivedDigest != digest {
		t.Fatalf("derived digest = %q, %v", derivedDigest, err)
	}
	other, otherDigest, err := NewContextPlan("model", []int{4096, 8192, 16384}, 3)
	if err != nil || other.SeedSet == plan.SeedSet || otherDigest == digest {
		t.Fatalf("fresh plan seed=%q digest=%q error=%v", other.SeedSet, otherDigest, err)
	}
	binding := ContextPlanBinding(digest, 2, 3)
	if binding.Schema != record.ExperimentBindingSchema || binding.Kind != "context" ||
		binding.Stage != string(StageExplore) || binding.PointIndex != 2 || binding.PointCount != 3 {
		t.Fatalf("context binding = %+v", binding)
	}
}

func TestContextPlanRejectsAmbiguousPointSets(t *testing.T) {
	tooManyPoints := make([]int, maximumContextPoints+1)
	for index := range tooManyPoints {
		tooManyPoints[index] = index + 1
	}
	tests := []struct {
		name     string
		model    string
		contexts []int
		samples  int
	}{
		{name: "model", contexts: []int{4096, 8192}, samples: 1},
		{name: "points", model: "model", contexts: []int{4096}, samples: 1},
		{name: "samples", model: "model", contexts: []int{4096, 8192}},
		{name: "samples max", model: "model", contexts: []int{4096, 8192},
			samples: maximumContextPerformanceSamples + 1},
		{name: "positive", model: "model", contexts: []int{0, 8192}, samples: 1},
		{name: "context max", model: "model", contexts: []int{4096, maximumContextTokens + 1}, samples: 1},
		{name: "distinct", model: "model", contexts: []int{4096, 4096}, samples: 1},
		{name: "point max", model: "model", contexts: tooManyPoints, samples: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := NewContextPlan(test.model, test.contexts, test.samples); err == nil {
				t.Fatal("invalid context plan was accepted")
			}
		})
	}
}

func TestContextCoverageRequiresClaimableObservations(t *testing.T) {
	resident := int64(6 << 30)
	decode := 25.0
	points := []ContextPoint{
		{
			State: ContextPointVerified, Context: analysis.Context{Requested: 4096},
			Capacity: analysis.Capacity{Resident: &analysis.ResidentObservation{
				Estimate: &resident, Status: analysis.StatusAvailable,
				Supports: []analysis.SupportClaim{analysis.ClaimExactContextResidentBytes},
			}},
			Performance: analysis.Performance{DecodeTPS: analysis.PerformanceObservation{
				Estimate: &decode, Status: analysis.StatusAvailable,
				Supports: []analysis.SupportClaim{analysis.ClaimObservedDecode},
			}},
		},
		{State: ContextPointUnverified, Context: analysis.Context{Requested: 8192}},
	}
	coverage := contextCoverage(points)
	if len(coverage.VerifiedContexts) != 1 || coverage.VerifiedContexts[0] != 4096 ||
		len(coverage.AllocationContexts) != 1 || len(coverage.PerformanceContexts) != 1 ||
		coverage.MaximumVerifiedContext == nil || *coverage.MaximumVerifiedContext != 4096 {
		t.Fatalf("context coverage = %+v", coverage)
	}
}

func TestContextActionsPreserveUnresolvedMeaning(t *testing.T) {
	effective := 4096
	report := ContextReport{
		Comparison: Comparison{Ready: true},
		Points: []ContextPoint{{
			State:   ContextPointAdjusted,
			Context: analysis.Context{Requested: 8192, Effective: &effective},
		}},
	}
	gaps, action := contextGapsAndAction(report)
	if len(gaps) != 1 || action == nil || action.Code != "repeat_context_point" {
		t.Fatalf("adjusted context result gaps=%v action=%+v", gaps, action)
	}
	report.Comparison = Comparison{Ready: false, Missing: []string{"runtime changed"}}
	_, action = contextGapsAndAction(report)
	if action == nil || action.Code != "align_context_experiment_factors" {
		t.Fatalf("incomparable context action = %+v", action)
	}
}

func TestFactorComparisonDistinguishesEqualDifferentAndMissing(t *testing.T) {
	first := &record.Record{Model: "same", StartedAt: "2026-08-31T10:00:00Z"}
	second := &record.Record{Model: "same", StartedAt: "2026-08-31T11:00:00Z"}
	modelFactor := func(result *record.Record) (any, bool) {
		if result == nil || result.Model == "" {
			return nil, false
		}
		return result.Model, true
	}
	if factor := compareFactor([]*record.Record{first, second}, "model", modelFactor); factor.State != FactorEqual {
		t.Fatalf("equal factor = %+v", factor)
	}
	second.Model = "different"
	if factor := compareFactor([]*record.Record{first, second}, "model", modelFactor); factor.State != FactorDifferent {
		t.Fatalf("different factor = %+v", factor)
	}
	if factor := compareFactor([]*record.Record{first, nil}, "model", modelFactor); factor.State != FactorMissing {
		t.Fatalf("missing factor = %+v", factor)
	}
}

func TestContextExperimentRequiresEqualMeasurementSeedSet(t *testing.T) {
	const artifact = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	first := sealedExperimentRecordWithSeed(t, 4096, nil, "2026-08-31T10:00:00Z",
		artifact, "Q5_K_M", 20, "seed-a")
	second := sealedExperimentRecordWithSeed(t, 8192, nil, "2026-08-31T11:00:00Z",
		artifact, "Q5_K_M", 20, "seed-b")
	report, err := AnalyzeContext([]*record.Record{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if report.Comparison.Ready || report.Comparison.Factors[3].State != FactorDifferent {
		t.Fatalf("changed context seed set remained comparable = %+v", report.Comparison)
	}
}

func TestContextBundleLoadingRejectsUntrustedShapes(t *testing.T) {
	if _, err := (ContextBundle{Schema: "wrong"}).Validate(); err == nil {
		t.Fatal("unsupported bundle schema was accepted")
	}
	directory := t.TempDir()
	for name, data := range map[string]string{
		"duplicate.json": `{"schema":"a","schema":"b"}`,
		"unknown.json":   `{"schema":"fitr.experiment.context.bundle.v1","unknown":true}`,
		"trailing.json":  `{}` + "\n{}",
	} {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadContextBundle(path); err == nil {
			t.Fatalf("untrusted bundle %q was accepted", name)
		}
	}
}

func TestAnalyzePlannedContextRebuildsSealedBundle(t *testing.T) {
	plan, digest, err := NewContextPlan("model", []int{4096, 8192}, 1)
	if err != nil {
		t.Fatal(err)
	}
	firstBinding := ContextPlanBinding(digest, 1, 2)
	secondBinding := ContextPlanBinding(digest, 2, 2)
	first := sealedContextRecord(t, 4096, &firstBinding, "2026-08-31T10:00:00Z", plan.SeedSet)
	second := sealedContextRecord(t, 8192, &secondBinding, "2026-08-31T11:00:00Z", plan.SeedSet)
	report, err := AnalyzePlannedContext([]*record.Record{first, second}, plan, digest)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Predeclared || !report.Comparison.Ready || len(report.Points) != 2 ||
		report.Coverage.MaximumVerifiedContext == nil || *report.Coverage.MaximumVerifiedContext != 8192 {
		t.Fatalf("planned report = %+v", report)
	}
	bundle, err := NewContextBundle(plan, digest, []*record.Record{first, second})
	if err != nil {
		t.Fatal(err)
	}
	data, err := bundle.JSON()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "context-bundle.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadContextBundle(path)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt, err := loaded.Validate(); err != nil || !rebuilt.Predeclared {
		t.Fatalf("rebuilt report = %+v, %v", rebuilt, err)
	}
	loaded.Report.Comparison.Ready = false
	if _, err := loaded.Validate(); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("tampered stored report error = %v", err)
	}
}

func TestAnalyzePlannedContextRejectsPointWithDifferentSeedSet(t *testing.T) {
	plan, digest, err := NewContextPlan("model", []int{4096, 8192}, 1)
	if err != nil {
		t.Fatal(err)
	}
	firstBinding := ContextPlanBinding(digest, 1, 2)
	secondBinding := ContextPlanBinding(digest, 2, 2)
	first := sealedContextRecord(t, 4096, &firstBinding, "2026-08-31T10:00:00Z", plan.SeedSet)
	second := sealedContextRecord(t, 8192, &secondBinding, "2026-08-31T11:00:00Z",
		"context-00000000000000000000000000000000")
	if _, err := AnalyzePlannedContext([]*record.Record{first, second}, plan, digest); err == nil ||
		!strings.Contains(err.Error(), "seed set") {
		t.Fatalf("changed context point seed error = %v", err)
	}
}

func sealedContextRecord(t *testing.T, requested int, binding *record.ExperimentBinding, started, seedSet string) *record.Record {
	t.Helper()
	return sealedExperimentRecordForLevel(t, requested, binding, started,
		"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"Q5_K_M", 20, seedSet, "quick")
}

func sealedExperimentRecord(t *testing.T, requested int, binding *record.ExperimentBinding, started,
	artifactDigest, quant string, decodeTPS float64) *record.Record {
	t.Helper()
	return sealedExperimentRecordWithSeed(t, requested, binding, started, artifactDigest, quant, decodeTPS, "")
}

func sealedExperimentRecordWithSeed(t *testing.T, requested int, binding *record.ExperimentBinding, started,
	artifactDigest, quant string, decodeTPS float64, seedSet string) *record.Record {
	t.Helper()
	return sealedExperimentRecordForLevel(t, requested, binding, started, artifactDigest, quant, decodeTPS,
		seedSet, "full")
}

func sealedExperimentRecordForLevel(t *testing.T, requested int, binding *record.ExperimentBinding, started,
	artifactDigest, quant string, decodeTPS float64, seedSet, level string) *record.Record {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "record", "testdata", "schema5-signed-v0.9.8.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result record.Record
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	profile, identity := result.Completion.Profile, result.Manifest.Model
	identity.Value = artifactDigest
	oldProvenance := *result.Manifest.Provenance
	result.Manifest, result.Completion, result.RunID = nil, nil, ""
	result.SchemaVersion = record.EvidenceSchemaVersion
	result.StartedAt, result.NumCtx, result.Experiment, result.Level = started, requested, binding, level
	if seedSet != "" {
		result.SeedSet = seedSet
	}
	result.ModelMeta.Details.QuantizationLevel = quant
	prepareContextMeasurements(t, &result, requested, decodeTPS)
	provenance, err := record.NewRunProvenance(oldProvenance.TaskSetSHA256, oldProvenance.SpecSHA256,
		profile, record.CurrentScoringPolicy(), record.SoftwareReceipt{
			FitrVersion: oldProvenance.FitrVersion, SoftwareBuildSHA256: oldProvenance.SoftwareBuildSHA256,
			BackendProtocol: oldProvenance.BackendProtocol,
		})
	if err != nil {
		t.Fatal(err)
	}
	result.Scorecard = score.Score(result.Measured(), profile)
	if err := result.AttachManifest(identity, provenance); err != nil {
		t.Fatal(err)
	}
	if err := result.CompleteEvidence(profile); err != nil {
		t.Fatal(err)
	}
	return &result
}

func prepareContextMeasurements(t *testing.T, result *record.Record, requested int, decodeTPS float64) {
	t.Helper()
	effective := requested
	fingerprint, err := device.NewFingerprintV2(result.Device, device.ContextVerification{
		RequestedTokens: requested, EffectiveTokens: &effective,
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
	sample := eval.SpeedResult{
		DecodeTPS: decodeTPS, TTFT: 0.5, PrefillTPS: 200, FirstOutputObserved: true,
		GatedCacheKnown: true, GatedPromptTok: 100, PrefillCacheKnown: true, PromptTok: 100,
		GatedLoadKnown: true, GatedResidencyKnown: true, GatedResident: true,
	}
	result.Speed = []eval.SpeedResult{sample}
	result.DecodeSum, result.TTFTSum = stats.MeanSD([]float64{decodeTPS}), stats.MeanSD([]float64{0.5})
	result.PrefillSum = stats.MeanSD([]float64{200})
	result.TaskPlan.SpeedSamples, result.TaskPlan.Memory = 1, true
	for index := range result.Checks {
		if result.Checks[index].Origin == "" {
			result.Checks[index].Origin = "builtin"
		}
	}
	result.TaskPlan.CheckTrialsLimit = len(result.Checks)
	result.TaskPlan.CheckPlanSHA256, err = record.ObservedCheckPlanSHA256(result.Checks)
	if err != nil {
		t.Fatal(err)
	}
	resident := int64(6 * device.GB)
	result.Memory = eval.MemoryResult{
		Outcome: eval.OutcomePass, ResidentGB: 6, PctOnGPU: 100, RequestedCtx: requested,
		EffectiveCtx: &effective, ResidentBytes: resident, AcceleratorBytes: resident,
	}
}
