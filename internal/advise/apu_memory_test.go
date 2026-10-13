package advise

import (
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/device"
)

func TestAPUCarveoutDoesNotRejectInventoryModel(t *testing.T) {
	table := Join(InventoryQuery{
		Tags: []InstalledModel{{Name: "qwen3:4b", Size: 23 * GiB / 10, Arch: llama8B()}},
		Current: device.Fingerprint{GPU: "AMD Radeon 780M", RAMGb: 60,
			VRAMGb: 2, VRAMSource: "drm sysfs"},
		HaveGB: 2, HaveSrc: "drm sysfs",
	})
	row := table.Rows[0]
	if row.State != StateUnproven || row.Fit != Skip || strings.Contains(row.Windows, " ok") {
		t.Fatalf("BIOS carveout became an automatic model budget: %+v", row)
	}
}
