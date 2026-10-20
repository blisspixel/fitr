package render

import (
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/score"
)

func tokenConditionsView() *analysis.TokenConditions {
	return &analysis.TokenConditions{
		TargetLabel: "64000", Semantics: "usable-input", State: "quality_failed", Tone: analysis.TokenToneGap,
		Passed: 4, Failed: 1, Uncertainty: "fixed battery; not a confidence interval or a maximum context",
		Reason:     "a required condition failed on quality, and a passing condition does not erase it",
		NextAction: "do not adopt this target from another condition or a higher tier",
		Cases: []analysis.TokenConditionCase{
			{Condition: "focused_baseline", Outcome: "pass"},
			{Condition: "topical_distractor", Outcome: "quality_failed"},
		},
	}
}

func TestTokenConditionsAgreeAcrossTerminalAndHTML(t *testing.T) {
	report := &analysis.Report{TokenConditions: tokenConditionsView()}
	text := strings.Join(strings.Fields(renderContextTaskResult(t, report)), " ")
	var html strings.Builder
	if err := WriteHTML(&html, Artifact{
		Model: "m", Scorecard: score.Scorecard{Model: "m", Needs: map[string]score.Verdict{}},
		Meta: Meta{Analysis: report},
	}); err != nil {
		t.Fatal(err)
	}
	page := html.String()
	for _, want := range []string{"64000", "usable-input", "quality_failed", "focused_baseline", "topical_distractor", report.TokenConditions.Reason, report.TokenConditions.Uncertainty} {
		if !strings.Contains(text, want) || !strings.Contains(page, want) {
			t.Fatalf("surfaces disagree on %q\nterminal:\n%s\nhtml:\n%s", want, text, page)
		}
	}
	if strings.Contains(text, "64K") || strings.Contains(html.String(), "64K") || strings.Contains(text, "%") {
		t.Fatal("token conditions abbreviated the target or printed a rate")
	}
}

func TestTokenAccountDoesNotRenderConditions(t *testing.T) {
	text := renderContextTaskResult(t, &analysis.Report{TokenAccount: tokenAccountView()})
	if strings.Contains(text, "token conditions") {
		t.Fatalf("v1 token account rendered conditions:\n%s", text)
	}
}
