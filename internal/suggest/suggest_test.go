package suggest

import (
	"strings"
	"testing"
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

func TestPartitionPutsFittingWeightsAheadOfOverBudgetMoE(t *testing.T) {
	items := []Candidate{
		{Name: "qwen2.5-coder:7b", Use: "coding"},
		{Name: "mixtral:8x7b", Use: "agent", CatalogMoE: true},
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
	if groups[0].Items[0].Name != "small-moe" || groups[0].Items[1].Name != "qwen2.5-coder:7b" {
		t.Fatalf("fitting order = %+v", groups[0].Items)
	}
	if len(groups[1].Items) != 1 || groups[1].Items[0].Name != "mixtral:8x7b" {
		t.Fatalf("over-budget = %+v", groups[1].Items)
	}
	if len(groups[2].Items) != 1 || groups[2].Items[0].Name != "unmeasured" {
		t.Fatalf("unmeasured = %+v", groups[2].Items)
	}

	short := Shortlist()
	shortBytes := make([]int64, len(short))
	for i, item := range short {
		switch item.Name {
		case "mixtral:8x7b":
			shortBytes[i] = 25 * gib
		case "qwen2.5-coder:32b":
			shortBytes[i] = 20 * gib
		default:
			shortBytes[i] = 2 * gib
		}
	}
	got := Partition(short, shortBytes, 15.6, true)
	if len(got) == 0 || len(got[0].Items) == 0 || got[0].Items[0].Name == "mixtral:8x7b" || got[0].Class != WeightFits {
		t.Fatalf("15.6 GiB CPU shortlist led with an over-budget MoE: %+v", got)
	}
	for _, item := range got[0].Items {
		if item.Name == "mixtral:8x7b" {
			t.Fatal("mixtral:8x7b was classified as fitting a 15.6 GiB budget")
		}
	}

	dense := Partition(items, bytes, 15.6, false)
	if dense[0].Items[0].Name != "qwen2.5-coder:7b" {
		t.Fatalf("a discrete GPU reordered the fitting group: %+v", dense[0].Items)
	}
	unknown := Partition(items, bytes, 0, true)
	if len(unknown) != 1 || unknown[0].Class != WeightUnmeasured || unknown[0].Items[0].Name != "qwen2.5-coder:7b" {
		t.Fatalf("an unknown budget was classified: %+v", unknown)
	}
	if !PreferMoE("AMD Radeon Graphics", "", "unified memory") {
		t.Fatal("an APU was not treated as a MoE-preference machine")
	}
	if PreferMoE("NVIDIA GeForce RTX 4090", "cuda", "nvidia-smi") {
		t.Fatal("a discrete NVIDIA GPU was treated as a CPU")
	}
}
