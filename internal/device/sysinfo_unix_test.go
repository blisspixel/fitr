//go:build !windows

package device

import (
	"math"
	"testing"
)

// An AMD APU exposes only its BIOS carve-out as mem_info_vram_total, so sizing
// against it marks a 60 GB 780M box unable to hold a small model (issue #29).
// The addressable pool is the carve-out plus the GTT aperture, under its own
// source label so a reader can see where the number came from. The combiner is
// unix-only, so this test lives beside it rather than in device_test.go.
func TestCombineUnifiedPoolSumsCarveOutAndGTT(t *testing.T) {
	gb, src, ok := combineUnifiedPool(2.0, 30.3)
	if !ok || src != DRMUnifiedPoolSource || math.Abs(gb-32.3) > 1e-9 {
		t.Fatalf("combined pool = %v %q ok=%v, want 32.3 %q", gb, src, ok, DRMUnifiedPoolSource)
	}
	// A discrete GPU's GTT aperture is smaller than its VRAM: not an APU pool.
	if _, _, ok := combineUnifiedPool(20, 8); ok {
		t.Fatal("VRAM larger than GTT was treated as a unified pool")
	}
	// Missing GTT is not a pool.
	if _, _, ok := combineUnifiedPool(2, 0); ok {
		t.Fatal("a missing GTT reading became a pool")
	}
	// Carve-out of zero is still a pool: the whole pool is GTT.
	gb, _, ok = combineUnifiedPool(0, 30.3)
	if !ok || math.Abs(gb-30.3) > 1e-9 {
		t.Fatalf("GTT-only pool = %v ok=%v, want 30.3", gb, ok)
	}
}
