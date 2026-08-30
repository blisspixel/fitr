package main

import (
	"strings"
	"testing"
)

// Placement changes comparability. The warning reports the runtime receipt
// without inventing a hardware bottleneck from placement alone.
func TestPlacementWarningSpeaksUpOnlyWhenItShould(t *testing.T) {
	for _, tc := range []struct {
		placement string
		want      string
	}{
		{"GPU 100%", ""},
		{"", ""},
		{"unknown", ""},
		{"GPU 65%", "partial offload"},
		{"GPU 5%", "partial offload"},
		{"CPU", "no offload"},
	} {
		got := placementWarning(tc.placement)
		if tc.want == "" {
			if got != "" {
				t.Fatalf("placement %q raised %q; a resident or unobserved run is not a fault",
					tc.placement, got)
			}
			continue
		}
		if !strings.Contains(got, tc.want) {
			t.Fatalf("placement %q produced %q, want it to mention %q", tc.placement, got, tc.want)
		}
		// A warning without a remedy is half an answer, which is this
		// project's stated rule for every negative verdict.
		if !strings.Contains(got, "re-run") && !strings.Contains(got, "only meaningful") {
			t.Fatalf("placement warning %q offers no next step", got)
		}
	}
}
