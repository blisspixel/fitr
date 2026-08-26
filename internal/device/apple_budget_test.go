package device

import (
	"strings"
	"testing"
)

// Apple Silicon shares one memory pool, but the GPU cannot wire all of it.
// Reporting installed RAM as GPU-available memory is the unified-memory version
// of grading against total instead of free: on a 128 GB machine it overstates
// the budget by tens of gigabytes and certifies a model that cannot load.
//
// That is the exact defect fitr cites in other tools, and it shipped here until
// this test existed. It is deliberately not behind a build tag: the policy is a
// number and a label, so every platform's CI can hold it.
func TestAppleGPUBudgetIsNotInstalledRAM(t *testing.T) {
	for _, ram := range []float64{36, 64, 128, 512} {
		got := ram * appleWiredLimitFraction
		if got >= ram {
			t.Errorf("%.0f GB installed: budget %.1f is not below it", ram, got)
		}
		if got < ram*0.5 {
			t.Errorf("%.0f GB installed: budget %.1f is implausibly conservative", ram, got)
		}
	}
	// The case that motivated this. A 111 GB model "fits" 128 GB of installed
	// RAM and does not fit what the GPU may actually wire.
	if budget := 128 * appleWiredLimitFraction; budget >= 111 {
		t.Errorf("budget %.1f GB still certifies a 111 GB model on a 128 GB machine", budget)
	}
}

// A trust decision keys on these by exact string. Changing one silently turned
// every macOS model unproven once already, so they are constants and this test
// is the reason they stay constants.
func TestAppleMemorySourcesAreLabelledHonestly(t *testing.T) {
	if !strings.Contains(AppleAssumedShareSource, "assumed") {
		t.Errorf("a derived budget must announce itself: %q", AppleAssumedShareSource)
	}
	if strings.Contains(AppleWiredLimitSource, "assumed") {
		t.Errorf("an explicit kernel setting is a measurement, not an assumption: %q",
			AppleWiredLimitSource)
	}
	if AppleWiredLimitSource == AppleAssumedShareSource {
		t.Error("measured and assumed budgets must be distinguishable by source")
	}
}
