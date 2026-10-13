package device

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Kernel ABI fixture from Linux 24a19b953e2a amdgpu_discovery.c and
// amdgpu_{vram,gtt}_mgr.c: GC 11.0.1 is AMD_IS_APU, counters are bytes.
func amdDRMFixture(t *testing.T, root, card, id string) string {
	t.Helper()
	path := filepath.Join(root, "pci", id)
	gc := filepath.Join(path, "ip_discovery", "die", "0", "GC", "0")
	if err := os.MkdirAll(gc, 0700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"vendor": "0x1002\n", "mem_info_vram_total": "2147483648\n",
		"mem_info_vram_used": "268435456\n", "mem_info_gtt_total": "32548810752\n",
		"mem_info_gtt_used": "536870912\n", "ip_discovery/die/0/GC/0/major": "11\n",
		"ip_discovery/die/0/GC/0/minor": "0\n", "ip_discovery/die/0/GC/0/revision": "1\n",
	} {
		if err := os.WriteFile(filepath.Join(path, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	driver := filepath.Join(root, "drivers", "amdgpu")
	if err := os.MkdirAll(driver, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(driver, filepath.Join(path, "driver")); err != nil {
		t.Fatal(err)
	}
	linkDRMCard(t, root, card, path)
	return path
}

func linkDRMCard(t *testing.T, root, card, path string) {
	t.Helper()
	dir := filepath.Join(root, "drm", card)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, filepath.Join(dir, "device")); err != nil {
		t.Fatal(err)
	}
}

func TestAPUGTTUsesOneAdapterMappingLimitWithoutSumming(t *testing.T) {
	root := t.TempDir()
	amdDRMFixture(t, root, "card0", "0000:c1:00.0")
	linkDRMCard(t, root, "card1", filepath.Join(root, "pci", "0000:c1:00.0"))
	gb, source := amdMemoryCapacityAt(filepath.Join(root, "drm"),
		"Advanced Micro Devices, Inc. [AMD/ATI] Phoenix1 (rev c4)", 60, 2, "drm sysfs")
	if gb != float64(32548810752)/GB || source != AMDGTTMappingSource {
		t.Fatalf("APU capacity = %v %q, want only adapter GTT mapping limit", gb, source)
	}
	adapters := readDRMAdapters(filepath.Join(root, "drm"))
	if len(adapters) != 1 || adapters[0].ID != "0000:c1:00.0" ||
		adapters[0].VRAM.Bytes != 2147483648 || adapters[0].GTTUsed.Bytes != 536870912 {
		t.Fatalf("adapter-bound observations = %+v", adapters)
	}
}

func TestAPUGTTUnknownCountersDoNotPromoteRAM(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "+1", "bogus", "1\n2", "18446744073709551616", "68719476736"} {
		t.Run(value, func(t *testing.T) {
			root := t.TempDir()
			path := amdDRMFixture(t, root, "card0", "0000:c1:00.0")
			counter := filepath.Join(path, "mem_info_gtt_total")
			if value == "" {
				os.Remove(counter)
			} else {
				os.WriteFile(counter, []byte(value), 0600)
			}
			gb, source := amdMemoryCapacityAt(filepath.Join(root, "drm"), "AMD Radeon 780M", 60, 2, "drm sysfs")
			gb, source = preferUnifiedMemoryForOS("linux", "AMD Radeon 780M", 60, gb, source)
			if gb != 0 || source != "" {
				t.Fatalf("unknown GTT became capacity: %v %q", gb, source)
			}
		})
	}
}

func TestAPUGTTRequiresPositiveIdentity(t *testing.T) {
	for _, name := range []string{"discrete IP", "absent IP", "wrong vendor", "unknown vendor", "wrong driver", "mixed adapters"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := amdDRMFixture(t, root, "card0", "0000:c1:00.0")
			mutateDRMIdentity(t, root, path, name)
			gb, source := amdMemoryCapacityAt(filepath.Join(root, "drm"), "AMD Radeon Graphics", 60, 2, "drm sysfs")
			if gb != 2 || source != "drm sysfs" {
				t.Fatalf("unidentified adapter promoted GTT: %v %q", gb, source)
			}
		})
	}
}

func mutateDRMIdentity(t *testing.T, root, path, name string) {
	t.Helper()
	switch name {
	case "discrete IP":
		os.WriteFile(filepath.Join(path, "ip_discovery/die/0/GC/0/revision"), []byte("0"), 0600)
	case "absent IP":
		os.RemoveAll(filepath.Join(path, "ip_discovery"))
	case "wrong vendor":
		os.WriteFile(filepath.Join(path, "vendor"), []byte("0x8086"), 0600)
	case "unknown vendor":
		os.Remove(filepath.Join(path, "vendor"))
	case "wrong driver":
		os.Remove(filepath.Join(path, "driver"))
	case "mixed adapters":
		amdDRMFixture(t, root, "card1", "0000:03:00.0")
	}
}

func TestAPUGTTCounterStatesRemainDistinct(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "counter")
	if got := readDRMCounter(path); got.State != drmAbsent {
		t.Fatalf("missing counter = %+v", got)
	}
	if got := readDRMCounter(root); got.State != drmUnreadable {
		t.Fatalf("directory counter = %+v", got)
	}
	for _, text := range []string{"0", "18446744073709551615", "x", strings.Repeat("1", 21)} {
		os.WriteFile(path, []byte(text), 0600)
		got := readDRMCounter(path)
		n, err := strconv.ParseUint(text, 10, 64)
		if err == nil && (got.State != drmObserved || got.Bytes != n) {
			t.Fatalf("valid counter %q = %+v", text, got)
		}
		if err != nil && got.State != drmInvalid {
			t.Fatalf("invalid counter %q = %+v", text, got)
		}
	}
}

func TestAPUGTTRejectsInvalidHostRAMAndWrongNamedAdapter(t *testing.T) {
	root := t.TempDir()
	amdDRMFixture(t, root, "card0", "0000:c1:00.0")
	for _, ram := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		gb, source := amdMemoryCapacityAt(filepath.Join(root, "drm"), "AMD Phoenix1", ram, 2, "drm sysfs")
		if gb != 0 || source != "" {
			t.Fatalf("invalid host RAM %v promoted capacity: %v %q", ram, gb, source)
		}
	}
	gb, source := amdMemoryCapacityAt(filepath.Join(root, "drm"), "NVIDIA RTX 5090", 60, 32, "nvidia-smi")
	if gb != 32 || source != "nvidia-smi" {
		t.Fatalf("AMD counter replaced NVIDIA capacity: %v %q", gb, source)
	}
}
