package device

import (
	"strings"

	"github.com/blisspixel/fitr/internal/capacity"
)

// AMDGTTMappingSource is an addressable mapping limit on a positively
// identified APU. It is neither extra RAM nor a safe model allocation budget.
const AMDGTTMappingSource = "unified memory (drm sysfs GTT mapping limit)"

// MemoryCapacity keeps serving placement ahead of physical graphics identity.
// Shared remains true under an operator override so an accelerator-only
// allocation cannot become evidence about a whole host pool.
type MemoryCapacity struct {
	Domain capacity.ResourceDomain
	GB     float64
	Source string
	Shared bool
}

// SelectMemoryCapacity is the common routing decision for advice, inventory
// and sealed capacity plans. An empty accelerator reading is not CPU evidence.
func SelectMemoryCapacity(fp Fingerprint) MemoryCapacity {
	if strings.EqualFold(strings.TrimSpace(fp.InferenceDevice), "CPU") ||
		(strings.TrimSpace(fp.GPU) == "" && fp.VRAMGb <= 0) {
		return MemoryCapacity{Domain: capacity.DomainHost, GB: fp.RAMGb,
			Source: "system physical memory", Shared: fp.RAMGb > 0}
	}
	selected := MemoryCapacity{Domain: capacity.DomainAccelerator, GB: fp.VRAMGb, Source: fp.VRAMSource}
	if IsUnifiedMemoryGPU(fp.GPU) || sharedCapacitySource(fp.VRAMSource) {
		selected.Domain, selected.Shared = capacity.DomainUnified, true
		if amdGPUName(fp.GPU) && fp.VRAMSource == "drm sysfs" {
			// A BIOS carve-out does not measure the whole shared address space.
			selected.GB, selected.Source = 0, ""
		}
	}
	return selected
}

func amdGPUName(name string) bool {
	u := strings.ToLower(name)
	return strings.Contains(u, "amd") || strings.Contains(u, "radeon") ||
		strings.Contains(u, "advanced micro devices")
}

func sharedCapacitySource(source string) bool {
	switch source {
	case NVIDIAUnifiedMemorySource, NVIDIAUnifiedProbeSource, AppleLegacyRAMSource, AMDGTTMappingSource:
		return true
	default:
		return false
	}
}
