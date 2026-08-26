package render

import (
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/score"
)

func plainDisplay(buf *strings.Builder) *textDisplay {
	return &textDisplay{out: buf, err: buf, pal: palette{},
		g: glyphs{" | ", "-", "+/-", "..."}}
}

// Every need label has to fit its column. The cap is a writing constraint, not
// a rendering one: "leaves tools alone when they don't apply" was 39 columns
// against a 34-column field, and %-34s pads without truncating, so the label
// ran straight into the reason with no separator and the row read as one
// run-on phrase.
func TestEveryNeedLabelFitsItsColumn(t *testing.T) {
	for key, label := range score.NeedLabel {
		if n := len([]rune(label)); n > score.LabelWidth {
			t.Errorf("label for %s is %d cols, the column is %d: %q",
				key, n, score.LabelWidth, label)
		}
	}
}

// The rule is the report's promise about its own width, and it used to be a
// promise the rows did not keep.
func TestScorecardRowsNeverExceedTheRule(t *testing.T) {
	var buf strings.Builder
	d := plainDisplay(&buf)
	sc := score.Scorecard{
		Model:  "a-model-with-a-name-long-enough-to-need-truncating:30b-instruct",
		UseFor: "daily driver (coding + agents), JSON/structured pipelines, no-filter writing/chat, (small footprint)",
		Needs: map[string]score.Verdict{
			"structured_output": {
				State: score.Inconclusive, Measure: "34/40 held [0.55-0.96]", Gate: "need >=0.70",
				Detail: []string{"format 20/20", "keywords 8/10", "line_rules 6/10", "json_object 12/14", "tool_args 9/11"},
				Note: "undecided: 70% sits inside 55-96%, so this run fits both clearing and missing " +
					"the bar. Not a fail; resident model contamination detected: other:7b; this run " +
					"does not count - not a fail, and not fixed by more trials",
			},
			"tool_restraint": {State: score.Fail, Measure: "1/2 clean", Gate: "need 2/2",
				Detail: []string{"left tools alone on an unrelated question", "kept calling a withdrawn tool (4 dead calls)"}},
			"vision": {State: score.NA, Note: "not a deficiency, just not what this model is for"},
		},
	}
	d.Result(sc, Meta{
		GPU: "NVIDIA GeForce RTX 4090", Driver: "32.0.16.1047", Device: "GPU 100%",
		Profile: "desktop-4090", Repeats: 8, Trials: 190, MDEpp: 10, MDEDiffpp: 14,
		ShowsIntervals: true, DecodeMean: 23.16, DecodeSD: 0.44, DecodeN: 8,
		DecodeSeries: []float64{22.7, 23.6, 23.1, 22.9, 23.4, 23.0, 23.3, 22.8},
	})
	for _, line := range strings.Split(buf.String(), "\n") {
		if n := len([]rune(line)); n > DefaultWidth {
			t.Errorf("line is %d cols against a %d-col rule: %q", n, DefaultWidth, line)
		}
	}
}

// A row's fields must land in the same screen column on every row, because
// column position is this report's primary hierarchy: the tag gutter is what
// the eye scans for FAIL.
func TestScorecardColumnsAreStable(t *testing.T) {
	var buf strings.Builder
	d := plainDisplay(&buf)
	d.Result(score.Scorecard{Model: "m", Needs: map[string]score.Verdict{
		"fast_and_decent": {State: score.Pass, Measure: "18.42 tok/s", Gate: "need >=20.0"},
		"tool_restraint":  {State: score.Fail, Measure: "1/2 clean", Gate: "need 2/2"},
		"vision":          {State: score.NA, Measure: "text-only"},
		"coding":          {State: score.Skip, Measure: "not measured"},
	}}, Meta{Repeats: 3})

	var starts []int
	for _, line := range strings.Split(buf.String(), "\n") {
		i := strings.Index(line, "[")
		if i < 0 || !strings.Contains(line, "]") {
			continue
		}
		starts = append(starts, i)
		// The label begins at a fixed offset from the tag, whatever the tag is.
		if got := strings.Index(line, "]") + 1 + colGap; got != contIndent {
			t.Errorf("label column starts at %d, want %d: %q", got, contIndent, line)
		}
	}
	if len(starts) != 4 {
		t.Fatalf("expected 4 verdict rows, got %d:\n%s", len(starts), buf.String())
	}
	for _, s := range starts {
		if s != gutter {
			t.Errorf("tag gutter moved to column %d, want %d", s, gutter)
		}
	}
}

// A label and the text beside it must never merge. This is the exact defect the
// old %-34s produced, and it is invisible in a screenshot of short labels.
func TestLabelAndMeasureNeverCollide(t *testing.T) {
	var buf strings.Builder
	d := plainDisplay(&buf)
	d.verdictRow(&buf, "a label far longer than the column can hold, by a mile",
		score.Verdict{State: score.Fail, Measure: "1/2 clean", Gate: "need 2/2"}, DefaultWidth)
	first := strings.Split(buf.String(), "\n")[0]
	if !strings.Contains(first, "... ") && !strings.Contains(first, "...  ") {
		t.Fatalf("over-long label was not truncated with a separator: %q", first)
	}
	if strings.Contains(first, "milen") || strings.Contains(first, "mile1/2") {
		t.Fatalf("label ran into the measure: %q", first)
	}
	if n := len([]rune(first)); n > DefaultWidth {
		t.Fatalf("row is %d cols: %q", n, first)
	}
}

// Results saved before verdicts carried parts hold only the composed string.
// Rendering them as an empty row would silently drop the explanation.
func TestLegacyVerdictsStillShowTheirExplanation(t *testing.T) {
	var buf strings.Builder
	d := plainDisplay(&buf)
	d.verdictRow(&buf, "reads images",
		score.Verdict{State: score.NA, Why: "text-only model - not a deficiency"}, DefaultWidth)
	if !strings.Contains(buf.String(), "not a deficiency") {
		t.Fatalf("legacy verdict lost its reason:\n%s", buf.String())
	}
}

// Below the floor the tail columns cannot hold a measure, so they move down
// rather than being cut to nothing.
func TestNarrowTerminalDropsTheTailInsteadOfShredding(t *testing.T) {
	var buf strings.Builder
	d := plainDisplay(&buf)
	d.verdictRow(&buf, "valid structured output",
		score.Verdict{State: score.Fail, Measure: "34/40 held [0.55-0.96]", Gate: "need >=0.70"}, MinWidth)
	out := buf.String()
	for _, line := range strings.Split(out, "\n") {
		if n := len([]rune(line)); n > MinWidth {
			t.Errorf("narrow row is %d cols against %d: %q", n, MinWidth, line)
		}
	}
	flat := strings.Join(strings.Fields(out), " ")
	if !strings.Contains(flat, "0.55-0.96") || !strings.Contains(flat, "need >=0.70") {
		t.Fatalf("narrow layout dropped the measure or the gate:\n%s", out)
	}
}
