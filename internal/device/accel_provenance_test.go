package device

import (
	"strings"
	"testing"
)

// GPUBackend is part of the comparability key and, on Ollama, is read from the
// serving runtime's log. That log rotates, can be unreadable, and can grow past
// the tail fitr samples, so an empty accelerator means either that the runtime
// computes on the CPU or that nothing was read. Letting the two share an empty
// string pools evidence from machines that compute differently, or splits one
// machine's evidence when its log rotates underneath it.
func TestUnobservedAcceleratorCannotProduceAComparabilityKey(t *testing.T) {
	fingerprint := validFingerprint()
	fingerprint.AccelSource = AccelSourceUnobserved
	fingerprint.GPUBackend = ""
	v2, err := NewFingerprintV2(fingerprint, verifiedContext(8192, 8192))
	if err != nil {
		t.Fatalf("sealing a fingerprint must still succeed: %v", err)
	}
	key, err := v2.ComparabilityKey()
	if err == nil {
		t.Fatalf("an unobserved accelerator produced a key: %q", key)
	}
	if !strings.Contains(err.Error(), "compute backend is unobserved") {
		t.Fatalf("refusal did not name the cause: %v", err)
	}
}

// A runtime that reported its backend compares normally.
func TestReportedAcceleratorProducesAKey(t *testing.T) {
	fingerprint := validFingerprint()
	fingerprint.AccelSource, fingerprint.GPUBackend = AccelSourceRuntime, "cuda"
	v2, err := NewFingerprintV2(fingerprint, verifiedContext(8192, 8192))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v2.ComparabilityKey(); err != nil {
		t.Fatalf("an observed accelerator was refused: %v", err)
	}
}

// Records sealed before this provenance existed keep their original meaning,
// the same way configuration provenance treats them.
func TestHistoricalRecordsWithoutAccelProvenanceStillCompare(t *testing.T) {
	fingerprint := validFingerprint()
	fingerprint.AccelSource, fingerprint.GPUBackend = "", "cuda"
	if !fingerprint.AcceleratorObserved() {
		t.Fatal("a historical record was retroactively treated as unobserved")
	}
	v2, err := NewFingerprintV2(fingerprint, verifiedContext(8192, 8192))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v2.ComparabilityKey(); err != nil {
		t.Fatalf("a historical record lost its key: %v", err)
	}
}

// The provenance gates the key without entering it, so recording how the
// accelerator was learned does not change what compares to what.
func TestAccelProvenanceIsNotPartOfTheKey(t *testing.T) {
	runtime := validFingerprint()
	runtime.AccelSource, runtime.GPUBackend = AccelSourceRuntime, "cuda"
	historical := runtime
	historical.AccelSource = ""

	first, err := mustKey(t, runtime)
	if err != nil {
		t.Fatal(err)
	}
	second, err := mustKey(t, historical)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("recording how the accelerator was learned changed the key")
	}
}

func mustKey(t *testing.T, fp Fingerprint) (string, error) {
	t.Helper()
	v2, err := NewFingerprintV2(fp, verifiedContext(8192, 8192))
	if err != nil {
		return "", err
	}
	return v2.ComparabilityKey()
}
