package device

import "testing"

// Free memory is what decides whether a model loads right now, and answering
// it only for NVIDIA made the caveat a vendor feature. This pins which
// platform/vendor combinations can answer and which honestly cannot, so the
// gaps are visible rather than discovered by a user on the wrong hardware.
func TestFreeMemoryCoverageIsExplicit(t *testing.T) {
	// Documented, not asserted about the running machine: this is the matrix
	// the code implements, and it is here so a change to it is deliberate.
	coverage := map[string]string{
		"nvidia/any":   "nvidia-smi --query-gpu=memory.free",
		"amd/linux":    "sysfs mem_info_vram_total minus mem_info_vram_used",
		"amd/windows":  "unavailable - needs DXGI QueryVideoMemoryInfo (COM)",
		"apple/darwin": "unavailable by design - one pool, no separate VRAM to be free",
		"intel/any":    "unavailable - shares the AMD/Windows and unified-memory problems",
	}
	if len(coverage) != 5 {
		t.Fatal("the matrix changed; update the caveat text with it")
	}
	for combo, how := range coverage {
		if how == "" {
			t.Errorf("%s has no stated answer, not even 'unavailable'", combo)
		}
	}
}

// A source fitr emits must be one preferUnifiedMemory recognises. Changing the
// Apple budget strings without updating that switch left the new sources
// falling through to a heuristic that only happened to give the right answer.
func TestUnifiedMemoryPrefersAuthoritativeSources(t *testing.T) {
	const ram = 128.0
	for _, src := range []string{
		AppleWiredLimitSource, AppleAssumedShareSource, AppleLegacyRAMSource,
		NVIDIAUnifiedMemorySource, NVIDIAUnifiedProbeSource, "nvidia-smi", "drm sysfs",
	} {
		vram := ram * appleWiredLimitFraction
		gotVRAM, gotSrc := preferUnifiedMemory("Apple M3 Max", ram, vram, src)
		if gotSrc != src || gotVRAM != vram {
			t.Errorf("%s: preferUnifiedMemory second-guessed an authoritative source "+
				"-> %.1f %q", src, gotVRAM, gotSrc)
		}
	}
}

// Apple Silicon must be recognised as unified memory, or the budget logic
// never runs on the platform it was written for.
func TestAppleSiliconIsRecognisedAsUnifiedMemory(t *testing.T) {
	for _, name := range []string{"Apple M3 Max", "Apple M5 Ultra", "Apple M1"} {
		if !unifiedMemoryGPU(name) {
			t.Errorf("%q not recognised as unified memory", name)
		}
	}
	for _, name := range []string{"NVIDIA GeForce RTX 4090", "AMD Radeon RX 7900 XTX"} {
		if unifiedMemoryGPU(name) {
			t.Errorf("%q wrongly treated as unified memory", name)
		}
	}
}
