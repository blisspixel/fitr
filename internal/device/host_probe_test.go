package device

import (
	"context"
	"testing"
)

// Device facts cannot change while one process runs, and probing them is a
// subprocess round-trip on Windows. Detect is on the path of nearly every
// command and some commands call it repeatedly, so the second probe must come
// from the cache: an empty CPU name is fatal downstream, and a probe sharing a
// budget on a loaded machine can return nothing.
func TestHostProbesAreReadOncePerProcess(t *testing.T) {
	resetHostProbes()
	t.Cleanup(resetHostProbes)
	ctx := context.Background()

	cpu1 := cachedCPUName(ctx)
	ram1 := cachedRAMGB(ctx)
	vram1, vsrc1 := cachedVRAMInfo(ctx)
	gpu1, drv1, date1 := cachedGPUInfo(ctx)

	for range 5 {
		if got := cachedCPUName(ctx); got != cpu1 {
			t.Fatalf("cpu changed between reads: %q then %q", cpu1, got)
		}
		if got := cachedRAMGB(ctx); got != ram1 {
			t.Fatalf("ram changed between reads: %v then %v", ram1, got)
		}
		gb, src := cachedVRAMInfo(ctx)
		if gb != vram1 || src != vsrc1 {
			t.Fatalf("vram changed between reads: %v/%q then %v/%q", vram1, vsrc1, gb, src)
		}
		name, drv, date := cachedGPUInfo(ctx)
		if name != gpu1 || drv != drv1 || date != date1 {
			t.Fatalf("gpu changed between reads: %q/%q/%q then %q/%q/%q",
				gpu1, drv1, date1, name, drv, date)
		}
	}
}

// Only a successful reading may be cached: one slow moment must not pin an
// empty value for the life of the process. Inducing a real probe failure is
// not portable -- a cancelled context kills a subprocess on Windows but does
// not stop a /proc read on Linux -- so the policy is asserted on the cache
// itself rather than by trying to break a probe.
func TestEmptyReadingsAreNotPinned(t *testing.T) {
	resetHostProbes()
	t.Cleanup(resetHostProbes)
	ctx := context.Background()

	// An empty cached CPU must not satisfy a later read.
	hostProbes.Lock()
	hostProbes.cpu = ""
	hostProbes.Unlock()
	if got := cachedCPUName(ctx); got != cpuName(ctx) {
		t.Fatalf("an empty cache entry was served instead of re-probing: %q", got)
	}

	// An unsuccessful GPU probe leaves the cache disarmed, so the next call
	// tries again rather than returning nothing forever.
	hostProbes.Lock()
	hostProbes.gpu, hostProbes.gpuOK = "", false
	hostProbes.Unlock()
	name, _, _ := cachedGPUInfo(ctx)
	hostProbes.Lock()
	armed := hostProbes.gpuOK
	hostProbes.Unlock()
	if name == "" && armed {
		t.Fatal("an empty GPU reading armed the cache")
	}
	if name != "" && !armed {
		t.Fatal("a successful GPU reading did not arm the cache")
	}
}

func TestDetectStillFillsTheFingerprint(t *testing.T) {
	resetHostProbes()
	t.Cleanup(resetHostProbes)
	fp := Detect(context.Background(), nil)
	if fp.Host == "" || fp.OS == "" {
		t.Fatalf("Detect returned an unusable fingerprint: %+v", fp)
	}
	// Repeat detection must agree with itself, because the comparability key
	// is built from it.
	if again := Detect(context.Background(), nil); again.Key() != fp.Key() {
		t.Fatalf("two detections disagree:\n%s\n%s", fp.Key(), again.Key())
	}
}
