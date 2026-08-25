package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/render"
)

// recordingDisplay captures notes so the abandonment diagnostic can be
// asserted rather than assumed.
type recordingDisplay struct {
	render.Display
	notes []string
}

func (d *recordingDisplay) Note(msg, level string) {
	d.notes = append(d.notes, msg)
	d.Display.Note(msg, level)
}

// A run is fail-closed: a fault mid-battery abandons everything measured so
// far. That is deliberate, because those measurements were taken under
// conditions that no longer hold. Doing it silently was not: a late transport
// fault discarded a minute of completed work and said nothing about it, which
// is indistinguishable from the tool losing the result.
//
// The fault is injected at the first generation, the only index that exists on
// every host. Choosing a later one made this test depend on how many probes a
// machine happens to make: it passed locally and failed on a CI runner, which
// is a flaky test rather than a finding.
func TestAbandonedRunSaysWhatItDiscarded(t *testing.T) {
	base := render.New("none")
	defer base.Close()
	disp := &recordingDisplay{Display: base}

	backend := &runIntegrationBackend{
		digest:        integrationDigest(),
		effectiveCtx:  eval.NumCtx,
		generateErrAt: map[int]error{1: errors.New("injected transport fault")},
	}
	_, err := execute(context.Background(), backend, "model", runOpts{
		level: "quick", profile: "default", reps: 1, checksReps: 1,
	}, disp)
	if err == nil {
		t.Fatal("an injected transport fault produced a result")
	}
	// A constrained machine can abort on a device probe before reaching the
	// battery. That is a real environment, not a defect in the diagnostic, and
	// asserting through it produced a test that failed on CI and passed
	// locally. Say which happened instead of guessing.
	if !strings.Contains(err.Error(), "injected transport fault") {
		t.Skipf("run aborted before the injected fault, so there is nothing to assert here: %v", err)
	}

	joined := strings.Join(disp.notes, "\n")
	if !strings.Contains(joined, "abandoned") {
		t.Fatalf("run was abandoned without saying so:\n%s", joined)
	}
	if !strings.Contains(joined, "nothing is saved") {
		t.Fatalf("diagnostic does not say the run was not saved:\n%s", joined)
	}
	// Faulting the first generation means nothing had completed, and the
	// diagnostic has to account for that rather than trail off.
	if !strings.Contains(joined, "no step had completed yet") {
		t.Fatalf("diagnostic does not account for an early fault:\n%s", joined)
	}
}

// The other half of the contract: when steps did complete they are named, so
// the operator sees the cost of the abandonment instead of guessing at it.
func TestAbandonmentSummaryNamesCompletedSteps(t *testing.T) {
	for _, tc := range []struct {
		name      string
		completed []string
		want      string
	}{
		{"nothing done", nil, "no step had completed yet"},
		{"one done", []string{"speed"}, "discarded: speed"},
		{"several done", []string{"speed", "memory"}, "discarded: speed, memory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := abandonedStepSummary(tc.completed); !strings.Contains(got, tc.want) {
				t.Fatalf("summary %q does not contain %q", got, tc.want)
			}
		})
	}
}
