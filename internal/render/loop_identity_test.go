package render

import (
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/score"
)

func TestResultPrintsARecordedServingLoop(t *testing.T) {
	const observation = "serving loop count 4; serving recurrent KV cache mode per_loop"
	var buf strings.Builder
	d := &textDisplay{out: &buf, err: &buf, pal: palette{}, g: glyphs{" | ", "-", "+/-", "..."}}
	d.Result(score.Scorecard{Model: "dory", UseFor: "unproven"}, Meta{
		NumCtx: 4096, ServingLoop: observation,
	})
	got := buf.String()
	if !strings.Contains(got, "loop     "+observation) {
		t.Fatalf("recorded loop is hidden:\n%s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "loop") && len([]rune(line)) > DefaultWidth {
			t.Fatalf("loop line is %d cols: %q", len([]rune(line)), line)
		}
	}
	buf.Reset()
	d.Result(score.Scorecard{Model: "llama", UseFor: "unproven"}, Meta{NumCtx: 4096})
	if strings.Contains(buf.String(), "serving loop") {
		t.Fatalf("an unobserved result grew a loop line:\n%s", buf.String())
	}
}

func TestJSONResultOmitsAnUnobservedServingLoop(t *testing.T) {
	var buf strings.Builder
	d := &jsonDisplay{out: &buf}
	d.Result(score.Scorecard{Model: "m", Needs: map[string]score.Verdict{}}, Meta{NumCtx: 4096})
	if strings.Contains(buf.String(), "serving_loop") {
		t.Fatalf("json grew a loop field:\n%s", buf.String())
	}
	buf.Reset()
	d.Result(score.Scorecard{Model: "m", Needs: map[string]score.Verdict{}}, Meta{
		NumCtx: 4096, ServingLoop: "serving loop count 4; serving recurrent KV cache mode last_loop",
	})
	if !strings.Contains(buf.String(), `"serving_loop":"serving loop count 4; serving recurrent KV cache mode last_loop"`) {
		t.Fatalf("json hid the recorded loop:\n%s", buf.String())
	}
	if got := htmlContext(Meta{NumCtx: 4096, ServingLoop: "serving loop count 4; serving recurrent KV cache mode last_loop"}); !strings.Contains(got, "4096") || !strings.Contains(got, "last_loop") {
		t.Fatalf("html context = %q", got)
	}
}
