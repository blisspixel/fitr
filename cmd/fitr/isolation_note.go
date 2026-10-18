package main

import (
	"strings"

	"github.com/blisspixel/fitr/internal/isolate"
)

// executionDisabledNote is the progress line when generated code will not
// run. A protocol that did not opt in says so. A fitr run whose sandbox
// probe failed says the probe reason, not that the operator forgot a flag.
func executionDisabledNote(isolatedRequested bool) string {
	if !isolatedRequested {
		return "generated-code execution is disabled by default; coding and executable agent tasks are SKIP"
	}
	reason := strings.TrimSpace(isolate.Probe().Reason)
	if reason == "" {
		reason = "isolated execution is unavailable"
	}
	note := "coding and executable agent tasks are SKIP; " + reason
	switch {
	case strings.Contains(reason, "bubblewrap (bwrap) is not installed"):
		note += "; install bubblewrap to score coding locally (fitr doctor shows sandbox status)"
	case strings.Contains(reason, "needs bubblewrap on Linux"):
		note += "; other results are unaffected"
	}
	return note
}
