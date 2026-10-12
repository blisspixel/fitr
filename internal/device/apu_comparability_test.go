package device

import (
	"context"
	"testing"

	"github.com/blisspixel/fitr/internal/ollama"
)

func TestAPUGeneralPlacementDoesNotBorrowFirstResident(t *testing.T) {
	b := placementBackend{name: "fake", running: []ollama.RunningModel{
		{Name: "cpu-model", Size: 100}, {Name: "gpu-model", Size: 100, SizeVRAM: 100},
	}}
	if got := inferenceDeviceFromRuntime(context.Background(), b, ""); got != "" {
		t.Fatalf("unbound device identity borrowed one resident model: %q", got)
	}
	if got := InferenceDeviceFor(context.Background(), b, "gpu-model"); got != "GPU 100%" {
		t.Fatalf("target-specific GPU placement = %q", got)
	}
	if got := InferenceDeviceFor(context.Background(), b, "cpu-model"); got != "CPU" {
		t.Fatalf("target-specific CPU placement = %q", got)
	}
}

func TestAPUGTTCapacityCannotCompareWithEqualDedicatedBytes(t *testing.T) {
	fp := validFingerprint()
	fp.GPU = "AMD Radeon 780M"
	fp.GPUBackend = "ROCm"
	fp.VRAMSource = "drm sysfs"
	dedicated, err := NewFingerprintV2(fp, verifiedContext(8192, 8192))
	if err != nil {
		t.Fatal(err)
	}
	fp.VRAMSource = "unified memory (drm sysfs GTT mapping limit)"
	shared, err := NewFingerprintV2(fp, verifiedContext(8192, 8192))
	if err != nil {
		t.Fatal(err)
	}
	a, err := dedicated.ComparabilityKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := shared.ComparabilityKey()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("equal numeric VRAM and GTT capacities share a key: %s", a)
	}
	const historical = "fp2:f4dc97879927de3355f2e8b5ba0a00a129899b68b629a84ed068e554c3f8e55a"
	if a != historical {
		t.Fatalf("historical dedicated-memory key changed: %s", a)
	}
}

func TestAPUHistoricalCapacitySourcesKeepExactKey(t *testing.T) {
	const historical = "fp2:f4dc97879927de3355f2e8b5ba0a00a129899b68b629a84ed068e554c3f8e55a"
	for _, source := range []string{"drm sysfs", "nvidia-smi", AppleLegacyRAMSource,
		NVIDIAUnifiedMemorySource, NVIDIAUnifiedProbeSource, AppleWiredLimitSource, AppleAssumedShareSource} {
		fp := validFingerprint()
		fp.GPU, fp.GPUBackend, fp.VRAMSource = "AMD Radeon 780M", "ROCm", source
		v2, err := NewFingerprintV2(fp, verifiedContext(8192, 8192))
		if err != nil {
			t.Fatal(err)
		}
		got, err := v2.ComparabilityKey()
		if err != nil || got != historical {
			t.Fatalf("historical %q key = %q, %v", source, got, err)
		}
	}
}
