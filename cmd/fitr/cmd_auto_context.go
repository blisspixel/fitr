package main

import (
	"errors"
	"fmt"

	"github.com/blisspixel/fitr/internal/automation"
	"github.com/blisspixel/fitr/internal/record"
)

// bindAutoContextSchedule seals the shared document-context plan after every
// candidate has been identified and before the auto plan digest exists. A
// runner that silently shrinks the output reserve is refused first, so the
// session never starts a schedule it cannot test.
func bindAutoContextSchedule(plan *automation.Plan, tiers []int, servedByMLX bool) error {
	if len(tiers) == 0 {
		return nil
	}
	if servedByMLX {
		return errors.New("auto context schedule refuses a model served by MLX, which silently reduces the output reserve and reports no field confirming it")
	}
	return plan.BindContextSchedule(tiers)
}

func (run *autoExecution) contextGroup(phase string) (record.ManagedStore, error) {
	id, err := automation.ContextStoreID(run.plan, phase)
	if err != nil {
		return record.ManagedStore{}, err
	}
	return record.CreateManagedStore(run.records, record.ManagedStoreSpec{Schema: record.ManagedStoreSpecSchema, ID: id, SessionID: run.plan.ID, Purpose: phase})
}

func (run *autoExecution) collectContextPoint(index int, state automation.State, candidate automation.Candidate) (*record.Record, error) {
	opts, expected, seed, err := run.contextPointOptions(index, state)
	if err != nil {
		return nil, err
	}
	run.display.Phase(state.Phase+" context", fmt.Sprintf("%d/%d  %s", index+1, len(run.plan.Candidates), candidate.Model))
	point, err := executeUnderLease(run.ctx, run.backend, candidate.Model, opts, run.display, run.lease)
	if err != nil {
		return nil, err
	}
	if err := run.recheckPoint(candidate, point); err != nil {
		return nil, err
	}
	if err := validateAutoContextPoint(run.plan, point, seed, expected); err != nil {
		return nil, err
	}
	return point, nil
}

func (run *autoExecution) contextPointOptions(index int, state automation.State) (runOpts, string, string, error) {
	if run.plan.ContextPolicy == nil {
		return runOpts{}, "", "", errors.New("context collection requires a sealed context schedule")
	}
	seed := run.plan.SeedSet
	expected := run.plan.ContextPlanSHA256
	if state.Phase == "confirmation" {
		if state.Confirmation == nil {
			return runOpts{}, "", "", errors.New("context confirmation has no sealed plan")
		}
		seed = state.Confirmation.SeedSet
		expected = state.Confirmation.ContextPlanSHA256
	}
	runID, err := record.NewRunID()
	if err != nil {
		return runOpts{}, "", "", err
	}
	reserve := float64(run.plan.Runtime.ReserveBytes) / float64(1<<30)
	tiers := append([]int(nil), run.plan.ContextPolicy.PayloadUTF8Bytes...)
	opts := runOpts{
		level: levelContext, profile: run.plan.Profile, seedSet: seed, reps: 1, checksReps: 1,
		numCtx: run.plan.Runtime.NumCtx, memoryCtx: run.plan.Runtime.NumCtx, capacityReserveGB: &reserve,
		ownedConfiguration: autoConfiguration(run.plan.Runtime), contextTiers: tiers, runID: runID,
	}
	opts.validatePrepared = func(point *runExecution) error {
		if _, err := run.currentRole(); err != nil {
			return err
		}
		if err := validateAutoDefinition(run.plan, point); err != nil {
			return err
		}
		if point.resolved.Identity.RuntimeBoundDigest() != run.plan.Candidates[index].ArtifactDigest {
			return errors.New("auto installed artifact changed")
		}
		configuration, err := run.runtime.ModelConfiguration(run.ctx, point.model)
		if err != nil {
			return err
		}
		if configuration.SHA256 != run.plan.Candidates[index].ModelConfigurationSHA256 {
			return errors.New("auto model template, parser or parameters changed")
		}
		binding, err := run.runtime.BindingMetadata(configuration.SHA256, run.plan.Candidates[index].ArtifactDigest)
		if err != nil {
			return err
		}
		point.result.RuntimeBinding = &binding
		if point.contextPlan == nil || point.contextPlan.PlanSHA256 != expected || point.result.TaskPlan.ContextPlanSHA256 != expected || point.result.TaskPlan.ContextCells != run.plan.ContextCells {
			return errors.New("context point does not match the sealed schedule")
		}
		return nil
	}
	opts.validateCapacity = func(point *runExecution) error {
		return run.preparePlacement(point, index, nil)
	}
	opts.validateLoaded = validateAutoLoaded
	return opts, expected, seed, nil
}

func validateAutoContextPoint(plan automation.Plan, point *record.Record, seed, expectedPlan string) error {
	if point == nil || point.Completion == nil {
		return errors.New("saved context evidence is incomplete")
	}
	if issue := point.EvidenceIntegrityIssue(); issue != "" {
		return errors.New(issue)
	}
	manifest := point.Manifest
	if manifest == nil || manifest.Provenance == nil || *manifest.Provenance != plan.Provenance ||
		manifest.Profile != plan.Profile || manifest.Level != levelContext || manifest.ExecutionPolicy != record.ExecutionDisabled ||
		manifest.Repeats != 1 || manifest.NumCtx != plan.Runtime.NumCtx || manifest.SeedSet != seed || point.Experiment != nil {
		return errors.New("saved context evidence changed the fixed collection protocol")
	}
	if point.ContextQuality == nil || point.TaskPlan.ContextPlanSHA256 != expectedPlan || point.TaskPlan.ContextCells != plan.ContextCells {
		return errors.New("saved context evidence does not match the sealed schedule")
	}
	deviceSHA, err := autoDeviceDigest(point.Device)
	if err != nil || deviceSHA != plan.DeviceSHA256 {
		return errors.New("saved context evidence changed the physical device or owned settings")
	}
	profile, launch, err := plan.Runtime.ProfileDigests()
	if err != nil {
		return err
	}
	binding := point.RuntimeBinding
	if binding == nil || binding.ArtifactDigest == "" || binding.ProfileSHA256 != profile || binding.LaunchConfigurationSHA256 != launch ||
		binding.ExecutableSHA256 != plan.Runtime.ExecutableSHA256 || binding.RuntimeVersion != plan.Runtime.RuntimeVersion {
		return errors.New("saved context evidence changed the owned runtime profile")
	}
	return nil
}

func (run *autoExecution) sealedContextPoints(phase string) (record.ManagedStoreRef, []*record.Record, error) {
	id, err := automation.ContextStoreID(run.plan, phase)
	if err != nil {
		return record.ManagedStoreRef{}, nil, err
	}
	store, err := record.OpenManagedStore(run.records, id)
	if err != nil {
		return record.ManagedStoreRef{}, nil, err
	}
	ref, err := store.Ref()
	if err != nil {
		return record.ManagedStoreRef{}, nil, err
	}
	points := make([]*record.Record, 0, len(run.plan.Candidates))
	for _, candidate := range run.plan.Candidates {
		point, err := store.Read(candidate.Model)
		if err != nil {
			return record.ManagedStoreRef{}, nil, err
		}
		points = append(points, point)
	}
	return ref, points, nil
}

// savedExplorationContext reads one open sibling after a crash. Confirmation
// has no recovery path: an interrupted confirmation ends the session, and a
// battery point without its context twin cannot be completed.
func (run *autoExecution) savedExplorationContext(candidate automation.Candidate, battery *record.Record) (string, error) {
	id, err := automation.ContextStoreID(run.plan, "exploration")
	if err != nil {
		return "", err
	}
	store, err := record.OpenManagedStore(run.records, id)
	if err != nil {
		return "", errors.New("interrupted point has no context evidence; requests remain charged and this session cannot retry")
	}
	point, err := store.ReadSaved(candidate.Model)
	if err != nil || point.Completion == nil {
		return "", errors.New("interrupted point has no complete context evidence; requests remain charged and this session cannot retry")
	}
	if err := validateAutoContextPoint(run.plan, point, run.plan.SeedSet, run.plan.ContextPlanSHA256); err != nil {
		return "", err
	}
	if point.RuntimeBinding.ArtifactDigest != candidate.ArtifactDigest || point.RuntimeBinding.ModelConfigurationSHA256 != candidate.ModelConfigurationSHA256 ||
		point.StableRunID() == battery.StableRunID() || point.Completion.EvidenceSHA256 == battery.Completion.EvidenceSHA256 {
		return "", errors.New("saved context evidence does not match the interrupted point")
	}
	return point.Completion.EvidenceSHA256, nil
}

// phaseContext reopens one closed sibling and checks it against the journal
// point that named its evidence. Exploration and confirmation use different
// seeds, so the caller passes the schedule that phase sealed.
func (run *autoExecution) phaseContext(phase, seed, expected string, completed []automation.Event) (record.ManagedStoreRef, []*record.Record, error) {
	ref, points, err := run.sealedContextPoints(phase)
	if err != nil {
		return record.ManagedStoreRef{}, nil, err
	}
	if len(points) != len(completed) || len(points) != len(run.plan.Candidates) {
		return record.ManagedStoreRef{}, nil, errors.New("context evidence does not cover the sealed schedule")
	}
	for index, point := range points {
		if err := validateAutoContextPoint(run.plan, point, seed, expected); err != nil {
			return record.ManagedStoreRef{}, nil, err
		}
		candidate := run.plan.Candidates[index]
		event := completed[index]
		if point.Model != candidate.Model || point.Completion.EvidenceSHA256 != event.ContextEvidenceSHA256 ||
			point.StableRunID() == event.RunID || point.Completion.EvidenceSHA256 == event.EvidenceSHA256 ||
			point.RuntimeBinding.ArtifactDigest != candidate.ArtifactDigest || point.RuntimeBinding.ModelConfigurationSHA256 != candidate.ModelConfigurationSHA256 {
			return record.ManagedStoreRef{}, nil, errors.New("stored context evidence differs from the sealed point")
		}
	}
	return ref, points, nil
}
