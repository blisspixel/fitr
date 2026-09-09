package render

import (
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/score"
)

func contextTaskReport(prefix *int, status analysis.ObservationStatus,
	tiers []analysis.ContextTaskTier,
) *analysis.Report {
	planned, pass, unavailable := 0, 0, 0
	for _, tier := range tiers {
		planned += tier.Planned
		pass += tier.Pass
		unavailable += tier.Unavailable
	}
	// Only an all-pass phase leaves larger payloads merely untested. A prefix
	// below a failing tier means the larger size was tested and did not hold.
	largestTested := prefix != nil && len(tiers) > 0 && tiers[len(tiers)-1].Outcome == "pass"
	return &analysis.Report{
		ContextTasks: &analysis.ContextTasks{
			Status: status, OperatingWindow: 16384, OutputReserve: 128,
			Outcome: "pass", Complete: unavailable == 0, Tiers: tiers,
			Planned: planned, Pass: pass, Unavailable: unavailable,
			VerifiedPrefixBytes: prefix, AtLeastLargestTested: largestTested,
		},
		NextActions: []analysis.Action{{Argv: []string{"fitr", "board"}, Reason: "compare receipts"}},
	}
}

func renderContextTaskResult(t *testing.T, report *analysis.Report) string {
	t.Helper()
	var output strings.Builder
	display := plainDisplay(&output)
	display.Result(score.Scorecard{Model: "m", Needs: map[string]score.Verdict{}}, Meta{Analysis: report})
	return output.String()
}

func TestResultRendersContextTaskTiersAsCounts(t *testing.T) {
	prefix := 8192
	text := renderContextTaskResult(t, contextTaskReport(&prefix, analysis.StatusAvailable,
		[]analysis.ContextTaskTier{
			{PayloadUTF8Bytes: 2048, Outcome: "pass", Planned: 9, Pass: 9},
			{PayloadUTF8Bytes: 8192, Outcome: "pass", Planned: 9, Pass: 9},
			{PayloadUTF8Bytes: 32768, Outcome: "fail", Planned: 9, Pass: 7, Fail: 2},
		}))
	for _, want := range []string{
		"context tasks", "16384 tokens, reserve 128",
		// Declared sizes are printed exactly as sealed, not rounded to KiB.
		"2048B", "8192B", "32768B",
		"9/9 cells", "7/9 cells",
		"verified prefix 8192B",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("context task section missing %q:\n%s", want, text)
		}
	}
	// A finite task set at one window carries no rate, so no surface may print
	// one and invite the reader to treat nine cells as a percentage.
	if strings.Contains(text, "%") {
		t.Fatalf("context task section printed a rate:\n%s", text)
	}
}

// An all-pass phase has only established the sizes it declared. Saying so is
// the difference between a tested bound and an implied capacity.
func TestResultSaysLargerPayloadsAreUntestedWhenEveryTierPassed(t *testing.T) {
	prefix := 8192
	text := renderContextTaskResult(t, contextTaskReport(&prefix, analysis.StatusAvailable,
		[]analysis.ContextTaskTier{
			{PayloadUTF8Bytes: 2048, Outcome: "pass", Planned: 9, Pass: 9},
			{PayloadUTF8Bytes: 8192, Outcome: "pass", Planned: 9, Pass: 9},
		}))
	// The note wraps at the resolved width, so match across the line break.
	flattened := strings.Join(strings.Fields(text), " ")
	if !strings.Contains(flattened, "larger payloads are untested") {
		t.Fatalf("an all-pass phase does not bound its own claim:\n%s", text)
	}
}

func TestResultExplainsAMissingVerifiedPrefix(t *testing.T) {
	text := renderContextTaskResult(t, contextTaskReport(nil, analysis.StatusAvailable,
		[]analysis.ContextTaskTier{
			{PayloadUTF8Bytes: 2048, Outcome: "unavailable", Planned: 9, Pass: 8, Unavailable: 1},
			{PayloadUTF8Bytes: 8192, Outcome: "unavailable", Planned: 9, Unavailable: 9},
		}))
	if !strings.Contains(text, "no verified prefix") || !strings.Contains(text, "unavailable") {
		t.Fatalf("a suppressed prefix is not explained:\n%s", text)
	}
}

func TestResultMarksDescriptiveOnlyContextTasks(t *testing.T) {
	text := renderContextTaskResult(t, contextTaskReport(nil, analysis.StatusDescriptiveOnly,
		[]analysis.ContextTaskTier{
			{PayloadUTF8Bytes: 2048, Outcome: "pass", Planned: 9, Pass: 9},
			{PayloadUTF8Bytes: 8192, Outcome: "pass", Planned: 9, Pass: 9},
		}))
	if !strings.Contains(text, "descriptive only") {
		t.Fatalf("an unclaimable phase is not marked:\n%s", text)
	}
}

// Every surface composes to the resolved width. The section is new, so it is
// held to the same rule as the ones the demo width guard already covers.
func TestContextTaskSectionFitsTheResolvedWidth(t *testing.T) {
	prefix := 32768
	text := renderContextTaskResult(t, contextTaskReport(&prefix, analysis.StatusDescriptiveOnly,
		[]analysis.ContextTaskTier{
			{PayloadUTF8Bytes: 2048, Outcome: "unavailable", Planned: 9, Pass: 4, Unavailable: 5},
			{PayloadUTF8Bytes: 65536, Outcome: "pass", Planned: 9, Pass: 9},
		}))
	width := Width()
	for _, line := range strings.Split(text, "\n") {
		if len([]rune(line)) > width {
			t.Fatalf("line exceeds the %d-column width by %d:\n%s", width, len([]rune(line))-width, line)
		}
	}
}

func TestResultOmitsTheSectionWithoutAPhase(t *testing.T) {
	text := renderContextTaskResult(t, &analysis.Report{
		NextActions: []analysis.Action{{Argv: []string{"fitr", "board"}, Reason: "compare receipts"}},
	})
	if strings.Contains(text, "context tasks") {
		t.Fatalf("a run with no phase rendered an empty context task section:\n%s", text)
	}
}
