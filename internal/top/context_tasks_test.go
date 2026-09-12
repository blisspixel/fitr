package top

import (
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/analysis"
)

// contextPhaseRun is a context-level run: the document phase is its only
// planned work, so it carries no decode, prefill or TTFT.
func contextPhaseRun(prefix *int) Run {
	tiers := []analysis.ContextTaskTier{
		{PayloadUTF8Bytes: 2048, Outcome: "pass", Planned: 9, Pass: 9},
		{PayloadUTF8Bytes: 8192, Outcome: "fail", Planned: 9, Pass: 4, Fail: 5},
	}
	unavailable := 0
	if prefix == nil {
		tiers[0] = analysis.ContextTaskTier{
			PayloadUTF8Bytes: 2048, Outcome: "unavailable", Planned: 9, Pass: 8, Unavailable: 1,
		}
		unavailable = 1
	}
	return Run{
		ID: "ctx", Model: "alpha:8b", StartedAt: time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC),
		Analysis: &analysis.Report{ContextTasks: &analysis.ContextTasks{
			Status: analysis.StatusAvailable, OperatingWindow: 16384, OutputReserve: 128,
			Tiers: tiers, Planned: 18, Pass: 13, Unavailable: unavailable,
			VerifiedPrefixBytes: prefix,
		}},
	}
}

func renderContextPhase(t *testing.T, height int, prefix *int) string {
	t.Helper()
	return renderContextPhaseAt(t, 120, height, prefix)
}

func renderContextPhaseAt(t *testing.T, width, height int, prefix *int) string {
	t.Helper()
	run := contextPhaseRun(prefix)
	state := NewState(Snapshot{History: []Run{run}})
	state.View, state.Width, state.Height = ViewResult, width, height
	state.Selected[ViewResult] = run.ID
	return Render(state, DefaultGlyphs(false)).Plain()
}

// At a narrow width the prefix sentence is clipped like every other long
// message in this view. It must still be present and marked as clipped, never
// dropped: an absent explanation reads as there being nothing to explain.
func TestNarrowResultViewClipsThePrefixRatherThanDroppingIt(t *testing.T) {
	out := renderContextPhaseAt(t, 80, 40, nil)
	if !strings.Contains(out, "no verified prefix") {
		t.Fatalf("the narrow view dropped the prefix line:\n%s", out)
	}
	if !strings.Contains(out, "...") {
		t.Fatalf("the narrow view clipped without marking it:\n%s", out)
	}
}

func TestResultViewRendersTheContextTaskPhase(t *testing.T) {
	prefix := 2048
	out := renderContextPhase(t, 40, &prefix)
	for _, want := range []string{
		"context tasks", "16384 tokens, reserve 128",
		"2048B", "8192B", "9/9 cells", "4/9 cells", "verified prefix 2048B",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("result view is missing %q:\n%s", want, out)
		}
	}
}

// A context-level run has no battery, so a compact view that dropped the phase
// would describe a run in which nothing at all was measured. The compact form
// may summarise the tiers, but it must still carry the window and the prefix.
func TestCompactResultKeepsTheContextTaskPhase(t *testing.T) {
	prefix := 2048
	out := renderContextPhase(t, 24, &prefix)
	for _, want := range []string{"context tasks", "16384 tokens, reserve 128", "verified prefix 2048B"} {
		if !strings.Contains(out, want) {
			t.Fatalf("compact result dropped %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "2 declared") {
		t.Fatalf("compact result did not summarise the tiers:\n%s", out)
	}
}

// A suppressed prefix reads as a suppression here too, and carries the warning
// role rather than being shown as an ordinary value.
func TestResultViewMarksASuppressedPrefix(t *testing.T) {
	run := contextPhaseRun(nil)
	state := NewState(Snapshot{History: []Run{run}})
	state.View, state.Width, state.Height = ViewResult, 120, 40
	state.Selected[ViewResult] = run.ID
	canvas := Render(state, DefaultGlyphs(false))
	if !strings.Contains(canvas.Plain(), "suppressed for the whole phase") {
		t.Fatalf("result view did not explain the missing prefix:\n%s", canvas.Plain())
	}
	warned := false
	for _, row := range canvas.Rows {
		for _, span := range row {
			if span.Role == RoleWarning && strings.Contains(span.Text, "no verified prefix") {
				warned = true
			}
		}
	}
	if !warned {
		t.Fatal("a suppressed prefix was not given the warning role")
	}
}

// Every surface takes the sentence from the analysis, so the monitor cannot
// describe a phase differently from the terminal or the export.
func TestResultViewUsesTheSharedPrefixSentence(t *testing.T) {
	for _, prefix := range []*int{nil, new(int)} {
		run := contextPhaseRun(prefix)
		note, _ := analysis.ContextTaskPrefixNote(run.Analysis.ContextTasks)
		out := renderContextPhase(t, 40, prefix)
		if !strings.Contains(out, note) {
			t.Fatalf("result view does not carry the shared sentence %q:\n%s", note, out)
		}
	}
}
