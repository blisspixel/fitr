package device

import (
	"context"
	"testing"
)

// A fit verdict computed against total memory answers a question nobody has.
// The reading must be a real observation or an honest absence, never a zero
// standing in for "not measured".
func TestAvailableVRAMIsMeasuredOrAbsent(t *testing.T) {
	gb, ok := AvailableVRAM(context.Background())
	if !ok {
		t.Skip("no nvidia-smi on this host; absence is a valid answer")
	}
	if gb <= 0 {
		t.Fatalf("reported ok with %v GB free; zero is not a reading", gb)
	}
	total := nvidiaSMIMemory(context.Background())
	if total > 0 && gb > total+0.5 {
		t.Fatalf("free %v GB exceeds total %v GB", gb, total)
	}
	t.Logf("free %.1f GB of %.1f GB total", gb, total)
}

// A cancelled probe reports absence, not an empty card.
func TestAvailableVRAMReportsAbsenceOnFailure(t *testing.T) {
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	if gb, ok := AvailableVRAM(dead); ok && gb <= 0 {
		t.Fatal("a failed probe claimed a reading of zero")
	}
}
