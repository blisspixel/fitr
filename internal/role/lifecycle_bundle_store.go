package role

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/blisspixel/fitr/internal/atomicfile"
	"github.com/blisspixel/fitr/internal/boundedio"
)

// SaveConfirmationBundle preserves one exact bundle per issued plan. A crash
// between this write and completion leaves the attempt unqualified and cannot
// authorize rerunning or resuming the same plan.
func (store Store) SaveConfirmationBundle(bundle ConfirmationBundle) (string, error) {
	data, err := bundle.JSON()
	if err != nil {
		return "", err
	}
	if len(data) > maximumConfirmationBundleBytes {
		return "", errors.New("confirmation bundle exceeds 64 MiB")
	}
	if err := store.checkDirectory(false); err != nil {
		return "", err
	}
	guard, err := store.acquire()
	if err != nil {
		return "", err
	}
	defer func() { _ = guard.Release() }()
	life, err := store.LoadLifecycle(bundle.Plan.Spec.Name)
	if err != nil {
		return "", err
	}
	plan, err := life.issuedPlan(bundle.Plan.PlanSHA256)
	if err != nil || !sameLifecycleValue(plan, bundle.Plan) {
		return "", errors.New("bundle does not match an issued role confirmation plan")
	}
	directory, err := store.lifecycleDirectory(".confirmations", true)
	if err != nil {
		return "", err
	}
	path := filepath.Join(directory, strings.TrimPrefix(plan.PlanSHA256, "sha256:")+".json")
	if err := rejectRoleSymlink(path); err == nil {
		existing, err := boundedio.ReadFile(path, maximumConfirmationBundleBytes)
		if err != nil {
			return "", err
		}
		if !bytes.Equal(existing, data) {
			return "", errors.New("issued confirmation already has a different immutable bundle")
		}
		return path, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := atomicfile.Write(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}
