package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/clipperhouse/displaywidth"

	"github.com/blisspixel/fitr/internal/cleanup"
)

func TestCleanupCommandReadOnlyAndDisplayModes(t *testing.T) {
	t.Setenv("FITR_BACKEND", "invalid-must-not-be-used")
	root := t.TempDir()
	file := filepath.Join(root, "download.incomplete")
	if err := os.WriteFile(file, []byte("unfinished"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -10)
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"plain", "auto", "rich", "json", "none"} {
		t.Setenv("NO_COLOR", "1")
		output, code := captureTopStdout(t, func() int {
			return cmdCleanup(t.Context(), []string{"plan", root, "--display", mode, "--min-age-days=7"})
		})
		if code != exitOK || strings.Contains(output, "\x1b") {
			t.Fatalf("%s: code=%d output=%s", mode, code, output)
		}
		switch mode {
		case "json":
			var plan cleanup.Plan
			if err := json.Unmarshal([]byte(output), &plan); err != nil {
				t.Fatal(err)
			}
			if plan.Schema != cleanup.Schema || plan.Root != root || plan.ReviewCandidateCount != 1 {
				t.Fatalf("bad plan: %+v", plan)
			}
		case "none":
			if output != "" {
				t.Fatal("none mode emitted output")
			}
		default:
			if !strings.Contains(output, "REVIEW") || !strings.Contains(output, "download.incomplete") || strings.Contains(output, root) {
				t.Fatalf("missing review or leaked root: %s", output)
			}
		}
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "unfinished" {
		t.Fatal("scan changed source file")
	}
}

func TestCleanupCommandArgumentsAndIncompleteStatus(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{{"delete"}, {"plan"}, {"plan", root, "extra"}, {"plan", root, "--display=invalid"}, {"plan", root, "--min-age-days=-1"}, {"plan", root, "--min-age-days=36501"}, {"plan", root, "--unknown"}} {
		_, code := captureTopStderr(t, func() int { return cmdCleanup(t.Context(), args) })
		if code != exitUsage {
			t.Fatalf("%v: got %d", args, code)
		}
	}
	for _, args := range [][]string{nil, {"--help"}, {"-h"}, {"plan", "--help"}} {
		_, code := captureTopStderr(t, func() int { return cmdCleanup(t.Context(), args) })
		if code != exitOK {
			t.Fatalf("help %v: got %d", args, code)
		}
	}
	_, code := captureTopStderr(t, func() int { return cmdCleanup(t.Context(), []string{"plan", filepath.Join(root, "missing")}) })
	if code != exitError {
		t.Fatal("missing root was successful")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	output, code := captureTopStdout(t, func() int { return cmdCleanup(ctx, []string{"plan", root, "--display=json"}) })
	if code != exitError || !strings.Contains(output, `"stop_reason":"cancelled"`) {
		t.Fatalf("incomplete plan not surfaced: code=%d output=%s", code, output)
	}
}

func TestCleanupRendererSanitizesAndExplainsBounds(t *testing.T) {
	t.Setenv("FITR_WIDTH", "60")
	t.Setenv("NO_COLOR", "")
	hostile := "model\x1b[2J\r\nINJECTED\u202e\t" + strings.Repeat("wide界", 25)
	plan := cleanup.Plan{Files: 3, Root: "private-absolute-path", StopReason: "entry_limit",
		TopLevel: []cleanup.Summary{{Name: hostile, ApparentBytes: 1 << 30}}, TopLevelOmitted: 2,
		Categories:   []cleanup.Summary{{Name: "possible_model_asset", ApparentBytes: 1 << 30}},
		LargestFiles: []cleanup.Entry{{Path: hostile}}, ReviewCandidates: []cleanup.Entry{{Path: hostile}},
		ReviewCandidateCount: 3, Issues: []cleanup.Issue{{Path: hostile, Code: "stat_failed"}}, IssueCount: 2,
		Notes: []string{"Read-only plan. Hard links are unresolved."},
	}
	for _, mode := range []string{"plain", "rich"} {
		var output bytes.Buffer
		writeCleanupPlan(&output, plan, mode)
		text := output.String()
		if strings.Contains(text, "\x1b[2J") || strings.Contains(text, "\u202e") || strings.Contains(text, plan.Root) {
			t.Fatalf("unsafe output: %q", text)
		}
		for _, want := range []string{"INCOMPLETE", "2 groups omitted", "2 review candidates omitted", "1 scan issues omitted", "entry_limit", "Read-only"} {
			if !strings.Contains(text, want) {
				t.Fatalf("lost %q: %s", want, text)
			}
		}
		if mode == "rich" && !strings.Contains(text, "\x1b[36m") {
			t.Fatal("rich mode has no color")
		}
		if mode == "plain" {
			for _, line := range strings.Split(text, "\n") {
				if displaywidth.String(line) > 60 {
					t.Fatalf("line overflow: %q", line)
				}
			}
		}
	}
}
