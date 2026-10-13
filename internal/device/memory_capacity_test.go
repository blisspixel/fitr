package device

import (
	"math"
	"testing"

	"github.com/blisspixel/fitr/internal/capacity"
)

func TestAPUCapacitySelectionKeepsPlacementAndSemanticsSeparate(t *testing.T) {
	for _, test := range []struct {
		name, gpu, placement, source string
		gb, wantGB                   float64
		domain                       capacity.ResourceDomain
		shared                       bool
	}{
		{"CPU on APU", "AMD Radeon 780M", "CPU", "drm sysfs", 2, 60, capacity.DomainHost, true},
		{"API missing is not CPU", "AMD Radeon 780M", "unknown", AMDGTTMappingSource, 30, 30, capacity.DomainUnified, true},
		{"Phoenix kernel identity", "AMD Phoenix1", "GPU 100%", AMDGTTMappingSource, 30, 30, capacity.DomainUnified, true},
		{"carveout is not shared capacity", "AMD Radeon 780M", "GPU 100%", "drm sysfs", 2, 0, capacity.DomainUnified, true},
		{"discrete VRAM", "AMD Radeon RX 7900 XTX", "GPU 100%", "drm sysfs", 24, 24, capacity.DomainAccelerator, false},
		{"no GPU backend is not CPU", "AMD Radeon RX 7900 XTX", "unknown", "drm sysfs", 24, 24, capacity.DomainAccelerator, false},
		{"missing GPU is not CPU", "", "unknown", "", 0, 0, "", false},
		{"missing placement is not CPU", "", "", "", 0, 0, "", false},
		{"negative capacity is not CPU", "", "unknown", "", -1, 0, "", false},
		{"malformed capacity is not CPU", "", "unknown", "", math.NaN(), 0, "", false},
		{"infinite capacity is not CPU", "", "unknown", "", math.Inf(1), 0, "", false},
		{"positive CPU without GPU", "", "CPU", "", 0, 60, capacity.DomainHost, true},
		{"positive VRAM without name", "", "unknown", "nvidia-smi", 24, 24, capacity.DomainAccelerator, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := SelectMemoryCapacity(Fingerprint{GPU: test.gpu, InferenceDevice: test.placement,
				RAMGb: 60, VRAMGb: test.gb, VRAMSource: test.source})
			if got.Domain != test.domain || got.GB != test.wantGB || got.Shared != test.shared {
				t.Fatalf("memory routing = %+v, want domain %s, GB %v, shared %v", got, test.domain, test.wantGB, test.shared)
			}
			if test.wantGB == 0 && got.Source != "" {
				t.Fatalf("unknown whole-pool capacity kept carveout source: %+v", got)
			}
		})
	}
}
