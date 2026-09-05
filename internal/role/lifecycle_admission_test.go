package role

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/record"
)

func managedAdmissionFixture(t *testing.T) (lifecycleFixture, record.ManagedStore, record.ManagedStoreRef) {
	t.Helper()
	return prepareManagedAdmission(t, newLifecycleFixture(t))
}

func prepareManagedAdmission(t *testing.T, f lifecycleFixture) (lifecycleFixture, record.ManagedStore, record.ManagedStoreRef) {
	t.Helper()
	f.issue(t)
	var err error
	f.life, err = f.roles.BeginConfirmation(f.plan.Spec.Name, f.plan.PlanSHA256, f.life.Digest, f.created.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	store, ref := lifecycleManagedSave(t, f, "admission", "confirmation")
	f.life, err = f.roles.FinishManagedConfirmation(f.plan.Spec.Name, f.plan.PlanSHA256, f.bundle, f.records, ref, f.life.Digest, f.timeNow)
	if err != nil {
		t.Fatal(err)
	}
	return f, store, ref
}

func TestManagedAdoptionChecksExpiryAfterValidationUnderLock(t *testing.T) {
	f, _, ref := managedAdmissionFixture(t)
	deadline := f.timeNow.Add(time.Second)
	calls := 0
	clock := func() time.Time {
		calls++
		guard, err := f.roles.acquire()
		if err == nil {
			_ = guard.Release()
			t.Fatal("final admission ran outside the lifecycle mutation lock")
		}
		current, err := f.roles.LoadLifecycle(f.plan.Spec.Name)
		if err != nil || current.Digest != f.life.Digest || current.IncumbentSHA256 != "" {
			t.Fatal("adoption was persisted before its final deadline admission", err)
		}
		// Deterministically represent validation consuming the remaining time.
		return deadline
	}
	_, err := f.roles.adoptManagedConfirmationBefore(f.plan.Spec.Name, f.plan.PlanSHA256, f.bundle, f.records, ref, f.life.Digest, f.timeNow, deadline, clock)
	if !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatal("stale entry time admitted an expired adoption", err, calls)
	}
	current, err := f.roles.LoadLifecycle(f.plan.Spec.Name)
	if err != nil || current.Digest != f.life.Digest || current.IncumbentSHA256 != "" {
		t.Fatal("expired admission changed the current selection", err)
	}
}

func TestManagedAdoptionStampsFinalAdmissionAndReconcilesWithoutAnotherWrite(t *testing.T) {
	f, _, ref := managedAdmissionFixture(t)
	admittedAt := f.timeNow.Add(2 * time.Second)
	deadline := admittedAt.Add(time.Second)
	clock := func() time.Time { return admittedAt }
	adopted, err := f.roles.adoptManagedConfirmationBefore(f.plan.Spec.Name, f.plan.PlanSHA256, f.bundle, f.records, ref, f.life.Digest, f.timeNow, deadline, clock)
	if err != nil || adopted.Validate() != nil {
		t.Fatal("finite adoption did not preserve the lifecycle seals", err)
	}
	last := adopted.Events[len(adopted.Events)-1]
	if last.At != admittedAt.UTC().Format(time.RFC3339Nano) || last.Selection.EvaluatedAt != f.bundle.Report.EvaluatedAt {
		t.Fatal("event reused its old admission time or changed evidence evaluation time")
	}
	repeated, err := f.roles.adoptManagedConfirmationBefore(f.plan.Spec.Name, f.plan.PlanSHA256, f.bundle, f.records, ref, f.life.Digest, deadline.Add(time.Second), deadline, func() time.Time {
		t.Fatal("exact durable reconciliation attempted another write admission")
		return time.Time{}
	})
	if err != nil || repeated.Digest != adopted.Digest || len(repeated.Events) != len(adopted.Events) {
		t.Fatal("expired session could not reconcile its exact already-durable adoption", err)
	}
}

func TestManagedAdoptionAdmissionCannotReplaceEvidenceValidation(t *testing.T) {
	f, store, ref := managedAdmissionFixture(t)
	if err := os.Remove(store.CanonicalPath(f.points[0].Model)); err != nil {
		t.Fatal(err)
	}
	_, err := f.roles.adoptManagedConfirmationBefore(f.plan.Spec.Name, f.plan.PlanSHA256, f.bundle, f.records, ref, f.life.Digest, f.timeNow, f.timeNow.Add(time.Hour), func() time.Time {
		t.Fatal("unvalidated evidence reached write admission")
		return f.timeNow
	})
	if err == nil {
		t.Fatal("missing managed evidence became a selection")
	}
}

func TestManagedAdoptionAdmissionRejectsClockRollbackAndExpiredPlan(t *testing.T) {
	for _, scenario := range []string{"zero deadline", "clock rollback", "zero clock", "expired plan"} {
		t.Run(scenario, func(t *testing.T) {
			f, _, ref := managedAdmissionFixture(t)
			deadline, observed := f.timeNow.Add(48*time.Hour), f.timeNow
			switch scenario {
			case "zero deadline":
				deadline = time.Time{}
			case "clock rollback":
				observed = f.timeNow.Add(-time.Second)
			case "zero clock":
				observed = time.Time{}
			case "expired plan":
				observed, _ = time.Parse(time.RFC3339Nano, f.plan.ExpiresAt)
			}
			_, err := f.roles.adoptManagedConfirmationBefore(f.plan.Spec.Name, f.plan.PlanSHA256, f.bundle, f.records, ref, f.life.Digest, f.timeNow, deadline, func() time.Time { return observed })
			if err == nil {
				t.Fatal("invalid final admission became a selection")
			}
		})
	}
}

func TestManagedAdoptionPublicDeadlineUsesTheRealClock(t *testing.T) {
	for _, scenario := range []string{"expired", "live"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := nextLifecycleRound(t, newLifecycleFixture(t), time.Now().Add(-3*time.Minute), false)
			f, _, ref := prepareManagedAdmission(t, fixture)
			// Reusing the historical caller time would accept an expired cutoff.
			deadline := f.timeNow.Add(time.Second)
			if scenario == "live" {
				deadline = time.Now().Add(time.Minute)
			}
			before := time.Now()
			adopted, err := f.roles.AdoptManagedConfirmationBefore(f.plan.Spec.Name, f.plan.PlanSHA256, f.bundle, f.records, ref, f.life.Digest, f.timeNow, deadline)
			if scenario == "expired" {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("public finite-deadline API did not use the real clock", err)
				}
				return
			}
			if err != nil || adopted.Validate() != nil {
				t.Fatal("live public adoption failed", err)
			}
			at, err := time.Parse(time.RFC3339Nano, adopted.Events[len(adopted.Events)-1].At)
			if err != nil || at.Before(before) || at.After(time.Now()) {
				t.Fatal("first adoption did not retain its real final admission timestamp", err)
			}
		})
	}
}
