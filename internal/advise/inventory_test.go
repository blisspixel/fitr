package advise

import (
	"strconv"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/device"
)

var inventoryTestDigest = "sha256:" + strings.Repeat("a", 64)
var inventoryOtherDigest = "sha256:" + strings.Repeat("b", 64)

func TestJoinUnprovenWhenNoEvidence(t *testing.T) {
	table := Join(InventoryQuery{
		Tags: []InstalledModel{{Name: "qwen3:8b", Size: 5 << 30}},
	})
	if len(table.Rows) != 1 {
		t.Fatalf("rows = %d", len(table.Rows))
	}
	row := table.Rows[0]
	if row.State != StateUnproven || row.Next != "fitr advise qwen3:8b" {
		t.Fatalf("unproven path = %+v", row)
	}
}

func TestJoinMeasuredNonDefaultCtxAsksApply(t *testing.T) {
	table := Join(InventoryQuery{
		Tags:       []InstalledModel{{Name: "m", ArtifactDigest: inventoryTestDigest}},
		CurrentKey: "k",
		Evidence: []InventoryEvidence{{
			Model: "m", ArtifactDigest: inventoryTestDigest, DeviceKey: "k",
			Level: "default", NumCtx: 16384, Repeats: 3,
		}},
	})
	row := table.Rows[0]
	if row.State != StateMeasured || row.Next != "fitr apply m" {
		t.Fatalf("measured custom ctx = %+v", row)
	}
	if !strings.Contains(row.Note, "16384") {
		t.Fatalf("apply pending must name the measured ctx: %q", row.Note)
	}
}

func TestJoinMeasuredServingMatchViews(t *testing.T) {
	table := Join(InventoryQuery{
		Tags:       []InstalledModel{{Name: "m", ArtifactDigest: inventoryTestDigest}},
		CurrentKey: "k",
		Serving:    map[string]int{"m": 16384},
		Evidence: []InventoryEvidence{{
			Model: "m", ArtifactDigest: inventoryTestDigest, DeviceKey: "k",
			Level: "default", NumCtx: 16384, Repeats: 3,
		}},
	})
	row := table.Rows[0]
	if row.Next != "fitr view m" {
		t.Fatalf("already serving measured ctx = %+v", row)
	}
	if row.Ctx != "16k" {
		t.Fatalf("ctx column = %q", row.Ctx)
	}
}

func TestJoinServingDiffersShowsPair(t *testing.T) {
	table := Join(InventoryQuery{
		Tags:       []InstalledModel{{Name: "m", ArtifactDigest: inventoryTestDigest}},
		CurrentKey: "k",
		Serving:    map[string]int{"m": 8192},
		Evidence: []InventoryEvidence{{
			Model: "m", ArtifactDigest: inventoryTestDigest, DeviceKey: "k",
			Level: "default", NumCtx: 16384, Repeats: 3,
		}},
	})
	row := table.Rows[0]
	if row.Next != "fitr apply m" || row.Ctx != "16k/8k" {
		t.Fatalf("serving differs = %+v", row)
	}
}

func TestJoinAttachesWindowsWhenArchitectureKnown(t *testing.T) {
	table := Join(InventoryQuery{
		Tags:   []InstalledModel{{Name: "llama", Size: 5 * GiB, Arch: llama8B()}},
		HaveGB: 8, HaveSrc: "nvidia-smi",
	})
	row := table.Rows[0]
	if row.Windows == "" || !strings.Contains(row.Windows, "ok") {
		t.Fatalf("windows = %q", row.Windows)
	}
	if row.Fit == "" {
		t.Fatal("fit tier must be set when architecture is known")
	}
}

func TestJoinMeasuredSameDevice(t *testing.T) {
	table := Join(InventoryQuery{
		Tags:       []InstalledModel{{Name: "qwen3:8b", Size: 5 << 30, ArtifactDigest: inventoryTestDigest}},
		CurrentKey: "box|gpu",
		Evidence: []InventoryEvidence{{
			Model: "qwen3:8b", ArtifactDigest: inventoryTestDigest,
			DeviceKey: "box|gpu", Level: "default",
		}},
	})
	row := table.Rows[0]
	if row.State != StateMeasured || row.Next != "fitr view qwen3:8b" {
		t.Fatalf("measured path = %+v", row)
	}
}

func TestJoinQuickMeasurementAsksForDefaultRun(t *testing.T) {
	table := Join(InventoryQuery{
		Tags:       []InstalledModel{{Name: "m", ArtifactDigest: inventoryTestDigest}},
		CurrentKey: "k",
		Evidence: []InventoryEvidence{{
			Model: "m", ArtifactDigest: inventoryTestDigest, DeviceKey: "k", Level: "quick",
		}},
	})
	row := table.Rows[0]
	if row.State != StateMeasured || row.Next != "fitr run m" {
		t.Fatalf("quick measured = %+v", row)
	}
	if !strings.Contains(row.Note, "--quick") {
		t.Fatalf("note should disclose the partial battery: %q", row.Note)
	}
}

func TestJoinStaleOnFingerprintChange(t *testing.T) {
	table := Join(InventoryQuery{
		Tags:       []InstalledModel{{Name: "m", ArtifactDigest: inventoryTestDigest}},
		CurrentKey: "new-runtime",
		Evidence: []InventoryEvidence{{
			Model: "m", ArtifactDigest: inventoryTestDigest, DeviceKey: "old-runtime", Level: "full",
		}},
	})
	row := table.Rows[0]
	if row.State != StateStale || row.Next != "fitr run m" {
		t.Fatalf("stale fingerprint = %+v", row)
	}
}

func TestJoinStaleWhenMutableTagChangesArtifact(t *testing.T) {
	table := Join(InventoryQuery{
		Tags: []InstalledModel{{
			Name: "qwen3:8b", ArtifactDigest: inventoryOtherDigest,
		}},
		CurrentKey: "k",
		Evidence: []InventoryEvidence{{
			Model: "qwen3:8b:latest", ArtifactDigest: inventoryTestDigest,
			DeviceKey: "k", Level: "full",
		}},
	})
	row := table.Rows[0]
	if row.State != StateStale || !strings.Contains(row.Note, "model artifact changed") {
		t.Fatalf("changed mutable tag = %+v", row)
	}
	if row.Next != "fitr run qwen3:8b" {
		t.Fatalf("changed artifact next = %q", row.Next)
	}
}

func TestJoinMissingOrMalformedCurrentDigestFailsClosed(t *testing.T) {
	for _, digest := range []string{"", "sha256:not-a-digest"} {
		table := Join(InventoryQuery{
			Tags:       []InstalledModel{{Name: "m", ArtifactDigest: digest}},
			CurrentKey: "k",
			Evidence: []InventoryEvidence{{
				Model: "m", ArtifactDigest: inventoryTestDigest, DeviceKey: "k", Level: "full",
			}},
		})
		row := table.Rows[0]
		if row.State != StateStale || !strings.Contains(row.Note, "verified model artifact digest") {
			t.Fatalf("digest %q did not fail closed: %+v", digest, row)
		}
	}
}

func TestJoinLegacyEvidenceWithoutArtifactDigestFailsClosed(t *testing.T) {
	table := Join(InventoryQuery{
		Tags:       []InstalledModel{{Name: "m", ArtifactDigest: inventoryTestDigest}},
		CurrentKey: "k",
		Evidence:   []InventoryEvidence{{Model: "m", DeviceKey: "k", Level: "full"}},
	})
	row := table.Rows[0]
	if row.State != StateStale || !strings.Contains(row.Note, "prior run has no verified") {
		t.Fatalf("legacy evidence = %+v", row)
	}
}

func TestJoinSelectsMatchingArtifactFromMultipleSavedRuns(t *testing.T) {
	table := Join(InventoryQuery{
		Tags:       []InstalledModel{{Name: "m", ArtifactDigest: inventoryTestDigest}},
		CurrentKey: "k",
		Evidence: []InventoryEvidence{
			{Model: "m", ArtifactDigest: inventoryOtherDigest, DeviceKey: "k", Level: "full"},
			{Model: "m:latest", ArtifactDigest: inventoryTestDigest, DeviceKey: "k", Level: "full"},
		},
	})
	if row := table.Rows[0]; row.State != StateMeasured {
		t.Fatalf("matching saved artifact was not selected: %+v", row)
	}
}

func TestJoinSelectsReusableEvidenceBeforeAStaleTie(t *testing.T) {
	current := device.Fingerprint{Host: "box", OS: "linux", CPU: "cpu", GPU: "gpu", Runtime: "runtime", Config: map[string]string{}}
	stale := current
	stale.OS = "windows"
	table := Join(InventoryQuery{
		Tags:    []InstalledModel{{Name: "m", ArtifactDigest: inventoryTestDigest}},
		Current: current, CurrentKey: current.Key(),
		Evidence: []InventoryEvidence{
			{Model: "m", ArtifactDigest: inventoryTestDigest, Device: stale, DeviceKey: current.Key(), Level: "full"},
			{Model: "m", ArtifactDigest: inventoryTestDigest, Device: current, DeviceKey: current.Key(), Level: "full"},
		},
	})
	if row := table.Rows[0]; row.State != StateMeasured {
		t.Fatalf("reusable evidence lost to stale tie: %+v", row)
	}
}

func TestStaleEvidenceCannotChangeCurrentFitProjection(t *testing.T) {
	current := device.Fingerprint{Host: "box", OS: "linux", Config: map[string]string{}}
	table := Join(InventoryQuery{
		Tags:    []InstalledModel{{Name: "m", Size: 5 * GiB, ArtifactDigest: inventoryOtherDigest}},
		Current: current, CurrentKey: current.Key(), HaveGB: 8, HaveSrc: "nvidia-smi",
		Evidence: []InventoryEvidence{{
			Model: "m", ArtifactDigest: inventoryTestDigest, Device: current, DeviceKey: current.Key(),
			WeightsB: 40 * GiB, Arch: llama8B(), NumCtx: 32768, Level: "full",
		}},
	})
	row := table.Rows[0]
	if row.State != StateStale {
		t.Fatalf("changed artifact was not stale: %+v", row)
	}
	if row.Fit == Incompatible {
		t.Fatalf("stale 40 GiB metadata contaminated the current 5 GiB projection: %+v", row)
	}
}

func TestEvidenceReuseRejectsAllPerformanceRelevantDeviceDrift(t *testing.T) {
	base := device.Fingerprint{
		Host: "box", OS: "linux", CPU: "cpu", RAMGb: 32, GPU: "gpu",
		GPUDriver: "1", GPUDriverDate: "today", GPUBackend: "cuda",
		VRAMGb: 16, VRAMSource: "nvidia-smi", Runtime: "runtime",
		InferenceDevice: "GPU 100%", Config: map[string]string{},
	}
	tag := InstalledModel{Name: "m", ArtifactDigest: inventoryTestDigest}
	for _, mutate := range []func(*device.Fingerprint){
		func(f *device.Fingerprint) { f.OS = "windows" },
		func(f *device.Fingerprint) { f.CPU = "other" },
		func(f *device.Fingerprint) { f.RAMGb = 64 },
		func(f *device.Fingerprint) { f.VRAMGb = 24 },
		func(f *device.Fingerprint) { f.GPUDriverDate = "tomorrow" },
	} {
		changed := base
		changed.Config = map[string]string{}
		mutate(&changed)
		ev := InventoryEvidence{
			Model: "m", ArtifactDigest: inventoryTestDigest, Device: base, DeviceKey: base.Key(),
		}
		if EvidenceReusable(tag, ev, InventoryQuery{Current: changed, CurrentKey: changed.Key()}) {
			t.Fatalf("device drift was reusable: saved=%+v current=%+v", base, changed)
		}
	}
}

func TestJoinNamesFingerprintFieldsThatChanged(t *testing.T) {
	saved := device.Fingerprint{Host: "box", GPU: "gpu", Runtime: "ollama 1", Config: map[string]string{}}
	current := saved
	current.Runtime = "ollama 2"
	table := Join(InventoryQuery{
		Tags:       []InstalledModel{{Name: "m", ArtifactDigest: inventoryTestDigest}},
		Current:    current,
		CurrentKey: current.Key(),
		Evidence: []InventoryEvidence{{
			Model: "m", ArtifactDigest: inventoryTestDigest,
			Device: saved, DeviceKey: saved.Key(), Level: "full",
		}},
	})
	row := table.Rows[0]
	if row.State != StateStale || !strings.Contains(row.Note, "runtime changed") {
		t.Fatalf("precise stale note = %+v", row)
	}
}

func TestJoinStaleOnIntegrityAndContamination(t *testing.T) {
	table := Join(InventoryQuery{
		Tags:       []InstalledModel{{Name: "a"}, {Name: "b"}},
		CurrentKey: "k",
		Evidence: []InventoryEvidence{
			{Model: "a", DeviceKey: "k", IntegrityIssue: "canonical result has no matching immutable history entry"},
			{Model: "b", DeviceKey: "k", Contaminated: true},
		},
	})
	by := map[string]InventoryRow{}
	for _, row := range table.Rows {
		by[row.Model] = row
	}
	if by["a"].State != StateStale || !strings.Contains(by["a"].Note, "immutable history") {
		t.Fatalf("integrity stale = %+v", by["a"])
	}
	if by["b"].State != StateStale || !strings.Contains(by["b"].Note, "contaminated") {
		t.Fatalf("contamination stale = %+v", by["b"])
	}
}

func TestJoinIncompatibleFromWeightsAlone(t *testing.T) {
	table := Join(InventoryQuery{
		Tags:   []InstalledModel{{Name: "huge", Size: 40 * GiB}},
		HaveGB: 16, HaveSrc: "nvidia-smi",
	})
	row := table.Rows[0]
	if row.State != StateIncompatible || row.Next != "try a smaller quant" {
		t.Fatalf("incompatible = %+v", row)
	}
	if strings.Contains(row.Next, "fitr run") {
		t.Fatal("incompatible must not suggest run")
	}
}

func TestJoinIgnoresUntrustedVRAMCarveForIncompatible(t *testing.T) {
	table := Join(InventoryQuery{
		Tags:   []InstalledModel{{Name: "qwen3:8b", Size: 5 * GiB}},
		HaveGB: 2, HaveSrc: "registry qwMemorySize",
	})
	if table.Rows[0].State != StateUnproven {
		t.Fatalf("registry carve-out must not mint incompatible: %+v", table.Rows[0])
	}
}

func TestJoinUnknownVRAMNeverIncompatible(t *testing.T) {
	table := Join(InventoryQuery{
		Tags: []InstalledModel{{Name: "huge", Size: 40 * GiB}},
	})
	if table.Rows[0].State != StateUnproven {
		t.Fatalf("unmeasured VRAM must not invent incompatible: %+v", table.Rows[0])
	}
}

func TestJoinLoadedBeatsWeightBudget(t *testing.T) {
	table := Join(InventoryQuery{
		Tags:   []InstalledModel{{Name: "huge", Size: 40 * GiB}},
		Loaded: []string{"huge"},
		HaveGB: 16, HaveSrc: "nvidia-smi",
	})
	row := table.Rows[0]
	if row.State != StateUnproven || !row.Loaded {
		t.Fatalf("loaded oversized model = %+v", row)
	}
	if row.State == StateIncompatible {
		t.Fatal("a running process is not incompatible")
	}
	if row.Fit == Incompatible {
		t.Fatalf("FIT must not mint incompatible for a loaded process: %+v", row)
	}
}

func TestJoinUnprovenServingDoesNotFillCtx(t *testing.T) {
	table := Join(InventoryQuery{
		Tags:    []InstalledModel{{Name: "m"}},
		Serving: map[string]int{"m": 8192},
	})
	if table.Rows[0].State != StateUnproven || table.Rows[0].Ctx != "" {
		t.Fatalf("CTX is measured, not serving-only: %+v", table.Rows[0])
	}
}

func TestJoinLoadedResidentSkipWhenOverBudget(t *testing.T) {
	table := Join(InventoryQuery{
		Tags:   []InstalledModel{{Name: "huge", Size: 40 * GiB, ResidentB: 20 * GiB}},
		Loaded: []string{"huge"},
		HaveGB: 16, HaveSrc: "nvidia-smi",
	})
	row := table.Rows[0]
	if row.State != StateUnproven || row.Fit != Skip {
		t.Fatalf("resident over the reading is skip, not incompatible: %+v", row)
	}
}

func TestJoinLoadedResidentCompatibleWithoutArchitecture(t *testing.T) {
	table := Join(InventoryQuery{
		Tags:    []InstalledModel{{Name: "loaded", ResidentB: 8 * GiB}},
		Loaded:  []string{"loaded"},
		HaveGB:  16,
		HaveSrc: "nvidia-smi",
	})
	row := table.Rows[0]
	if row.State != StateUnproven || row.Fit != Compatible {
		t.Fatalf("measured resident should establish current fit: %+v", row)
	}
	if row.Windows != "" {
		t.Fatalf("resident measurement must not invent context windows: %q", row.Windows)
	}
}

func TestServingOfAmbiguousAliasIsUnknown(t *testing.T) {
	n, ok := servingOf("qwen3:8b", map[string]int{"qwen3:8b:latest": 8192, "qwen3:8b:LATEST": 16384})
	if ok || n != 0 {
		t.Fatalf("ambiguous serving = %d, %v", n, ok)
	}
}

func TestServingOfExactConflictStaysUnknown(t *testing.T) {
	n, ok := servingOf("qwen3:8b", map[string]int{"qwen3:8b": 0, "qwen3:8b:latest": 8192})
	if ok || n != 0 {
		t.Fatalf("conflicting exact observation = %d, %v", n, ok)
	}
}

func TestJoinMeasuredEvenWhenWeightsExceedVRAM(t *testing.T) {
	table := Join(InventoryQuery{
		Tags: []InstalledModel{{
			Name: "cpu-offload", Size: 40 * GiB, ArtifactDigest: inventoryTestDigest,
		}},
		CurrentKey: "k",
		HaveGB:     16,
		Evidence: []InventoryEvidence{{
			Model: "cpu-offload", ArtifactDigest: inventoryTestDigest, DeviceKey: "k", Level: "full",
		}},
	})
	if table.Rows[0].State != StateMeasured {
		t.Fatalf("a successful measurement is measured, not incompatible: %+v", table.Rows[0])
	}
}

func TestJoinSortsMeasuredBeforeUnprovenAndNeverRanksQuality(t *testing.T) {
	table := Join(InventoryQuery{
		Tags: []InstalledModel{
			{Name: "zeta-unproven"},
			{Name: "alpha-measured", ArtifactDigest: inventoryTestDigest},
			{Name: "mid-stale", ArtifactDigest: inventoryTestDigest},
			{Name: "zzz-incompatible", Size: 40 * GiB},
		},
		CurrentKey: "k",
		HaveGB:     8, HaveSrc: "nvidia-smi",
		Evidence: []InventoryEvidence{
			{Model: "alpha-measured", ArtifactDigest: inventoryTestDigest, DeviceKey: "k", Level: "full"},
			{Model: "mid-stale", ArtifactDigest: inventoryTestDigest, DeviceKey: "old", Level: "full"},
		},
	})
	got := make([]string, len(table.Rows))
	for i, row := range table.Rows {
		got[i] = row.State + ":" + row.Model
	}
	want := []string{
		"measured:alpha-measured",
		"stale:mid-stale",
		"unproven:zeta-unproven",
		"incompatible:zzz-incompatible",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestJoinLatestAliasMatchesEvidence(t *testing.T) {
	table := Join(InventoryQuery{
		Tags:       []InstalledModel{{Name: "qwen3:8b", ArtifactDigest: inventoryTestDigest}},
		CurrentKey: "k",
		Evidence: []InventoryEvidence{{
			Model: "qwen3:8b:latest", ArtifactDigest: inventoryTestDigest,
			DeviceKey: "k", Level: "full",
		}},
	})
	if table.Rows[0].State != StateMeasured {
		t.Fatalf(":latest alias should match: %+v", table.Rows[0])
	}
}

func TestJoinLlamaServerOneRowAndDedup(t *testing.T) {
	table := Join(InventoryQuery{
		Tags: []InstalledModel{
			{Name: "only-one"},
			{Name: "only-one"},
		},
	})
	if len(table.Rows) != 1 {
		t.Fatalf("duplicate tags must collapse: %d", len(table.Rows))
	}
}

func TestJoinEmptyTags(t *testing.T) {
	table := Join(InventoryQuery{})
	if len(table.Rows) != 0 || table.Hidden != 0 {
		t.Fatalf("empty = %+v", table)
	}
}

func TestJoinDoesNotCallFitOnEveryRow(t *testing.T) {
	// Regression: inventory must not require architecture metadata. A tag with
	// only a name is a valid unproven candidate.
	table := Join(InventoryQuery{Tags: []InstalledModel{{Name: "mystery"}}})
	if table.Rows[0].State != StateUnproven || table.Rows[0].Note != "" {
		t.Fatalf("name-only tag = %+v", table.Rows[0])
	}
}

func TestJoinLoadedFirstWithinState(t *testing.T) {
	table := Join(InventoryQuery{
		Tags:   []InstalledModel{{Name: "aaa"}, {Name: "zzz"}},
		Loaded: []string{"zzz"},
	})
	if table.Rows[0].Model != "zzz" || !table.Rows[0].Loaded {
		t.Fatalf("loaded should sort first within unproven: %+v", table.Rows)
	}
}

func TestJoinFitTierFromSavedArchitecture(t *testing.T) {
	table := Join(InventoryQuery{
		Tags: []InstalledModel{{
			Name: "llama3.1:8b", Size: 5 * GiB, ArtifactDigest: inventoryTestDigest,
		}},
		CurrentKey: "k",
		HaveGB:     8, HaveSrc: "nvidia-smi",
		Evidence: []InventoryEvidence{{
			Model: "llama3.1:8b", ArtifactDigest: inventoryTestDigest,
			DeviceKey: "k", Level: "default",
			Arch: llama8B(), WeightsB: 5 * GiB, NumCtx: 8192,
		}},
	})
	row := table.Rows[0]
	if row.State != StateMeasured || row.Fit != Compatible {
		t.Fatalf("measured fit = %+v", row)
	}
	if row.Next != "fitr view llama3.1:8b" {
		t.Fatalf("measured next stays view: %q", row.Next)
	}
}

func TestEvidenceReusableRejectsPlacementAndComparabilityChanges(t *testing.T) {
	current := device.Fingerprint{
		Host: "box", GPU: "gpu", GPUDriver: "driver", GPUBackend: "cuda",
		Runtime: "runtime", InferenceDevice: "GPU 100%", Config: map[string]string{},
	}
	tag := InstalledModel{Name: "model", ArtifactDigest: inventoryTestDigest}
	evidence := InventoryEvidence{
		Model: "model", ArtifactDigest: inventoryTestDigest,
		Device: current, DeviceKey: current.Key(),
	}
	query := InventoryQuery{Current: current, CurrentKey: current.Key()}
	if !EvidenceReusable(tag, evidence, query) {
		t.Fatal("exact artifact and placement should be reusable")
	}
	evidence.Device.InferenceDevice = "CPU + GPU"
	if EvidenceReusable(tag, evidence, query) {
		t.Fatal("changed placement reused saved evidence")
	}
	evidence.Device = current
	evidence.ComparableIssue = "effective context is unverified"
	if EvidenceReusable(tag, evidence, query) {
		t.Fatal("unverified effective context reused saved evidence")
	}
}

func TestEvidenceReusableAcceptsEquivalentAcceleratorPlacementLabels(t *testing.T) {
	saved := device.Fingerprint{
		Host: "box", GPU: "NVIDIA GeForce RTX 4090", GPUDriver: "driver", GPUBackend: "cuda",
		Runtime: "runtime", InferenceDevice: "GPU 100%", Config: map[string]string{},
	}
	current := saved
	current.InferenceDevice = "CUDA / NVIDIA GeForce RTX 4090"
	tag := InstalledModel{Name: "model", ArtifactDigest: inventoryTestDigest}
	evidence := InventoryEvidence{
		Model: "model", ArtifactDigest: inventoryTestDigest,
		Device: saved, DeviceKey: saved.Key(),
	}
	query := InventoryQuery{Current: current, CurrentKey: current.Key()}
	if !EvidenceReusable(tag, evidence, query) {
		t.Fatal("equivalent full-accelerator placement labels made fresh evidence stale")
	}
	current.InferenceDevice = "GPU 50%"
	query.Current = current
	if EvidenceReusable(tag, evidence, query) {
		t.Fatal("partial accelerator placement reused full-accelerator evidence")
	}
}

func TestJoinUnprovenWithArchSkipsAdviseWhenItFits(t *testing.T) {
	table := Join(InventoryQuery{
		Tags:   []InstalledModel{{Name: "llama3.1:8b", Size: 5 * GiB, Arch: llama8B()}},
		HaveGB: 8, HaveSrc: "nvidia-smi",
	})
	row := table.Rows[0]
	if row.State != StateUnproven || row.Fit != Compatible {
		t.Fatalf("unproven fit = %+v", row)
	}
	if row.Next != "fitr run llama3.1:8b" {
		t.Fatalf("known-fit unproven should run, not advise: %q", row.Next)
	}
}

func TestJoinLowMemoryUnprovenUsesCtxRemedy(t *testing.T) {
	// 5 GiB weights + 1 GiB KV at 8k needs 6; 5.5 GB leaves a shorter window.
	table := Join(InventoryQuery{
		Tags:   []InstalledModel{{Name: "llama3.1:8b", Size: 5 * GiB, Arch: llama8B()}},
		HaveGB: 5.5, HaveSrc: "nvidia-smi",
	})
	row := table.Rows[0]
	if row.State != StateUnproven || row.Fit != LowMemory || !strings.Contains(row.Next, "--ctx") {
		t.Fatalf("low-memory unproven = %+v", row)
	}
}

func TestJoinNVIDIAUnifiedIdentityDoesNotTurnProbeIntoFitBudget(t *testing.T) {
	table := Join(InventoryQuery{
		Tags:    []InstalledModel{{Name: "llama3.1:8b", Size: 5 * GiB, Arch: llama8B()}},
		Current: device.Fingerprint{GPU: "NVIDIA GB10"},
		HaveGB:  121.7, HaveSrc: "nvidia-smi",
	})
	row := table.Rows[0]
	if row.Fit != Skip || strings.Contains(row.Windows, " ok") || row.Next != "fitr advise llama3.1:8b" {
		t.Fatalf("automatic shared-memory inventory row = %+v", row)
	}
}

func TestJoinWholeSystemUnifiedCapacityNeverRejectsFromProjection(t *testing.T) {
	for _, gpu := range []string{"AMD Radeon 780M", "Intel UHD Graphics 770"} {
		t.Run(gpu, func(t *testing.T) {
			table := Join(InventoryQuery{
				Tags: []InstalledModel{{
					Name: "large-model", Size: 9 * GiB, Arch: llama8B(),
				}},
				Current: device.Fingerprint{GPU: gpu},
				HaveGB:  8, HaveSrc: device.AppleLegacyRAMSource,
			})
			row := table.Rows[0]
			if row.State != StateUnproven || row.Fit != Skip || strings.Contains(row.Windows, " ok") {
				t.Fatalf("automatic whole-system inventory row = %+v", row)
			}
			if row.Next != "fitr advise large-model" {
				t.Fatalf("automatic whole-system next = %q", row.Next)
			}
		})
	}
}

func TestJoinExplicitBudgetRemainsAuthoritativeOnUnifiedGPU(t *testing.T) {
	table := Join(InventoryQuery{
		Tags:    []InstalledModel{{Name: "model", Size: 5 * GiB, Arch: llama8B()}},
		Current: device.Fingerprint{GPU: "AMD Radeon 780M"},
		HaveGB:  8, HaveSrc: "--vram-gb 8",
	})
	row := table.Rows[0]
	if row.State != StateUnproven || row.Fit != Compatible || !strings.Contains(row.Windows, " ok") {
		t.Fatalf("declared unified-memory inventory budget = %+v", row)
	}
}

func TestJoinCapsAtOneHundred(t *testing.T) {
	tags := make([]InstalledModel, 120)
	for i := range tags {
		tags[i] = InstalledModel{Name: "model-" + strconv.Itoa(i)}
	}
	table := Join(InventoryQuery{Tags: tags})
	if len(table.Rows) != maxInventoryRows {
		t.Fatalf("rows = %d", len(table.Rows))
	}
	if table.Hidden != 20 {
		t.Fatalf("hidden = %d", table.Hidden)
	}
}
