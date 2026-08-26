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

// Machine-supplied strings have no length of their own, and the demo assets
// only exercise whatever hardware happens to be running the test: this caught
// nothing locally on an "NVIDIA GeForce RTX 4090" and failed in CI on a
// "Microsoft Hyper-V Video", one column past the rule. Every surface that
// prints a device name gets an implausibly long one here instead.
func TestLongMachineStringsStayInsideTheRule(t *testing.T) {
	const longGPU = "Advanced Micro Devices Radeon Instinct MI300X Accelerator (Bare Metal Passthrough)"
	const longCPU = "Intel(R) Xeon(R) Platinum 8592+ Processor with Advanced Matrix Extensions (128 logical)"

	surfaces := map[string]func(w *strings.Builder){
		"board": func(w *strings.Builder) {
			WriteBoard(w, Board{Results: 1, Groups: []BoardGroup{{
				GPU: longGPU, Driver: "32.0.31007.5012.1234.5678", KV: "q8_0",
				NumCtx: 131072, EffectiveCtx: 65536, Note: "measured here",
				Rows: []BoardRow{{Model: "a-model-name-well-past-the-column:70b-instruct-q5",
					ParamSize: "70.6B", Quant: "Q5_K_M", DecodeMean: 9.1, Repeats: 8,
					Serves: []string{"coding", "unattended_agentic", "structured_output", "uncensored", "vision"}}},
			}}}, "plain")
		},
		"inventory": func(w *strings.Builder) {
			WriteInventory(w, Inventory{
				Fitr: "0.9.7", CPU: longCPU, GPU: longGPU, GPUBackend: "rocm",
				MemoryGB: 192, MemorySource: "rocm-smi", FreeGB: 12,
				RuntimeKind: "llama-server", RuntimeURL: "http://192.168.100.200:8080/v1",
				Also:    []string{"ollama http://127.0.0.1:11434"},
				Profile: "a-profile-with-an-unreasonably-descriptive-name", Uncalibrated: true,
				Warnings: []string{"3 saved result files could not be trusted and were skipped, " +
					"because their device fingerprint does not match this machine"},
				Rows: []InventoryRow{{
					Model: "a-model-name-well-past-the-column:70b-instruct-q5", State: "unproven",
					Fit: "low_memory", SizeB: 48 << 30, Next: "fitr advise a-model-name-well-past-the-column:70b-instruct-q5",
					Note: "weights exceed the memory reading; the process is running, so the budget is the suspect number",
				}},
			}, "plain")
		},
		"scorecard": func(w *strings.Builder) {
			d := plainDisplay(w)
			d.Result(score.Scorecard{Model: "a-model-name-well-past-the-column:70b-instruct-q5",
				UseFor: "daily driver (coding + agents), JSON/structured pipelines, no-filter writing/chat"},
				Meta{GPU: longGPU, Driver: "32.0.31007.5012.1234.5678", Device: "GPU 87% / CPU 13%",
					Profile: "a-profile-with-an-unreasonably-descriptive-name", Repeats: 8,
					ParamSize: "70.6B", Quant: "Q5_K_M", Family: "qwen3moe"})
		},
	}
	for name, render := range surfaces {
		var buf strings.Builder
		render(&buf)
		limit := DefaultWidth
		if name == "board" {
			limit = boardWidth
		}
		for _, line := range strings.Split(buf.String(), "\n") {
			if n := len([]rune(line)); n > limit {
				t.Errorf("%s: %d cols against %d: %q", name, n, limit, line)
			}
		}
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
