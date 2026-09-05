package role

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/blisspixel/fitr/internal/atomicfile"
	"github.com/blisspixel/fitr/internal/boundedio"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/strictjson"
)

func (store Store) LoadLifecycle(name string) (Lifecycle, error) {
	if _, err := store.Load(name); err != nil {
		return Lifecycle{}, err
	}
	path, err := store.lifecyclePath(name, false)
	if errors.Is(err, os.ErrNotExist) {
		return emptyLifecycle(name), nil
	}
	if err != nil {
		return Lifecycle{}, err
	}
	if err := rejectRoleSymlink(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return emptyLifecycle(name), nil
		}
		return Lifecycle{}, err
	}
	data, err := boundedio.ReadFile(path, maximumLifecycleBytes)
	if err != nil {
		return Lifecycle{}, err
	}
	if err := strictjson.Validate(data); err != nil {
		return Lifecycle{}, err
	}
	var life Lifecycle
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&life); err != nil {
		return Lifecycle{}, err
	}
	if life.Name != name {
		return Lifecycle{}, errors.New("role lifecycle does not match its filename")
	}
	return life, life.Validate()
}

func (store Store) lifecyclePath(name string, create bool) (string, error) {
	if _, err := store.path(name); err != nil {
		return "", err
	}
	directory, err := store.lifecycleDirectory(".lifecycle", create)
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, name+".json"), nil
}

func (store Store) lifecycleDirectory(name string, create bool) (string, error) {
	if name != ".lifecycle" && name != ".confirmations" {
		return "", errors.New("unknown role sidecar directory")
	}
	if err := store.checkDirectory(false); err != nil {
		return "", err
	}
	directory := filepath.Join(store.Dir, name)
	if err := rejectRoleSymlink(directory); err != nil {
		if !create || !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("role lifecycle storage must be a directory without symbolic links")
	}
	return directory, nil
}

type lifecycleMutation func(Library, Lifecycle, *LifecycleEvent) error
type lifecycleAdmission func(LifecycleEvent) (time.Time, error)

func (store Store) transition(name, expected string, event LifecycleEvent, now time.Time, check lifecycleMutation) (Lifecycle, error) {
	return store.transitionAdmitted(name, expected, event, now, check, nil)
}

func (store Store) transitionAdmitted(name, expected string, event LifecycleEvent, now time.Time, check lifecycleMutation, admit lifecycleAdmission) (Lifecycle, error) {
	if now.IsZero() || !roleDigestValid(expected) {
		return Lifecycle{}, errors.New("lifecycle transition requires a current time and expected digest")
	}
	if err := store.checkDirectory(false); err != nil {
		return Lifecycle{}, err
	}
	guard, err := store.acquire()
	if err != nil {
		return Lifecycle{}, err
	}
	defer func() { _ = guard.Release() }()
	library, err := store.Load(name)
	if err != nil {
		return Lifecycle{}, err
	}
	life, err := store.LoadLifecycle(name)
	if err != nil {
		return Lifecycle{}, err
	}
	if expected != life.Digest {
		if lifecycleRepeated(life, event, expected) {
			if err := check(library, life, &event); err != nil {
				return Lifecycle{}, err
			}
			return life, nil
		}
		return Lifecycle{}, errors.New("role lifecycle changed; reload its digest before trying again")
	}
	if len(life.Events) >= maximumLifecycleEvents {
		return Lifecycle{}, errors.New("role lifecycle reached its 256-event limit")
	}
	event.At = now.UTC().Format(time.RFC3339Nano)
	if err := check(library, life, &event); err != nil {
		return Lifecycle{}, err
	}
	event.Sequence, event.PreviousDigest = len(life.Events)+1, life.Digest
	event.Digest, err = lifecycleDigest(LifecycleSchema+".event", event)
	if err != nil {
		return Lifecycle{}, err
	}
	life.appendEvent(event)
	return store.writeLifecycle(life, admit)
}

func (store Store) writeLifecycle(life Lifecycle, admit lifecycleAdmission) (Lifecycle, error) {
	if err := life.Validate(); err != nil {
		return Lifecycle{}, err
	}
	if admit != nil {
		if err := admitLifecycleAdoption(&life, admit); err != nil {
			return Lifecycle{}, err
		}
	}
	data, err := json.MarshalIndent(life, "", "  ")
	if err != nil {
		return Lifecycle{}, err
	}
	if len(data)+1 > maximumLifecycleBytes {
		return Lifecycle{}, errors.New("role lifecycle exceeds eight MiB")
	}
	path, err := store.lifecyclePath(life.Name, true)
	if err != nil {
		return Lifecycle{}, err
	}
	if err := atomicfile.Write(path, append(data, '\n'), 0o600); err != nil {
		return Lifecycle{}, err
	}
	return life, nil
}

func lifecycleRepeated(life Lifecycle, event LifecycleEvent, expected string) bool {
	if len(life.Events) == 0 {
		return false
	}
	last := life.Events[len(life.Events)-1]
	if last.PreviousDigest != expected {
		return false
	}
	last.Sequence, last.At, last.PreviousDigest, last.Digest = 0, "", "", ""
	return sameLifecycleValue(last, event)
}

func (store Store) IssueConfirmation(plan ConfirmationPlan, expected string, now time.Time) (Lifecycle, error) {
	if err := plan.Validate(); err != nil {
		return Lifecycle{}, err
	}
	current, err := store.LoadLifecycle(plan.Spec.Name)
	if err != nil {
		return Lifecycle{}, err
	}
	event := LifecycleEvent{Action: "issued", PlanSHA256: plan.PlanSHA256, Plan: &plan, IncumbentSHA256: current.IncumbentSHA256}
	return store.transition(plan.Spec.Name, expected, event, now, func(library Library, life Lifecycle, _ *LifecycleEvent) error {
		if library.CurrentRevision != plan.SpecSHA256 {
			return errors.New("role changed after confirmation planning")
		}
		if err := liveConfirmationPlan(plan, now); err != nil {
			return err
		}
		count := 0
		for _, item := range life.Events {
			if item.Action == "issued" {
				count++
			}
		}
		if count >= maximumLifecyclePlans {
			if existing, err := life.issuedPlan(plan.PlanSHA256); err != nil || !sameLifecycleValue(*existing, plan) {
				return errors.New("role lifecycle reached its 32-plan limit")
			}
		}
		return nil
	})
}

func liveConfirmationPlan(plan ConfirmationPlan, now time.Time) error {
	created, err := time.Parse(time.RFC3339Nano, plan.CreatedAt)
	expires, expiryErr := time.Parse(time.RFC3339Nano, plan.ExpiresAt)
	if err != nil || expiryErr != nil || now.Before(created) || !now.Before(expires) {
		return errors.New("confirmation plan is not live; issue a fresh plan and seed")
	}
	return nil
}

func (store Store) BeginConfirmation(name, planSHA256, expected string, now time.Time) (Lifecycle, error) {
	event := LifecycleEvent{Action: "started", PlanSHA256: planSHA256}
	return store.transition(name, expected, event, now, func(library Library, life Lifecycle, _ *LifecycleEvent) error {
		plan, err := life.issuedPlan(planSHA256)
		if err != nil {
			return err
		}
		if library.CurrentRevision != plan.SpecSHA256 {
			return errors.New("role changed after confirmation issuance")
		}
		return liveConfirmationPlan(*plan, now)
	})
}

// FinishConfirmation records a terminal outcome. Interrupted work stays started
// until explicitly failed or cancelled; it never gains a second attempt.
func (store Store) FinishConfirmation(name, planSHA256, status string, bundle *ConfirmationBundle, records record.Store, expected string, now time.Time) (Lifecycle, error) {
	return store.finishConfirmation(name, planSHA256, status, bundle, records, nil, expected, now)
}

func (store Store) finishConfirmation(name, planSHA256, status string, bundle *ConfirmationBundle, records record.Store, ref *record.ManagedStoreRef, expected string, now time.Time) (Lifecycle, error) {
	if status != "completed" && status != "cancelled" && status != "failed" {
		return Lifecycle{}, errors.New("confirmation outcome must be completed, cancelled or failed")
	}
	event := LifecycleEvent{Action: status, PlanSHA256: planSHA256}
	if status == "completed" {
		if bundle == nil || bundle.Plan.PlanSHA256 != planSHA256 {
			return Lifecycle{}, errors.New("completed attempt requires its exact confirmation bundle")
		}
		receipt, err := confirmationAttemptWithStore(*bundle, records, ref, now)
		if err != nil {
			return Lifecycle{}, err
		}
		event.Completion = &receipt
	} else if bundle != nil {
		return Lifecycle{}, errors.New("failed or cancelled attempt cannot retain a completion bundle")
	}
	return store.transition(name, expected, event, now, func(_ Library, life Lifecycle, _ *LifecycleEvent) error {
		plan, err := life.issuedPlan(planSHA256)
		if err != nil {
			return err
		}
		if bundle != nil && !sameLifecycleValue(*plan, bundle.Plan) {
			return errors.New("completion changed the issued plan")
		}
		return nil
	})
}

func confirmationAttempt(bundle ConfirmationBundle, records record.Store, now time.Time) (ConfirmationAttemptReceipt, error) {
	return confirmationAttemptWithStore(bundle, records, nil, now)
}

func confirmationAttemptWithStore(bundle ConfirmationBundle, records record.Store, ref *record.ManagedStoreRef, now time.Time) (ConfirmationAttemptReceipt, error) {
	report, err := bundle.Validate()
	if err != nil {
		return ConfirmationAttemptReceipt{}, err
	}
	if err := liveConfirmationPlan(bundle.Plan, now); err != nil {
		return ConfirmationAttemptReceipt{}, err
	}
	if len(bundle.PointRecords) != len(bundle.Plan.Candidates) {
		return ConfirmationAttemptReceipt{}, errors.New("completed confirmation requires every planned point")
	}
	digest, err := lifecycleDigest(LifecycleSchema+".bundle", bundle)
	if err != nil {
		return ConfirmationAttemptReceipt{}, err
	}
	receipt := ConfirmationAttemptReceipt{BundleSHA256: digest, State: report.State, ChosenEvidenceSHA256: report.ChosenEvidenceSHA256, EvaluatedAt: report.EvaluatedAt}
	var expiry time.Time
	for _, point := range bundle.PointRecords {
		if point == nil || point.Completion == nil || point.Manifest == nil || point.EvidenceIntegrityIssue() != "" {
			return ConfirmationAttemptReceipt{}, errors.New("completed confirmation requires all sealed point records")
		}
		attachment, err := confirmationAttachment(point, records, ref)
		if err != nil {
			return ConfirmationAttemptReceipt{}, err
		}
		if attachment.EvidenceSHA256 != point.Completion.EvidenceSHA256 || attachment.RunID != point.StableRunID() {
			return ConfirmationAttemptReceipt{}, errors.New("confirmation requires exact canonical current twins")
		}
		receipt.Points = append(receipt.Points, ConfirmationPoint{Attachment: attachment, Model: point.Manifest.Model, StartedAt: point.StartedAt, StoreRef: cloneManagedStoreRef(ref), RuntimeBinding: record.CloneRuntimeBinding(point.RuntimeBinding)})
		started, err := time.Parse(time.RFC3339Nano, point.StartedAt)
		if err != nil || started.After(now) {
			return ConfirmationAttemptReceipt{}, errors.New("confirmation point has an invalid live timestamp")
		}
		pointExpiry := started.Add(time.Duration(bundle.Plan.Spec.MaxAgeDays) * 24 * time.Hour)
		if expiry.IsZero() || pointExpiry.Before(expiry) {
			expiry = pointExpiry
		}
	}
	receipt.ExpiresAt = expiry.UTC().Format(time.RFC3339Nano)
	return receipt, nil
}

func (store Store) AdoptConfirmation(name, planSHA256 string, bundle ConfirmationBundle, records record.Store, expected string, now time.Time) (Lifecycle, error) {
	return store.adoptConfirmation(name, planSHA256, bundle, records, nil, expected, now)
}

func (store Store) adoptConfirmation(name, planSHA256 string, bundle ConfirmationBundle, records record.Store, ref *record.ManagedStoreRef, expected string, now time.Time) (Lifecycle, error) {
	return store.adoptConfirmationAdmitted(name, planSHA256, bundle, records, ref, expected, now, nil)
}

func (store Store) adoptConfirmationAdmitted(name, planSHA256 string, bundle ConfirmationBundle, records record.Store, ref *record.ManagedStoreRef, expected string, now time.Time, admit lifecycleAdmission) (Lifecycle, error) {
	if bundle.Plan.PlanSHA256 != planSHA256 || bundle.Plan.Spec.Name != name {
		return Lifecycle{}, errors.New("adoption bundle belongs to another role or plan")
	}
	attempt, err := confirmationAttemptWithStore(bundle, records, ref, now)
	if err != nil {
		return Lifecycle{}, err
	}
	selected, err := selectedConfirmationPoint(bundle.Plan, attempt.Points)
	if err != nil {
		return Lifecycle{}, err
	}
	selection := SelectionReceipt{SpecSHA256: bundle.Plan.SpecSHA256, PlanSHA256: planSHA256, BundleSHA256: attempt.BundleSHA256, ChosenEvidenceSHA256: bundle.Plan.ChosenEvidenceSHA256, Selected: selected, Points: attempt.Points, ExpiresAt: attempt.ExpiresAt, EvaluatedAt: attempt.EvaluatedAt}
	event := LifecycleEvent{Action: "adopted", PlanSHA256: planSHA256, Selection: &selection}
	return store.transitionAdmitted(name, expected, event, now, func(library Library, _ Lifecycle, _ *LifecycleEvent) error {
		return currentSelection(selection, bundle.Plan, library, records, now)
	}, admit)
}

func currentSelection(selection SelectionReceipt, plan ConfirmationPlan, library Library, records record.Store, now time.Time) error {
	if library.CurrentRevision != selection.SpecSHA256 {
		return errors.New("role revision changed; original qualification is stale")
	}
	expires, err := time.Parse(time.RFC3339Nano, selection.ExpiresAt)
	if err != nil || !now.Before(expires) {
		return errors.New("confirmation evidence has expired")
	}
	var points []*record.Record
	for _, point := range selection.Points {
		current, canonical, err := readLifecyclePoint(point, records)
		if err != nil || current.Completion == nil || current.Manifest == nil || current.Completion.EvidenceSHA256 != point.Attachment.EvidenceSHA256 || current.StableRunID() != point.Attachment.RunID || canonical != point.Attachment.Path || current.StartedAt != point.StartedAt || current.Manifest.Model != point.Model {
			return errors.New("confirmation evidence no longer has exact canonical current twins")
		}
		if !sameLifecycleValue(current.RuntimeBinding, point.RuntimeBinding) {
			return errors.New("selected evidence runtime binding changed")
		}
		points = append(points, current)
	}
	evaluated, err := time.Parse(time.RFC3339Nano, selection.EvaluatedAt)
	if err != nil {
		return err
	}
	bundle, err := NewConfirmationBundle(plan, points, evaluated)
	if err != nil || bundle.Report.State != "confirmed" {
		return errors.New("current evidence does not reestablish the original confirmation")
	}
	digest, err := lifecycleDigest(LifecycleSchema+".bundle", bundle)
	if err != nil || digest != selection.BundleSHA256 {
		return errors.New("current confirmation does not match the adoption receipt")
	}
	return nil
}

func (store Store) RollbackSelection(name, receiptSHA256 string, records record.Store, expected string, now time.Time) (Lifecycle, error) {
	life, err := store.LoadLifecycle(name)
	if err != nil {
		return Lifecycle{}, err
	}
	original := life.findEvent(receiptSHA256)
	if original == nil || original.Action != "adopted" {
		return Lifecycle{}, errors.New("rollback target must be an existing adoption receipt")
	}
	selection := *original.Selection
	selection.RollbackOf = receiptSHA256
	event := LifecycleEvent{Action: "rolled-back", PlanSHA256: selection.PlanSHA256, Selection: &selection}
	plan, err := life.issuedPlan(selection.PlanSHA256)
	if err != nil {
		return Lifecycle{}, err
	}
	return store.transition(name, expected, event, now, func(library Library, _ Lifecycle, _ *LifecycleEvent) error {
		return currentSelection(selection, *plan, library, records, now)
	})
}

func (store Store) ReviewSelection(name string, records record.Store, now time.Time) (SelectionStatus, error) {
	if now.IsZero() {
		return SelectionStatus{}, errors.New("selection review requires a current time")
	}
	if err := store.checkDirectory(false); err != nil {
		return SelectionStatus{}, err
	}
	guard, err := store.acquire()
	if err != nil {
		return SelectionStatus{}, err
	}
	defer func() { _ = guard.Release() }()
	library, err := store.Load(name)
	if err != nil {
		return SelectionStatus{}, err
	}
	life, err := store.LoadLifecycle(name)
	if err != nil {
		return SelectionStatus{}, err
	}
	return reviewSelectionSnapshot(library, life, records, now)
}

func reviewSelectionSnapshot(library Library, life Lifecycle, records record.Store, now time.Time) (SelectionStatus, error) {
	status := SelectionStatus{Schema: LifecycleSchema + ".status", Role: library.Name, Scope: "battery_screening", State: "unselected", LifecycleDigest: life.Digest, PreviousSHA256: life.PreviousSHA256, EvaluatedAt: now.UTC().Format(time.RFC3339Nano)}
	status.Attempts, status.LastAttempt = lifecycleAttempts(life)
	if life.IncumbentSHA256 == "" {
		return status, nil
	}
	event := life.findEvent(life.IncumbentSHA256)
	status.ReceiptSHA256, status.Selection = event.Digest, event.Selection
	selectedAt, _ := time.Parse(time.RFC3339Nano, event.At)
	if now.Before(selectedAt) {
		status.State, status.Reason = "stale", "selection receipt is newer than the current clock"
		return status, nil
	}
	plan, err := life.issuedPlan(event.PlanSHA256)
	if err != nil {
		return SelectionStatus{}, err
	}
	if err := currentSelection(*event.Selection, *plan, library, records, now); err != nil {
		status.State, status.Reason = "stale", err.Error()
	} else {
		status.State = "qualified"
	}
	return status, nil
}
