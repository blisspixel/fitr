package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/blisspixel/fitr/internal/automation"
	"github.com/blisspixel/fitr/internal/autoruntime"
	"github.com/blisspixel/fitr/internal/capacity"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/modelref"
	"github.com/blisspixel/fitr/internal/ollama"
)

type autoPlacementBackend interface {
	PS(context.Context) ([]ollama.RunningModel, error)
	Accel(context.Context) string
}

type autoAllocationObserver interface {
	WaitAccel(context.Context, string) (string, error)
}

// One guard belongs to one point. Its original ceiling remains fixed; fresh
// free memory is compared only with the reserve because the model is resident.
type autoPlacementGuard struct {
	mu        sync.Mutex
	backend   autoPlacementBackend
	candidate automation.Candidate
	context   int
	domain    capacity.ResourceDomain
	ceiling   int64
	reserve   int64
	accel     string
	failed    error
	available func(context.Context, capacity.ResourceDomain, time.Time) (*capacity.MemoryObservation, error)
}

func newAutoPlacementGuard(backend autoPlacementBackend, candidate automation.Candidate, runtime autoruntime.Spec, policy capacity.Policy, expected device.Fingerprint) (*autoPlacementGuard, error) {
	if err := runtime.Validate(); err != nil {
		return nil, err
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if backend == nil || candidate.Model == "" || candidate.ArtifactDigest == "" || autoResidentDigest(candidate.ArtifactDigest) != candidate.ArtifactDigest ||
		policy.UsableBudgetBytes == nil || policy.OperatorReserveBytes == nil || *policy.OperatorReserveBytes != runtime.ReserveBytes {
		return nil, errors.New("auto placement requires the exact candidate and original explicit reserve policy")
	}
	if policy.ResourceDomain != capacity.DomainHost && policy.ResourceDomain != capacity.DomainAccelerator {
		return nil, errors.New("auto placement requires an explicit host or dedicated accelerator domain")
	}
	guard := &autoPlacementGuard{backend: backend, candidate: candidate, context: runtime.NumCtx,
		domain: policy.ResourceDomain, ceiling: *policy.UsableBudgetBytes, reserve: *policy.OperatorReserveBytes,
		available: func(ctx context.Context, domain capacity.ResourceDomain, at time.Time) (*capacity.MemoryObservation, error) {
			return autoPlacementAvailable(ctx, domain, at, expected)
		}}
	return guard, nil
}

func autoPlacementAvailable(ctx context.Context, domain capacity.ResourceDomain, at time.Time, expected device.Fingerprint) (*capacity.MemoryObservation, error) {
	if domain == capacity.DomainHost {
		return availableObservation(ctx, domain, at), nil
	}
	bytes, err := device.SingleNVIDIAAvailable(ctx, expected)
	if err != nil {
		return nil, err
	}
	return &capacity.MemoryObservation{Kind: capacity.ObservationCurrentAvailable, ResourceDomain: domain,
		Bytes: bytes, Source: "single nvidia-smi name,memory.total,memory.free", ObservedAt: at.UTC().Format(time.RFC3339Nano)}, nil
}

// Observe runs after an actual inference attempt and its timing snapshot. It
// performs metadata observations only and cannot extend the admitted deadline.
func (guard *autoPlacementGuard) Observe(ctx context.Context, attempt ollama.InferenceAttempt) error {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if guard.failed != nil {
		return guard.failed
	}
	if err := guard.observe(ctx, attempt); err != nil {
		guard.failed = fmt.Errorf("%w: auto post-attempt placement: %w", ollama.ErrUnverifiedLocalExecution, err)
		return guard.failed
	}
	return nil
}

func (guard *autoPlacementGuard) observe(ctx context.Context, attempt ollama.InferenceAttempt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); !ok || !deadline.After(time.Now()) {
		return errors.New("placement observation has no live admitted deadline")
	}
	if (attempt.Kind != ollama.InferenceGenerate && attempt.Kind != ollama.InferenceChat) || attempt.MaxOutputTokens <= 0 ||
		!modelref.SameServed(attempt.Model, guard.candidate.Model) || attempt.NumCtx != guard.context {
		return errors.New("inference request differs from the fixed candidate or context")
	}
	models, err := guard.backend.PS(ctx)
	if err != nil {
		return err
	}
	if err := guard.validateAllocation(models); err != nil {
		return err
	}
	if err := guard.validateAccel(ctx); err != nil {
		return err
	}
	observedAt := time.Now()
	available, err := guard.available(ctx, guard.domain, observedAt)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if available == nil || available.Validate() != nil || available.Kind != capacity.ObservationCurrentAvailable || available.ResourceDomain != guard.domain {
		return errors.New("current free memory is unavailable for the admitted resource domain")
	}
	at, err := time.Parse(time.RFC3339Nano, available.ObservedAt)
	if err != nil || at.Before(observedAt) || at.After(time.Now()) {
		return errors.New("post-attempt memory observation is stale or has invalid timing")
	}
	if available.Bytes <= guard.reserve {
		return errors.New("resident candidate leaves no current headroom beyond the declared reserve")
	}
	return nil
}

func (guard *autoPlacementGuard) validateAllocation(models []ollama.RunningModel) error {
	if len(models) != 1 {
		return errors.New("owned runtime must report exactly one resident candidate")
	}
	model := models[0]
	if !modelref.SameServed(model.Name, guard.candidate.Model) || autoResidentDigest(model.Digest) != guard.candidate.ArtifactDigest || model.ContextLength != guard.context {
		return errors.New("resident artifact or effective context differs from the admitted candidate")
	}
	if model.Size <= 0 || model.Size >= guard.ceiling || model.SizeVRAM < 0 || model.SizeVRAM > model.Size {
		return errors.New("resident allocation is invalid or exceeds the original usable ceiling")
	}
	if (guard.domain == capacity.DomainAccelerator && model.SizeVRAM != model.Size) || (guard.domain == capacity.DomainHost && model.SizeVRAM != 0) {
		return errors.New("resident candidate changed memory domain or uses partial offload")
	}
	if model.ExpiresAt != "" {
		expires, err := time.Parse(time.RFC3339Nano, model.ExpiresAt)
		if err != nil || !expires.After(time.Now()) {
			return errors.New("resident candidate has an invalid or expired allocation receipt")
		}
	}
	return nil
}

func (guard *autoPlacementGuard) validateAccel(ctx context.Context) error {
	expected := "cuda"
	if guard.domain == capacity.DomainHost {
		expected = "cpu"
	}
	var accel string
	if observer, ok := guard.backend.(autoAllocationObserver); ok && guard.accel == "" {
		var err error
		accel, err = observer.WaitAccel(ctx, expected)
		if err != nil {
			return err
		}
	} else {
		accel = guard.backend.Accel(ctx)
	}
	accel = device.NormalizeAccel(accel)
	if accel != expected || (guard.accel != "" && accel != guard.accel) {
		return errors.New("owned compute backend is unknown, changed or disagrees with the admitted domain")
	}
	guard.accel = accel
	return nil
}

func autoResidentDigest(value string) string {
	if value != strings.TrimSpace(value) {
		return ""
	}
	encoded := strings.TrimPrefix(value, "sha256:")
	if len(encoded) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(encoded); err != nil {
		return ""
	}
	return "sha256:" + strings.ToLower(encoded)
}
