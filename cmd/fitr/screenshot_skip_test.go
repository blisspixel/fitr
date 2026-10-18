package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunScreenshotNamesTheRecordedSandboxLimit(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_PROFILES", dir)
	t.Setenv("FITR_RESULTS", t.TempDir())
	if code := cmdScreenshots(context.Background(), []string{dir}); code != exitOK {
		t.Fatalf("screenshots exited %d", code)
	}
	b, err := os.ReadFile(filepath.Join(dir, "run.svg"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(strings.Fields(strings.Join(svgTextLines(string(b)), " ")), " ")
	if strings.Contains(text, "allow-unsafe-exec") {
		t.Fatal("run screenshot repeated a retired flag sentence")
	}
	const sentence = "scoring generated code needs bubblewrap on Linux; other results are unaffected"
	if strings.Count(text, sentence) < 2 {
		t.Fatalf("run screenshot named the operating-system limit %d times", strings.Count(text, sentence))
	}
}
