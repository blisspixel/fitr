package role

import (
	"context"
	"errors"
	"time"

	"github.com/blisspixel/fitr/internal/record"
)

// FinishManagedConfirmation pins one closed confirmation group within results.
// Failed and cancelled attempts use FinishConfirmation without a store receipt.
func (store Store) FinishManagedConfirmation(name, planSHA256 string, bundle ConfirmationBundle, results record.Store, ref record.ManagedStoreRef, expected string, now time.Time) (Lifecycle, error) {
	return store.finishConfirmation(name, planSHA256, "completed", &bundle, results, &ref, expected, now)
}

func (store Store) AdoptManagedConfirmation(name, planSHA256 string, bundle ConfirmationBundle, results record.Store, ref record.ManagedStoreRef, expected string, now time.Time) (Lifecycle, error) {
	return store.adoptConfirmation(name, planSHA256, bundle, results, &ref, expected, now)
}

// AdoptManagedConfirmationBefore admits a first adoption write only before the
// finite deadline. The real clock is checked under the mutation lock after
// evidence and lifecycle validation, and becomes the new event timestamp.
// Once admitted, the bounded atomic local write may finish after the cutoff;
// it cannot safely be interrupted or undone midway through an OS commit.
// Exact already-durable adoption reconciliation retains the ordinary API's
// evidence checks and does not admit another write or require a live deadline.
func (store Store) AdoptManagedConfirmationBefore(name, planSHA256 string, bundle ConfirmationBundle, results record.Store, ref record.ManagedStoreRef, expected string, now, deadline time.Time) (Lifecycle, error) {
	return store.adoptManagedConfirmationBefore(name, planSHA256, bundle, results, ref, expected, now, deadline, time.Now)
}

func (store Store) adoptManagedConfirmationBefore(name, planSHA256 string, bundle ConfirmationBundle, results record.Store, ref record.ManagedStoreRef, expected string, now, deadline time.Time, clock func() time.Time) (Lifecycle, error) {
	if deadline.IsZero() {
		return Lifecycle{}, errors.New("managed adoption requires a finite deadline")
	}
	admit := func(event LifecycleEvent) (time.Time, error) {
		// Persisted event ordering uses wall time. Drop the process monotonic
		// reading so a backwards wall-clock change cannot evade this check.
		at := clock().UTC()
		if at.IsZero() || at.Before(now) {
			return time.Time{}, errors.New("adoption clock moved backwards before write admission")
		}
		if !at.Before(deadline) {
			return time.Time{}, errors.Join(context.DeadlineExceeded, errors.New("adoption allowance expired before write admission"))
		}
		if err := liveConfirmationPlan(bundle.Plan, at); err != nil {
			return time.Time{}, err
		}
		expiry, err := time.Parse(time.RFC3339Nano, event.Selection.ExpiresAt)
		if err != nil || !at.Before(expiry) {
			return time.Time{}, errors.New("confirmation evidence expired before write admission")
		}
		return at, nil
	}
	return store.adoptConfirmationAdmitted(name, planSHA256, bundle, results, &ref, expected, now, admit)
}

// Only the timestamp changes after full validation. Re-seal its dependent
// event and lifecycle identities without repeating expensive evidence reads.
func admitLifecycleAdoption(life *Lifecycle, admit lifecycleAdmission) error {
	last := len(life.Events) - 1
	if last < 0 || life.Events[last].Action != "adopted" || life.Events[last].Selection == nil {
		return errors.New("write admission requires a validated adoption event")
	}
	event := life.Events[last]
	at, err := admit(event)
	if err != nil {
		return err
	}
	event.At, event.Digest = at.UTC().Format(time.RFC3339Nano), ""
	event.Digest, err = lifecycleDigest(LifecycleSchema+".event", event)
	if err != nil {
		return err
	}
	life.Events[last], life.IncumbentSHA256, life.Digest = event, event.Digest, ""
	life.Digest, err = lifecycleDigest(LifecycleSchema, *life)
	return err
}

func cloneManagedStoreRef(ref *record.ManagedStoreRef) *record.ManagedStoreRef {
	if ref == nil {
		return nil
	}
	cloned := *ref
	return &cloned
}

func confirmationAttachment(point *record.Record, results record.Store, ref *record.ManagedStoreRef) (Attachment, error) {
	if ref == nil {
		return AttachRecord(results.CanonicalPath(point.Model), results)
	}
	store, err := record.ResolveManagedStore(results, *ref)
	if err != nil {
		return Attachment{}, err
	}
	spec, err := store.Spec()
	if err != nil || spec.Purpose != "confirmation" {
		return Attachment{}, errors.New("role completion requires a closed confirmation evidence group")
	}
	current, err := store.Read(point.Model)
	if err != nil || current.EvidenceIntegrityIssue() != "" || current.Completion == nil {
		return Attachment{}, errors.New("managed confirmation lacks valid signed current evidence")
	}
	return Attachment{Path: store.CanonicalPath(point.Model), EvidenceSHA256: current.Completion.EvidenceSHA256, RunID: current.StableRunID()}, nil
}

func readLifecyclePoint(point ConfirmationPoint, results record.Store) (*record.Record, string, error) {
	if point.StoreRef == nil {
		return readCanonicalRoleRecord(point.Attachment.Path, results)
	}
	store, err := record.ResolveManagedStore(results, *point.StoreRef)
	if err != nil {
		return nil, "", err
	}
	spec, err := store.Spec()
	if err != nil || spec.Purpose != "confirmation" {
		return nil, "", errors.New("selection references another managed evidence purpose")
	}
	canonical := store.CanonicalPath(point.Model.Resolved)
	if point.Attachment.Path != canonical {
		return nil, "", errors.New("managed selection path does not match its fixed-root canonical identity")
	}
	current, err := store.Read(point.Model.Resolved)
	return current, canonical, err
}
