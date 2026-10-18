package main

import (
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/isolate"
)

func TestExecutionDisabledNoteSeparatesAChoiceFromAMissingSandbox(t *testing.T) {
	chosen := executionDisabledNote(false)
	if !strings.Contains(chosen, "disabled by default") {
		t.Fatalf("opt-out = %q", chosen)
	}
	got := executionDisabledNote(true)
	if strings.Contains(got, "disabled by default") {
		t.Fatalf("probe failure read as a forgotten flag: %q", got)
	}
	reason := isolate.Probe().Reason
	switch {
	case strings.Contains(reason, "bubblewrap (bwrap) is not installed"):
		if !strings.Contains(got, "install bubblewrap") || !strings.Contains(got, "fitr doctor") {
			t.Fatalf("missing package note = %q", got)
		}
	case strings.Contains(reason, "needs bubblewrap on Linux"):
		if !strings.Contains(got, "other results are unaffected") || !strings.Contains(got, reason) {
			t.Fatalf("other OS note = %q", got)
		}
	default:
		if !strings.Contains(got, reason) {
			t.Fatalf("note dropped the probe reason: %q", got)
		}
	}
}
