package render

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/clipperhouse/displaywidth"

	"github.com/blisspixel/fitr/internal/role"
)

func TestAutoViewKeepsQualityBeforeBudgetAndSanitizesAtEveryWidth(t *testing.T) {
	view := AutoStatus{SessionID: "auto-fixture", Role: "daily\x1b[2J\nrole", State: "awaiting_adoption",
		Label: "Fresh confirmation complete; adoption pending", Incumbent: "daily:q5 (qualified)",
		Candidate: strings.Repeat("candidate", 18) + "\x1b]0;spoof\a", CandidateCount: 2,
		Choice:            "chosen\x1b[2J\nmodel",
		ExplorationPoints: 2, ConfirmationPoints: 2, Requests: 241, RequestLimit: 600,
		OutputTokens: 60241, OutputTokenLimit: 250000, ProtectedRequests: 248, ProtectedOutputTokens: 89882,
		ExpiresAt: "2026-09-05T14:00:00Z", Next: "fitr auto adopt auto-fixture"}
	for _, width := range []int{40, 80, 120} {
		t.Setenv("FITR_WIDTH", strconv.Itoa(width))
		t.Setenv("NO_COLOR", "1")
		for _, mode := range []string{"plain", "rich"} {
			var out bytes.Buffer
			WriteAutoStatus(&out, view, mode)
			text := out.String()
			if strings.ContainsAny(text, "\x1b\a") || strings.Contains(text, "spoof") || strings.Contains(text, "work  241") {
				t.Fatal("unsafe or misleading status", text)
			}
			for _, line := range strings.Split(text, "\n") {
				if utf8.RuneCountInString(line) > width {
					t.Fatalf("%d-column status overflow: %q", width, line)
				}
			}
			if strings.Index(text, "selected") > strings.Index(text, "budget used") || !strings.Contains(text, "adoption pending") {
				t.Fatal("quality state lost precedence", text)
			}
		}
	}
}

func TestAutoExplorationReviewShowsGapsBeforeBudgetWithoutNextSteps(t *testing.T) {
	view := AutoStatus{SessionID: "auto-fixture", Role: "daily", State: "unresolved",
		Label: "Evidence is insufficient to select a model", Incumbent: "No model selected",
		Review: &role.ReviewReport{EvaluatedAt: "2026-09-05T18:00:00Z", State: "exploration-lead",
			Next: "fitr role confirm must-not-appear",
			Candidates: []role.Candidate{
				{Model: "first\x1b]0;hidden-title\a界", State: "unresolved\x1b[2J",
					Reasons: []string{"quality: confidence interval crosses the required floor\nwith uncertain evidence", strings.Repeat("wide界", 20)}},
				{Model: "second", State: "eligible", Preference: &role.PreferenceResult{Estimate: 0.5, Low: 0.3, High: 0.7}},
				{Model: "third", State: "ineligible", Reasons: []string{"memory: exceeds role limit"}},
			}, Gaps: []string{"Candidate comparison remains unresolved.\x1b[2J"}}}
	for _, width := range []int{40, 80, 120} {
		t.Setenv("FITR_WIDTH", strconv.Itoa(width))
		t.Setenv("NO_COLOR", "1")
		for _, mode := range []string{"plain", "rich"} {
			var out bytes.Buffer
			WriteAutoStatus(&out, view, mode)
			text := out.String()
			flat := strings.Join(strings.Fields(text), " ")
			for _, wanted := range []string{"Exploration rechecked at", "Recorded session outcome unchanged.", "quality: confidence", "UNRESOLVED", "ELIGIBLE", "INELIGIBLE", "bounds 0.300 to 0.700", "comparison remains unresolved"} {
				if !strings.Contains(flat, wanted) {
					t.Fatalf("missing %q at %d columns: %s", wanted, width, text)
				}
			}
			if strings.ContainsAny(text, "\x1b\a") || strings.Contains(text, "hidden-title") || strings.Contains(text, "must-not-appear") ||
				strings.Contains(text, "confirm next") || strings.Index(text, "quality:") > strings.Index(text, "budget used") {
				t.Fatal("review lost safety, history or quality ordering", text)
			}
			for _, line := range strings.Split(text, "\n") {
				if displaywidth.String(line) > width {
					t.Fatalf("%d-column exploration review overflow: %q", width, line)
				}
			}
		}
	}
}
