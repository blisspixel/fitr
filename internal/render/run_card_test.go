package render

import (
	"fmt"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/score"
)

func TestUseForInkFollowsTheOutcome(t *testing.T) {
	t.Setenv("FITR_WIDTH", "80")
	cases := []struct {
		name        string
		useFor      string
		calibration bool
		wantColor   string
	}{
		{name: "accepted", useFor: "coding and structured output", wantColor: "\x1b[32m"},
		{name: "gap", useFor: "no need passed yet, but 2 were unmeasured/blocked - this is not a verdict", wantColor: "\x1b[33m"},
		{name: "calibration", useFor: "coding and structured output", calibration: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line := useForLine(t, renderUseFor(t, tc.useFor, tc.calibration, true))
			if strings.Contains(line, "\x1b[36m") {
				t.Fatalf("use for rendered in cyan:\n%s", line)
			}
			if tc.wantColor != "" && !strings.Contains(line, tc.wantColor) {
				t.Fatalf("use for missing %q:\n%s", tc.wantColor, line)
			}
			if tc.calibration && (strings.Contains(line, "\x1b[32m") || strings.Contains(line, "\x1b[33m") || !strings.Contains(line, "calibration evidence only")) {
				t.Fatalf("calibration use for took a qualification color:\n%s", line)
			}
		})
	}
}

func TestUseForPlainTextKeepsTheWords(t *testing.T) {
	t.Setenv("FITR_WIDTH", "80")
	text := renderUseFor(t, "no need passed yet", false, false)
	if strings.Contains(text, "\x1b[") {
		t.Fatalf("plain use for gained color:\n%s", text)
	}
	if !strings.Contains(text, "no need passed yet") {
		t.Fatalf("plain wording changed:\n%s", text)
	}
}

func TestRunCardCollapsesLimits(t *testing.T) {
	t.Setenv("FITR_WIDTH", "80")
	messages := longGapMessages(12)
	card := renderLimits(t, messages, true)
	if gutterCount(card, "limits") != 1 || gutterCount(card, "limit") != 0 {
		t.Fatalf("run card did not collapse limits:\n%s", card)
	}
	if !strings.Contains(card, "fitr view tiny:1b") {
		t.Fatalf("collapsed line did not point at view:\n%s", card)
	}
	if n := strings.Count(card, "\n"); n > 60 {
		t.Fatalf("run card is %d lines, the 80-column card must fit in 60", n)
	}
	for i := range messages {
		if strings.Contains(card, fmt.Sprintf("gap-%02d", i)) {
			t.Fatalf("collapsed card still printed gap-%02d:\n%s", i, card)
		}
	}
}

func TestViewHTMLAndJSONKeepEveryLimit(t *testing.T) {
	t.Setenv("FITR_WIDTH", "80")
	messages := longGapMessages(12)
	full := renderLimits(t, messages, false)
	if gutterCount(full, "limits") != 0 || gutterCount(full, "limit") != 12 {
		t.Fatalf("unmarked result collapsed limits:\n%s", full)
	}
	if strings.Count(full, "\n") <= 60 {
		t.Fatalf("unmarked result was only %d lines; the collapse test has nothing to prove", strings.Count(full, "\n"))
	}
	flat := strings.Join(strings.Fields(full), " ")
	for _, message := range messages {
		if !strings.Contains(flat, message) {
			t.Fatalf("view-style result dropped %q", message)
		}
	}
	report, sc := limitReport(messages)
	var jsonOut strings.Builder
	(&jsonDisplay{out: &jsonOut}).Result(sc, Meta{Analysis: report, Level: "quick", Repeats: 1})
	var htmlOut strings.Builder
	if err := WriteHTML(&htmlOut, Artifact{Model: "tiny:1b", Scorecard: sc, Meta: Meta{Analysis: report}}); err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if !strings.Contains(jsonOut.String(), message) || !strings.Contains(htmlOut.String(), message) {
			t.Fatalf("json or html dropped %q", message)
		}
	}
}

func TestRunCardPrintsOneNext(t *testing.T) {
	t.Setenv("FITR_WIDTH", "80")
	report := &analysis.Report{NextActions: []analysis.Action{{
		Argv: []string{"fitr", "run", "tiny:1b", "-k", "3"}, Reason: "collect enough repeats",
	}}}
	var output strings.Builder
	display := plainDisplay(&output)
	MarkRunCard(display)
	display.Result(score.Scorecard{Model: "tiny:1b", UseFor: "instruction following"}, Meta{Analysis: report, Level: "quick"})
	if gutterCount(output.String(), "next") != 1 || !strings.Contains(output.String(), "fitr run tiny:1b -k 3") {
		t.Fatalf("card next = %d:\n%s", gutterCount(output.String(), "next"), output.String())
	}
}

func renderUseFor(t *testing.T, useFor string, calibration, color bool) string {
	t.Helper()
	var output strings.Builder
	display := plainDisplay(&output)
	if color {
		display.pal = pickPalette(true)
	}
	display.Result(score.Scorecard{Model: "tiny:1b", UseFor: useFor}, Meta{Calibration: calibration, Level: "quick"})
	return output.String()
}

func useForLine(t *testing.T, text string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "use for") {
			return line
		}
	}
	t.Fatalf("no use for line:\n%s", text)
	return ""
}

func renderLimits(t *testing.T, messages []string, card bool) string {
	t.Helper()
	report, sc := limitReport(messages)
	var output strings.Builder
	display := plainDisplay(&output)
	if card {
		MarkRunCard(display)
	}
	display.Result(sc, Meta{Analysis: report, Level: "quick", Repeats: 1, NumCtx: 8192})
	return output.String()
}

func limitReport(messages []string) (*analysis.Report, score.Scorecard) {
	gaps := make([]analysis.EvidenceGap, len(messages))
	for i, message := range messages {
		gaps[i] = analysis.EvidenceGap{Code: analysis.GapCheckRepeatsLow, Message: message}
	}
	report := &analysis.Report{
		Gaps: gaps,
		NextActions: []analysis.Action{{
			Argv: []string{"fitr", "run", "tiny:1b", "-k", "3"}, Reason: "collect enough repeats",
		}},
	}
	sc := score.Scorecard{
		Model: "tiny:1b", UseFor: "instruction following",
		Needs: map[string]score.Verdict{"instruction_precision": {State: score.Pass, Why: "held"}},
	}
	return report, sc
}

func longGapMessages(n int) []string {
	messages := make([]string, n)
	for i := range messages {
		messages[i] = fmt.Sprintf("gap-%02d says this evidence is missing a denominator that view, html, and json must keep verbatim, because a single collapsed count on the run card is not the receipt and the reader still needs the full limit", i)
	}
	return messages
}

func gutterCount(text, label string) int {
	n := 0
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == label && (len(fields) < 2 || fields[1] != "experiment") {
			n++
		}
	}
	return n
}
