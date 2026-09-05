package role

import (
	"errors"

	"github.com/blisspixel/fitr/internal/record"
)

func validateCandidateRuntime(candidate, first ConfirmationCandidate) error {
	if candidate.RuntimeBinding != nil {
		if err := candidate.RuntimeBinding.ValidateFor(candidate.Model); err != nil {
			return err
		}
	}
	if !record.SameRuntimeProfile(candidate.RuntimeBinding, first.RuntimeBinding) {
		return errors.New("confirmation candidates have different owned runtime profiles")
	}
	return nil
}
