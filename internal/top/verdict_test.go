package top

import (
	"strings"
	"testing"
)

func TestFailedVerdictKeepsDecidingEvidenceAtEveryWidth(t *testing.T) {
	run := Run{Verdicts: []Verdict{{
		Need: "tool_restraint", Label: "leaves unused tools alone", State: "FAIL",
		Why: "withdrawal; did not stop cleanly after a tool was withdrawn; ordinary irrelevance: 3/3 clean [0.21-1.00]",
	}}}
	for _, width := range []int{40, 80, 120} {
		canvas := NewCanvas(width, 10)
		renderResultVerdicts(contentWriter(&canvas), run, width)
		text := canvas.Plain()
		if !strings.Contains(text, "[FAIL]") || !strings.Contains(text, "withdrawal;") {
			t.Errorf("width %d hid the reason for failure:\n%s", width, text)
		}
		for _, line := range strings.Split(text, "\n") {
			if len([]rune(line)) > width {
				t.Errorf("width %d overflowed: %q", width, line)
			}
		}
	}
}

func TestBoardDetailLeadsWithDecidingFailure(t *testing.T) {
	run := Run{Model: "model", Verdicts: []Verdict{{
		Need: "tool_restraint", Label: "leaves unused tools alone", State: "FAIL",
		Why: "withdrawal; did not stop cleanly after a tool was withdrawn",
	}}}
	canvas := NewCanvas(42, 20)
	for i, line := range wideBoardDetail(run, true, 42, DefaultGlyphs(false)) {
		canvas.SetLine(i, line...)
	}
	if text := canvas.Plain(); !strings.Contains(text, "why not     [FAIL] withdrawal;") {
		t.Fatalf("selected Board detail hides the deciding failure:\n%s", text)
	}
}

func TestCompactResultKeepsDecidingFailure(t *testing.T) {
	run := Run{Verdicts: []Verdict{{
		Need: "tool_restraint", Label: "leaves unused tools alone", State: "FAIL",
		Why: "withdrawal; did not stop cleanly after a tool was withdrawn",
	}}}
	canvas := NewCanvas(40, 14)
	renderCompactVerdicts(contentWriter(&canvas), run)
	if text := canvas.Plain(); !strings.Contains(text, "[FAIL] withdrawal;") {
		t.Fatalf("compact result dropped the deciding failure:\n%s", text)
	}
}
