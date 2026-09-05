package role

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/record"
)

func lifecycleManagedSave(t *testing.T, f lifecycleFixture, id, purpose string) (record.ManagedStore, record.ManagedStoreRef) {
	t.Helper()
	store, err := record.CreateManagedStore(f.records, record.ManagedStoreSpec{Schema: record.ManagedStoreSpecSchema, ID: id, SessionID: id, Purpose: purpose})
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
	return store, ref
}

func lifecycleManagedAdopt(t *testing.T, f *lifecycleFixture, id string) (record.ManagedStore, record.ManagedStoreRef) {
	t.Helper()
	f.issue(t)
	var err error
	f.life, err = f.roles.BeginConfirmation(f.plan.Spec.Name, f.plan.PlanSHA256, f.life.Digest, f.created.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	store, ref := lifecycleManagedSave(t, *f, id, "confirmation")
	f.life, err = f.roles.FinishManagedConfirmation(f.plan.Spec.Name, f.plan.PlanSHA256, f.bundle, f.records, ref, f.life.Digest, f.timeNow)
	if err != nil {
		t.Fatal(err)
	}
	f.life, err = f.roles.AdoptManagedConfirmation(f.plan.Spec.Name, f.plan.PlanSHA256, f.bundle, f.records, ref, f.life.Digest, f.timeNow.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return store, ref
}

func TestManagedLifecyclePreservesIncumbentAndRollbackAcrossStores(t *testing.T) {
	first := newLifecycleFixture(t)
	store, ref := lifecycleManagedAdopt(t, &first, "first")
	original := first.life.IncumbentSHA256
	second := nextLifecycleRound(t, first, first.timeNow.Add(time.Minute), true)
	_, _ = lifecycleManagedSave(t, second, "explore", "exploration")
	status, err := first.roles.ReviewSelection(first.plan.Spec.Name, first.records, second.timeNow)
	if err != nil || status.State != "qualified" || *status.Selection.Selected.StoreRef != ref {
		t.Fatal("exploration invalidated incumbent", err, status)
	}
	_, _ = lifecycleManagedAdopt(t, &second, "second")
	rolled, err := first.roles.RollbackSelection(first.plan.Spec.Name, original, first.records, second.life.Digest, second.timeNow.Add(3*time.Second))
	if err != nil {
		t.Fatal("original twins should survive without restoration", err)
	}
	status, err = first.roles.ReviewSelection(first.plan.Spec.Name, first.records, second.timeNow.Add(4*time.Second))
	if err != nil || status.State != "qualified" || status.Selection.Selected.Attachment.EvidenceSHA256 != first.points[0].Completion.EvidenceSHA256 {
		t.Fatal("rollback lost original evidence", err)
	}
	if err := os.Remove(store.CanonicalPath(first.points[0].Model)); err != nil {
		t.Fatal(err)
	}
	if _, err := first.roles.RollbackSelection(first.plan.Spec.Name, original, first.records, rolled.Digest, second.timeNow.Add(5*time.Second)); err == nil {
		t.Fatal("missing original evidence resurrected")
	}
	status, err = first.roles.ReviewSelection(first.plan.Spec.Name, first.records, second.timeNow.Add(5*time.Second))
	if err != nil || status.State == "qualified" {
		t.Fatal("missing selected twin stayed qualified", err)
	}
}

func TestManagedLifecycleRejectsWrongPurposeSealPathAndLegacyBypass(t *testing.T) {
	f := newLifecycleFixture(t)
	f.issue(t)
	var err error
	f.life, err = f.roles.BeginConfirmation(f.plan.Spec.Name, f.plan.PlanSHA256, f.life.Digest, f.created.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	_, wrong := lifecycleManagedSave(t, f, "wrong-purpose", "exploration")
	if _, err := f.roles.FinishManagedConfirmation(f.plan.Spec.Name, f.plan.PlanSHA256, f.bundle, f.records, wrong, f.life.Digest, f.timeNow); err == nil {
		t.Fatal("exploration store completed confirmation")
	}
	store, ref := lifecycleManagedSave(t, f, "correct", "confirmation")
	bad := ref
	bad.SealSHA256 = "sha256:" + strings.Repeat("f", 64)
	if _, err := f.roles.FinishManagedConfirmation(f.plan.Spec.Name, f.plan.PlanSHA256, f.bundle, f.records, bad, f.life.Digest, f.timeNow); err == nil {
		t.Fatal("forged reference accepted")
	}
	if _, err := AttachRecord(store.CanonicalPath(f.points[0].Model), record.Store{Dir: filepath.Dir(store.CanonicalPath(f.points[0].Model))}); err == nil {
		t.Fatal("raw directory promoted managed record")
	}
	point := ConfirmationPoint{StoreRef: &ref, Model: f.points[0].Manifest.Model, Attachment: Attachment{Path: store.CanonicalPath(f.points[0].Model) + ".copy"}}
	if _, _, err := readLifecyclePoint(point, f.records); err == nil {
		t.Fatal("copied canonical path accepted")
	}
}

func TestReviewManagedKeepsRoleLibraryUnchangedAndRequiresExplicitSet(t *testing.T) {
	f := newLifecycleFixture(t)
	_, ref := lifecycleManagedSave(t, f, "exploration", "exploration")
	before, err := f.roles.Load(f.plan.Spec.Name)
	if err != nil {
		t.Fatal(err)
	}
	models := []string{f.points[0].Model, f.points[1].Model}
	report, err := ReviewManaged(f.plan.Spec, f.records, ref, models, f.timeNow)
	if err != nil || report.State != "exploration-lead" || report.Lead != f.points[0].Completion.EvidenceSHA256 {
		t.Fatal("managed review failed", err, report)
	}
	after, err := f.roles.Load(f.plan.Spec.Name)
	if err != nil || !confirmationEqual(before, after) {
		t.Fatal("managed review mutated role library", err)
	}
	for _, selected := range [][]string{nil, {models[0]}, {models[0], models[0]}, {models[0], "missing"}} {
		if _, err := ReviewManaged(f.plan.Spec, f.records, ref, selected, f.timeNow); err == nil {
			t.Fatal("invalid explicit set accepted")
		}
	}
	stale, err := ReviewManaged(f.plan.Spec, f.records, ref, models, f.timeNow.Add(3660*24*time.Hour))
	if err != nil || stale.State != "unresolved" || stale.Candidates[0].State != "stale" {
		t.Fatal("stale managed evidence qualified", err)
	}
}
