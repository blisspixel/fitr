package render

import (
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/score"
)

func tokenAccountView() *analysis.TokenAccount {
	return &analysis.TokenAccount{
		TargetLabel: "64000", Semantics: "usable-input", State: "refused", Tone: analysis.TokenToneGap,
		PayloadLabel: "64000", OverheadLabel: "200", ReserveLabel: "128",
		WindowLabel: "enforced 131072; requested 131072",
		Passed:      0, Unresolved: 1, Seeds: 1,
		Uncertainty: "fixed battery; not a confidence interval or a maximum context",
		Reason:      "a 65536 byte document pack is not a 64000 usable-input token qualification",
		FreeMemory:  "free memory was unmeasured at 2026-10-17T12:00:00.000000000Z",
		NextAction:  "measure the sealed usable-input target with native payload and overhead counts",
	}
}

func TestTokenAccountAgreesAcrossTerminalAndHTML(t *testing.T) {
	report := &analysis.Report{TokenAccount: tokenAccountView()}
	text := renderContextTaskResult(t, report)
	var html strings.Builder
	if err := WriteHTML(&html, Artifact{
		Model: "m", Scorecard: score.Scorecard{Model: "m", Needs: map[string]score.Verdict{}},
		Meta: Meta{Analysis: report},
	}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"64000", "65536", "usable-input", "refused", report.TokenAccount.Reason} {
		if !strings.Contains(text, want) || !strings.Contains(html.String(), want) {
			t.Fatalf("surfaces disagree on %q\nterminal:\n%s\nhtml:\n%s", want, text, html.String())
		}
	}
	if strings.Contains(text, "64K") || strings.Contains(html.String(), "64K") || strings.Contains(text, "%") {
		t.Fatal("token account abbreviated the target or printed a rate")
	}
}

func TestBytePhaseDoesNotRenderATokenAccount(t *testing.T) {
	prefix := 64 << 10
	text := renderContextTaskResult(t, contextTaskReport(&prefix, analysis.StatusAvailable, []analysis.ContextTaskTier{
		{PayloadUTF8Bytes: prefix, Outcome: "pass", Planned: 9, Pass: 9},
	}))
	if strings.Contains(text, "token account") || strings.Contains(text, "usable-input token qualification") {
		t.Fatalf("byte phase rendered a token claim:\n%s", text)
	}
}
