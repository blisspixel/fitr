package mcp

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/capacity"
	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/role"
	"github.com/blisspixel/fitr/internal/score"
	"github.com/blisspixel/fitr/internal/stats"
)

type selectionFixture struct {
	source  *localEvidence
	roles   role.Store
	records record.Store
	library role.Library
	life    role.Lifecycle
	points  []*record.Record
	now     time.Time
	storeID string
}

func selectionDigest(value string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(value)))
}

func selectionSpec() role.Spec {
	rate, speed := 0.5, 1.0
	return role.Spec{Schema: role.SpecSchema, Name: "structured-work", Description: "private role description", MaxAgeDays: 30,
		Decision: decision.DecisionSpec{Schema: decision.SpecSchema, Name: "private decision title", Evidence: decision.EvidenceDecide,
			Requirements: []decision.Requirement{
				{ID: "quality", Behavior: &decision.BehaviorRequirement{Need: "structured_output", MinimumRate: &rate}},
				{ID: "context", Context: &decision.ContextRequirement{MinimumEffectiveTokens: 4096}},
				{ID: "memory", Capacity: &decision.CapacityRequirement{MaximumResidentBytes: 8 * device.GB}},
				{ID: "speed", Performance: &decision.PerformanceRequirement{Metric: decision.MetricDecodeTPS, AtLeast: &speed}},
			}}, Preferences: []role.Preference{{Requirement: "speed", Weight: 1, Worst: 0, Best: 100}}}
}

func selectionClone[T any](t *testing.T, value T) T {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result T
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func selectionReseal(t *testing.T, result *record.Record) *record.Record {
	t.Helper()
	profile, identity, provenance := result.Completion.Profile, result.Manifest.Model, *result.Manifest.Provenance
	result.Manifest, result.Completion, result.RunID = nil, nil, ""
	var err error
	result.EvidenceCounts, err = result.DeriveEvidenceCounts()
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
	if issue := result.EvidenceIntegrityIssue(); issue != "" {
		t.Fatal(issue)
	}
	return result
}

func selectionRecord(t *testing.T, model string, speed float64, started time.Time, owned bool) *record.Record {
	t.Helper()
	result := currentMCPFixture(t)
	result.Model, result.ModelMeta = model, ollama.ModelInfo{Name: model}
	result.StartedAt, result.Level, result.SeedSet = started.Format(time.RFC3339), "full", "private-exploration"
	result.NumCtx, result.Device.Runtime = 8192, "runtime-1"
	identity := &result.Manifest.Model
	identity.Requested, identity.Resolved, identity.Runtime, identity.Value = model, model, "runtime-1", selectionDigest(model)
	result.Checks = nil
	for round := range result.Repeats {
		for index := range 8 {
			id := fmt.Sprintf("check-%d", index)
			result.Checks = append(result.Checks, eval.CheckOutcome{TaskID: id, Family: fmt.Sprintf("family-%d", index),
				Need: "structured_output", Origin: "builtin", Seed: eval.InstanceSeed(result.SeedSet, id, round), Pass: true, Outcome: eval.OutcomePass})
		}
	}
	result.TaskPlan.CheckTrialsLimit = len(result.Checks)
	var err error
	result.TaskPlan.CheckPlanSHA256, err = record.ObservedCheckPlanSHA256(result.Checks)
	if err != nil {
		t.Fatal(err)
	}
	selectionMeasurements(t, result, speed)
	if owned {
		result.RuntimeBinding = &record.RuntimeBinding{Schema: record.RuntimeBindingSchema, Kind: "owned_ollama",
			ProfileSHA256: selectionDigest("profile"), ExecutableSHA256: selectionDigest("executable"),
			LaunchConfigurationSHA256: selectionDigest("launch"), ModelConfigurationSHA256: selectionDigest("config-" + model),
			ArtifactDigest: identity.Value, RuntimeVersion: identity.Runtime, OwnershipSHA256: selectionDigest("exploration ownership")}
	}
	return selectionReseal(t, result)
}

func selectionMeasurements(t *testing.T, result *record.Record, speed float64) {
	t.Helper()
	contextSize := 8192
	fingerprint, err := device.NewFingerprintV2(result.Device, device.ContextVerification{
		RequestedTokens: contextSize, EffectiveTokens: &contextSize, EffectiveSource: device.ContextSourceRuntimeReport})
	if err != nil {
		t.Fatal(err)
	}
	result.DeviceV2 = &fingerprint
	result.DeviceKey, err = fingerprint.ComparabilityKey()
	if err != nil {
		t.Fatal(err)
	}
	sample := eval.SpeedResult{DecodeTPS: speed, TTFT: 0.5, PrefillTPS: 200, FirstOutputObserved: true,
		GatedCacheKnown: true, GatedPromptTok: 100, PrefillCacheKnown: true, PromptTok: 100,
		GatedLoadKnown: true, GatedResidencyKnown: true, GatedResident: true}
	result.Speed = []eval.SpeedResult{sample, sample, sample}
	result.DecodeSum = stats.MeanSD([]float64{speed, speed, speed})
	result.TTFTSum, result.PrefillSum = stats.MeanSD([]float64{0.5, 0.5, 0.5}), stats.MeanSD([]float64{200, 200, 200})
	result.TaskPlan.SpeedSamples, result.TaskPlan.Memory = 3, true
	result.Memory = eval.MemoryResult{Outcome: eval.OutcomePass, ResidentGB: 6, PctOnGPU: 100,
		RequestedCtx: contextSize, EffectiveCtx: &contextSize, ResidentBytes: 6 * device.GB, AcceleratorBytes: 6 * device.GB}
}

func selectionCapacity(t *testing.T, point *record.Record, control role.ConfirmationCapacity) *capacity.Plan {
	t.Helper()
	policy, err := capacity.BuildPolicy(capacity.PolicyInput{ResourceDomain: capacity.DomainAccelerator,
		OperatorBudgetBytes: control.OperatorBudgetBytes, OperatorReserveBytes: control.OperatorReserveBytes})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := policy.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return &capacity.Plan{Schema: capacity.PlanSchema, Policy: policy, Prediction: capacity.Prediction{
		Schema: capacity.PredictionSchema, CreatedAt: point.StartedAt, ArtifactSHA256: point.Manifest.Model.Value,
		ResourceDomain: capacity.DomainAccelerator, RequestedContext: point.ContextSize(), PlacementAssumption: "runtime placement",
		State: capacity.PredictionUnavailable, Missing: []string{"architecture"}, Excluded: []string{"runtime buffers"}, PolicySHA256: digest}}
}

// Exercise the same signed records, runtime bindings, closed managed references
// and lifecycle APIs used by auto. No backend or model is started by this fixture.
func newSelectionFixture(t *testing.T, root string, managed bool) selectionFixture {
	t.Helper()
	created := time.Now().UTC().Truncate(time.Second).Add(-5 * time.Minute)
	return newSelectionFixtureAt(t, root, managed, created, "private-confirmation")
}

func newSelectionFixtureAt(t *testing.T, root string, managed bool, created time.Time, storeID string) selectionFixture {
	t.Helper()
	f := selectionFixture{roles: role.Store{Dir: filepath.Join(root, ".roles")}, records: record.Store{Dir: root}, now: created.Add(3 * time.Minute), storeID: storeID}
	sources := []*record.Record{selectionRecord(t, "private-fast-model", 80, created.Add(-time.Hour), managed),
		selectionRecord(t, "private-slow-model", 20, created.Add(-time.Hour), managed)}
	plan, err := role.NewConfirmationPlan(selectionSpec(), sources, sources[0].Completion.EvidenceSHA256, created)
	if err != nil {
		t.Fatal(err)
	}
	for index, source := range sources {
		point := selectionClone(t, source)
		point.StartedAt, point.SeedSet = created.Add(time.Minute).Format(time.RFC3339), plan.SeedSet
		binding := role.ConfirmationPlanBinding(plan, index+1)
		point.Experiment, point.TaskPlan = &binding, plan.Protocol.TaskPlan
		for trial, check := range plan.Protocol.Checks {
			point.Checks[trial].Seed = check.Seed
		}
		point.CapacityPlan = selectionCapacity(t, point, plan.Candidates[index].Capacity)
		if point.RuntimeBinding != nil {
			point.RuntimeBinding.OwnershipSHA256 = selectionDigest("fresh ownership")
		}
		f.points = append(f.points, selectionReseal(t, point))
	}
	f.library, err = f.roles.Define(plan.Spec)
	if err != nil {
		t.Fatal(err)
	}
	f.life, err = f.roles.LoadLifecycle(plan.Spec.Name)
	if err != nil {
		t.Fatal(err)
	}
	f.life, err = f.roles.IssueConfirmation(plan, f.life.Digest, created)
	if err != nil {
		t.Fatal(err)
	}
	f.life, err = f.roles.BeginConfirmation(plan.Spec.Name, plan.PlanSHA256, f.life.Digest, created.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	f.complete(t, plan, managed)
	f.source, err = newLocalEvidence(root)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *selectionFixture) complete(t *testing.T, plan role.ConfirmationPlan, managed bool) {
	t.Helper()
	bundle, err := role.NewConfirmationBundle(plan, f.points, f.now.Add(-time.Minute))
	if err != nil || bundle.Report.State != "confirmed" {
		t.Fatal("fixture must confirm", bundle.Report, err)
	}
	if managed {
		f.completeManaged(t, plan, bundle)
		return
	}
	for _, point := range f.points {
		if _, err := f.records.Save(point); err != nil {
			t.Fatal(err)
		}
	}
	f.life, err = f.roles.FinishConfirmation(plan.Spec.Name, plan.PlanSHA256, "completed", &bundle, f.records, f.life.Digest, f.now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	f.life, err = f.roles.AdoptConfirmation(plan.Spec.Name, plan.PlanSHA256, bundle, f.records, f.life.Digest, f.now.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
}

func (f *selectionFixture) completeManaged(t *testing.T, plan role.ConfirmationPlan, bundle role.ConfirmationBundle) {
	t.Helper()
	store, err := record.CreateManagedStore(f.records, record.ManagedStoreSpec{Schema: record.ManagedStoreSpecSchema,
		ID: f.storeID, SessionID: "private-auto-session", Purpose: "confirmation"})
	if err != nil {
		t.Fatal(err)
	}
	for _, point := range f.points {
		if _, err := store.Save(point); err != nil {
			t.Fatal(err)
		}
	}
	ref, err := store.Close()
	if err != nil {
		t.Fatal(err)
	}
	f.life, err = f.roles.FinishManagedConfirmation(plan.Spec.Name, plan.PlanSHA256, bundle, f.records, ref, f.life.Digest, f.now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	f.life, err = f.roles.AdoptManagedConfirmation(plan.Spec.Name, plan.PlanSHA256, bundle, f.records, ref, f.life.Digest, f.now.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
}
