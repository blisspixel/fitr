package role

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/record"
)

func nextLifecycleRound(t *testing.T, prior lifecycleFixture, created time.Time, slowWins bool) lifecycleFixture {
	t.Helper()
	fast, slow := 80.0, 20.0
	chosen := 0
	if slowWins {
		fast, slow, chosen = 20, 80, 1
	}
	sources := []*record.Record{roleConfirmationRecord(t, "fast", fast, 8, created.Add(-time.Hour)), roleConfirmationRecord(t, "slow", slow, 8, created.Add(-time.Hour))}
	plan, err := NewConfirmationPlan(prior.plan.Spec, sources, sources[chosen].Completion.EvidenceSHA256, created)
	if err != nil {
		t.Fatal(err)
	}
	points := make([]*record.Record, len(sources))
	for index, source := range sources {
		point := roleConfirmationClone(t, source)
		point.StartedAt, point.SeedSet = created.Add(time.Minute).Format(time.RFC3339), plan.SeedSet
		binding := ConfirmationPlanBinding(plan, index+1)
		point.Experiment, point.TaskPlan = &binding, plan.Protocol.TaskPlan
		for trial, check := range plan.Protocol.Checks {
			point.Checks[trial].Seed = check.Seed
		}
		point.CapacityPlan = roleConfirmationCapacityPlan(t, point, plan.Candidates[index].Capacity)
		points[index] = roleConfirmationReseal(t, point)
	}
	now := created.Add(2 * time.Minute)
	bundle, err := NewConfirmationBundle(plan, points, now)
	if err != nil {
		t.Fatal(err)
	}
	life, err := prior.roles.LoadLifecycle(plan.Spec.Name)
	if err != nil {
		t.Fatal(err)
	}
	return lifecycleFixture{roles: prior.roles, records: prior.records, plan: plan, points: points, bundle: bundle, created: created, timeNow: now, life: life}
}

func TestLifecycleRollbackAppendsReceiptAndPreservesProvenance(t *testing.T) {
	first := newLifecycleFixture(t)
	first.issue(t)
	first.start(t)
	first.finish(t)
	first.adopt(t)
	original := first.life.IncumbentSHA256
	second := nextLifecycleRound(t, first, first.timeNow.Add(time.Minute), true)
	second.issue(t)
	if second.life.Events[len(second.life.Events)-1].IncumbentSHA256 != original {
		t.Fatal("new challenge lost incumbent binding")
	}
	second.start(t)
	second.finish(t)
	second.adopt(t)
	if second.life.PreviousSHA256 != original {
		t.Fatal("prior receipt was discarded")
	}
	if _, err := first.roles.RollbackSelection(first.plan.Spec.Name, original, first.records, second.life.Digest, second.timeNow.Add(2*time.Second)); err == nil {
		t.Fatal("rollback resurrected overwritten evidence")
	}
	// Explicitly restoring exact historical canonical twins is a separate user
	// action. Rollback itself must not perform this store write or switch models.
	for _, point := range first.points {
		roleReviewSave(t, first.records, point)
	}
	rolled, err := first.roles.RollbackSelection(first.plan.Spec.Name, original, first.records, second.life.Digest, second.timeNow.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	receipt := rolled.Events[len(rolled.Events)-1]
	if receipt.Action != "rolled-back" || receipt.Selection.RollbackOf != original || rolled.PreviousSHA256 != second.life.IncumbentSHA256 || len(rolled.Events) != 9 {
		t.Fatalf("rollback rewrote provenance: %+v", rolled)
	}
	if !sameLifecycleValue(rolled.Events[3], first.life.Events[3]) {
		t.Fatal("original adoption receipt changed")
	}
	repeated, err := first.roles.RollbackSelection(first.plan.Spec.Name, original, first.records, second.life.Digest, second.timeNow.Add(3*time.Second))
	if err != nil || repeated.Digest != rolled.Digest {
		t.Fatal("rollback not idempotent", err)
	}
	status, err := first.roles.ReviewSelection(first.plan.Spec.Name, first.records, second.timeNow.Add(time.Hour))
	if err != nil || status.State != "qualified" || status.Selection.RollbackOf != original {
		t.Fatalf("rollback status: %+v err=%v", status, err)
	}
}

func TestLifecycleOldPlanCannotReplaceIncumbentWithLatestCAS(t *testing.T) {
	a := newLifecycleFixture(t)
	a.issue(t)
	a.start(t)
	a.finish(t)
	b := nextLifecycleRound(t, a, a.timeNow.Add(time.Minute), true)
	b.issue(t)
	b.start(t)
	b.finish(t)
	b.adopt(t)
	for _, point := range a.points {
		roleReviewSave(t, a.records, point)
	}
	_, err := a.roles.AdoptConfirmation(a.plan.Spec.Name, a.plan.PlanSHA256, a.bundle, a.records, b.life.Digest, b.timeNow.Add(2*time.Second))
	if err == nil || !strings.Contains(err.Error(), "incumbent changed") {
		t.Fatalf("old choice replaced a different incumbent: %v", err)
	}
	loaded, err := a.roles.LoadLifecycle(a.plan.Spec.Name)
	if err != nil || loaded.IncumbentSHA256 != b.life.IncumbentSHA256 {
		t.Fatal("failed adoption changed current selection")
	}
}

func TestLifecycleChallengesMustIncludeIncumbentAndPreserveCurrentRevision(t *testing.T) {
	f := newLifecycleFixture(t)
	f.issue(t)
	f.start(t)
	f.finish(t)
	f.adopt(t)
	sources := []*record.Record{roleConfirmationRecord(t, "different-a", 80, 8, f.created), roleConfirmationRecord(t, "different-b", 20, 8, f.created)}
	plan, err := NewConfirmationPlan(f.plan.Spec, sources, sources[0].Completion.EvidenceSHA256, f.timeNow.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.roles.IssueConfirmation(plan, f.life.Digest, f.timeNow.Add(time.Minute)); err == nil || !strings.Contains(err.Error(), "incumbent artifact") {
		t.Fatalf("incumbent omitted: %v", err)
	}
	spec := f.plan.Spec
	spec.Description = "role revised"
	if _, err := f.roles.Define(spec); err != nil {
		t.Fatal(err)
	}
	if _, err := f.roles.IssueConfirmation(plan, f.life.Digest, f.timeNow.Add(time.Minute)); err == nil {
		t.Fatal("old role revision issued")
	}
	if _, err := f.roles.RollbackSelection(f.plan.Spec.Name, f.life.IncumbentSHA256, f.records, f.life.Digest, f.timeNow.Add(time.Hour)); err == nil {
		t.Fatal("incompatible rollback accepted")
	}
}

func TestLifecycleRejectsInvalidClockBoundsAndSidecarDirectories(t *testing.T) {
	f := newLifecycleFixture(t)
	for _, now := range []time.Time{{}, f.created.Add(-time.Second), f.created.Add(24 * time.Hour)} {
		if _, err := f.roles.IssueConfirmation(f.plan, f.life.Digest, now); err == nil {
			t.Fatal("invalid issuance clock accepted")
		}
	}
	if _, err := f.roles.IssueConfirmation(f.plan, "", f.created); err == nil {
		t.Fatal("unbounded CAS accepted")
	}
	if _, err := f.roles.ReviewSelection(f.plan.Spec.Name, f.records, time.Time{}); err == nil {
		t.Fatal("zero review clock accepted")
	}
	if _, err := (Store{}).LoadLifecycle("role"); err == nil {
		t.Fatal("empty store accepted")
	}
	if _, err := f.roles.RollbackSelection(f.plan.Spec.Name, "missing", f.records, f.life.Digest, f.created); err == nil {
		t.Fatal("missing rollback accepted")
	}
	if err := os.WriteFile(filepath.Join(f.roles.Dir, ".lifecycle"), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.roles.LoadLifecycle(f.plan.Spec.Name); err == nil {
		t.Fatal("file used as sidecar directory")
	}
	os.Remove(filepath.Join(f.roles.Dir, ".lifecycle"))
	f.issue(t)
	if err := os.WriteFile(filepath.Join(f.roles.Dir, ".confirmations"), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.roles.SaveConfirmationBundle(f.bundle); err == nil {
		t.Fatal("file used as bundle directory")
	}
}
