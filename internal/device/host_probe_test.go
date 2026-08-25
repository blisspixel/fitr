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

// A probe that failed under load must be allowed to succeed later. Pinning an
// empty reading for the life of the process would turn one slow moment into a
// command that can never work.
func TestFailedHostProbesAreNotCached(t *testing.T) {
	resetHostProbes()
	t.Cleanup(resetHostProbes)

	// A cancelled context makes every probe fail the way a timeout does.
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	_ = cachedCPUName(dead)
	_, _, _ = cachedGPUInfo(dead)

	hostProbes.Lock()
	cpuCached, gpuCached := hostProbes.cpu, hostProbes.gpuOK
	hostProbes.Unlock()
	if cpuCached != "" {
		t.Fatalf("a failed CPU probe was cached as %q", cpuCached)
	}
	if gpuCached {
		t.Fatal("a failed GPU probe was cached")
	}

	// With a live context the reading is available again.
	if got := cachedCPUName(context.Background()); got == "" && cpuName(context.Background()) != "" {
		t.Fatal("CPU probe stayed empty after the failure cleared")
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
