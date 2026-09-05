package role

import (
	"errors"
	"time"

	"github.com/blisspixel/fitr/internal/record"
)

// ReviewSelectionSnapshot rechecks supplied role/lifecycle snapshots without
// acquiring a filesystem lock or writing files. A nil lifecycle means its
// sidecar was absent. The caller owns snapshot consistency and filesystem read
// limits; canonical/history and closed managed-store checks remain unchanged.
// This observes qualification at now, not authority to adopt or execute.
func ReviewSelectionSnapshot(library Library, life *Lifecycle, records record.Store, now time.Time) (SelectionStatus, error) {
	if now.IsZero() {
		return SelectionStatus{}, errors.New("selection review requires a current time")
	}
	if err := library.Validate(); err != nil {
		return SelectionStatus{}, err
	}
	value := emptyLifecycle(library.Name)
	if life != nil {
		value = *life
	}
	if value.Name != library.Name {
		return SelectionStatus{}, errors.New("selection lifecycle belongs to another role")
	}
	if err := value.Validate(); err != nil {
		return SelectionStatus{}, err
	}
	status, err := reviewSelectionSnapshot(library, value, records, now)
	if err != nil {
		return SelectionStatus{}, err
	}
	return cloneConfirmation(status)
}
