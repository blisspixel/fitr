package top

import (
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/analysis"
)

func TestResultViewProjectsTheTokenAccount(t *testing.T) {
	run := Run{
		ID: "token", Model: "alpha:8b", StartedAt: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC),
		Analysis: &analysis.Report{TokenAccount: &analysis.TokenAccount{
			TargetLabel: "64000", Semantics: "usable-input", State: "refused", Tone: analysis.TokenToneGap,
			Reason:     "a 65536 byte document pack is not a 64000 usable-input token qualification",
			NextAction: "measure the sealed usable-input target with native payload and overhead counts",
		}},
	}
	state := NewState(Snapshot{History: []Run{run}})
	state.View, state.Width, state.Height = ViewResult, 100, 40
	state.Selected[ViewResult] = run.ID
	out := Render(state, DefaultGlyphs(false)).Plain()
	for _, want := range []string{"token account", "64000 usable-input tokens", "refused", "65536"} {
		if !strings.Contains(out, want) {
			t.Fatalf("result view missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "64K") {
		t.Fatalf("result view abbreviated the target:\n%s", out)
	}
}

func TestContextPhaseDoesNotInventATokenAccount(t *testing.T) {
	prefix := 2048
	out := renderContextPhase(t, 40, &prefix)
	if strings.Contains(out, "token account") {
		t.Fatalf("byte phase rendered a token account:\n%s", out)
	}
}
