package suggest

import (
	"strings"
	"testing"
	"time"
)

func TestStartCommandNamesTheInstalledStoppedUnit(t *testing.T) {
	if got := StartCommand(true, Unit{User: true, Exists: true}, Unit{Exists: true}); got != "systemctl --user start ollama" {
		t.Fatalf("user unit = %q", got)
	}
	if got := StartCommand(true, Unit{}, Unit{Exists: true}); got != "sudo systemctl start ollama" {
		t.Fatalf("system unit = %q", got)
	}
	if got := StartCommand(true, Unit{Exists: true, Active: true}, Unit{Exists: true, Active: true}); got != "" {
		t.Fatalf("running units produced %q", got)
	}
	if got := StartCommand(true, Unit{}, Unit{}); got != "ollama serve" {
		t.Fatalf("binary only = %q", got)
	}
	if got := StartCommand(false, Unit{}, Unit{}); got != "" {
		t.Fatalf("nothing installed produced %q", got)
	}
}

func TestAcceleratorPackageIgnoresAVulkanTree(t *testing.T) {
	if got := AcceleratorPackage("AMD Radeon Graphics", []string{"libggml-cpu-x64.so", "vulkan"}); got != "" {
		t.Fatalf("vulkan directory was called CPU-only: %q", got)
	}
	got := AcceleratorPackage("AMD Radeon Graphics", []string{"libggml-cpu-x64.so", "libggml-cpu-sse42.so"})
	if got != "ollama-vulkan or ollama-rocm" {
		t.Fatalf("cpu-only directory = %q", got)
	}
	if AcceleratorPackage("", []string{"libggml-cpu-x64.so"}) != "" {
		t.Fatal("a machine with no GPU was told to install a GPU package")
	}
}

func TestProjectWeightsDoesNotInventKV(t *testing.T) {
	got := ProjectWeights(16, 5*1024*1024*1024)
	if !got.Known || !got.WeightsFit || !strings.Contains(got.Note, "projection, not a measurement") || strings.Contains(got.Note, "KV cache is") {
		t.Fatalf("projection = %+v", got)
	}
	if !strings.Contains(got.Note, "were not calculated") {
		t.Fatalf("note invented a cache figure: %q", got.Note)
	}
	missing := ProjectWeights(0, 5)
	if missing.Known || missing.WeightsFit {
		t.Fatalf("unmeasured budget was projected: %+v", missing)
	}
}

func TestManifestBytesSumsLayersAndRefusesAPartialDocument(t *testing.T) {
	body := []byte(`{"config":{"size":10},"layers":[{"size":20},{"size":30}]}`)
	got, err := ManifestBytes(body)
	if err != nil || got != 60 {
		t.Fatalf("size = %d, %v", got, err)
	}
	if _, err := ManifestBytes([]byte(`{"config":{"size":10}}`)); err == nil {
		t.Fatal("a manifest without layers produced a size")
	}
}

func TestShortlistDropsTheOctoberFamilies(t *testing.T) {
	retired := []string{
		"qwen2.5-coder:1.5b",
		"qwen2.5-coder:7b",
		"qwen2.5-coder:14b",
		"qwen2.5-coder:32b",
		"llama3.2:3b",
		"deepseek-coder:6.7b",
		"mixtral:8x7b",
	}
	got := Shortlist()
	for _, item := range got {
		for _, name := range retired {
			if item.Name == name {
				t.Fatalf("reviewed set still names %s", name)
			}
		}
	}
	if ShortlistReviewed == "2026-10-12" {
		t.Fatal("review date is still 2026-10-12")
	}
}

func TestShortlistIsTheNovemberReview(t *testing.T) {
	want := []Candidate{
		{Name: "qwen3.5:2b", Use: "general"},
		{Name: "qwen3:8b", Use: "general"},
		{Name: "qwen3.5:9b", Use: "general"},
		{Name: "gemma4:e4b", Use: "general"},
		{Name: "gpt-oss:20b", Use: "agent", CatalogMoE: true},
		{Name: "qwen3.5:27b", Use: "general"},
		{Name: "qwen3-coder:30b", Use: "coding", CatalogMoE: true},
		{Name: "gemma4:26b", Use: "agent", CatalogMoE: true},
	}
	got := Shortlist()
	if len(got) != len(want) {
		t.Fatalf("len %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("shortlist[%d] = %+v, want %+v", i, got[i], want[i])
		}
		repo, tag, err := SplitName(got[i].Name)
		if err != nil || repo == "" || tag == "" || tag == "latest" {
			t.Fatalf("name %q did not keep an explicit library tag: %s %s %v", got[i].Name, repo, tag, err)
		}
	}
	if ShortlistReviewed != "2026-11-12" {
		t.Fatalf("review date = %s", ShortlistReviewed)
	}
}

func TestReviewedLinesNameTheDateAndTheStaleAge(t *testing.T) {
	reviewed := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	fresh := ReviewedLines(reviewed)
	if len(fresh) != 1 || fresh[0] != "names are a fixed reviewed set (2026-11-12), not a search. A catalog tag is not a measured purpose." {
		t.Fatalf("fresh = %#v", fresh)
	}
	if note := ReviewAgeNote(ShortlistReviewed, reviewed.AddDate(0, 0, ReviewStaleAfter)); note != "" {
		t.Fatalf("day %d produced %q", ReviewStaleAfter, note)
	}
	// 2026-12-28 01:00 at UTC+14 is still 2026-12-27 in UTC. The age uses the
	// local calendar date, so this is 46 days after 2026-11-12.
	staleNow := time.Date(2026, 12, 28, 1, 0, 0, 0, time.FixedZone("east", 14*3600))
	stale := ReviewedLines(staleNow)
	if len(stale) != 2 || stale[0] != fresh[0] || stale[1] != "reviewed set is 46 days old; newer families may exist" {
		t.Fatalf("stale = %#v", stale)
	}
	if ReviewAgeNote("not-a-date", staleNow) != "" || ReviewAgeNote(ShortlistReviewed, reviewed.AddDate(0, 0, -1)) != "" {
		t.Fatal("a bad or future review date produced an age")
	}
}

func TestPartitionPutsFittingWeightsAheadOfOverBudgetMoE(t *testing.T) {
	items := []Candidate{
		{Name: "dense-mid", Use: "coding"},
		{Name: "large-moe", Use: "agent", CatalogMoE: true},
		{Name: "small-moe", Use: "coding", CatalogMoE: true},
		{Name: "unmeasured", Use: "general"},
	}
	const gib = int64(1024 * 1024 * 1024)
	bytes := []int64{5 * gib, 25 * gib, 4 * gib, 0}
	groups := Partition(items, bytes, 15.6, true)
	if len(groups) != 3 || groups[0].Class != WeightFits || groups[1].Class != WeightOverBudget ||
		groups[2].Class != WeightUnmeasured {
		t.Fatalf("groups = %+v", groups)
	}
	if groups[0].Items[0].Name != "small-moe" || groups[0].Items[1].Name != "dense-mid" {
		t.Fatalf("fitting order = %+v", groups[0].Items)
	}
	if len(groups[1].Items) != 1 || groups[1].Items[0].Name != "large-moe" {
		t.Fatalf("over-budget = %+v", groups[1].Items)
	}
	if len(groups[2].Items) != 1 || groups[2].Items[0].Name != "unmeasured" {
		t.Fatalf("unmeasured = %+v", groups[2].Items)
	}

	dense := Partition(items, bytes, 15.6, false)
	if dense[0].Items[0].Name != "dense-mid" {
		t.Fatalf("a discrete GPU reordered the fitting group: %+v", dense[0].Items)
	}
	unknown := Partition(items, bytes, 0, true)
	if len(unknown) != 1 || unknown[0].Class != WeightUnmeasured || unknown[0].Items[0].Name != "dense-mid" {
		t.Fatalf("an unknown budget was classified: %+v", unknown)
	}
	if !PreferMoE("AMD Radeon Graphics", "", "unified memory") {
		t.Fatal("an APU was not treated as a MoE-preference machine")
	}
	if PreferMoE("NVIDIA GeForce RTX 4090", "cuda", "nvidia-smi") {
		t.Fatal("a discrete NVIDIA GPU was treated as a CPU")
	}
}

func TestShortlistKeepsOverBudgetWeightsBehindTheFittingMoE(t *testing.T) {
	const gib = int64(1024 * 1024 * 1024)
	short := Shortlist()
	shortBytes := make([]int64, len(short))
	for i, item := range short {
		switch item.Name {
		case "qwen3-coder:30b", "gemma4:26b":
			shortBytes[i] = 25 * gib
		case "qwen3.5:27b":
			shortBytes[i] = 20 * gib
		default:
			shortBytes[i] = 2 * gib
		}
	}
	got := Partition(short, shortBytes, 15.6, true)
	if len(got) < 2 || got[0].Class != WeightFits || len(got[0].Items) == 0 || got[0].Items[0].Name != "gpt-oss:20b" {
		t.Fatalf("15.6 GiB CPU shortlist did not lead with the fitting MoE: %+v", got)
	}
	if got[1].Class != WeightOverBudget {
		t.Fatalf("over-budget group = %+v", got)
	}
	over := map[string]bool{}
	for _, item := range got[1].Items {
		over[item.Name] = true
	}
	for _, name := range []string{"qwen3-coder:30b", "gemma4:26b", "qwen3.5:27b"} {
		if !over[name] {
			t.Fatalf("%s was not over the 15.6 GiB budget: %+v", name, got[1].Items)
		}
	}
	for _, item := range got[0].Items {
		if over[item.Name] {
			t.Fatalf("%s was classified as fitting a 15.6 GiB budget", item.Name)
		}
	}
	gpu := Partition(short, shortBytes, 15.6, false)
	if gpu[0].Class != WeightFits || gpu[0].Items[0].Name != "qwen3.5:2b" {
		t.Fatalf("a discrete GPU reordered the shortlist: %+v", gpu[0].Items)
	}
}
