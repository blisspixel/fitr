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

func TestOrderPrefersCatalogMoEOnCPU(t *testing.T) {
	items := Shortlist()
	ordered := Order(items, true)
	if ordered[0].Name != "mixtral:8x7b" {
		t.Fatalf("first CPU candidate = %s", ordered[0].Name)
	}
	if !PreferMoE("AMD Radeon Graphics", "", "unified memory") {
		t.Fatal("an APU was not treated as a MoE-preference machine")
	}
	if PreferMoE("NVIDIA GeForce RTX 4090", "cuda", "nvidia-smi") {
		t.Fatal("a discrete NVIDIA GPU was treated as a CPU")
	}
}
