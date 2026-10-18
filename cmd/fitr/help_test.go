package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAskedRootHelpStartsWithTheVersion(t *testing.T) {
	t.Setenv("FITR_WIDTH", "80")
	old := os.Args
	os.Args = []string{"fitr", "--help"}
	t.Cleanup(func() { os.Args = old })
	stdout, stderr, code := captureCommandOutput(t, run)
	if code != exitOK || stderr != "" {
		t.Fatalf("exit=%d stderr=%q\n%s", code, stderr, stdout)
	}
	first, _, _ := strings.Cut(stdout, "\n")
	if !strings.HasPrefix(first, "fitr "+version) {
		t.Fatalf("first line = %q", first)
	}
}

func TestHelpUnknownCommandStaysAUsageError(t *testing.T) {
	stdout, stderr, code := captureCommandOutput(t, func() int {
		return cmdHelp(context.Background(), []string{"not-a-command"})
	})
	if code != exitUsage || stdout != "" || !strings.Contains(stderr, "error:") || !strings.Contains(stderr, "hint:") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestBadRunFlagDoesNotDumpHelp(t *testing.T) {
	stdout, stderr, code := captureCommandOutput(t, func() int {
		return cmdRun(context.Background(), []string{"model", "--not-a-flag"})
	})
	if code != exitUsage || stdout != "" || !strings.Contains(stderr, "not-a-flag") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestHelpPagesMatchTheWidthFixtures(t *testing.T) {
	pages := []struct {
		name string
		run  func() int
	}{
		{"main", func() int { writeMainHelp(os.Stdout); return exitOK }},
		{"run", func() int { return cmdRun(context.Background(), []string{"--help"}) }},
	}
	for _, width := range []string{"80", "40"} {
		t.Run(width, func(t *testing.T) {
			t.Setenv("FITR_WIDTH", width)
			for _, page := range pages {
				t.Run(page.name, func(t *testing.T) {
					got, stderr, code := captureCommandOutput(t, page.run)
					if code != exitOK || stderr != "" {
						t.Fatalf("exit=%d stderr=%q", code, stderr)
					}
					path := filepath.Join("testdata", "help-"+page.name+"-"+width+".txt")
					want, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if got != string(want) {
						t.Fatalf("help page drifted from %s\ngot:\n%s", path, got)
					}
				})
			}
		})
	}
}
