package main

import (
	"context"
	"strings"
	"testing"
)

func TestDiscoveryCapturesAnIdeaAndPlansWithoutBackendAccess(t *testing.T) {
	t.Setenv("FITR_RESULTS", t.TempDir())
	t.Setenv("FITR_BACKEND", "invalid-must-not-be-used")
	commands := [][]string{
		{"add", "https://example.com/model", "--role", "coding", "--model", "model;q4", "--harness", "pi", "--claim", "reported fast"},
		{"list", "--role", "coding", "--display", "json"},
		{"plan", "--role", "coding", "--display", "json"},
		{"plan", "--role", "coding"},
	}
	for _, args := range commands {
		output, code := captureTopStdout(t, func() int { return cmdDiscover(context.Background(), args) })
		if code != exitOK || !strings.Contains(strings.ToLower(output), "unmeasured") {
			t.Fatalf("%v: code %d, output %s", args, code, output)
		}
		if args[0] == "plan" && len(args) == 3 && !strings.Contains(output, "fitr advise 'model;q4'") {
			t.Fatalf("unsafe or missing command: %s", output)
		}
		if args[0] == "plan" && len(args) > 3 && !strings.Contains(output, `"proposals"`) {
			t.Fatalf("JSON lost the plan steps: %s", output)
		}
	}
}

func TestDiscoveryCommandRejectsInvalidArguments(t *testing.T) {
	t.Setenv("FITR_RESULTS", t.TempDir())
	for _, args := range [][]string{
		{"unknown"}, {"add"}, {"add", "source"}, {"list", "extra"},
		{"plan", "--model", "model"}, {"list", "--display", "invalid"},
		{"add", "source", "extra", "--role", "coding"},
	} {
		_, code := captureTopStderr(t, func() int { return cmdDiscover(context.Background(), args) })
		if code != exitUsage {
			t.Fatalf("%v exit = %d", args, code)
		}
	}
	for _, args := range [][]string{nil, {"--help"}, {"add", "--help"}, {"list"}, {"plan", "--display", "none"}} {
		_, code := captureTopStdout(t, func() int { return cmdDiscover(context.Background(), args) })
		if code != exitOK {
			t.Fatalf("%v exit = %d", args, code)
		}
	}
}
