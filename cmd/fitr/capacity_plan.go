package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/advise"
	"github.com/blisspixel/fitr/internal/capacity"
	"github.com/blisspixel/fitr/internal/device"
)

func (run *runExecution) prepareCapacityPlan() error {
	if !run.result.TaskPlan.Memory {
		return nil
	}
	domain := capacityDomain(run.result.Device)
	observedAt := time.Now().UTC()
	policyInput := capacity.PolicyInput{ResourceDomain: domain}
	policyInput.Addressable = addressableObservation(run.result.Device, domain, observedAt)
	policyInput.CurrentAvailable = availableObservation(run.ctx, domain, observedAt)
	policyInput.Container = containerObservation(domain, observedAt)
	var err error
	policyInput.OperatorBudgetBytes, err = optionalPositiveGiBBytes(run.opts.capacityBudgetGB)
	if err != nil {
		return err
	}
	policyInput.OperatorReserveBytes, err = optionalNonnegativeGiBBytes(run.opts.capacityReserveGB)
	if err != nil {
		return err
	}
	policy, err := capacity.BuildPolicy(policyInput)
	if err != nil {
		if run.opts.capacityReserveGB != nil && policyInput.CurrentAvailable == nil {
			return errors.New("--capacity-reserve-gb needs a current memory reading on this platform; use --capacity-budget-gb for an explicit safe budget")
		}
		return err
	}
	prediction, err := run.capacityPrediction(policy, domain, observedAt)
	if err != nil {
		return err
	}
	plan := capacity.Plan{Schema: capacity.PlanSchema, Policy: policy, Prediction: prediction}
	if err := plan.Validate(); err != nil {
		return err
	}
	run.result.CapacityPlan = &plan
	if policy.UsableBudgetBytes != nil {
		run.display.Note(fmt.Sprintf("capacity policy sealed before allocation: %.2f GiB usable via %s; swap excluded",
			float64(*policy.UsableBudgetBytes)/advise.GiB, policy.Formula), "")
	}
	return nil
}

func capacityDomain(fp device.Fingerprint) capacity.ResourceDomain {
	if device.IsUnifiedMemoryGPU(fp.GPU) || fp.VRAMSource == device.NVIDIAUnifiedMemorySource ||
		fp.VRAMSource == device.NVIDIAUnifiedProbeSource {
		return capacity.DomainUnified
	}
	if strings.EqualFold(strings.TrimSpace(fp.InferenceDevice), "CPU") || strings.TrimSpace(fp.GPU) == "" {
		return capacity.DomainHost
	}
	return capacity.DomainAccelerator
}

func addressableObservation(fp device.Fingerprint, domain capacity.ResourceDomain,
	observedAt time.Time) *capacity.MemoryObservation {
	gb, source := fp.VRAMGb, fp.VRAMSource
	if domain == capacity.DomainHost {
		gb, source = fp.RAMGb, "system physical memory"
	}
	if domain == capacity.DomainUnified && (source == device.AppleWiredLimitSource ||
		source == device.AppleAssumedShareSource) {
		// These sources are GPU policy limits, not addressable-pool readings.
		return nil
	}
	bytes, err := gibBytes(gb)
	if err != nil || strings.TrimSpace(source) == "" {
		return nil
	}
	return &capacity.MemoryObservation{
		Kind: capacity.ObservationAddressable, ResourceDomain: domain,
		Bytes: bytes, Source: source, ObservedAt: observedAt.Format(time.RFC3339Nano),
	}
}

func availableObservation(ctx context.Context, domain capacity.ResourceDomain,
	observedAt time.Time) *capacity.MemoryObservation {
	var bytes int64
	var source string
	var ok bool
	if domain == capacity.DomainUnified || domain == capacity.DomainHost {
		bytes, source, ok = device.SystemMemoryAvailable(ctx)
	} else {
		var gb float64
		gb, source, ok = device.AvailableVRAMWithSource(ctx)
		if ok {
			bytes, _ = gibBytes(gb)
		}
	}
	if !ok || bytes <= 0 {
		return nil
	}
	return &capacity.MemoryObservation{
		Kind: capacity.ObservationCurrentAvailable, ResourceDomain: domain,
		Bytes: bytes, Source: source, ObservedAt: observedAt.Format(time.RFC3339Nano),
	}
}

func containerObservation(domain capacity.ResourceDomain, observedAt time.Time) *capacity.ContainerReceipt {
	if domain == capacity.DomainAccelerator {
		return nil
	}
	observed, ok := device.ContainerMemoryLimit()
	if !ok {
		return nil
	}
	return &capacity.ContainerReceipt{
		ResourceDomain: domain, LimitBytes: observed.LimitBytes,
		CurrentBytes: observed.CurrentBytes, HeadroomBytes: observed.LimitBytes - observed.CurrentBytes,
		Source: observed.Source, ObservedAt: observedAt.Format(time.RFC3339Nano),
	}
}

func (run *runExecution) capacityPrediction(policy capacity.Policy, domain capacity.ResourceDomain,
	createdAt time.Time) (capacity.Prediction, error) {
	memoryCtx := run.opts.memoryCtx
	if memoryCtx <= 0 {
		memoryCtx = memoryProbeCtx
	}
	artifactBytes := run.resolved.Identity.SizeBytes
	if artifactBytes <= 0 {
		artifactBytes = run.resolved.Info.Size
	}
	var artifact *int64
	missing := make([]string, 0, 2)
	if artifactBytes > 0 {
		artifact = &artifactBytes
	} else {
		missing = append(missing, "artifact bytes")
	}
	arch := advise.ArchFromKVs(run.resolved.Info.Info)
	kvElementBytes, kvType := 2.0, "f16 assumed"
	if configured := strings.TrimSpace(run.result.Device.Config["OLLAMA_KV_CACHE_TYPE"]); configured != "" {
		kvType = configured
		if parsed, ok := advise.KVElemBytes(configured); ok {
			kvElementBytes = parsed
		} else {
			kvElementBytes = 0
		}
	}
	var kv *int64
	if projected, ok := advise.ProjectKVBytes(arch, memoryCtx, kvElementBytes); ok {
		kv = &projected
	} else {
		missing = append(missing, "conventional KV projection")
	}
	return capacity.BuildPrediction(policy, capacity.PredictionInput{
		CreatedAt: createdAt, ArtifactSHA256: run.resolved.Identity.Value,
		ResourceDomain: domain, RequestedContext: memoryCtx, Architecture: arch.Name,
		KVDataType: kvType, KVElementBytes: kvElementBytes,
		PlacementAssumption: "runtime default; unverified before allocation",
		ArtifactBytes:       artifact, KVBytes: kv, Missing: missing,
		Excluded: []string{"in-flight compute and activation peaks", "runtime buffers, mappings, and allocator overhead"},
	})
}

func optionalPositiveGiBBytes(value *float64) (*int64, error) {
	if value == nil {
		return nil, nil
	}
	bytes, err := gibBytes(*value)
	if err != nil {
		return nil, err
	}
	return &bytes, nil
}

func optionalNonnegativeGiBBytes(value *float64) (*int64, error) {
	if value == nil {
		return nil, nil
	}
	if *value == 0 {
		bytes := int64(0)
		return &bytes, nil
	}
	return optionalPositiveGiBBytes(value)
}

func gibBytes(gb float64) (int64, error) {
	bytes := gb * advise.GiB
	if gb <= 0 || math.IsNaN(bytes) || math.IsInf(bytes, 0) || bytes > math.MaxInt64 {
		return 0, errors.New("capacity value is outside the supported byte range")
	}
	return int64(math.Round(bytes)), nil
}
