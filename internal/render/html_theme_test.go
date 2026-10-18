package render

import (
	"bytes"
	"strings"
	"testing"
)

func TestHTMLUseForIsNotLavenderAndEmptyDriverIsUnknown(t *testing.T) {
	artifact := sampleArtifact()
	artifact.Device.GPUDriver = ""
	artifact.Device.GPUDriverDate = ""
	artifact.Scorecard.UseFor = "no need passed yet"
	var buf bytes.Buffer
	if err := WriteHTML(&buf, artifact); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if strings.Contains(strings.ToLower(got), "#d2a8ff") || strings.Contains(got, `class="use"`) {
		t.Fatal("scorecard still paints use-for in lavender")
	}
	if !strings.Contains(got, `class="warn"`) || !strings.Contains(got, "no need passed yet") {
		t.Fatal("a gap headline did not use the warning role")
	}
	if !strings.Contains(got, "<th class=\"k\">driver</th><td>unknown</td>") {
		t.Fatalf("empty driver was not unknown:\n%s", got)
	}
	if !strings.Contains(got, `aria-label="fitr"`) || !strings.Contains(got, "<svg ") {
		t.Fatal("the mark is missing its name")
	}
	if strings.Contains(got, "<html lang=\"en\" data-theme") {
		t.Fatal("the page must open dark with no stored theme")
	}
	for _, want := range []string{`data-theme-choice="light"`, "var(--print-bg)", "var(--print-text)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("scorecard missing %s", want)
		}
	}
}
