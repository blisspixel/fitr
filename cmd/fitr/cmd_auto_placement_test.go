package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/automation"
	"github.com/blisspixel/fitr/internal/autoruntime"
	"github.com/blisspixel/fitr/internal/capacity"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/ollama"
)

type autoPlacementFixtureBackend struct {
	models        []ollama.RunningModel
	accel         string
	psErr, logErr error
	psCalls       int
	waits         int
	waitExpected  string
	seenContext   context.Context
}

func (backend *autoPlacementFixtureBackend) PS(ctx context.Context) ([]ollama.RunningModel, error) {
	backend.psCalls++
	backend.seenContext = ctx
	return backend.models, backend.psErr
}

func (backend *autoPlacementFixtureBackend) Accel(context.Context) string { return backend.accel }

func (backend *autoPlacementFixtureBackend) WaitAccel(_ context.Context, expected string) (string, error) {
	backend.waits++
	backend.waitExpected = expected
	return backend.accel, backend.logErr
}

func autoPlacementFixture(t *testing.T) (*autoPlacementGuard, *autoPlacementFixtureBackend, ollama.InferenceAttempt, context.Context) {
	t.Helper()
	digest := strings.Repeat("a", 64)
	root := t.TempDir()
	runtime := autoruntime.Spec{Schema: autoruntime.SpecSchema, Executable: filepath.Join(root, "ollama.exe"), ModelStore: filepath.Join(root, "models"),
		ExecutableSHA256: "sha256:" + digest, LibrariesSHA256: "sha256:" + digest, RuntimeVersion: "0.32.14", NumCtx: 4096, KVCacheType: "f16", ReserveBytes: 2 << 30}
	at := time.Now().Add(-time.Minute)
	policy, err := capacity.BuildPolicy(capacity.PolicyInput{ResourceDomain: capacity.DomainAccelerator, OperatorReserveBytes: &runtime.ReserveBytes,
		CurrentAvailable: &capacity.MemoryObservation{Kind: capacity.ObservationCurrentAvailable, ResourceDomain: capacity.DomainAccelerator,
			Bytes: 10 << 30, Source: "fixture before loading", ObservedAt: at.Format(time.RFC3339Nano)}})
	if err != nil {
		t.Fatal(err)
	}
	backend := &autoPlacementFixtureBackend{accel: "cuda", models: []ollama.RunningModel{{Name: "candidate:Latest", Digest: digest,
		Size: 4 << 30, SizeVRAM: 4 << 30, ContextLength: runtime.NumCtx, ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano)}}}
	guard, err := newAutoPlacementGuard(backend, automation.Candidate{Model: "candidate", ArtifactDigest: "sha256:" + digest}, runtime, policy, device.Fingerprint{})
	if err != nil {
		t.Fatal(err)
	}
	// Every test injects metadata. No installed runtime or device is contacted.
	guard.available = func(_ context.Context, domain capacity.ResourceDomain, now time.Time) (*capacity.MemoryObservation, error) {
		return &capacity.MemoryObservation{Kind: capacity.ObservationCurrentAvailable, ResourceDomain: domain,
			Bytes: 3 << 30, Source: "fixture after loading", ObservedAt: now.Format(time.RFC3339Nano)}, nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return guard, backend, ollama.InferenceAttempt{Kind: ollama.InferenceGenerate, Model: "candidate:latest", NumCtx: runtime.NumCtx, MaxOutputTokens: 64}, ctx
}

func TestAutoPlacementUsesFreshReceiptsWithoutDoubleCountingResidentBytes(t *testing.T) {
	guard, backend, attempt, ctx := autoPlacementFixture(t)
	for _, kind := range []string{ollama.InferenceGenerate, ollama.InferenceChat} {
		attempt.Kind = kind
		if err := guard.Observe(ctx, attempt); err != nil {
			t.Fatal("4 GiB resident plus 3 GiB free satisfies the original ceiling and 2 GiB reserve", err)
		}
	}
	if backend.psCalls != 2 || backend.waits != 1 || backend.waitExpected != "cuda" || backend.seenContext != ctx {
		t.Fatal("guard reused residency, repeated its initial log wait or replaced the admitted context")
	}
}

func TestAutoPlacementRejectsChangedReloadAndRemainsStopped(t *testing.T) {
	guard, backend, attempt, ctx := autoPlacementFixture(t)
	if err := guard.Observe(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	backend.models[0].SizeVRAM /= 2
	if err := guard.Observe(ctx, attempt); !errors.Is(err, ollama.ErrUnverifiedLocalExecution) {
		t.Fatal("later partial-offload reload retained the first full-GPU observation", err)
	}
	backend.models[0].SizeVRAM = backend.models[0].Size
	if err := guard.Observe(ctx, attempt); !errors.Is(err, ollama.ErrUnverifiedLocalExecution) || backend.psCalls != 2 {
		t.Fatal("later recovery erased a failed placement observation", err)
	}
}

func TestAutoPlacementRejectsWrongArtifactContextAndDomain(t *testing.T) {
	for _, scenario := range []string{"request model", "request context", "unload", "missing", "extra", "alias collision", "artifact", "missing digest", "context", "invalid size", "ceiling", "partial", "cpu", "expired", "unknown backend", "logs", "ps"} {
		t.Run(scenario, func(t *testing.T) {
			guard, backend, attempt, ctx := autoPlacementFixture(t)
			mutateAutoPlacementFixture(scenario, backend, &attempt)
			if err := guard.Observe(ctx, attempt); !errors.Is(err, ollama.ErrUnverifiedLocalExecution) {
				t.Fatal("unverified inference placement accepted", err)
			}
		})
	}
}

func mutateAutoPlacementFixture(scenario string, backend *autoPlacementFixtureBackend, attempt *ollama.InferenceAttempt) {
	switch scenario {
	case "request model":
		attempt.Model = "other"
	case "request context":
		attempt.NumCtx /= 2
	case "unload":
		attempt.MaxOutputTokens = 0
	case "missing":
		backend.models = nil
	case "extra", "alias collision":
		other := backend.models[0]
		other.Name = "other"
		if scenario == "alias collision" {
			other.Name = "candidate"
		}
		backend.models = append(backend.models, other)
	case "artifact":
		backend.models[0].Digest = strings.Repeat("b", 64)
	case "missing digest":
		backend.models[0].Digest = ""
	case "context":
		backend.models[0].ContextLength /= 2
	case "invalid size":
		backend.models[0].Size = 0
	case "ceiling":
		backend.models[0].Size, backend.models[0].SizeVRAM = 8<<30, 8<<30
	case "partial":
		backend.models[0].SizeVRAM /= 2
	case "cpu":
		backend.models[0].SizeVRAM = 0
	case "expired":
		backend.models[0].ExpiresAt = time.Now().Add(-time.Minute).Format(time.RFC3339Nano)
	case "unknown backend":
		backend.accel = ""
	case "logs":
		backend.logErr = errors.New("fixture allocation log is unverified")
	case "ps":
		backend.psErr = errors.New("fixture resident inventory is unavailable")
	}
}

func TestAutoPlacementRequiresCurrentReserveObservation(t *testing.T) {
	for _, scenario := range []string{"missing", "error", "old", "future", "domain", "at reserve"} {
		t.Run(scenario, func(t *testing.T) {
			guard, _, attempt, ctx := autoPlacementFixture(t)
			read := guard.available
			guard.available = func(ctx context.Context, domain capacity.ResourceDomain, now time.Time) (*capacity.MemoryObservation, error) {
				observation, err := read(ctx, domain, now)
				switch scenario {
				case "missing":
					return nil, nil
				case "error":
					return nil, errors.New("fixture selected device is ambiguous")
				case "old":
					observation.ObservedAt = now.Add(-time.Minute).Format(time.RFC3339Nano)
				case "future":
					observation.ObservedAt = now.Add(time.Minute).Format(time.RFC3339Nano)
				case "domain":
					observation.ResourceDomain = capacity.DomainHost
				case "at reserve":
					observation.Bytes = guard.reserve
				}
				return observation, err
			}
			if err := guard.Observe(ctx, attempt); !errors.Is(err, ollama.ErrUnverifiedLocalExecution) {
				t.Fatal("missing or stale reserve observation accepted", err)
			}
		})
	}
}

func TestAutoPlacementHonorsCancellationAndBackendChanges(t *testing.T) {
	for _, scenario := range []string{"cancelled", "no deadline", "changed backend", "host"} {
		t.Run(scenario, func(t *testing.T) {
			guard, backend, attempt, ctx := autoPlacementFixture(t)
			switch scenario {
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			case "no deadline":
				ctx = context.Background()
			case "changed backend":
				if err := guard.Observe(ctx, attempt); err != nil {
					t.Fatal(err)
				}
				backend.accel = "vulkan"
			case "host":
				guard.domain, backend.accel, backend.models[0].SizeVRAM = capacity.DomainHost, "cpu", 0
				if err := guard.Observe(ctx, attempt); err != nil {
					t.Fatal("host-only receipt rejected", err)
				}
				if backend.waitExpected != "cpu" {
					t.Fatal("host placement did not request CPU allocation facts")
				}
				backend.models[0].SizeVRAM = backend.models[0].Size
			}
			if err := guard.Observe(ctx, attempt); !errors.Is(err, ollama.ErrUnverifiedLocalExecution) {
				t.Fatal("invalid observation was reusable", err)
			}
		})
	}
}
