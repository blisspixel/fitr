package top

import (
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/analysis"
)

func TestResultViewProjectsTokenConditions(t *testing.T) {
	run := Run{
		ID: "conditions", Model: "alpha:8b", StartedAt: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC),
		Analysis: &analysis.Report{TokenConditions: &analysis.TokenConditions{
			TargetLabel: "64000", Semantics: "usable-input", State: "quality_failed", Tone: analysis.TokenToneGap,
			Reason:      "a required condition failed on quality, and a passing condition does not erase it",
			NextAction:  "do not adopt this target from another condition or a higher tier",
			Uncertainty: "fixed battery; not a confidence interval or a maximum context",
			Cases: []analysis.TokenConditionCase{
				{Condition: "focused_baseline", Outcome: "pass"},
				{Condition: "topical_distractor", Outcome: "quality_failed"},
			},
		}},
	}
	state := NewState(Snapshot{History: []Run{run}})
	state.View, state.Width, state.Height = ViewResult, 100, 40
	state.Selected[ViewResult] = run.ID
	out := Render(state, DefaultGlyphs(false)).Plain()
	for _, want := range []string{"token conditions", "64000 usable-input tokens", "quality_failed", "focused_baseline", "topical_distractor"} {
		if !strings.Contains(out, want) {
			t.Fatalf("result view missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "64K") || strings.Contains(out, "%") {
		t.Fatalf("result view abbreviated the target or printed a rate:\n%s", out)
	}
}

func TestTokenAccountDoesNotInventConditions(t *testing.T) {
	run := Run{
		ID: "token", Model: "alpha:8b", StartedAt: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC),
		Analysis: &analysis.Report{TokenAccount: &analysis.TokenAccount{
			TargetLabel: "64000", Semantics: "usable-input", State: "refused", Tone: analysis.TokenToneGap,
		}},
	}
	state := NewState(Snapshot{History: []Run{run}})
	state.View, state.Width, state.Height = ViewResult, 100, 40
	state.Selected[ViewResult] = run.ID
	out := Render(state, DefaultGlyphs(false)).Plain()
	if strings.Contains(out, "token conditions") {
		t.Fatalf("v1 token account rendered conditions:\n%s", out)
	}
}
