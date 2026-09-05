package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"github.com/blisspixel/fitr/internal/automation"
	"github.com/blisspixel/fitr/internal/autoruntime"
	"github.com/blisspixel/fitr/internal/capacity"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/lock"
	"github.com/blisspixel/fitr/internal/modelref"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/role"
)

type ownedAutoBackend struct {
	*ollama.Client
	runtime *autoruntime.Runtime
}

func (b *ownedAutoBackend) Accel(ctx context.Context) string { return b.runtime.Accel(ctx) }

func (b *ownedAutoBackend) WaitAccel(ctx context.Context, expected string) (string, error) {
	return b.runtime.WaitAccel(ctx, expected)
}

type autoExecution struct {
	ctx     context.Context
	runtime *autoruntime.Runtime
	backend *ownedAutoBackend
	lease   *lock.Lock
	session *automation.Session
	plan    automation.Plan
	roles   role.Store
	records record.Store
	display render.Display
}

func autoStores() (role.Store, record.Store) {
	return role.Store{Dir: filepath.Join(resultsDir(), ".roles")}, record.Store{Dir: resultsDir()}
}

func startAuto(ctx context.Context, command autoCommand) int {
	created := time.Now()
	if command.wall < 2*time.Second || command.wall > 24*time.Hour || command.confirmationWall <= 0 || command.confirmationWall >= command.wall {
		return autoFailure(errors.New("wall limit must be 2 seconds to 24 hours with a smaller positive confirmation allowance"))
	}
	ctx, cancel := context.WithDeadline(ctx, created.Add(command.wall))
	defer cancel()
	roles, records := autoStores()
	inputs, err := loadAutoStart(command, roles, records, created)
	if err != nil {
		return autoFailure(err)
	}
	lease, err := lock.Acquire("eval", "auto role "+command.role)
	if err != nil {
		return autoFailure(err)
	}
	defer func() { _ = lease.Release() }()
	return startAutoLeased(ctx, command, roles, records, lease, inputs, created)
}

func autoTaskSpec() (*eval.Spec, error) {
	tasks, err := eval.LoadSpec()
	if err != nil {
		return nil, err
	}
	user, err := eval.LoadUserChecks(eval.UserTasksDir())
	if err != nil {
		return nil, err
	}
	tasks.Checks, err = eval.MergeChecks(tasks.Checks, user)
	return tasks, err
}

func autoDigest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", hash), nil
}

func autoDeviceDigest(fp device.Fingerprint) (string, error) {
	// Placement and the compute library are observed only after loading. The
	// physical device, driver, runtime and owned settings are frozen beforehand.
	fp.InferenceDevice, fp.GPUBackend = "", ""
	return autoDigest(fp)
}

func autoConfiguration(spec autoruntime.Spec) map[string]string {
	return map[string]string{"OLLAMA_MODELS": spec.ModelStore, "OLLAMA_NO_CLOUD": "true", "OLLAMA_NOPRUNE": "true",
		"OLLAMA_MAX_LOADED_MODELS": "1", "OLLAMA_NUM_PARALLEL": "1", "OLLAMA_CONTEXT_LENGTH": strconv.Itoa(spec.NumCtx),
		"OLLAMA_KV_CACHE_TYPE": spec.KVCacheType, "OLLAMA_FLASH_ATTENTION": strconv.FormatBool(spec.FlashAttention),
		"OLLAMA_GPU_OVERHEAD": strconv.FormatInt(spec.ReserveBytes, 10), "LLAMA_ARG_FIT": "off"}
}

func (run *autoExecution) preparePlan(command autoCommand, prepared autoruntime.Prepared, tasks *eval.Spec, spec role.Spec, selection role.SelectionStatus, created time.Time) (automation.Plan, error) {
	id, err := automation.NewID()
	if err != nil {
		return automation.Plan{}, err
	}
	seed, err := automation.NewID()
	if err != nil {
		return automation.Plan{}, err
	}
	envelope, err := eval.PlanRequestEnvelope(tasks, eval.RequestEnvelopeOptions{Backend: "ollama", Level: "full", Repeats: command.repeats, CheckRepeats: command.repeats, ContextProbe: true})
	if err != nil {
		return automation.Plan{}, err
	}
	envelopeSHA, err := envelope.Digest()
	if err != nil {
		return automation.Plan{}, err
	}
	plan := automation.Plan{ID: id, Mode: command.mode, Adoption: command.adoption, Spec: spec, RoleRevision: selectionRevision(spec), IncumbentSHA256: selection.ReceiptSHA256, LifecycleSHA256: selection.LifecycleDigest,
		Runtime: prepared.Spec, Profile: command.profile, Repeats: command.repeats, SeedSet: seed, EnvelopeSHA256: envelopeSHA, PointRequests: envelope.MaxRequests, PointRequestedOutputTokens: envelope.MaxRequestedOutputTokens, Limits: command.limits}
	for index, model := range command.candidates {
		point := &runExecution{ctx: run.ctx, backend: run.backend, model: model, opts: autoRunOptions(plan), display: render.New("none")}
		if err := point.prepare(); err != nil {
			return plan, err
		}
		candidate, err := run.preparedCandidate(index, point)
		if err != nil {
			return plan, err
		}
		plan.Candidates = append(plan.Candidates, candidate)
		if index == 0 {
			plan.Provenance = point.provenance
			plan.Profile = point.profile.Name
			plan.SoftwareSHA256 = point.provenance.SoftwareBuildSHA256
			plan.TaskSetSHA256 = point.provenance.TaskSetSHA256
			plan.SpecSHA256 = point.provenance.SpecSHA256
			plan.DeviceSHA256, err = autoDeviceDigest(point.result.Device)
			if err != nil {
				return plan, err
			}
		} else if err := validateAutoDefinition(plan, point); err != nil {
			return plan, err
		}
		if err := point.prepareCapacityPlan(); err != nil {
			return plan, err
		}
		if err := validateAutoProjection(point); err != nil {
			return plan, err
		}
	}
	if err := autoIncumbentIncluded(plan, prepared, selection); err != nil {
		return plan, err
	}
	if err := plan.Seal(created); err != nil {
		return plan, err
	}
	return plan, nil
}

func selectionRevision(spec role.Spec) string { digest, _ := spec.Digest(); return digest }

func autoCandidateScope(info ollama.ModelInfo) error {
	if info.IsRemote() {
		return ollama.ErrRemoteExecution
	}
	for _, capability := range info.Capabilities {
		switch capability {
		case "vision", "audio", "embedding":
			return errors.New("auto v1 requires a text model without additional vision, audio or embedding components")
		}
	}
	return nil
}

func autoIncumbentIncluded(plan automation.Plan, prepared autoruntime.Prepared, selection role.SelectionStatus) error {
	if plan.Mode == "establish" {
		return nil
	}
	if selection.Selection == nil || selection.Selection.Selected.RuntimeBinding == nil {
		return errors.New("incumbent lacks an owned runtime profile; establish matching owned evidence explicitly before an improve session")
	}
	incumbent := selection.Selection.Selected
	if incumbent.RuntimeBinding.ProfileSHA256 != prepared.ProfileSHA256 {
		return errors.New("incumbent was measured under a different owned runtime profile")
	}
	for _, candidate := range plan.Candidates {
		if candidate.ArtifactDigest == incumbent.Model.RuntimeBoundDigest() && candidate.ModelConfigurationSHA256 == incumbent.RuntimeBinding.ModelConfigurationSHA256 {
			return nil
		}
	}
	return errors.New("improve requires the unchanged incumbent in the explicit candidate shortlist")
}

func autoRunOptions(plan automation.Plan) runOpts {
	reserve := float64(plan.Runtime.ReserveBytes) / float64(1<<30)
	return runOpts{level: "full", profile: plan.Profile, seedSet: plan.SeedSet, reps: plan.Repeats, checksReps: plan.Repeats, numCtx: plan.Runtime.NumCtx, memoryCtx: plan.Runtime.NumCtx, capacityReserveGB: &reserve, ownedConfiguration: autoConfiguration(plan.Runtime)}
}

func validateAutoDefinition(plan automation.Plan, point *runExecution) error {
	if err := plan.Provenance.CompatibilityError(point.provenance); err != nil {
		return err
	}
	if point.provenance != plan.Provenance {
		return errors.New("auto software build or fixed provenance changed before admission")
	}
	deviceSHA, err := autoDeviceDigest(point.result.Device)
	if err != nil {
		return err
	}
	if deviceSHA != plan.DeviceSHA256 {
		return errors.New("auto physical device, driver or owned settings changed")
	}
	return nil
}

func validateAutoProjection(point *runExecution) error {
	if point.result.CapacityPlan == nil {
		return errors.New("auto needs a capacity policy before loading")
	}
	projection := point.result.CapacityPlan.Prediction
	if len(projection.Missing) > 0 || projection.ArtifactBytes == nil || projection.KVBytes == nil || projection.KnownComponentBytes == nil {
		return errors.New("auto cannot project every required text model component at the fixed context")
	}
	if point.result.CapacityPlan.Policy.OperatorReserveBytes == nil {
		return errors.New("auto needs an explicit reserve before its bounded load probe")
	}
	if point.result.CapacityPlan.Policy.ResourceDomain == capacity.DomainAccelerator {
		available, err := device.SingleNVIDIAAvailable(point.ctx, point.result.Device)
		if err != nil {
			return err
		}
		reserve := *point.result.CapacityPlan.Policy.OperatorReserveBytes
		if available <= reserve || *projection.KnownComponentBytes >= available-reserve {
			return errors.New("auto selected accelerator has insufficient current headroom after its reserve")
		}
	}
	return requireRoleHeadroom(point.result.CapacityPlan.Policy, *projection.KnownComponentBytes)
}

func validateAutoLoaded(point *runExecution, receipt device.ContextVerification) error {
	if receipt.EffectiveTokens == nil || *receipt.EffectiveTokens != point.result.NumCtx || receipt.Probe == nil {
		return errors.New("auto load probe did not verify the exact requested context")
	}
	models, err := point.backend.PS(point.ctx)
	if err != nil {
		return err
	}
	if len(models) != 1 || !modelref.SameServed(models[0].Name, point.model) || models[0].ContextLength != point.result.NumCtx || models[0].Size <= 0 {
		return errors.New("auto requires one owned model with exact-context resident allocation")
	}
	allocation := models[0]
	observer, ok := point.backend.(interface{ Accel(context.Context) string })
	if !ok {
		return errors.New("auto requires an owned compute backend observer")
	}
	point.result.Device.GPUBackend = device.NormalizeAccel(observer.Accel(point.ctx))
	if point.result.Device.GPUBackend == "" {
		return errors.New("auto load has no recognized compute backend observation from the owned process")
	}
	domain := point.result.CapacityPlan.Policy.ResourceDomain
	if (domain == capacity.DomainHost) != (point.result.Device.GPUBackend == "cpu") {
		return errors.New("owned compute backend disagrees with the planned memory domain")
	}
	if (domain == capacity.DomainAccelerator && allocation.SizeVRAM != allocation.Size) || (domain == capacity.DomainHost && allocation.SizeVRAM != 0) {
		return errors.New("auto v1 does not authorize partial offload or a changed memory domain")
	}
	return requireRoleHeadroom(point.result.CapacityPlan.Policy, allocation.Size)
}

func (run *autoExecution) currentRole() (role.Lifecycle, error) {
	library, err := run.roles.Load(run.plan.Spec.Name)
	if err != nil {
		return role.Lifecycle{}, err
	}
	if library.CurrentRevision != run.plan.RoleRevision {
		return role.Lifecycle{}, errors.New("auto role policy changed")
	}
	life, err := run.roles.LoadLifecycle(run.plan.Spec.Name)
	if err != nil {
		return life, err
	}
	if life.IncumbentSHA256 != run.plan.IncumbentSHA256 {
		return life, errors.New("auto incumbent changed")
	}
	if err := run.validateLifecycleChanges(life); err != nil {
		return life, err
	}
	if run.plan.IncumbentSHA256 != "" {
		status, err := run.roles.ReviewSelection(run.plan.Spec.Name, run.records, time.Now())
		if err != nil {
			return life, err
		}
		if status.State != "qualified" {
			return life, errors.New("auto incumbent evidence is no longer qualified")
		}
	}
	return life, nil
}

func (run *autoExecution) validateLifecycleChanges(life role.Lifecycle) error {
	if life.Digest == run.plan.LifecycleSHA256 {
		return nil
	}
	if run.session == nil {
		return errors.New("role lifecycle changed before auto admission")
	}
	_, state, err := run.session.Snapshot()
	if err != nil {
		return err
	}
	if state.Confirmation == nil {
		return errors.New("role lifecycle changed during exploration")
	}
	found := false
	for _, event := range life.Events {
		if event.PreviousDigest == run.plan.LifecycleSHA256 {
			found = true
		}
		if found && (event.PlanSHA256 != state.Confirmation.PlanSHA256 ||
			(event.Action != "issued" && event.Action != "started" && event.Action != "completed")) {
			return errors.New("another role lifecycle action changed this auto session")
		}
	}
	if !found {
		return errors.New("auto lifecycle baseline is missing")
	}
	return nil
}

func (run *autoExecution) pointOptions(index int, state automation.State) runOpts {
	opts := autoRunOptions(run.plan)
	if state.Phase == "confirmation" {
		binding := role.ConfirmationPlanBinding(*state.Confirmation, index+1)
		opts = roleConfirmationRunOptions(*state.Confirmation, index+1, &binding)
		opts.ownedConfiguration = autoConfiguration(run.plan.Runtime)
		opts.validatePrepared = func(point *runExecution) error {
			return role.ValidatePreparedOwnedConfirmationPoint(*state.Confirmation, index+1, point.result, point.resolved.Identity, point.provenance)
		}
	}
	priorPrepared, priorCapacity := opts.validatePrepared, opts.validateCapacity
	opts.runID = state.ActiveRunID
	opts.validatePrepared = func(point *runExecution) error {
		if _, err := run.currentRole(); err != nil {
			return err
		}
		if err := validateAutoDefinition(run.plan, point); err != nil {
			return err
		}
		candidate := run.plan.Candidates[index]
		if point.resolved.Identity.RuntimeBoundDigest() != candidate.ArtifactDigest {
			return errors.New("auto installed artifact changed")
		}
		configuration, err := run.runtime.ModelConfiguration(run.ctx, point.model)
		if err != nil {
			return err
		}
		if configuration.SHA256 != candidate.ModelConfigurationSHA256 {
			return errors.New("auto model template, parser or parameters changed")
		}
		binding, err := run.runtime.BindingMetadata(configuration.SHA256, candidate.ArtifactDigest)
		if err != nil {
			return err
		}
		point.result.RuntimeBinding = &binding
		if priorPrepared != nil {
			return priorPrepared(point)
		}
		return nil
	}
	opts.validateCapacity = func(point *runExecution) error {
		return run.preparePlacement(point, index, priorCapacity)
	}
	opts.validateLoaded = validateAutoLoaded
	return opts
}

func (run *autoExecution) preparePlacement(point *runExecution, index int, priorCapacity func(*runExecution) error) error {
	if err := validateAutoProjection(point); err != nil {
		return err
	}
	if priorCapacity != nil {
		if err := priorCapacity(point); err != nil {
			return err
		}
	}
	guard, err := newAutoPlacementGuard(run.backend, run.plan.Candidates[index], run.plan.Runtime, point.result.CapacityPlan.Policy, point.result.Device)
	if err != nil {
		return err
	}
	run.backend.ObserveInference = guard.Observe
	return nil
}

type autoStartInputs struct {
	runtimeSpec autoruntime.Spec
	tasks       *eval.Spec
	spec        role.Spec
	selection   role.SelectionStatus
}

func loadAutoStart(command autoCommand, roles role.Store, records record.Store, created time.Time) (autoStartInputs, error) {
	library, err := roles.Load(command.role)
	if err != nil {
		return autoStartInputs{}, err
	}
	spec, err := library.CurrentSpec()
	if err != nil {
		return autoStartInputs{}, err
	}
	runtimeSpec, err := loadAutoRuntime(command.runtimePath)
	if err != nil {
		return autoStartInputs{}, err
	}
	tasks, err := autoTaskSpec()
	if err != nil {
		return autoStartInputs{}, err
	}
	if err := automation.ValidateFeasibility(spec, tasks, command.repeats, runtimeSpec.NumCtx); err != nil {
		return autoStartInputs{}, err
	}
	selection, err := roles.ReviewSelection(command.role, records, created)
	if err != nil {
		return autoStartInputs{}, err
	}
	if (command.mode == "establish" && selection.State != "unselected") || (command.mode == "improve" && selection.State != "qualified") {
		return autoStartInputs{}, errors.New("establish requires an unselected role; improve requires a current qualified incumbent")
	}
	return autoStartInputs{runtimeSpec, tasks, spec, selection}, nil
}

func startAutoLeased(ctx context.Context, command autoCommand, roles role.Store, records record.Store, lease *lock.Lock, inputs autoStartInputs, created time.Time) int {
	prepared, err := autoruntime.Prepare(ctx, inputs.runtimeSpec)
	if err != nil {
		return autoFailure(err)
	}
	runtime, err := autoruntime.Start(ctx, prepared)
	if err != nil {
		return autoFailure(err)
	}
	defer func() { _ = runtime.Close() }()
	backend := &ownedAutoBackend{Client: &ollama.Client{BaseURL: runtime.URL(), HTTP: runtime.HTTPClient()}, runtime: runtime}
	// No inference is allowed during plan construction, even by a future helper.
	backend.Admission = func(context.Context, ollama.InferenceRequest) (ollama.InferencePermit, error) {
		return ollama.InferencePermit{}, errors.New("auto plan has not authorized inference")
	}
	display := render.New(command.display)
	defer display.Close()
	run := &autoExecution{ctx: ctx, runtime: runtime, backend: backend, lease: lease, roles: roles, records: records, display: display}
	plan, err := run.preparePlan(command, prepared, inputs.tasks, inputs.spec, inputs.selection, created)
	if err != nil {
		return autoFailure(err)
	}
	session, err := (automation.Store{Results: records.Dir}).Create(plan)
	if err != nil {
		return autoFailure(err)
	}
	defer func() { _ = session.Close() }()
	run.session, run.plan = session, plan
	backend.Admission = session.Reserve
	display.Phase("session", plan.ID)
	display.Phase("allowance", fmt.Sprintf("%d requests; %d requested output tokens; confirmation reserved", plan.Limits.MaxRequests, plan.Limits.MaxRequestedOutputTokens))
	if err := run.collect(); err != nil {
		run.fail(err)
		return autoFailure(err)
	}
	if err := runtime.Close(); err != nil {
		run.fail(err)
		return autoFailure(err)
	}
	if err := autoAdoptIfDeclared(session); err != nil {
		return autoFailure(err)
	}
	return showAuto(plan.ID, command.display)
}

func (run *autoExecution) preparedCandidate(index int, point *runExecution) (automation.Candidate, error) {
	configuration, err := run.runtime.ModelConfiguration(run.ctx, point.model)
	if err != nil {
		return automation.Candidate{}, err
	}
	if err := autoCandidateScope(point.resolved.Info); err != nil {
		return automation.Candidate{}, err
	}
	candidate := automation.Candidate{ID: fmt.Sprintf("candidate-%d", index+1), Model: point.model, ArtifactDigest: point.resolved.Identity.RuntimeBoundDigest(), ModelConfigurationSHA256: configuration.SHA256}
	return candidate, nil
}
