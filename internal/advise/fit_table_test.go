package advise

import (
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/device"
)

func TestContextFitPointsStopAtMaxAndIncludeRequested(t *testing.T) {
	got := fitCtxPoints(10000, 6144)
	want := []int{2048, 4096, 6144, 8192, 10000}
	if len(got) != len(want) {
		t.Fatalf("points = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("points = %v, want %v", got, want)
		}
	}
	for _, n := range fitCtxPoints(4096, 0) {
		if n > 4096 {
			t.Fatalf("point %d exceeds max 4096", n)
		}
	}
}

func TestContextFitLlamaTable(t *testing.T) {
	// 5 GiB weights, 8 GiB budget, 1 GiB KV per 8k.
	tble := ContextFit(Input{
		WeightsB: 5 * GiB, HaveGB: 8, HaveSrc: "nvidia-smi",
		Ctx: 32768, Arch: llama8B(),
	})
	if tble == nil || len(tble.Points) < 4 {
		t.Fatalf("table = %+v", tble)
	}
	if !strings.Contains(tble.Note, "other allocation") {
		t.Fatalf("must disclose unmeasured runtime allocation: %q", tble.Note)
	}
	var saw8k, saw32k FitPoint
	for _, p := range tble.Points {
		if p.OtherKnown {
			t.Fatalf("other resident must be n/a without --load/--fit: %+v", p)
		}
		if p.WeightsGB != 5.0 {
			t.Fatalf("weights must be constant: %+v", p)
		}
		switch p.Ctx {
		case 8192:
			saw8k = p
		case 32768:
			saw32k = p
		}
	}
	if saw8k.Tier != Compatible || saw8k.NeedGB != 6.0 {
		t.Fatalf("8k = %+v, want compatible 6.0 GB", saw8k)
	}
	if saw32k.Tier != Incompatible || !saw32k.Requested {
		t.Fatalf("32k = %+v, want requested incompatible", saw32k)
	}
	if !saw8k.Suggested && !suggestedAt(tble, 16384) {
		t.Fatal("largest compatible window should be suggested when requested does not fit")
	}
	got := CompactWindows(tble)
	if !strings.Contains(got, "8k ok") || !strings.Contains(got, ">32k no") {
		t.Fatalf("compact windows = %q", got)
	}
	if !strings.Contains(got, "*") {
		t.Fatalf("suggested window must be marked: %q", got)
	}
}

func TestCompactCtxPairIsMeasuredThenServing(t *testing.T) {
	if got := compactCtxPair(0, 8192, true); got != "" {
		t.Fatalf("unmeasured = %q", got)
	}
	if got := compactCtxPair(16384, 16384, true); got != "16k" {
		t.Fatalf("matching = %q", got)
	}
	if got := compactCtxPair(16384, 8192, true); got != "16k/8k" {
		t.Fatalf("differ = %q", got)
	}
	if got := compactCtxPair(16384, 0, false); got != "16k" {
		t.Fatalf("unknown serving = %q", got)
	}
}

func suggestedAt(tble *FitTable, ctx int) bool {
	for _, p := range tble.Points {
		if p.Ctx == ctx && p.Suggested {
			return true
		}
	}
	return false
}

func TestContextFitHybridSkipsAlgebra(t *testing.T) {
	a := llama8B()
	a.Hybrid = true
	a.RecurrentLayers = 12
	tble := ContextFit(Input{WeightsB: 5 * GiB, HaveGB: 24, HaveSrc: "x", Arch: a, Ctx: 8192})
	if tble == nil || len(tble.Points) != 0 {
		t.Fatalf("hybrid without a measurement must not project KV: %+v", tble)
	}
	if !strings.Contains(tble.Note, "hybrid") {
		t.Fatalf("note = %q", tble.Note)
	}
}

func TestContextFitHybridSingleMeasuredPoint(t *testing.T) {
	a := llama8B()
	a.Hybrid = true
	tble := ContextFit(Input{
		WeightsB: 5 * GiB, HaveGB: 24, HaveSrc: "nvidia-smi", Arch: a,
		ResidentB: 11 * GiB, ResidentCtx: 8192, ResidentSrc: "ps",
	})
	if len(tble.Points) != 1 || tble.Points[0].Ctx != 8192 {
		t.Fatalf("hybrid measured table = %+v", tble.Points)
	}
	if tble.Points[0].Tier != Compatible || tble.Points[0].OtherKnown ||
		tble.Points[0].OtherGB != 0 || tble.Points[0].NeedGB != 11 ||
		tble.Points[0].AllocationEvidence != allocationObservedTotal {
		t.Fatalf("hybrid point = %+v", tble.Points[0])
	}
}

func TestContextFitHybridOmitsUnboundAllocatorProjection(t *testing.T) {
	a := llama8B()
	a.Hybrid = true
	tble := ContextFit(Input{
		WeightsB: 5 * GiB, HaveGB: 24, HaveSrc: "--vram-gb", Arch: a,
		Ctx: 8192, FitB: 10 * GiB, FitSrc: "llama-fit-params",
	})
	if len(tble.Points) != 0 || !strings.Contains(tble.Note, "final context and placement were not captured") {
		t.Fatalf("hybrid unbound projection table = %+v", tble)
	}
}

func TestContextFitDoesNotAttachAllocatorProjectionToRequestedContext(t *testing.T) {
	tble := ContextFit(Input{
		WeightsB: 5 * GiB, HaveGB: 16, HaveSrc: "--vram-gb",
		Ctx: 8192, Arch: llama8B(), FitB: 8 * GiB, FitSrc: "llama-fit-params",
	})
	var at8 FitPoint
	for _, p := range tble.Points {
		if p.Ctx == 8192 {
			at8 = p
		}
	}
	if at8.OtherKnown || at8.OtherGB != 0 || at8.NeedGB != 6 ||
		at8.AllocationEvidence != allocationProjection || !strings.Contains(at8.Note, "no matched total evidence") {
		t.Fatalf("unbound projection contaminated requested point: %+v", at8)
	}
}

func TestContextFitNVIDIAAddressableCapacityNeverSuggestsUnobservedFit(t *testing.T) {
	tble := ContextFit(Input{
		WeightsB: 5 * GiB, HaveGB: 8, HaveSrc: device.NVIDIAUnifiedMemorySource,
		Ctx: 32768, Arch: llama8B(),
	})
	var sawSkip bool
	for _, p := range tble.Points {
		if p.Suggested || p.Tier == Compatible {
			t.Fatalf("addressable-only point claimed fit: %+v", p)
		}
		switch p.Tier {
		case Skip:
			sawSkip = true
		default:
			t.Fatalf("addressable-only point had definitive tier: %+v", p)
		}
		if p.CapacityDeltaGB == nil || p.HeadroomGB != 0 {
			t.Fatalf("addressable capacity must expose a delta, not headroom: %+v", p)
		}
	}
	if !sawSkip || tble.HaveSemantics != "addressable_capacity" || !strings.Contains(CompactWindows(tble), "?") {
		t.Fatalf("addressable curve = %+v compact=%q", tble, CompactWindows(tble))
	}
}

func TestContextFitNVIDIAUnifiedIdentityConstrainsNonzeroProbe(t *testing.T) {
	tble := ContextFit(Input{
		WeightsB: 5 * GiB, HaveGB: 8, HaveSrc: "nvidia-smi",
		NVIDIAUnifiedMemory: true, Ctx: 8192, Arch: llama8B(),
	})
	for _, point := range tble.Points {
		if point.Tier == Compatible || point.Suggested {
			t.Fatalf("shared identity claimed automatic fit: %+v", point)
		}
	}
}

func TestContextFitBuffersOnlyAtMeasuredCtx(t *testing.T) {
	tble := ContextFit(Input{
		WeightsB: 5 * GiB, HaveGB: 16, HaveSrc: "nvidia-smi",
		Ctx: 8192, Arch: llama8B(),
		ResidentB: 8 * GiB, ResidentCtx: 8192,
	})
	var at8, at16 FitPoint
	for _, p := range tble.Points {
		switch p.Ctx {
		case 8192:
			at8 = p
		case 16384:
			at16 = p
		}
	}
	if !at8.OtherKnown || at8.OtherGB <= 0 {
		t.Fatalf("8k should include derived other resident: %+v", at8)
	}
	if at16.OtherKnown {
		t.Fatalf("16k must not invent other resident from an 8k allocation: %+v", at16)
	}
}

func TestContextFitOverlaysSavedTimingsOnlyAtMatchingCtx(t *testing.T) {
	tble := ContextFit(Input{
		WeightsB: 5 * GiB, HaveGB: 16, HaveSrc: "nvidia-smi",
		Arch: llama8B(), Ctx: 8192,
		Timings: []SavedTiming{{Ctx: 8192, DecodeTPS: 24.8, PrefillTPS: 180}},
	})
	var at8, at4 FitPoint
	for _, p := range tble.Points {
		switch p.Ctx {
		case 8192:
			at8 = p
		case 4096:
			at4 = p
		}
	}
	if at8.DecodeTPS != 24.8 || at8.PrefillTPS != 180 {
		t.Fatalf("8k overlay = %+v", at8)
	}
	if at4.DecodeTPS != 0 {
		t.Fatalf("must not copy timings onto a different ctx: %+v", at4)
	}
}

func TestContextFitNoVRAM(t *testing.T) {
	tble := ContextFit(Input{WeightsB: 5 * GiB, Arch: llama8B()})
	if tble == nil || len(tble.Points) != 0 || tble.HaveGB != 0 {
		t.Fatalf("unmeasured VRAM must not size a table: %+v", tble)
	}
}

func TestEvaluateAttachesFitTable(t *testing.T) {
	r := Evaluate(Input{
		WeightsB: 5 * GiB, HaveGB: 8, HaveSrc: "--vram-gb",
		Ctx: 8192, Arch: llama8B(),
	})
	if r.Fit == nil || len(r.Fit.Points) == 0 {
		t.Fatal("Evaluate must attach a context-fit table")
	}
}
