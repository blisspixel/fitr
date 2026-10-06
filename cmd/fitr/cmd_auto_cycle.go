package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/automation"
	"github.com/blisspixel/fitr/internal/autoruntime"
	"github.com/blisspixel/fitr/internal/lock"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/role"
)

func (run *autoExecution) group(phase string) (record.ManagedStore, error) {
	suffix := "-explore"
	if phase == "confirmation" {
		suffix = "-confirm"
	} else if phase != "exploration" {
		return record.ManagedStore{}, errors.New("invalid auto evidence phase")
	}
	return record.CreateManagedStore(run.records, record.ManagedStoreSpec{Schema: record.ManagedStoreSpecSchema, ID: run.plan.ID + suffix, SessionID: run.plan.ID, Purpose: phase})
}

func (run *autoExecution) collect() error {
	for {
		_, state, err := run.session.Snapshot()
		if err != nil {
			return err
		}
		if state.Outcome != "" {
			return nil
		}
		if err := run.ctx.Err(); err != nil {
			return err
		}
		if _, err := run.currentRole(); err != nil {
			return err
		}
		switch state.Phase {
		case "exploration", "confirmation":
			if err := run.collectPhase(state); err != nil {
				return err
			}
		case "comparing":
			if err := run.compareExploration(state); err != nil {
				return err
			}
		case "awaiting_adoption":
			return nil
		default:
			return errors.New("unknown auto phase")
		}
	}
}

func (run *autoExecution) collectPhase(state automation.State) error {
	ctx, cancel := context.WithDeadline(run.ctx, state.Deadline(run.plan))
	defer cancel()
	phaseRun := *run
	phaseRun.ctx = ctx
	return phaseRun.collectPhaseUnderDeadline(state)
}

func (run *autoExecution) collectPhaseUnderDeadline(state automation.State) error {
	group, err := run.group(state.Phase)
	if err != nil {
		return err
	}
	// The sibling is opened before any point starts so a crash can still see
	// it. It stays open until every point has both records: the admission
	// hook charges the active battery point, and that point cannot complete
	// without the context evidence digest.
	var contextStore record.ManagedStore
	if run.plan.ContextPolicy != nil {
		contextStore, err = run.contextGroup(state.Phase)
		if err != nil {
			return err
		}
	}
	completed := state.CompletedExploration
	if state.Phase == "confirmation" {
		completed = state.CompletedConfirmation
	}
	if state.ActivePoint != 0 {
		return errors.New("an interrupted point must be resolved before collection")
	}
	if err := run.collectRemainingPoints(state, completed, group, contextStore); err != nil {
		return err
	}
	if run.plan.ContextPolicy != nil {
		if _, err := contextStore.Close(); err != nil {
			return err
		}
	}
	ref, err := group.Close()
	if err != nil {
		return err
	}
	if state.Phase == "exploration" {
		return run.session.Append(automation.Event{Action: "exploration_closed", StoreRef: &ref}, time.Now())
	}
	return run.finishConfirmation(ref)
}

func (run *autoExecution) collectRemainingPoints(state automation.State, completed []automation.Event, group, contextStore record.ManagedStore) error {
	for index := len(completed); index < len(run.plan.Candidates); index++ {
		if err := run.collectCandidatePoint(state, index, group, contextStore); err != nil {
			return err
		}
	}
	return nil
}

func (run *autoExecution) collectCandidatePoint(state automation.State, index int, group, contextStore record.ManagedStore) error {
	if _, err := run.currentRole(); err != nil {
		return err
	}
	runID, err := record.NewRunID()
	if err != nil {
		return err
	}
	if err := run.session.Append(automation.Event{Action: "point_started", Phase: state.Phase, Point: index + 1, RunID: runID}, time.Now()); err != nil {
		return err
	}
	if _, state, err = run.session.Snapshot(); err != nil {
		return err
	}
	candidate := run.plan.Candidates[index]
	run.runtime.BeginLoadObservation()
	run.display.Phase(state.Phase, fmt.Sprintf("%d/%d  %s", index+1, len(run.plan.Candidates), candidate.Model))
	point, err := executeUnderLease(run.ctx, run.backend, candidate.Model, run.pointOptions(index, state), run.display, run.lease)
	if err != nil {
		return err
	}
	if err := run.recheckPoint(candidate, point); err != nil {
		return err
	}
	if _, err := group.Save(point); err != nil {
		return err
	}
	return run.completeCandidatePoint(state, index, point, contextStore)
}

func (run *autoExecution) completeCandidatePoint(state automation.State, index int, point *record.Record, contextStore record.ManagedStore) error {
	finished := automation.Event{Action: "point_completed", Phase: state.Phase, Point: index + 1, RunID: point.StableRunID(), EvidenceSHA256: point.Completion.EvidenceSHA256}
	if run.plan.ContextPolicy != nil {
		contextPoint, err := run.collectContextPoint(index, state, run.plan.Candidates[index])
		if err != nil {
			return err
		}
		if err := saveContextBesideBattery(contextStore, contextPoint, point); err != nil {
			return err
		}
		finished.ContextEvidenceSHA256 = contextPoint.Completion.EvidenceSHA256
	}
	return run.session.Append(finished, time.Now())
}

func saveContextBesideBattery(store record.ManagedStore, contextPoint, battery *record.Record) error {
	if contextPoint.Completion == nil {
		return errors.New("context point has no completion")
	}
	if _, err := store.Save(contextPoint); err != nil {
		return err
	}
	if contextPoint.StableRunID() == battery.StableRunID() || contextPoint.Completion.EvidenceSHA256 == battery.Completion.EvidenceSHA256 {
		return errors.New("context point reused the battery run")
	}
	return nil
}

func (run *autoExecution) recheckPoint(candidate automation.Candidate, point *record.Record) error {
	if err := run.ctx.Err(); err != nil {
		return err
	}
	if _, err := run.currentRole(); err != nil {
		return err
	}
	identity, err := resolveRunModel(run.ctx, run.backend, candidate.Model)
	if err != nil {
		return err
	}
	if identity.Identity.RuntimeBoundDigest() != candidate.ArtifactDigest {
		return errors.New("auto artifact changed during collection")
	}
	configuration, err := run.runtime.ModelConfiguration(run.ctx, candidate.Model)
	if err != nil {
		return err
	}
	if configuration.SHA256 != candidate.ModelConfigurationSHA256 {
		return errors.New("auto model configuration changed during collection")
	}
	if _, err := autoruntime.Prepare(run.ctx, run.plan.Runtime); err != nil {
		return err
	}
	if accel := run.runtime.Accel(run.ctx); accel == "" || accel != point.Device.GPUBackend {
		return errors.New("owned compute backend changed or became unverified during collection")
	}
	if point.RuntimeBinding == nil || point.RuntimeBinding.ArtifactDigest != candidate.ArtifactDigest || point.RuntimeBinding.ModelConfigurationSHA256 != configuration.SHA256 {
		return errors.New("auto completion lost its tested runtime binding")
	}
	if issue := point.EvidenceIntegrityIssue(); issue != "" {
		return errors.New(issue)
	}
	return nil
}

func autoGroupPoints(plan automation.Plan, records record.Store, ref *record.ManagedStoreRef, phase string, events []automation.Event) ([]*record.Record, error) {
	if ref == nil {
		return nil, errors.New("auto phase has no sealed evidence store")
	}
	group, err := record.ResolveManagedStore(records, *ref)
	if err != nil {
		return nil, err
	}
	spec, err := group.Spec()
	if err != nil {
		return nil, err
	}
	if spec.SessionID != plan.ID || spec.Purpose != phase || len(events) != len(plan.Candidates) {
		return nil, errors.New("auto store belongs to another session or incomplete schedule")
	}
	points := make([]*record.Record, 0, len(plan.Candidates))
	for index, candidate := range plan.Candidates {
		point, err := group.Read(candidate.Model)
		if err != nil {
			return nil, err
		}
		if point.Completion == nil || point.StableRunID() != events[index].RunID || point.Completion.EvidenceSHA256 != events[index].EvidenceSHA256 || point.RuntimeBinding == nil || point.RuntimeBinding.ArtifactDigest != candidate.ArtifactDigest || point.RuntimeBinding.ModelConfigurationSHA256 != candidate.ModelConfigurationSHA256 {
			return nil, errors.New("auto stored point differs from its original signed identity")
		}
		seed := plan.SeedSet
		if phase == "confirmation" && point.Experiment != nil {
			seed = point.SeedSet // the confirmation plan independently checks its fresh seed
		}
		if err := validateAutoSavedPoint(plan, point, seed); err != nil {
			return nil, err
		}
		points = append(points, point)
	}
	return points, nil
}

func (run *autoExecution) compareExploration(state automation.State) error {
	points, err := autoGroupPoints(run.plan, run.records, state.ExplorationStore, "exploration", state.CompletedExploration)
	if err != nil {
		return err
	}
	models := make([]string, len(run.plan.Candidates))
	for index, candidate := range run.plan.Candidates {
		models[index] = candidate.Model
	}
	contextRef, contextPoints, err := run.explorationContext(state)
	if err != nil {
		return err
	}
	review, err := role.ReviewManagedWithContext(run.plan.Spec, run.records, *state.ExplorationStore, contextRef, models, time.Now())
	if err != nil {
		return err
	}
	return run.issueExplorationConfirmation(points, contextPoints, review)
}

func (run *autoExecution) explorationContext(state automation.State) (*record.ManagedStoreRef, []*record.Record, error) {
	if run.plan.ContextPolicy == nil {
		return nil, nil, nil
	}
	ref, loaded, err := run.phaseContext("exploration", run.plan.SeedSet, run.plan.ContextPlanSHA256, state.CompletedExploration)
	if err != nil {
		return nil, nil, err
	}
	return &ref, loaded, nil
}

func (run *autoExecution) issueExplorationConfirmation(points, contextPoints []*record.Record, review role.ReviewReport) error {
	chosen := review.Lead
	if review.State == "single-qualified" {
		for _, candidate := range review.Candidates {
			if candidate.State == "eligible" {
				chosen = candidate.ID
			}
		}
	}
	if chosen == "" {
		outcome := "unresolved"
		switch review.State {
		case "no-qualified-candidate":
			outcome = "no_qualified"
		case "tradeoff":
			outcome = "overlap"
		}
		run.display.Note(review.Next, "warn")
		return run.session.Append(automation.Event{Action: "finished", Outcome: outcome}, time.Now())
	}
	var plan role.ConfirmationPlan
	var err error
	if run.plan.ContextPolicy != nil {
		plan, err = role.NewConfirmationPlanWithContext(run.plan.Spec, points, contextPoints, chosen, time.Now())
	} else {
		plan, err = role.NewConfirmationPlan(run.plan.Spec, points, chosen, time.Now())
	}
	if err != nil {
		return err
	}
	life, err := run.currentRole()
	if err != nil {
		return err
	}
	// Recording this before issuance deliberately closes the adoption path if
	// interrupted between the independent session and role state writes.
	if err := run.session.Append(automation.Event{Action: "confirmation_started", Confirmation: &plan}, time.Now()); err != nil {
		return err
	}
	life, err = run.roles.IssueConfirmation(plan, life.Digest, time.Now())
	if err != nil {
		return err
	}
	_, err = run.roles.BeginConfirmation(run.plan.Spec.Name, plan.PlanSHA256, life.Digest, time.Now())
	return err
}

func (run *autoExecution) finishConfirmation(ref record.ManagedStoreRef) error {
	_, state, err := run.session.Snapshot()
	if err != nil {
		return err
	}
	points, err := autoGroupPoints(run.plan, run.records, &ref, "confirmation", state.CompletedConfirmation)
	if err != nil {
		return err
	}
	var contextPoints []*record.Record
	var contextRef record.ManagedStoreRef
	if run.plan.ContextPolicy != nil {
		if state.Confirmation == nil {
			return errors.New("context confirmation has no sealed plan")
		}
		contextRef, contextPoints, err = run.phaseContext("confirmation", state.Confirmation.SeedSet, state.Confirmation.ContextPlanSHA256, state.CompletedConfirmation)
		if err != nil {
			return err
		}
	}
	bundle, err := role.NewConfirmationBundleWithContext(*state.Confirmation, points, contextPoints, time.Now())
	if err != nil {
		return err
	}
	life, err := run.currentRole()
	if err != nil {
		return err
	}
	if _, err := run.roles.SaveConfirmationBundle(bundle); err != nil {
		return err
	}
	if run.plan.ContextPolicy != nil {
		_, err = run.roles.FinishManagedConfirmationWithContext(run.plan.Spec.Name, bundle.Plan.PlanSHA256, bundle, run.records, ref, contextRef, life.Digest, time.Now())
	} else {
		_, err = run.roles.FinishManagedConfirmation(run.plan.Spec.Name, bundle.Plan.PlanSHA256, bundle, run.records, ref, life.Digest, time.Now())
	}
	if err != nil {
		return err
	}
	if err := run.session.Append(automation.Event{Action: "confirmation_closed", StoreRef: &ref}, time.Now()); err != nil {
		return err
	}
	if bundle.Report.State != "confirmed" {
		run.display.Note("Fresh confirmation did not establish the preselected choice. The incumbent is retained.", "warn")
		return run.session.Append(automation.Event{Action: "finished", Outcome: "incumbent_retained"}, time.Now())
	}
	return nil
}

func (run *autoExecution) fail(cause error) {
	_, state, err := run.session.Snapshot()
	if err != nil || state.Outcome != "" {
		return
	}
	outcome := "failed"
	if errors.Is(cause, context.Canceled) {
		outcome = "cancelled"
	} else if errors.Is(cause, automation.ErrBudget) || errors.Is(cause, context.DeadlineExceeded) {
		outcome = "budget_exhausted"
	} else if errors.Is(cause, automation.ErrStale) {
		outcome = "stale"
	}
	run.closeFailedConfirmation(state, outcome)
	if err := run.session.Append(automation.Event{Action: "finished", Outcome: outcome}, time.Now()); err != nil {
		fmt.Fprintf(os.Stderr, "  could not record auto stop: %s\n", terminalText(err.Error()))
	}
}

func resumeAuto(ctx context.Context, session *automation.Session, mode string) error {
	journal, state, err := session.Snapshot()
	if err != nil {
		return err
	}
	roles, records := autoStores()
	run := &autoExecution{session: session, plan: journal.Plan, roles: roles, records: records}
	if state.Outcome != "" {
		return errors.New("this auto session is terminal; a new start requires a new explicit investigation")
	}
	if state.Phase == "confirmation" {
		err := errors.New("interrupted confirmation cannot resume or gain a new seed in this session")
		run.fail(err)
		return err
	}
	if state.Phase == "awaiting_adoption" {
		return nil
	}
	now := time.Now()
	if now.Before(state.LastObservedAt) || !now.Before(state.Deadline(journal.Plan)) {
		return errors.New("auto session clock or original wall allowance no longer permits work")
	}
	if _, err := run.currentRole(); err != nil {
		return err
	}
	if state.ActivePoint != 0 {
		if err := run.recoverSavedExploration(state); err != nil {
			run.fail(err)
			return err
		}
	}
	expires, _ := time.Parse(time.RFC3339Nano, journal.Plan.ExpiresAt)
	ctx, cancel := context.WithDeadline(ctx, expires)
	defer cancel()
	return resumeAutoOwned(ctx, run, mode)
}

func (run *autoExecution) recoverSavedExploration(state automation.State) error {
	group, err := run.group("exploration")
	if err != nil {
		return err
	}
	candidate := run.plan.Candidates[state.ActivePoint-1]
	point, err := group.ReadSaved(candidate.Model)
	if err != nil {
		return errors.New("interrupted exploration has no complete saved point; requests remain charged and this session cannot retry")
	}
	if point.StableRunID() != state.ActiveRunID || point.Completion == nil || point.RuntimeBinding == nil || point.RuntimeBinding.ArtifactDigest != candidate.ArtifactDigest || point.RuntimeBinding.ModelConfigurationSHA256 != candidate.ModelConfigurationSHA256 {
		return errors.New("saved exploration does not match the interrupted point")
	}
	if err := validateAutoSavedPoint(run.plan, point, run.plan.SeedSet); err != nil {
		return err
	}
	event := automation.Event{Action: "point_completed", Phase: "exploration", Point: state.ActivePoint, RunID: point.StableRunID(), EvidenceSHA256: point.Completion.EvidenceSHA256}
	if run.plan.ContextPolicy != nil {
		evidence, err := run.savedExplorationContext(candidate, point)
		if err != nil {
			return err
		}
		event.ContextEvidenceSHA256 = evidence
	}
	return run.session.Append(event, time.Now())
}

func validateAutoSavedPoint(plan automation.Plan, point *record.Record, seed string) error {
	if issue := point.EvidenceIntegrityIssue(); issue != "" {
		return errors.New(issue)
	}
	manifest := point.Manifest
	if manifest == nil || manifest.Provenance == nil || *manifest.Provenance != plan.Provenance ||
		manifest.Profile != plan.Profile || manifest.Level != "full" || manifest.ExecutionPolicy != record.ExecutionDisabled ||
		manifest.Repeats != plan.Repeats || manifest.NumCtx != plan.Runtime.NumCtx || manifest.SeedSet != seed {
		return errors.New("saved auto evidence changed the fixed collection protocol")
	}
	deviceSHA, err := autoDeviceDigest(point.Device)
	if err != nil || deviceSHA != plan.DeviceSHA256 {
		return errors.New("saved auto evidence changed the physical device or owned settings")
	}
	profile, launch, err := plan.Runtime.ProfileDigests()
	if err != nil {
		return err
	}
	binding := point.RuntimeBinding
	if binding == nil || binding.ProfileSHA256 != profile || binding.LaunchConfigurationSHA256 != launch ||
		binding.ExecutableSHA256 != plan.Runtime.ExecutableSHA256 || binding.RuntimeVersion != plan.Runtime.RuntimeVersion {
		return errors.New("saved auto evidence changed the owned runtime profile")
	}
	return nil
}

func autoAdoptIfDeclared(session *automation.Session) error {
	journal, state, err := session.Snapshot()
	if err != nil {
		return err
	}
	if journal.Plan.Adoption == "confirmed-only" && state.Phase == "awaiting_adoption" && state.Outcome == "" {
		return adoptAuto(session)
	}
	return nil
}

func adoptAuto(session *automation.Session) error {
	journal, state, err := session.Snapshot()
	if err != nil {
		return err
	}
	if state.Outcome == "adopted" {
		return nil
	}
	if state.Outcome != "" || state.Phase != "awaiting_adoption" || state.Confirmation == nil || state.ConfirmationStore == nil {
		return errors.New("auto adoption requires this session's complete fresh confirmation")
	}
	now := time.Now()
	roles, records := autoStores()
	run := &autoExecution{session: session, plan: journal.Plan, roles: roles, records: records}
	life, err := roles.LoadLifecycle(journal.Plan.Spec.Name)
	if err != nil {
		return err
	}
	expected, repeated := autoAdoptionRetry(journal.Plan, state, life)
	if now.Before(state.LastObservedAt) || (!repeated && !now.Before(state.Deadline(journal.Plan))) {
		return errors.New("auto adoption is outside the original session allowance")
	}
	if !repeated {
		life, err = run.currentRole()
		if err != nil {
			return err
		}
		expected = life.Digest
	}
	if _, err := autoGroupPoints(journal.Plan, records, state.ConfirmationStore, "confirmation", state.CompletedConfirmation); err != nil {
		return err
	}
	path := filepath.Join(roles.Dir, ".confirmations", strings.TrimPrefix(state.Confirmation.PlanSHA256, "sha256:")+".json")
	bundle, err := role.LoadConfirmationBundle(path)
	if err != nil {
		return err
	}
	if bundle.Plan.PlanSHA256 != state.Confirmation.PlanSHA256 || bundle.Report.State != "confirmed" {
		return errors.New("auto confirmation bundle changed or did not confirm the preselected choice")
	}
	if _, err := roles.AdoptManagedConfirmationBefore(journal.Plan.Spec.Name, bundle.Plan.PlanSHA256, bundle, records, *state.ConfirmationStore, expected, now, state.Deadline(journal.Plan)); err != nil {
		return err
	}
	return session.Append(automation.Event{Action: "finished", Outcome: "adopted"}, time.Now())
}

// Reconcile a crash after the role's durable adoption but before the session
// journal advanced. The role API still revalidates the exact bundle, store and
// current qualification, and its idempotent transition checks the whole event.
func autoAdoptionRetry(plan automation.Plan, state automation.State, life role.Lifecycle) (string, bool) {
	if state.Confirmation == nil || len(life.Events) == 0 {
		return "", false
	}
	last := life.Events[len(life.Events)-1]
	if last.Action != "adopted" || last.Digest != life.IncumbentSHA256 || last.PlanSHA256 != state.Confirmation.PlanSHA256 {
		return "", false
	}
	at, err := time.Parse(time.RFC3339Nano, last.At)
	if err != nil || at.Before(state.LastObservedAt) || !at.Before(state.Deadline(plan)) {
		return "", false
	}
	return last.PreviousDigest, true
}

func (run *autoExecution) closeFailedConfirmation(state automation.State, outcome string) {
	if state.Confirmation == nil {
		return
	}
	life, loadErr := run.roles.LoadLifecycle(run.plan.Spec.Name)
	if loadErr != nil {
		return
	}
	lastAction := ""
	for _, event := range life.Events {
		if event.PlanSHA256 == state.Confirmation.PlanSHA256 {
			lastAction = event.Action
		}
	}
	if lastAction == "started" {
		status := "failed"
		if outcome == "cancelled" {
			status = "cancelled"
		}
		if _, err := run.roles.FinishConfirmation(run.plan.Spec.Name, state.Confirmation.PlanSHA256, status, nil, run.records, life.Digest, time.Now()); err != nil {
			fmt.Fprintf(os.Stderr, "  could not close confirmation: %s\n", terminalText(err.Error()))
		}
	}
}

func resumeAutoOwned(ctx context.Context, run *autoExecution, mode string) error {
	lease, err := lock.Acquire("eval", "resume auto "+run.plan.ID)
	if err != nil {
		return err
	}
	defer func() { _ = lease.Release() }()
	prepared, err := autoruntime.Prepare(ctx, run.plan.Runtime)
	if err != nil {
		return err
	}
	runtime, err := autoruntime.Start(ctx, prepared)
	if err != nil {
		return err
	}
	defer func() { _ = runtime.Close() }()
	display := render.New(mode)
	defer display.Close()
	backend := &ownedAutoBackend{Client: &ollama.Client{BaseURL: runtime.URL(), HTTP: runtime.HTTPClient(), Admission: run.session.Reserve}, runtime: runtime}
	run.ctx, run.runtime, run.backend, run.lease, run.display = ctx, runtime, backend, lease, display
	if err := run.collect(); err != nil {
		run.fail(err)
		return err
	}
	if err := runtime.Close(); err != nil {
		run.fail(err)
		return err
	}
	return autoAdoptIfDeclared(run.session)
}
