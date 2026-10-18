package render

import (
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/score"
)

func TestUnmeasuredQualityIsNamedBeforeSpeed(t *testing.T) {
	var buf strings.Builder
	plainDisplay(&buf).Result(score.Scorecard{Model: "m", Needs: map[string]score.Verdict{
		"fast_and_decent":   {State: score.Pass, Measure: "17.66 tok/s"},
		"coding":            {State: score.Skip, Measure: "not measured"},
		"structured_output": {State: score.Skip},
		"low_footprint":     {State: score.Pass, Measure: "4.0 GiB"},
	}}, Meta{DecodeN: 1, DecodeMean: 17.66})
	text := buf.String()
	quality := strings.Index(text, score.QualityUnmeasuredLead)
	speed := strings.Index(text, "interactive response")
	perf := strings.Index(text, "performance")
	if quality < 0 || speed < 0 || quality > speed || (perf >= 0 && perf < speed) {
		t.Fatalf("speed still leads an unmeasured card:\n%s", text)
	}
	if strings.Contains(text, "fitr run") {
		t.Fatalf("the card invented a command:\n%s", text)
	}
	var encoded strings.Builder
	(&jsonDisplay{out: &encoded}).Result(score.Scorecard{Model: "m", Needs: map[string]score.Verdict{
		"coding":          {State: score.Skip},
		"fast_and_decent": {State: score.Pass},
	}}, Meta{})
	if !strings.Contains(encoded.String(), `"quality_measured":false`) {
		t.Fatalf("json treated speed as quality: %s", encoded.String())
	}
}

func TestMeasuredQualityFailureIsNotCalledUnmeasured(t *testing.T) {
	var buf strings.Builder
	plainDisplay(&buf).Result(score.Scorecard{Model: "m", Needs: map[string]score.Verdict{
		"coding":          {State: score.Fail, Measure: "0/4 correct"},
		"fast_and_decent": {State: score.Pass, Measure: "17.66 tok/s"},
	}}, Meta{})
	if strings.Contains(buf.String(), score.QualityUnmeasuredLead) {
		t.Fatalf("a failed gate became unmeasured:\n%s", buf.String())
	}
	var encoded strings.Builder
	(&jsonDisplay{out: &encoded}).Result(score.Scorecard{Model: "m", Needs: map[string]score.Verdict{
		"coding": {State: score.Fail},
	}}, Meta{})
	if !strings.Contains(encoded.String(), `"quality_measured":true`) {
		t.Fatalf("json dropped a measured failure: %s", encoded.String())
	}
}

func TestHTMLNamesUnmeasuredQualityBeforeTheSpeedTable(t *testing.T) {
	var buf strings.Builder
	err := WriteHTML(&buf, Artifact{Model: "m", Scorecard: score.Scorecard{Needs: map[string]score.Verdict{
		"fast_and_decent": {State: score.Pass, Why: "17.66 tok/s"},
		"coding":          {State: score.Skip, Why: "not measured"},
	}}, Meta: Meta{DecodeN: 1, DecodeMean: 17.66}})
	if err != nil {
		t.Fatal(err)
	}
	text := buf.String()
	quality := strings.Index(text, score.QualityUnmeasuredLead)
	perf := strings.Index(text, "Performance")
	if quality < 0 || perf < 0 || quality > perf {
		t.Fatalf("html hid the unmeasured quality gap:\n%s", text)
	}
}
