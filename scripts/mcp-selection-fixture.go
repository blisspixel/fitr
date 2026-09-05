//go:build ignore

// This acceptance helper creates synthetic managed selection evidence through
// public storage and lifecycle APIs. It never starts a model or runtime.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

func main() {
	if err := fixture(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func fixture() error {
	if len(os.Args) != 4 || (os.Args[3] != "qualified" && os.Args[3] != "stale") {
		return errors.New("usage: go run scripts/mcp-selection-fixture.go <empty-results> <sealed-fixture> <qualified|stale>")
	}
	root, err := filepath.EvalSymlinks(os.Args[1])
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		return errors.New("fixture destination must be an existing empty directory")
	}
	data, err := os.ReadFile(os.Args[2])
	if err != nil {
		return err
	}
	created := time.Now().UTC().Truncate(time.Second).Add(-5 * time.Minute)
	records, roles := record.Store{Dir: root}, role.Store{Dir: filepath.Join(root, ".roles")}
	plan, points, err := selectionPlan(data, created)
	if err != nil {
		return err
	}
	if err := publishSelection(records, roles, plan, points, created); err != nil {
		return err
	}
	status, err := roles.ReviewSelection("coding", records, time.Now().UTC())
	if err != nil {
		return err
	}
	if status.State != "qualified" || status.Selection == nil || status.Selection.Selected.RuntimeBinding == nil {
		return errors.New("synthetic managed selection failed to qualify with owned runtime evidence")
	}
	if os.Args[3] == "stale" {
		spec := plan.Spec
		spec.Description += " changed"
		if _, err := roles.Define(spec); err != nil {
			return err
		}
		status, err = roles.ReviewSelection("coding", records, time.Now().UTC())
		if err != nil {
			return err
		}
		if status.State != "stale" || status.Selection == nil {
			return errors.New("changed role failed to preserve a stale selection")
		}
	}
	return json.NewEncoder(os.Stdout).Encode(status)
}

func selectionDigest(value string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(value)))
}

func selectionSpec() role.Spec {
	rate, speed := 0.5, 1.0
	return role.Spec{Schema: role.SpecSchema, Name: "coding", Description: "private-role-canary", MaxAgeDays: 30,
		Decision: decision.DecisionSpec{Schema: decision.SpecSchema, Name: "private-decision-canary", Evidence: decision.EvidenceDecide,
			Requirements: []decision.Requirement{
				{ID: "private-quality-canary", Behavior: &decision.BehaviorRequirement{Need: "structured_output", MinimumRate: &rate}},
				{ID: "context", Context: &decision.ContextRequirement{MinimumEffectiveTokens: 4096}},
				{ID: "memory", Capacity: &decision.CapacityRequirement{MaximumResidentBytes: 8 * device.GB}},
				{ID: "speed", Performance: &decision.PerformanceRequirement{Metric: decision.MetricDecodeTPS, AtLeast: &speed}},
			}}, Preferences: []role.Preference{{Requirement: "speed", Weight: 1, Worst: 0, Best: 100}}}
}

func selectionRecord(data []byte, model string, speed float64, started time.Time) (*record.Record, error) {
	var result record.Record
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	if result.Manifest == nil || result.Manifest.Provenance == nil || result.Completion == nil {
		return nil, errors.New("historical fixture lacks sealed manifest and completion")
	}
	result.SchemaVersion, result.Model, result.ModelMeta = record.EvidenceSchemaVersion, model, ollama.ModelInfo{Name: model}
	result.StartedAt, result.Level, result.SeedSet = started.Format(time.RFC3339), "full", "private-exploration"
	result.NumCtx, result.Device.Runtime = 8192, "private-runtime-canary"
	identity := &result.Manifest.Model
	identity.Requested, identity.Resolved, identity.Runtime, identity.Value = "private-alias-canary-"+model, model, result.Device.Runtime, selectionDigest(model)
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
		return nil, err
	}
	if err := selectionMeasurements(&result, speed); err != nil {
		return nil, err
	}
	result.RuntimeBinding = &record.RuntimeBinding{Schema: record.RuntimeBindingSchema, Kind: "owned_ollama",
		ProfileSHA256: selectionDigest("profile"), ExecutableSHA256: selectionDigest("executable"),
		LaunchConfigurationSHA256: selectionDigest("launch"), ModelConfigurationSHA256: selectionDigest("config-" + model),
		ArtifactDigest: identity.Value, RuntimeVersion: identity.Runtime, OwnershipSHA256: selectionDigest("private-exploration-ownership")}
	return &result, reseal(&result)
}

func selectionMeasurements(result *record.Record, speed float64) error {
	contextSize := 8192
	fingerprint, err := device.NewFingerprintV2(result.Device, device.ContextVerification{
		RequestedTokens: contextSize, EffectiveTokens: &contextSize, EffectiveSource: device.ContextSourceRuntimeReport})
	if err != nil {
		return err
	}
	result.DeviceV2 = &fingerprint
	result.DeviceKey, err = fingerprint.ComparabilityKey()
	if err != nil {
		return err
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
	return nil
}

func reseal(result *record.Record) error {
	profile, identity, previous := result.Completion.Profile, result.Manifest.Model, *result.Manifest.Provenance
	result.Manifest, result.Completion, result.RunID = nil, nil, ""
	var err error
	result.EvidenceCounts, err = result.DeriveEvidenceCounts()
	if err != nil {
		return err
	}
	result.Scorecard = score.Score(result.Measured(), profile)
	provenance, err := record.NewRunProvenance(previous.TaskSetSHA256, previous.SpecSHA256, profile,
		record.CurrentScoringPolicy(), record.SoftwareReceipt{FitrVersion: previous.FitrVersion,
			SoftwareBuildSHA256: previous.SoftwareBuildSHA256, BackendProtocol: previous.BackendProtocol})
	if err != nil {
		return err
	}
	if err := result.AttachManifest(identity, provenance); err != nil {
		return err
	}
	if err := result.CompleteEvidence(profile); err != nil {
		return err
	}
	if issue := result.EvidenceIntegrityIssue(); issue != "" {
		return fmt.Errorf("synthetic evidence integrity: %s", issue)
	}
	return nil
}

func selectionCapacity(point *record.Record, control role.ConfirmationCapacity) (*capacity.Plan, error) {
	policy, err := capacity.BuildPolicy(capacity.PolicyInput{ResourceDomain: capacity.DomainAccelerator,
		OperatorBudgetBytes: control.OperatorBudgetBytes, OperatorReserveBytes: control.OperatorReserveBytes})
	if err != nil {
		return nil, err
	}
	digest, err := policy.Digest()
	if err != nil {
		return nil, err
	}
	return &capacity.Plan{Schema: capacity.PlanSchema, Policy: policy, Prediction: capacity.Prediction{
		Schema: capacity.PredictionSchema, CreatedAt: point.StartedAt, ArtifactSHA256: point.Manifest.Model.Value,
		ResourceDomain: capacity.DomainAccelerator, RequestedContext: point.ContextSize(), PlacementAssumption: "runtime placement",
		State: capacity.PredictionUnavailable, Missing: []string{"architecture"}, Excluded: []string{"runtime buffers"}, PolicySHA256: digest}}, nil
}

func selectionPlan(data []byte, created time.Time) (role.ConfirmationPlan, []*record.Record, error) {
	sources := make([]*record.Record, 2)
	for index, speed := range []float64{80, 20} {
		var err error
		sources[index], err = selectionRecord(data, fmt.Sprintf("private-model-canary-%d", index), speed, created.Add(-time.Hour))
		if err != nil {
			return role.ConfirmationPlan{}, nil, err
		}
	}
	plan, err := role.NewConfirmationPlan(selectionSpec(), sources, sources[0].Completion.EvidenceSHA256, created)
	if err != nil {
		return plan, nil, err
	}
	points := make([]*record.Record, len(sources))
	for index, source := range sources {
		raw, err := json.Marshal(source)
		if err != nil {
			return plan, nil, err
		}
		var point record.Record
		if err := json.Unmarshal(raw, &point); err != nil {
			return plan, nil, err
		}
		point.StartedAt, point.SeedSet = created.Add(time.Minute).Format(time.RFC3339), plan.SeedSet
		binding := role.ConfirmationPlanBinding(plan, index+1)
		point.Experiment, point.TaskPlan = &binding, plan.Protocol.TaskPlan
		for trial, check := range plan.Protocol.Checks {
			point.Checks[trial].Seed = check.Seed
		}
		point.CapacityPlan, err = selectionCapacity(&point, plan.Candidates[index].Capacity)
		if err != nil {
			return plan, nil, err
		}
		point.RuntimeBinding.OwnershipSHA256 = selectionDigest("private-fresh-ownership")
		if err := reseal(&point); err != nil {
			return plan, nil, err
		}
		points[index] = &point
	}
	return plan, points, nil
}

func publishSelection(records record.Store, roles role.Store, plan role.ConfirmationPlan, points []*record.Record, created time.Time) error {
	if _, err := roles.Define(plan.Spec); err != nil {
		return err
	}
	life, err := roles.LoadLifecycle(plan.Spec.Name)
	if err != nil {
		return err
	}
	life, err = roles.IssueConfirmation(plan, life.Digest, created)
	if err != nil {
		return err
	}
	life, err = roles.BeginConfirmation(plan.Spec.Name, plan.PlanSHA256, life.Digest, created.Add(time.Second))
	if err != nil {
		return err
	}
	bundle, err := role.NewConfirmationBundle(plan, points, created.Add(2*time.Minute))
	if err != nil {
		return err
	}
	if bundle.Report.State != "confirmed" {
		return errors.New("synthetic confirmation failed to confirm")
	}
	store, err := record.CreateManagedStore(records, record.ManagedStoreSpec{Schema: record.ManagedStoreSpecSchema,
		ID: "private-confirmation", SessionID: "private-auto-session", Purpose: "confirmation"})
	if err != nil {
		return err
	}
	for _, point := range points {
		if _, err := store.Save(point); err != nil {
			return err
		}
	}
	ref, err := store.Close()
	if err != nil {
		return err
	}
	life, err = roles.FinishManagedConfirmation(plan.Spec.Name, plan.PlanSHA256, bundle, records, ref, life.Digest, created.Add(2*time.Minute))
	if err != nil {
		return err
	}
	_, err = roles.AdoptManagedConfirmation(plan.Spec.Name, plan.PlanSHA256, bundle, records, ref, life.Digest, created.Add(3*time.Minute-time.Second))
	return err
}
