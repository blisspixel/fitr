package advise

import (
	"strconv"
	"strings"
	"testing"
)

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
		Tags:       []InstalledModel{{Name: "m"}},
		CurrentKey: "k",
		Evidence:   []InventoryEvidence{{Model: "m", DeviceKey: "k", Level: "default", NumCtx: 16384, Repeats: 3}},
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
		Tags:       []InstalledModel{{Name: "m"}},
		CurrentKey: "k",
		Serving:    map[string]int{"m": 16384},
		Evidence:   []InventoryEvidence{{Model: "m", DeviceKey: "k", Level: "default", NumCtx: 16384, Repeats: 3}},
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
		Tags:       []InstalledModel{{Name: "m"}},
		CurrentKey: "k",
		Serving:    map[string]int{"m": 8192},
		Evidence:   []InventoryEvidence{{Model: "m", DeviceKey: "k", Level: "default", NumCtx: 16384, Repeats: 3}},
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
		Tags:       []InstalledModel{{Name: "qwen3:8b", Size: 5 << 30}},
		CurrentKey: "box|gpu",
		Evidence: []InventoryEvidence{{
			Model: "qwen3:8b", DeviceKey: "box|gpu", Level: "default",
		}},
	})
	row := table.Rows[0]
	if row.State != StateMeasured || row.Next != "fitr view qwen3:8b" {
		t.Fatalf("measured path = %+v", row)
	}
}

func TestJoinQuickMeasurementAsksForDefaultRun(t *testing.T) {
	table := Join(InventoryQuery{
		Tags:       []InstalledModel{{Name: "m"}},
		CurrentKey: "k",
		Evidence:   []InventoryEvidence{{Model: "m", DeviceKey: "k", Level: "quick"}},
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
		Tags:       []InstalledModel{{Name: "m"}},
		CurrentKey: "new-runtime",
		Evidence:   []InventoryEvidence{{Model: "m", DeviceKey: "old-runtime", Level: "full"}},
	})
	row := table.Rows[0]
	if row.State != StateStale || row.Next != "fitr run m" {
		t.Fatalf("stale fingerprint = %+v", row)
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
}

func TestJoinMeasuredEvenWhenWeightsExceedVRAM(t *testing.T) {
	table := Join(InventoryQuery{
		Tags:       []InstalledModel{{Name: "cpu-offload", Size: 40 * GiB}},
		CurrentKey: "k",
		HaveGB:     16,
		Evidence:   []InventoryEvidence{{Model: "cpu-offload", DeviceKey: "k", Level: "full"}},
	})
	if table.Rows[0].State != StateMeasured {
		t.Fatalf("a successful measurement is measured, not incompatible: %+v", table.Rows[0])
	}
}

func TestJoinSortsMeasuredBeforeUnprovenAndNeverRanksQuality(t *testing.T) {
	table := Join(InventoryQuery{
		Tags: []InstalledModel{
			{Name: "zeta-unproven"},
			{Name: "alpha-measured"},
			{Name: "mid-stale"},
			{Name: "zzz-incompatible", Size: 40 * GiB},
		},
		CurrentKey: "k",
		HaveGB:     8, HaveSrc: "nvidia-smi",
		Evidence: []InventoryEvidence{
			{Model: "alpha-measured", DeviceKey: "k", Level: "full"},
			{Model: "mid-stale", DeviceKey: "old", Level: "full"},
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
		Tags:       []InstalledModel{{Name: "qwen3:8b"}},
		CurrentKey: "k",
		Evidence:   []InventoryEvidence{{Model: "qwen3:8b:latest", DeviceKey: "k", Level: "full"}},
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
		Tags:       []InstalledModel{{Name: "llama3.1:8b", Size: 5 * GiB}},
		CurrentKey: "k",
		HaveGB:     8, HaveSrc: "nvidia-smi",
		Evidence: []InventoryEvidence{{
			Model: "llama3.1:8b", DeviceKey: "k", Level: "default",
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
