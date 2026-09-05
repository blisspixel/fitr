package main

import (
	"errors"
	"fmt"

	"github.com/blisspixel/fitr/internal/capacity"
	"github.com/blisspixel/fitr/internal/role"
)

func validateRoleCapacityBeforeLoad(plan role.ConfirmationPlan, point int, run *runExecution) error {
	if err := role.ValidateConfirmationCapacityPoint(plan, point, run.result); err != nil {
		return err
	}
	expected := plan.Candidates[point-1].Capacity
	if expected.ResourceDomain == "" {
		return errors.New("confirmation preflight needs an exploration capacity domain; collect current evidence first")
	}
	if expected.HostRemainderBytes > 0 {
		return errors.New("confirmation preflight needs a combined host and accelerator memory policy for partial offloading")
	}
	return requireRoleHeadroom(run.result.CapacityPlan.Policy, expected.ObservedDomainBytes)
}

// A previous resident allocation is a minimum resource check for this exact
// configuration. It does not establish peak allocation or a future success.
// Keep transient availability separate from the operator's sealed ceiling.
func requireRoleHeadroom(policy capacity.Policy, resident int64) error {
	if resident <= 0 || policy.CurrentAvailable == nil || policy.UsableBudgetBytes == nil {
		return errors.New("confirmation preflight needs current memory availability, an explicit budget and measured resident bytes")
	}
	available := min(policy.CurrentAvailable.Bytes, *policy.UsableBudgetBytes)
	if policy.Container != nil {
		available = min(available, policy.Container.HeadroomBytes)
	}
	if resident >= available {
		return fmt.Errorf("confirmation preflight blocked: previous resident allocation %d bytes leaves no headroom within %d currently usable bytes", resident, available)
	}
	return nil
}
