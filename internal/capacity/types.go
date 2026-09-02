// Package capacity defines the policy and prediction receipts that exist
// before a runtime allocation is observed.
package capacity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const (
	PlanSchema       = "fitr.capacity.plan.v1"
	PolicySchema     = "fitr.capacity.policy.v1"
	PredictionSchema = "fitr.capacity.prediction.v1"
)

type ResourceDomain string

const (
	DomainAccelerator ResourceDomain = "accelerator_memory"
	DomainUnified     ResourceDomain = "unified_memory"
	DomainHost        ResourceDomain = "host_memory"
)

type ObservationKind string

const (
	ObservationAddressable      ObservationKind = "addressable_capacity"
	ObservationCurrentAvailable ObservationKind = "current_available"
)

type MemoryObservation struct {
	Kind           ObservationKind `json:"kind"`
	ResourceDomain ResourceDomain  `json:"resource_domain"`
	Bytes          int64           `json:"bytes"`
	Source         string          `json:"source"`
	ObservedAt     string          `json:"observed_at"`
}

type ContainerReceipt struct {
	ResourceDomain ResourceDomain `json:"resource_domain"`
	LimitBytes     int64          `json:"limit_bytes"`
	CurrentBytes   int64          `json:"current_bytes"`
	HeadroomBytes  int64          `json:"headroom_bytes"`
	Source         string         `json:"source"`
	ObservedAt     string         `json:"observed_at"`
}

type SwapPolicy string

const SwapExcluded SwapPolicy = "excluded"

type BudgetFormula string

const (
	FormulaOperatorBudget          BudgetFormula = "operator_budget"
	FormulaAvailableMinusReserve   BudgetFormula = "current_available-operator_reserve"
	FormulaConstrainedMinusReserve BudgetFormula = "min(current_available,container_headroom)-operator_reserve"
)

// Policy records the exact facts and arithmetic that define a safe budget.
// A policy may preserve addressable and transient observations without having
// a UsableBudgetBytes value. That is a sealed gap, not an implicit default.
type Policy struct {
	Schema               string             `json:"schema"`
	ResourceDomain       ResourceDomain     `json:"resource_domain"`
	Addressable          *MemoryObservation `json:"addressable,omitempty"`
	CurrentAvailable     *MemoryObservation `json:"current_available,omitempty"`
	Container            *ContainerReceipt  `json:"container,omitempty"`
	OperatorBudgetBytes  *int64             `json:"operator_budget_bytes,omitempty"`
	OperatorReserveBytes *int64             `json:"operator_reserve_bytes,omitempty"`
	Swap                 SwapPolicy         `json:"swap_policy"`
	UsableBudgetBytes    *int64             `json:"usable_budget_bytes,omitempty"`
	Formula              BudgetFormula      `json:"formula,omitempty"`
}

type PolicyInput struct {
	ResourceDomain       ResourceDomain
	Addressable          *MemoryObservation
	CurrentAvailable     *MemoryObservation
	Container            *ContainerReceipt
	OperatorBudgetBytes  *int64
	OperatorReserveBytes *int64
}

func BuildPolicy(input PolicyInput) (Policy, error) {
	policy := Policy{
		Schema: PolicySchema, ResourceDomain: input.ResourceDomain,
		Addressable:      cloneObservation(input.Addressable),
		CurrentAvailable: cloneObservation(input.CurrentAvailable),
		Container:        cloneContainer(input.Container), Swap: SwapExcluded,
		OperatorBudgetBytes:  cloneInt64(input.OperatorBudgetBytes),
		OperatorReserveBytes: cloneInt64(input.OperatorReserveBytes),
	}
	if input.OperatorBudgetBytes != nil && input.OperatorReserveBytes != nil {
		return Policy{}, errors.New("operator budget and reserve are mutually exclusive")
	}
	if input.OperatorBudgetBytes != nil {
		policy.UsableBudgetBytes = cloneInt64(input.OperatorBudgetBytes)
		policy.Formula = FormulaOperatorBudget
	}
	if input.OperatorReserveBytes != nil {
		if policy.CurrentAvailable == nil {
			return Policy{}, errors.New("operator reserve requires a current-availability observation")
		}
		base := policy.CurrentAvailable.Bytes
		policy.Formula = FormulaAvailableMinusReserve
		if policy.Container != nil && policy.Container.HeadroomBytes < base {
			base = policy.Container.HeadroomBytes
			policy.Formula = FormulaConstrainedMinusReserve
		}
		usable := base - *input.OperatorReserveBytes
		if usable <= 0 {
			return Policy{}, errors.New("operator reserve leaves no usable capacity")
		}
		policy.UsableBudgetBytes = &usable
	}
	if err := policy.Validate(); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

func (p Policy) Validate() error {
	if p.Schema != PolicySchema {
		return fmt.Errorf("unsupported capacity policy schema %q", p.Schema)
	}
	if !validDomain(p.ResourceDomain) {
		return fmt.Errorf("unsupported capacity resource domain %q", p.ResourceDomain)
	}
	if p.Swap != SwapExcluded {
		return fmt.Errorf("unsupported swap policy %q", p.Swap)
	}
	if err := p.validateObservations(); err != nil {
		return err
	}
	if err := p.validateOperatorInputs(); err != nil {
		return err
	}
	if err := p.validateCapacityRelationships(); err != nil {
		return err
	}
	return p.validateFormula()
}

func (p Policy) validateObservations() error {
	for name, observation := range map[string]*MemoryObservation{
		"addressable": p.Addressable, "current available": p.CurrentAvailable,
	} {
		if observation != nil {
			if err := observation.Validate(); err != nil {
				return fmt.Errorf("%s observation: %w", name, err)
			}
			if observation.ResourceDomain != p.ResourceDomain {
				return fmt.Errorf("%s observation uses a different resource domain", name)
			}
		}
	}
	if p.Addressable != nil && p.Addressable.Kind != ObservationAddressable {
		return errors.New("addressable observation has the wrong kind")
	}
	if p.CurrentAvailable != nil && p.CurrentAvailable.Kind != ObservationCurrentAvailable {
		return errors.New("current-availability observation has the wrong kind")
	}
	if p.Container != nil {
		if err := p.Container.Validate(); err != nil {
			return fmt.Errorf("container receipt: %w", err)
		}
		if p.Container.ResourceDomain != p.ResourceDomain {
			return errors.New("container receipt uses a different resource domain")
		}
	}
	return nil
}

func (p Policy) validateOperatorInputs() error {
	if p.OperatorBudgetBytes != nil && p.OperatorReserveBytes != nil {
		return errors.New("operator budget and reserve are mutually exclusive")
	}
	if p.OperatorBudgetBytes != nil && *p.OperatorBudgetBytes <= 0 {
		return errors.New("operator budget must be positive")
	}
	if p.OperatorReserveBytes != nil && *p.OperatorReserveBytes < 0 {
		return errors.New("operator reserve cannot be negative")
	}
	return nil
}

func (p Policy) validateCapacityRelationships() error {
	if p.Addressable != nil {
		if p.CurrentAvailable != nil && p.CurrentAvailable.Bytes > p.Addressable.Bytes {
			return errors.New("current availability exceeds addressable capacity")
		}
		if p.UsableBudgetBytes != nil && *p.UsableBudgetBytes > p.Addressable.Bytes {
			return errors.New("usable budget exceeds addressable capacity")
		}
	}
	return nil
}

func (p Policy) validateFormula() error {
	switch p.Formula {
	case "":
		if p.UsableBudgetBytes != nil || p.OperatorBudgetBytes != nil || p.OperatorReserveBytes != nil {
			return errors.New("capacity policy has budget inputs without a formula")
		}
	case FormulaOperatorBudget:
		if p.OperatorBudgetBytes == nil || p.UsableBudgetBytes == nil ||
			*p.OperatorBudgetBytes != *p.UsableBudgetBytes {
			return errors.New("operator-budget formula does not match its usable budget")
		}
	case FormulaAvailableMinusReserve, FormulaConstrainedMinusReserve:
		if p.CurrentAvailable == nil || p.OperatorReserveBytes == nil || p.UsableBudgetBytes == nil {
			return errors.New("reserve formula is missing an input or result")
		}
		base := p.CurrentAvailable.Bytes
		if p.Formula == FormulaConstrainedMinusReserve {
			if p.Container == nil {
				return errors.New("constrained formula is missing its container receipt")
			}
			base = min(base, p.Container.HeadroomBytes)
		}
		if *p.UsableBudgetBytes != base-*p.OperatorReserveBytes || *p.UsableBudgetBytes <= 0 {
			return errors.New("reserve formula does not match its usable budget")
		}
	default:
		return fmt.Errorf("unsupported capacity formula %q", p.Formula)
	}
	return nil
}

func (o MemoryObservation) Validate() error {
	if o.Kind != ObservationAddressable && o.Kind != ObservationCurrentAvailable {
		return fmt.Errorf("unsupported observation kind %q", o.Kind)
	}
	if !validDomain(o.ResourceDomain) {
		return fmt.Errorf("unsupported resource domain %q", o.ResourceDomain)
	}
	if o.Bytes <= 0 {
		return errors.New("bytes must be positive")
	}
	if strings.TrimSpace(o.Source) == "" || o.Source != strings.TrimSpace(o.Source) {
		return errors.New("source is required without surrounding whitespace")
	}
	if _, err := time.Parse(time.RFC3339Nano, o.ObservedAt); err != nil {
		return fmt.Errorf("observed_at: %w", err)
	}
	return nil
}

func (c ContainerReceipt) Validate() error {
	if !validDomain(c.ResourceDomain) {
		return fmt.Errorf("unsupported resource domain %q", c.ResourceDomain)
	}
	if c.LimitBytes <= 0 || c.CurrentBytes < 0 || c.CurrentBytes >= c.LimitBytes ||
		c.HeadroomBytes != c.LimitBytes-c.CurrentBytes {
		return errors.New("limit, current use, and headroom are inconsistent")
	}
	if strings.TrimSpace(c.Source) == "" || c.Source != strings.TrimSpace(c.Source) {
		return errors.New("source is required without surrounding whitespace")
	}
	if _, err := time.Parse(time.RFC3339Nano, c.ObservedAt); err != nil {
		return fmt.Errorf("observed_at: %w", err)
	}
	return nil
}

type PredictionState string

const (
	PredictionComponentProjection PredictionState = "component_projection"
	PredictionUnavailable         PredictionState = "unavailable"
)

// Prediction is created before the runtime allocation is observed. Known
// components remain separate from excluded runtime allocation. Artifact bytes
// may be mapped or placed across resource domains, so their sum is not called
// a lower bound and cannot establish fit or failure by itself.
type Prediction struct {
	Schema              string          `json:"schema"`
	CreatedAt           string          `json:"created_at"`
	ArtifactSHA256      string          `json:"artifact_sha256"`
	ResourceDomain      ResourceDomain  `json:"resource_domain"`
	RequestedContext    int             `json:"requested_context"`
	Architecture        string          `json:"architecture,omitempty"`
	KVDataType          string          `json:"kv_data_type,omitempty"`
	KVElementBytes      float64         `json:"kv_element_bytes,omitempty"`
	PlacementAssumption string          `json:"placement_assumption"`
	ArtifactBytes       *int64          `json:"artifact_bytes,omitempty"`
	KVBytes             *int64          `json:"kv_bytes,omitempty"`
	KnownComponentBytes *int64          `json:"known_component_bytes,omitempty"`
	State               PredictionState `json:"state"`
	Missing             []string        `json:"missing,omitempty"`
	Excluded            []string        `json:"excluded"`
	PolicySHA256        string          `json:"policy_sha256"`
}

func (p Prediction) Validate() error {
	if p.Schema != PredictionSchema {
		return fmt.Errorf("unsupported capacity prediction schema %q", p.Schema)
	}
	if _, err := time.Parse(time.RFC3339Nano, p.CreatedAt); err != nil {
		return fmt.Errorf("prediction created_at: %w", err)
	}
	if !digestPattern(p.ArtifactSHA256) {
		return errors.New("prediction artifact identity is not a SHA-256 digest")
	}
	if !validDomain(p.ResourceDomain) {
		return fmt.Errorf("unsupported prediction resource domain %q", p.ResourceDomain)
	}
	if p.RequestedContext <= 0 {
		return errors.New("prediction requested context must be positive")
	}
	if strings.TrimSpace(p.PlacementAssumption) == "" {
		return errors.New("prediction placement assumption is required")
	}
	if !digestPattern(p.PolicySHA256) {
		return errors.New("prediction policy digest is invalid")
	}
	if p.KVElementBytes < 0 || math.IsNaN(p.KVElementBytes) || math.IsInf(p.KVElementBytes, 0) {
		return errors.New("prediction KV element size is invalid")
	}
	for name, value := range map[string]*int64{
		"artifact": p.ArtifactBytes, "KV": p.KVBytes, "known component": p.KnownComponentBytes,
	} {
		if value != nil && *value <= 0 {
			return fmt.Errorf("prediction %s bytes must be positive", name)
		}
	}
	wantComponents, known, valid := knownComponentSum(p.ArtifactBytes, p.KVBytes)
	if !valid {
		return errors.New("prediction component sum overflows")
	}
	if known != (p.KnownComponentBytes != nil) || known && *p.KnownComponentBytes != wantComponents {
		return errors.New("prediction component sum does not match its known components")
	}
	if len(p.Excluded) == 0 {
		return errors.New("prediction must disclose excluded allocation")
	}
	switch p.State {
	case PredictionComponentProjection:
		if p.KnownComponentBytes == nil {
			return errors.New("component projection has no known component")
		}
	case PredictionUnavailable:
		if p.KnownComponentBytes != nil {
			return errors.New("unavailable prediction contains known components")
		}
	default:
		return fmt.Errorf("unsupported prediction state %q", p.State)
	}
	return nil
}

type Plan struct {
	Schema     string     `json:"schema"`
	Policy     Policy     `json:"policy"`
	Prediction Prediction `json:"prediction"`
}

func (p Plan) Validate() error {
	if p.Schema != PlanSchema {
		return fmt.Errorf("unsupported capacity plan schema %q", p.Schema)
	}
	if err := p.Policy.Validate(); err != nil {
		return fmt.Errorf("capacity policy: %w", err)
	}
	if err := p.Prediction.Validate(); err != nil {
		return fmt.Errorf("capacity prediction: %w", err)
	}
	if p.Policy.ResourceDomain != p.Prediction.ResourceDomain {
		return errors.New("capacity policy and prediction use different resource domains")
	}
	digest, err := p.Policy.Digest()
	if err != nil {
		return err
	}
	if p.Prediction.PolicySHA256 != digest {
		return errors.New("capacity prediction references a different policy")
	}
	return nil
}

func (p Policy) Digest() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("encode capacity policy: %w", err)
	}
	sum := sha256.Sum256(append([]byte("fitr.capacity.policy.v1\x00"), b...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func ClonePlan(source *Plan) *Plan {
	if source == nil {
		return nil
	}
	clone := *source
	clone.Policy.Addressable = cloneObservation(source.Policy.Addressable)
	clone.Policy.CurrentAvailable = cloneObservation(source.Policy.CurrentAvailable)
	clone.Policy.Container = cloneContainer(source.Policy.Container)
	clone.Policy.OperatorBudgetBytes = cloneInt64(source.Policy.OperatorBudgetBytes)
	clone.Policy.OperatorReserveBytes = cloneInt64(source.Policy.OperatorReserveBytes)
	clone.Policy.UsableBudgetBytes = cloneInt64(source.Policy.UsableBudgetBytes)
	clone.Prediction.ArtifactBytes = cloneInt64(source.Prediction.ArtifactBytes)
	clone.Prediction.KVBytes = cloneInt64(source.Prediction.KVBytes)
	clone.Prediction.KnownComponentBytes = cloneInt64(source.Prediction.KnownComponentBytes)
	clone.Prediction.Missing = append([]string(nil), source.Prediction.Missing...)
	clone.Prediction.Excluded = append([]string(nil), source.Prediction.Excluded...)
	return &clone
}

func knownComponentSum(values ...*int64) (int64, bool, bool) {
	var total int64
	known := false
	for _, value := range values {
		if value == nil {
			continue
		}
		if *value > math.MaxInt64-total {
			return 0, true, false
		}
		total += *value
		known = true
	}
	return total, known, true
}

func validDomain(domain ResourceDomain) bool {
	return domain == DomainAccelerator || domain == DomainUnified || domain == DomainHost
}

func digestPattern(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func cloneObservation(source *MemoryObservation) *MemoryObservation {
	if source == nil {
		return nil
	}
	clone := *source
	return &clone
}

func cloneContainer(source *ContainerReceipt) *ContainerReceipt {
	if source == nil {
		return nil
	}
	clone := *source
	return &clone
}

func cloneInt64(source *int64) *int64 {
	if source == nil {
		return nil
	}
	clone := *source
	return &clone
}
