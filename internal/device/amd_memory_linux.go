package device

import (
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/blisspixel/fitr/internal/boundedio"
)

type drmCounterState string

const (
	drmAbsent     drmCounterState = "absent"
	drmUnreadable drmCounterState = "unreadable"
	drmInvalid    drmCounterState = "invalid"
	drmObserved   drmCounterState = "observed"
)

type drmCounter struct {
	State drmCounterState
	Bytes uint64
}

type drmAdapterMemory struct {
	ID, Vendor, Driver string
	APU                bool
	VRAM, VRAMUsed     drmCounter
	GTT, GTTUsed       drmCounter
}

func amdMemoryCapacity(gpu string, ram, vram float64, source string) (float64, string) {
	return amdMemoryCapacityAt("/sys/class/drm", gpu, ram, vram, source)
}

func amdMemoryCapacityAt(root, gpu string, ram, vram float64, source string) (float64, string) {
	// The legacy name probe does not expose a PCI identity. Until serving
	// adapter identity is captured, only one unambiguous host adapter can bind
	// the new counter to that name. Never choose the largest GTT across cards.
	if !amdGPUName(gpu) || source == "nvidia-smi" {
		return vram, source
	}
	adapters := readDRMAdapters(root)
	if len(adapters) != 1 || !adapters[0].APU {
		return vram, source
	}
	observed := adapters[0]
	if observed.GTT.State != drmObserved || observed.GTT.Bytes == 0 || observed.GTT.Bytes > math.MaxInt64 ||
		ram <= 0 || math.IsNaN(ram) || math.IsInf(ram, 0) ||
		float64(observed.GTT.Bytes) > ram*GB {
		return 0, ""
	}
	return float64(observed.GTT.Bytes) / GB, AMDGTTMappingSource
}

func readDRMAdapters(root string) []drmAdapterMemory {
	dir, err := os.Open(root)
	if err != nil {
		return nil
	}
	defer dir.Close()
	entries, err := dir.ReadDir(65)
	if (err != nil && !errors.Is(err, io.EOF)) || len(entries) > 64 {
		return nil
	}
	seen := map[string]bool{}
	var adapters []drmAdapterMemory
	for _, entry := range entries {
		if !drmCardName(entry.Name()) {
			continue
		}
		path, err := filepath.EvalSymlinks(filepath.Join(root, entry.Name(), "device"))
		if err != nil || seen[path] {
			continue
		}
		seen[path] = true
		adapters = append(adapters, readDRMAdapter(path))
	}
	return adapters
}

func drmCardName(name string) bool {
	if !strings.HasPrefix(name, "card") {
		return false
	}
	_, err := strconv.ParseUint(strings.TrimPrefix(name, "card"), 10, 32)
	return err == nil
}

func readDRMAdapter(path string) drmAdapterMemory {
	vendor, _ := boundedio.ReadFile(filepath.Join(path, "vendor"), 4096)
	driver, _ := filepath.EvalSymlinks(filepath.Join(path, "driver"))
	adapter := drmAdapterMemory{ID: filepath.Base(path), Vendor: strings.TrimSpace(string(vendor)),
		Driver: filepath.Base(driver)}
	adapter.APU = adapter.Vendor == "0x1002" && adapter.Driver == "amdgpu" && amdAPUDiscovery(path)
	adapter.VRAM = readDRMCounter(filepath.Join(path, "mem_info_vram_total"))
	adapter.VRAMUsed = readDRMCounter(filepath.Join(path, "mem_info_vram_used"))
	adapter.GTT = readDRMCounter(filepath.Join(path, "mem_info_gtt_total"))
	adapter.GTTUsed = readDRMCounter(filepath.Join(path, "mem_info_gtt_used"))
	return adapter
}

func readDRMCounter(path string) drmCounter {
	b, err := boundedio.ReadFile(path, 4096)
	if errors.Is(err, os.ErrNotExist) {
		return drmCounter{State: drmAbsent}
	}
	if err != nil {
		return drmCounter{State: drmUnreadable}
	}
	text := strings.TrimSpace(string(b))
	if len(text) == 0 || len(text) > 20 || strings.ContainsAny(text, "+- \t\r\n") {
		return drmCounter{State: drmInvalid}
	}
	n, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return drmCounter{State: drmInvalid}
	}
	return drmCounter{State: drmObserved, Bytes: n}
}

func amdAPUDiscovery(path string) bool {
	// Linux 24a19b953e2a amdgpu_discovery.c exposes named GC links and these
	// exact versions set AMD_IS_APU. A large GTT/VRAM ratio is not identity.
	base := filepath.Join(path, "ip_discovery", "die", "0", "GC", "0")
	major, minor, revision := readDRMCounter(filepath.Join(base, "major")),
		readDRMCounter(filepath.Join(base, "minor")), readDRMCounter(filepath.Join(base, "revision"))
	if major.State != drmObserved || minor.State != drmObserved || revision.State != drmObserved ||
		major.Bytes > 255 || minor.Bytes > 255 || revision.Bytes > 255 {
		return false
	}
	version := major.Bytes<<16 | minor.Bytes<<8 | revision.Bytes
	switch version {
	case 9<<16 | 1<<8, 9<<16 | 2<<8 | 2, 9<<16 | 3<<8,
		10<<16 | 1<<8 | 3, 10<<16 | 1<<8 | 4,
		10<<16 | 3<<8 | 1, 10<<16 | 3<<8 | 3, 10<<16 | 3<<8 | 6, 10<<16 | 3<<8 | 7,
		11<<16 | 1, 11<<16 | 4, 11<<16 | 5<<8, 11<<16 | 5<<8 | 1,
		11<<16 | 5<<8 | 2, 11<<16 | 5<<8 | 3, 11<<16 | 5<<8 | 4, 11<<16 | 5<<8 | 6,
		11<<16 | 7<<8, 11<<16 | 7<<8 | 1:
		return true
	default:
		return false
	}
}
