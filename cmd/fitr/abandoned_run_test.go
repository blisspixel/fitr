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
func TestAbandonedRunSaysWhatItDiscarded(t *testing.T) {
	base := render.New("none")
	defer base.Close()
	disp := &recordingDisplay{Display: base}

	// Learn how many generations a clean run makes, then fault the last one.
	// Guessing an index is environment-dependent: the same literal landed
	// before any step completed on a CI runner and after one locally, which
	// is a flaky test rather than a finding.
	probe := &runIntegrationBackend{digest: integrationDigest(), effectiveCtx: eval.NumCtx}
	if _, err := execute(context.Background(), probe, "model", runOpts{
		level: "quick", profile: "default", reps: 1, checksReps: 1,
	}, base); err != nil {
		t.Fatalf("clean run failed, so the fault index cannot be derived: %v", err)
	}
	if probe.generateCalls < 2 {
		t.Fatalf("a quick run made %d generations; too few to fault a late one", probe.generateCalls)
	}

	backend := &runIntegrationBackend{
		digest:        integrationDigest(),
		effectiveCtx:  eval.NumCtx,
		generateErrAt: map[int]error{probe.generateCalls: errors.New("injected transport fault")},
	}
	_, err := execute(context.Background(), backend, "model", runOpts{
		level: "quick", profile: "default", reps: 1, checksReps: 1,
	}, disp)
	if err == nil {
		t.Fatal("an injected transport fault produced a result")
	}

	joined := strings.Join(disp.notes, "\n")
	if !strings.Contains(joined, "discarded") {
		t.Fatalf("run was abandoned without naming the cost:\n%s", joined)
	}
	if !strings.Contains(joined, "nothing is saved") {
		t.Fatalf("diagnostic does not say the run was not saved:\n%s", joined)
	}
	// It must name a step that actually completed, not a generic apology.
	if !strings.Contains(joined, "identity") && !strings.Contains(joined, "speed") {
		t.Fatalf("diagnostic names no completed step:\n%s", joined)
	}
}
