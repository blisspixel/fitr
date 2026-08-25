package device

import (
	"strings"
	"testing"
)

// The 0.9.6 shape: a headset's virtual display named the machine while the
// memory budget came from a GeForce card. Nothing objected, and every run was
// sealed to a device that does not exist.
func TestIdentityConflictsCatchesAMixedFingerprint(t *testing.T) {
	f := Fingerprint{
		Host: "box", OS: "windows",
		GPU: "Meta Virtual Monitor", GPUBackend: "cuda",
		VRAMGb: 24.0, VRAMSource: "nvidia-smi",
	}
	conflicts := f.IdentityConflicts()
	if len(conflicts) == 0 {
		t.Fatal("a virtual display sized by nvidia-smi raised no conflict")
	}
	joined := strings.Join(conflicts, " | ")
	if !strings.Contains(joined, "nvidia-smi") || !strings.Contains(joined, "Meta Virtual Monitor") {
		t.Fatalf("conflict does not name both sources: %s", joined)
	}
}

func TestIdentityConflictsAcceptsConsistentMachines(t *testing.T) {
	for _, f := range []Fingerprint{
		{Host: "a", OS: "windows", GPU: "NVIDIA GeForce RTX 4090", GPUBackend: "cuda",
			VRAMGb: 24.0, VRAMSource: "nvidia-smi"},
		{Host: "b", OS: "darwin", GPU: "Apple M3 Max", GPUBackend: "metal",
			VRAMGb: 48, VRAMSource: "unified memory"},
		{Host: "c", OS: "linux", GPU: "AMD Radeon RX 7900 XTX", GPUBackend: "rocm",
			VRAMGb: 24, VRAMSource: "sysfs"},
		// Vulkan runs on anyone's hardware, so the name proves nothing.
		{Host: "d", OS: "linux", GPU: "Intel Arc A770", GPUBackend: "vulkan",
			VRAMGb: 16, VRAMSource: "sysfs"},
		// CPU-only and fully unmeasured machines are legitimate states.
		{Host: "e", OS: "linux", GPUBackend: "cpu"},
		{Host: "f", OS: "linux"},
	} {
		if c := f.IdentityConflicts(); len(c) != 0 {
			t.Fatalf("consistent fingerprint %+v raised %v", f, c)
		}
	}
}

func TestIdentityConflictsFlagsASizedButUnnamedDevice(t *testing.T) {
	f := Fingerprint{Host: "a", OS: "linux", VRAMGb: 12, VRAMSource: "sysfs"}
	c := f.IdentityConflicts()
	if len(c) != 1 || !strings.Contains(c[0], "no name") {
		t.Fatalf("a sized but unnamed device raised %v", c)
	}
	// Unmeasured memory with no name is not a conflict; it is honest absence.
	if c := (Fingerprint{Host: "a", OS: "linux"}).IdentityConflicts(); len(c) != 0 {
		t.Fatalf("an unmeasured device raised %v", c)
	}
}
