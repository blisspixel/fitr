package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/automation"
	"github.com/blisspixel/fitr/internal/autoruntime"
	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/role"
)

// These fixtures exercise signed evidence and connected lifecycle transitions.
// They never launch a runtime or establish actual model/workflow quality.
type autoLifecycleFixture struct {
	run    *autoExecution
	points []*record.Record
}

func autoLifecycleMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func autoLifecycleHash(text string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(text)))
}

func newAutoLifecycleFixture(t *testing.T, previous *autoLifecycleFixture, adoption string, speeds [2]float64) *autoLifecycleFixture {
	t.Helper()
	if previous == nil {
		root, err := filepath.EvalSymlinks(t.TempDir())
		autoLifecycleMust(t, err)
		t.Setenv("FITR_RESULTS", root)
		t.Setenv("FITR_BACKEND", "invalid-must-not-run")
	}
	roles, records := autoStores()
	spec := initialRoleSpec("coding", "structured_output", 0.5, 22, eval.NumCtx, 30)
	minimum := 1.0
	spec.Decision.Requirements = append(spec.Decision.Requirements, decision.Requirement{
		ID: "speed", Performance: &decision.PerformanceRequirement{Metric: decision.MetricDecodeTPS, AtLeast: &minimum},
	})
	spec.Preferences = []role.Preference{{Requirement: "speed", Weight: 1, Worst: 0, Best: 100}}
	_, err := roles.Define(spec)
	autoLifecycleMust(t, err)
	life, err := roles.LoadLifecycle(spec.Name)
	autoLifecycleMust(t, err)
	plan := autoLifecyclePlan(t, spec, life, records.Dir, adoption)
	f := &autoLifecycleFixture{run: &autoExecution{ctx: t.Context(), roles: roles, records: records, plan: plan, display: render.New("none")}}
	for index, speed := range speeds {
		f.points = append(f.points, f.point(t, index, speed, nil))
	}
	first := f.points[0]
	f.run.plan.Provenance = *first.Manifest.Provenance
	f.run.plan.SoftwareSHA256 = first.Manifest.Provenance.SoftwareBuildSHA256
	f.run.plan.TaskSetSHA256 = first.Manifest.Provenance.TaskSetSHA256
	f.run.plan.SpecSHA256 = first.Manifest.Provenance.SpecSHA256
	f.run.plan.DeviceSHA256, err = autoDeviceDigest(first.Device)
	autoLifecycleMust(t, err)
	for index, point := range f.points {
		f.run.plan.Candidates[index].ArtifactDigest = point.Manifest.Model.RuntimeBoundDigest()
	}
	autoLifecycleMust(t, f.run.plan.Seal(time.Now().Add(-time.Minute)))
	f.run.session, err = (automation.Store{Results: records.Dir}).Create(f.run.plan)
	autoLifecycleMust(t, err)
	autoLifecycleCleanup(t, f.run.session)
	return f
}

func autoLifecyclePlan(t *testing.T, spec role.Spec, life role.Lifecycle, root, adoption string) automation.Plan {
	t.Helper()
	id, err := automation.NewID()
	autoLifecycleMust(t, err)
	tasks, err := eval.LoadSpec()
	autoLifecycleMust(t, err)
	envelope, err := eval.PlanRequestEnvelope(tasks, eval.RequestEnvelopeOptions{
		Backend: "ollama", Level: "full", Repeats: 3, CheckRepeats: 3, ContextProbe: true,
	})
	autoLifecycleMust(t, err)
	envelopeSHA, err := envelope.Digest()
	autoLifecycleMust(t, err)
	plan := automation.Plan{ID: id, Mode: "establish", Adoption: adoption, Spec: spec, RoleRevision: selectionRevision(spec),
		LifecycleSHA256: life.Digest, IncumbentSHA256: life.IncumbentSHA256,
		Runtime: autoruntime.Spec{Schema: autoruntime.SpecSchema, Executable: filepath.Join(root, "ollama.exe"),
			ModelStore: filepath.Join(root, "models"), ExecutableSHA256: autoLifecycleHash("fixture executable"),
			LibrariesSHA256: autoLifecycleHash("fixture libraries"), RuntimeVersion: "0.32.14", NumCtx: eval.NumCtx, KVCacheType: "f16"},
		Profile: "default", Repeats: 3, SeedSet: id, EnvelopeSHA256: envelopeSHA,
		PointRequests: envelope.MaxRequests, PointRequestedOutputTokens: envelope.MaxRequestedOutputTokens,
		Limits: automation.Limits{MaxRequests: 4 * envelope.MaxRequests, MaxRequestedOutputTokens: 4 * envelope.MaxRequestedOutputTokens,
			MaxPoints: 4, WallSeconds: 3600, ConfirmationWallSeconds: 1200}}
	if life.IncumbentSHA256 != "" {
		plan.Mode = "improve"
	}
	for _, name := range []string{"first", "second"} {
		plan.Candidates = append(plan.Candidates, automation.Candidate{ID: name, Model: name,
			ModelConfigurationSHA256: autoLifecycleHash(name + " configuration")})
	}
	return plan
}

func autoLifecycleCleanup(t *testing.T, session *automation.Session) {
	t.Helper()
	t.Cleanup(func() { autoLifecycleMust(t, session.Close()) })
}

func (f *autoLifecycleFixture) point(t *testing.T, index int, speed float64, confirmation *role.ConfirmationPlan) *record.Record {
	t.Helper()
	plan := f.run.plan
	candidate := plan.Candidates[index]
	point := cliRoleConfirmationRecord(t, candidate.Model, speed, time.Now())
	point.Device.Config = autoConfiguration(plan.Runtime)
	point.Device.GPUBackend = "vulkan"
	point.Device.InferenceDevice = "GPU"
	point.SeedSet = plan.SeedSet
	counts := map[string]int{}
	for trial := range point.Checks {
		check := &point.Checks[trial]
		check.Seed = eval.InstanceSeed(point.SeedSet, check.TaskID, counts[check.TaskID])
		counts[check.TaskID]++
	}
	profile, launch, err := plan.Runtime.ProfileDigests()
	autoLifecycleMust(t, err)
	point.RuntimeBinding = &record.RuntimeBinding{Schema: record.RuntimeBindingSchema, Kind: "owned_ollama",
		ProfileSHA256: profile, LaunchConfigurationSHA256: launch, ExecutableSHA256: plan.Runtime.ExecutableSHA256,
		ModelConfigurationSHA256: candidate.ModelConfigurationSHA256, ArtifactDigest: point.Manifest.Model.RuntimeBoundDigest(),
		RuntimeVersion: plan.Runtime.RuntimeVersion, OwnershipSHA256: autoLifecycleHash(plan.ID + point.StableRunID())}
	point.RunID = ""
	autoLifecycleMust(t, prepareMockEvidence(point))
	if confirmation != nil {
		cliRoleFreshPoint(t, point, *confirmation, index, time.Now())
	}
	autoLifecycleMust(t, point.RuntimeBinding.ValidateFor(point.Manifest.Model))
	if issue := point.EvidenceIntegrityIssue(); issue != "" {
		t.Fatal(issue)
	}
	return point
}

func (f *autoLifecycleFixture) state(t *testing.T) automation.State {
	t.Helper()
	_, state, err := f.run.session.Snapshot()
	autoLifecycleMust(t, err)
	return state
}

func (f *autoLifecycleFixture) savePoint(t *testing.T, index int, point *record.Record, complete bool) {
	t.Helper()
	phase := f.state(t).Phase
	autoLifecycleMust(t, f.run.session.Append(automation.Event{
		Action: "point_started", Phase: phase, Point: index + 1, RunID: point.StableRunID(),
	}, time.Now()))
	_, err := f.run.session.Reserve(t.Context(), ollama.InferenceRequest{Kind: "generate", Model: point.Model, MaxOutputTokens: 10})
	autoLifecycleMust(t, err)
	group, err := f.run.group(phase)
	autoLifecycleMust(t, err)
	_, err = group.Save(point)
	autoLifecycleMust(t, err)
	if complete {
		autoLifecycleMust(t, f.run.session.Append(automation.Event{
			Action: "point_completed", Phase: phase, Point: index + 1, RunID: point.StableRunID(), EvidenceSHA256: point.Completion.EvidenceSHA256,
		}, time.Now()))
	}
}

func (f *autoLifecycleFixture) compare(t *testing.T) {
	t.Helper()
	for index, point := range f.points {
		f.savePoint(t, index, point, true)
	}
	group, err := f.run.group("exploration")
	autoLifecycleMust(t, err)
	ref, err := group.Close()
	autoLifecycleMust(t, err)
	autoLifecycleMust(t, f.run.session.Append(automation.Event{Action: "exploration_closed", StoreRef: &ref}, time.Now()))
	autoLifecycleMust(t, f.run.compareExploration(f.state(t)))
	if state := f.state(t); state.Phase != "confirmation" || state.Confirmation == nil {
		t.Fatalf("comparison did not seal fresh confirmation: %+v", state)
	}
}

func (f *autoLifecycleFixture) confirm(t *testing.T, speeds [2]float64) {
	t.Helper()
	state := f.state(t)
	for index, speed := range speeds {
		f.savePoint(t, index, f.point(t, index, speed, state.Confirmation), true)
	}
	group, err := f.run.group("confirmation")
	autoLifecycleMust(t, err)
	ref, err := group.Close()
	autoLifecycleMust(t, err)
	autoLifecycleMust(t, f.run.finishConfirmation(ref))
}

func (f *autoLifecycleFixture) selection(t *testing.T) role.SelectionStatus {
	t.Helper()
	status, err := f.run.roles.ReviewSelection(f.run.plan.Spec.Name, f.run.records, time.Now())
	autoLifecycleMust(t, err)
	return status
}

func (f *autoLifecycleFixture) bundle(t *testing.T) role.ConfirmationBundle {
	t.Helper()
	path := filepath.Join(f.run.roles.Dir, ".confirmations", strings.TrimPrefix(f.state(t).Confirmation.PlanSHA256, "sha256:")+".json")
	bundle, err := role.LoadConfirmationBundle(path)
	autoLifecycleMust(t, err)
	return bundle
}

func TestAutoLifecycleManualAndDeclaredAdoption(t *testing.T) {
	for _, policy := range []string{"manual", "confirmed-only"} {
		t.Run(policy, func(t *testing.T) {
			f := newAutoLifecycleFixture(t, nil, policy, [2]float64{80, 20})
			f.compare(t)
			f.confirm(t, [2]float64{80, 20})
			if f.bundle(t).Report.State != "confirmed" {
				t.Fatal("fixture did not confirm the preselected candidate")
			}
			if state := f.state(t); state.Phase != "awaiting_adoption" || state.Outcome != "" {
				t.Fatalf("fresh comparison state = %+v", state)
			}
			if f.selection(t).State != "unselected" {
				t.Fatal("confirmation adopted without the declared policy")
			}
			autoLifecycleMust(t, autoAdoptIfDeclared(f.run.session))
			if policy == "manual" {
				if f.selection(t).State != "unselected" {
					t.Fatal("manual mode adopted implicitly")
				}
				autoLifecycleMust(t, adoptAuto(f.run.session))
			}
			selection := f.selection(t)
			if f.state(t).Outcome != "adopted" || selection.State != "qualified" || selection.Selection.Selected.Model.Resolved != "first" || selection.Selection.Selected.StoreRef == nil {
				t.Fatalf("managed selection missing: %+v", selection)
			}
			autoLifecycleMust(t, adoptAuto(f.run.session))
			if f.selection(t).ReceiptSHA256 != selection.ReceiptSHA256 {
				t.Fatal("idempotent adoption replaced the receipt")
			}
			library, err := f.run.roles.Load(f.run.plan.Spec.Name)
			autoLifecycleMust(t, err)
			if len(library.Candidates) != 0 {
				t.Fatal("managed cycle modified ordinary role attachments")
			}
		})
	}
}

func autoLifecycleIncumbent(t *testing.T) (*autoLifecycleFixture, role.SelectionStatus) {
	t.Helper()
	f := newAutoLifecycleFixture(t, nil, "confirmed-only", [2]float64{80, 20})
	f.compare(t)
	f.confirm(t, [2]float64{80, 20})
	autoLifecycleMust(t, autoAdoptIfDeclared(f.run.session))
	return f, f.selection(t)
}

func TestAutoLifecycleUnexpectedWinnerRetainsOriginalManagedIncumbent(t *testing.T) {
	first, incumbent := autoLifecycleIncumbent(t)
	original, err := os.ReadFile(incumbent.Selection.Selected.Attachment.Path)
	autoLifecycleMust(t, err)
	next := newAutoLifecycleFixture(t, first, "confirmed-only", [2]float64{20, 80})
	next.compare(t)
	if next.selection(t).ReceiptSHA256 != incumbent.ReceiptSHA256 {
		t.Fatal("exploration replaced the incumbent")
	}
	next.confirm(t, [2]float64{80, 20})
	if next.bundle(t).Report.State != "unexpected-winner" {
		t.Fatalf("fresh result = %s", next.bundle(t).Report.State)
	}
	if next.state(t).Outcome != "incumbent_retained" {
		t.Fatalf("unexpected winner outcome = %s", next.state(t).Outcome)
	}
	if err := adoptAuto(next.run.session); err == nil {
		t.Fatal("post-result winner hunting adopted an unexpected winner")
	}
	status := next.selection(t)
	if status.State != "qualified" || status.ReceiptSHA256 != incumbent.ReceiptSHA256 || *status.Selection.Selected.StoreRef != *incumbent.Selection.Selected.StoreRef {
		t.Fatal("failed challenger invalidated the incumbent")
	}
	current, err := os.ReadFile(incumbent.Selection.Selected.Attachment.Path)
	autoLifecycleMust(t, err)
	if string(current) != string(original) {
		t.Fatal("challenger changed original signed evidence bytes")
	}
}

func TestAutoLifecycleQualityFloorPrecedesFasterPreference(t *testing.T) {
	f := newAutoLifecycleFixture(t, nil, "manual", [2]float64{80, 20})
	for index := range f.points[0].Checks {
		f.points[0].Checks[index].Pass = false
		f.points[0].Checks[index].Outcome = eval.OutcomeFail
	}
	autoLifecycleMust(t, prepareMockEvidence(f.points[0]))
	f.compare(t)
	if f.state(t).Confirmation.ChosenEvidenceSHA256 != f.points[1].Completion.EvidenceSHA256 {
		t.Fatal("speed preference bypassed the mandatory quality floor")
	}
	f.run.fail(context.Canceled)
	if f.selection(t).State != "unselected" {
		t.Fatal("exploration screening established a role selection")
	}
}

func TestAutoLifecycleSuccessfulChallengeKeepsOriginalRollbackEvidence(t *testing.T) {
	first, incumbent := autoLifecycleIncumbent(t)
	next := newAutoLifecycleFixture(t, first, "confirmed-only", [2]float64{20, 80})
	next.compare(t)
	next.confirm(t, [2]float64{20, 80})
	autoLifecycleMust(t, autoAdoptIfDeclared(next.run.session))
	selected := next.selection(t)
	if selected.State != "qualified" || selected.Selection.Selected.Model.Resolved != "second" || selected.ReceiptSHA256 == incumbent.ReceiptSHA256 {
		t.Fatal("confirmed challenger did not replace the fitr selection")
	}
	_, err := next.run.roles.RollbackSelection(next.run.plan.Spec.Name, incumbent.ReceiptSHA256, next.run.records, selected.LifecycleDigest, time.Now())
	autoLifecycleMust(t, err)
	rolled := next.selection(t)
	if rolled.State != "qualified" || rolled.Selection.Selected.Attachment.EvidenceSHA256 != incumbent.Selection.Selected.Attachment.EvidenceSHA256 || *rolled.Selection.Selected.StoreRef != *incumbent.Selection.Selected.StoreRef {
		t.Fatal("successful adoption lost the original evidence required for rollback")
	}
}

func TestAutoLifecycleRejectsForeignEventsBeforeMoreWork(t *testing.T) {
	for _, phase := range []string{"exploration", "confirmation"} {
		t.Run(phase, func(t *testing.T) {
			f := newAutoLifecycleFixture(t, nil, "manual", [2]float64{80, 20})
			if phase == "confirmation" {
				f.compare(t)
			}
			before := f.state(t)
			foreign, err := role.NewConfirmationPlan(f.run.plan.Spec, f.points, f.points[0].Completion.EvidenceSHA256, time.Now())
			autoLifecycleMust(t, err)
			life, err := f.run.roles.LoadLifecycle(f.run.plan.Spec.Name)
			autoLifecycleMust(t, err)
			_, err = f.run.roles.IssueConfirmation(foreign, life.Digest, time.Now())
			autoLifecycleMust(t, err)
			if _, err := f.run.currentRole(); err == nil {
				t.Fatal("foreign lifecycle event was accepted under the original authorization")
			}
			after := f.state(t)
			if after.Requests != before.Requests || after.Points != before.Points || after.Phase != before.Phase {
				t.Fatal("foreign state changed session progress")
			}
		})
	}
}

func TestAutoLifecycleInterruptedConfirmationCannotRetryOrAdopt(t *testing.T) {
	first, incumbent := autoLifecycleIncumbent(t)
	next := newAutoLifecycleFixture(t, first, "confirmed-only", [2]float64{20, 80})
	next.compare(t)
	next.savePoint(t, 0, next.point(t, 0, 20, next.state(t).Confirmation), false)
	before := next.state(t)
	if err := resumeAuto(context.Background(), next.run.session, "none"); err == nil {
		t.Fatal("interrupted confirmation resumed")
	}
	after := next.state(t)
	if after.Outcome != "failed" || after.Confirmation.PlanSHA256 != before.Confirmation.PlanSHA256 || after.Requests != before.Requests {
		t.Fatal("interrupted attempt lost its sealed identity or charged work")
	}
	if err := next.run.session.Append(automation.Event{Action: "confirmation_started", Confirmation: before.Confirmation}, time.Now()); err == nil {
		t.Fatal("terminal session accepted another confirmation")
	}
	if err := adoptAuto(next.run.session); err == nil {
		t.Fatal("interrupted confirmation adopted")
	}
	status := next.selection(t)
	if status.State != "qualified" || status.ReceiptSHA256 != incumbent.ReceiptSHA256 || status.LastAttempt.Action != "failed" {
		t.Fatal("interruption did not retain the qualified incumbent")
	}
}

func TestAutoLifecycleReconcilesAdoptionBeforeJournalCompletion(t *testing.T) {
	f := newAutoLifecycleFixture(t, nil, "manual", [2]float64{80, 20})
	f.compare(t)
	f.confirm(t, [2]float64{80, 20})
	state := f.state(t)
	bundle := f.bundle(t)
	life, err := f.run.roles.LoadLifecycle(f.run.plan.Spec.Name)
	autoLifecycleMust(t, err)
	_, err = f.run.roles.AdoptManagedConfirmation(f.run.plan.Spec.Name, bundle.Plan.PlanSHA256, bundle, f.run.records, *state.ConfirmationStore, life.Digest, time.Now())
	autoLifecycleMust(t, err)
	adopted := f.selection(t)
	if f.state(t).Outcome != "" {
		t.Fatal("fixture did not stop between role and journal writes")
	}
	autoLifecycleMust(t, adoptAuto(f.run.session))
	if f.state(t).Outcome != "adopted" || f.selection(t).ReceiptSHA256 != adopted.ReceiptSHA256 {
		t.Fatal("reconciliation created a different selection")
	}
}

func TestAutoLifecycleRecoversOnlyExactSavedExplorationWithoutRefund(t *testing.T) {
	f := newAutoLifecycleFixture(t, nil, "manual", [2]float64{80, 20})
	f.savePoint(t, 0, f.points[0], false)
	before := f.state(t)
	autoLifecycleMust(t, f.run.session.Close())
	session, err := (automation.Store{Results: f.run.records.Dir}).Open(f.run.plan.ID)
	autoLifecycleMust(t, err)
	f.run.session = session
	autoLifecycleCleanup(t, session)
	autoLifecycleMust(t, f.run.recoverSavedExploration(f.state(t)))
	after := f.state(t)
	if after.ActivePoint != 0 || len(after.CompletedExploration) != 1 || after.Requests != before.Requests || after.RequestedOutputTokens != before.RequestedOutputTokens || after.CompletedExploration[0].EvidenceSHA256 != f.points[0].Completion.EvidenceSHA256 {
		t.Fatal("saved completion recovery changed evidence identity or charged work")
	}
	group, err := f.run.group("exploration")
	autoLifecycleMust(t, err)
	if _, err := group.Read(f.points[0].Model); err == nil {
		t.Fatal("recovery promoted an open partial group")
	}
}

func TestAutoLifecycleRecoveryRejectsValidSignedDifferentProtocol(t *testing.T) {
	for _, mutation := range []string{"seed", "device", "runtime"} {
		t.Run(mutation, func(t *testing.T) {
			f := newAutoLifecycleFixture(t, nil, "manual", [2]float64{80, 20})
			point := f.points[0]
			switch mutation {
			case "seed":
				point.SeedSet = "different-exploration"
			case "device":
				point.Device.Host = "different-host"
			case "runtime":
				point.RuntimeBinding.ProfileSHA256 = autoLifecycleHash("different runtime profile")
			}
			autoLifecycleMust(t, prepareMockEvidence(point))
			f.savePoint(t, 0, point, false)
			before := f.state(t)
			if err := f.run.recoverSavedExploration(before); err == nil {
				t.Fatal("signed evidence from another protocol completed the point")
			}
			after := f.state(t)
			if len(after.CompletedExploration) != 0 || after.Requests != before.Requests || after.ActiveRunID != before.ActiveRunID {
				t.Fatal("rejected recovery changed progress or request accounting")
			}
		})
	}
}
