package render

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/role"

	"github.com/clipperhouse/displaywidth"
)

func TestRoleReviewKeepsStatesAndWidth(t *testing.T) {
	for _, width := range []int{40, 60, 80, 120} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			t.Setenv("FITR_WIDTH", strconv.Itoa(width))
			t.Setenv("NO_COLOR", "1")
			var output bytes.Buffer
			WriteRoleReview(&output, role.ReviewReport{
				Role: strings.Repeat("模型", 30), State: "exploration-lead", Lead: "a",
				Candidates: []role.Candidate{
					{ID: "a", Model: "good model", State: "eligible", Preference: &role.PreferenceResult{Estimate: 0.9, Low: 0.8, High: 1},
						Evaluation: &decision.Evaluation{Requirements: []decision.RequirementResult{{ID: "quality", State: decision.RequirementEstablished}}}},
					{ID: "b", Model: "fast weak model", State: "ineligible", Reasons: []string{"bad \x1b[2J text and " + strings.Repeat("long", 30)}},
				},
				Gaps: []string{"not generally qualified for agentic work"}, Next: "confirm the role before adopting",
			}, "plain")
			text := output.String()
			for _, want := range []string{"INELIGIBLE", "Fresh confirmation", "Battery screening only", "not a joint confidence interval"} {
				if !strings.Contains(strings.Join(strings.Fields(text), " "), want) {
					t.Fatalf("missing state %q: %s", want, text)
				}
			}
			if strings.Contains(text, "\x1b") {
				t.Fatal("terminal control survived")
			}
			for _, line := range strings.Split(text, "\n") {
				if displaywidth.String(line) > width {
					t.Fatalf("line exceeds %d: %s", width, line)
				}
			}
		})
	}
}

func TestRoleIndexEmptyAndRich(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "1")
	for _, libraries := range [][]role.Library{nil, {{Name: "coding"}}} {
		var output bytes.Buffer
		WriteRoleLibraries(&output, libraries, "rich")
		if !strings.Contains(output.String(), "\x1b[") || !strings.Contains(output.String(), "fitr / roles") {
			t.Fatal("role index lost rich hierarchy")
		}
	}
}

func TestRoleReviewDoesNotInventLeadFromMissingIdentity(t *testing.T) {
	for _, state := range []string{"single-qualified", "unresolved", "exploration-lead"} {
		var output bytes.Buffer
		WriteRoleReview(&output, role.ReviewReport{
			Role: "coding", State: state,
			Candidates: []role.Candidate{{Model: "failed", State: "ineligible"}, {Model: "screened", State: "eligible"}},
		}, "plain")
		if strings.Contains(output.String(), "Survives metric bounds") {
			t.Fatalf("empty lead became a recommendation in %s: %s", state, output.String())
		}
	}
}

func TestRoleSelectionKeepsIncumbentAndSanitizesOutput(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("FORCE_COLOR", "1")
	t.Setenv("FITR_WIDTH", "40")
	for _, state := range []string{"qualified", "stale", "unselected"} {
		var output bytes.Buffer
		WriteRoleSelection(&output, role.SelectionStatus{
			Role: "coding\x1b[2J", State: state, Reason: "changed " + strings.Repeat("模型", 30),
			LastAttempt: &role.LifecycleAttemptStatus{Action: "failed", At: "2026-09-05T12:00:00Z"},
		}, "rich")
		text := output.String()
		if strings.Contains(text, "\x1b") || !strings.Contains(text, strings.ToUpper(state)) || !strings.Contains(text, "failed") {
			t.Fatalf("selection state or terminal safety lost: %q", text)
		}
		if strings.Contains(text, "fitr role review") != (state != "qualified") {
			t.Fatalf("wrong next action: %s", text)
		}
		for _, line := range strings.Split(text, "\n") {
			if displaywidth.String(line) > 40 {
				t.Fatalf("selection overflows: %q", line)
			}
		}
	}
}
