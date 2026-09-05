package role

import (
	"reflect"
	"testing"
	"time"
)

func TestSelectionSnapshotSharesOrdinaryAndManagedStatusWithoutLockWrites(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "managed"}[managed], func(t *testing.T) {
			f := newLifecycleFixture(t)
			if managed {
				lifecycleManagedAdopt(t, &f, "snapshot")
			} else {
				f.issue(t)
				f.start(t)
				f.finish(t)
				f.adopt(t)
			}
			library, err := f.roles.Load(f.plan.Spec.Name)
			if err != nil {
				t.Fatal(err)
			}
			now := f.timeNow.Add(2 * time.Second)
			want, err := f.roles.ReviewSelection(library.Name, f.records, now)
			if err != nil || want.State != "qualified" {
				t.Fatal(want, err)
			}
			held, err := f.roles.acquire()
			if err != nil {
				t.Fatal(err)
			}
			defer held.Release()
			got, err := ReviewSelectionSnapshot(library, &f.life, f.records, now)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatal("snapshot diverged or tried to acquire the held lock", got, err)
			}
			got.Selection.Selected.Attachment.Path = "mutated"
			got.LastAttempt.Action = "mutated"
			again, err := ReviewSelectionSnapshot(library, &f.life, f.records, now)
			if err != nil || !reflect.DeepEqual(again, want) {
				t.Fatal("returned status aliases supplied snapshots", err)
			}
			stale, err := ReviewSelectionSnapshot(library, &f.life, f.records, now.Add(31*24*time.Hour))
			if err != nil || stale.State != "stale" {
				t.Fatal("expired evidence qualified", err)
			}
		})
	}
}

func TestSelectionSnapshotValidatesWholeInputsAndAbsentLifecycle(t *testing.T) {
	f := newLifecycleFixture(t)
	library, err := f.roles.Load(f.plan.Spec.Name)
	if err != nil {
		t.Fatal(err)
	}
	status, err := ReviewSelectionSnapshot(library, nil, f.records, f.timeNow)
	if err != nil || status.State != "unselected" || status.LifecycleDigest != f.life.Digest {
		t.Fatal(status, err)
	}
	if _, err := ReviewSelectionSnapshot(library, nil, f.records, time.Time{}); err == nil {
		t.Fatal("zero clock accepted")
	}
	bad := library
	bad.CurrentRevision = "invalid"
	if _, err := ReviewSelectionSnapshot(bad, nil, f.records, f.timeNow); err == nil {
		t.Fatal("invalid library accepted")
	}
	for _, life := range []Lifecycle{{Name: "other"}, {Name: library.Name}} {
		if _, err := ReviewSelectionSnapshot(library, &life, f.records, f.timeNow); err == nil {
			t.Fatal("invalid lifecycle accepted")
		}
	}
}
