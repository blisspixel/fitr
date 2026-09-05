package role

import (
	"errors"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/capacity"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/record"
)

// ConfirmationCapacity preserves operator controls, not transient available
// memory. ObservedResidentBytes is an evidence-bound preflight lower bound;
// it is not a prediction of full peak allocation. Accelerator and host bytes
// remain separate; unified memory is counted once in its shared domain.
type ConfirmationCapacity struct {
	ResourceDomain        capacity.ResourceDomain `json:"resource_domain,omitempty"`
	OperatorBudgetBytes   *int64                  `json:"operator_budget_bytes,omitempty"`
	OperatorReserveBytes  *int64                  `json:"operator_reserve_bytes,omitempty"`
	ObservedResidentBytes int64                   `json:"observed_resident_bytes"`
	ObservedDomainBytes   int64                   `json:"observed_domain_bytes"`
	HostRemainderBytes    int64                   `json:"host_remainder_bytes"`
	AttributionSource     string                  `json:"attribution_source"`
	Source                string                  `json:"source"`
}

func confirmationCapacityFrom(spec Spec, result *record.Record) (ConfirmationCapacity, error) {
	value, err := confirmationAllocationFrom(result)
	if err != nil {
		return ConfirmationCapacity{}, err
	}
	value.Source = "exploration_policy"
	if result.CapacityPlan != nil {
		policy := result.CapacityPlan.Policy
		if value.ResourceDomain != policy.ResourceDomain {
			return ConfirmationCapacity{}, errors.New("exploration capacity policy domain differs from its device")
		}
		value.OperatorBudgetBytes, value.OperatorReserveBytes = policy.OperatorBudgetBytes, policy.OperatorReserveBytes
	}
	if value.OperatorBudgetBytes == nil && value.OperatorReserveBytes == nil {
		var ceiling int64
		for _, requirement := range spec.Decision.Requirements {
			if requirement.Capacity == nil {
				continue
			}
			constraint := requirement.Capacity
			if constraint.RequestedContext != 0 && constraint.RequestedContext != result.ContextSize() {
				return ConfirmationCapacity{}, errors.New("role capacity context differs from exploration")
			}
			bytes := constraint.MaximumResidentBytes
			if bytes == 0 {
				bytes = int64(constraint.MaximumResidentGB * float64(device.GB))
			}
			if ceiling == 0 || bytes < ceiling {
				ceiling = bytes
			}
		}
		value.OperatorBudgetBytes, value.Source = &ceiling, "role_capacity_floor"
	}
	return value, value.validate()
}

func (value ConfirmationCapacity) validate() error {
	if value.ResourceDomain != capacity.DomainAccelerator && value.ResourceDomain != capacity.DomainHost && value.ResourceDomain != capacity.DomainUnified {
		return errors.New("invalid confirmation capacity domain")
	}
	if err := value.validateAttribution(); err != nil {
		return err
	}
	if value.Source != "exploration_policy" && value.Source != "role_capacity_floor" {
		return errors.New("invalid confirmation capacity source")
	}
	if (value.OperatorBudgetBytes == nil) == (value.OperatorReserveBytes == nil) || value.ObservedResidentBytes <= 0 {
		return errors.New("confirmation requires one explicit capacity control and observed resident bytes")
	}
	if value.OperatorBudgetBytes != nil && (*value.OperatorBudgetBytes <= 0 || *value.OperatorBudgetBytes > 1e18) {
		return errors.New("confirmation capacity budget is outside its bounds")
	}
	if value.OperatorReserveBytes != nil && (*value.OperatorReserveBytes < 0 || *value.OperatorReserveBytes > 1e18) {
		return errors.New("confirmation capacity reserve is outside its bounds")
	}
	return nil
}

func confirmationAllocationFrom(result *record.Record) (ConfirmationCapacity, error) {
	allocation, ok := result.Memory.VerifiedAllocationAt(result.ContextSize())
	if !ok {
		return ConfirmationCapacity{}, errors.New("confirmation requires exact-context verified resident allocation")
	}
	value := ConfirmationCapacity{ResourceDomain: confirmationCapacityDomain(result.Device),
		ObservedResidentBytes: allocation.ResidentBytes, ObservedDomainBytes: allocation.ResidentBytes,
		AttributionSource: "memory.verified_allocation_at_requested_context"}
	switch value.ResourceDomain {
	case capacity.DomainAccelerator:
		value.ObservedDomainBytes = allocation.AcceleratorBytes
		value.HostRemainderBytes = allocation.ResidentBytes - allocation.AcceleratorBytes
	case capacity.DomainHost:
		if allocation.AcceleratorBytes != 0 {
			return ConfirmationCapacity{}, errors.New("host-only placement has accelerator allocation")
		}
	}
	return value, value.validateAttribution()
}

func confirmationCapacityDomain(fp device.Fingerprint) capacity.ResourceDomain {
	if device.IsUnifiedMemoryGPU(fp.GPU) || fp.VRAMSource == device.NVIDIAUnifiedMemorySource || fp.VRAMSource == device.NVIDIAUnifiedProbeSource {
		return capacity.DomainUnified
	}
	if strings.EqualFold(strings.TrimSpace(fp.InferenceDevice), "CPU") || strings.TrimSpace(fp.GPU) == "" {
		return capacity.DomainHost
	}
	return capacity.DomainAccelerator
}

func (value ConfirmationCapacity) validateAttribution() error {
	if value.AttributionSource != "memory.verified_allocation_at_requested_context" || value.ObservedResidentBytes <= 0 ||
		value.ObservedResidentBytes > 1e18 || value.ObservedDomainBytes <= 0 || value.ObservedDomainBytes > value.ObservedResidentBytes ||
		value.HostRemainderBytes < 0 || value.HostRemainderBytes != value.ObservedResidentBytes-value.ObservedDomainBytes {
		return errors.New("confirmation capacity attribution is invalid")
	}
	if value.ResourceDomain != capacity.DomainAccelerator && value.HostRemainderBytes != 0 {
		return errors.New("shared or host capacity cannot carry accelerator remainder")
	}
	return nil
}

// ValidateConfirmationCapacityPoint must run after fresh capacity preparation
// and before loading a model. The caller separately checks fresh headroom;
// explicit operator budgets are not automatically clamped by BuildPolicy.
func ValidateConfirmationCapacityPoint(plan ConfirmationPlan, pointIndex int, result *record.Record) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	if result == nil || result.CapacityPlan == nil || pointIndex < 1 || pointIndex > len(plan.Candidates) {
		return errors.New("confirmation requires a prepared capacity policy")
	}
	if err := result.CapacityPlan.Validate(); err != nil {
		return err
	}
	want, policy := plan.Candidates[pointIndex-1].Capacity, result.CapacityPlan.Policy
	if policy.ResourceDomain != want.ResourceDomain ||
		!confirmationEqual(want.OperatorBudgetBytes, policy.OperatorBudgetBytes) ||
		!confirmationEqual(want.OperatorReserveBytes, policy.OperatorReserveBytes) {
		return errors.New("confirmation capacity controls differ from the sealed plan")
	}
	prediction := result.CapacityPlan.Prediction
	if prediction.ArtifactSHA256 != plan.Candidates[pointIndex-1].Model.RuntimeBoundDigest() || prediction.RequestedContext != plan.Protocol.RequestedContext {
		return errors.New("confirmation capacity prediction binds another artifact or context")
	}
	started, err := confirmationTime(result.StartedAt)
	if err != nil {
		return err
	}
	expires, _ := confirmationTime(plan.ExpiresAt)
	return confirmationCapacityWindow(result.CapacityPlan, started, expires)
}

func confirmationCapacityWindow(allocation *capacity.Plan, started, before time.Time) error {
	policy := allocation.Policy
	created, err := confirmationTime(allocation.Prediction.CreatedAt)
	if err != nil || created.Before(started) || created.After(before) {
		return errors.New("confirmation capacity prediction timing is invalid")
	}
	for _, observation := range []*capacity.MemoryObservation{policy.Addressable, policy.CurrentAvailable} {
		if observation != nil {
			observed, err := time.Parse(time.RFC3339Nano, observation.ObservedAt)
			if err != nil || observed.Before(started) || observed.After(before) {
				return errors.New("confirmation cannot reuse earlier capacity observations")
			}
		}
	}
	if policy.Container != nil {
		observed, err := time.Parse(time.RFC3339Nano, policy.Container.ObservedAt)
		if err != nil || observed.Before(started) || observed.After(before) {
			return errors.New("confirmation cannot reuse earlier container observations")
		}
	}
	return nil
}
