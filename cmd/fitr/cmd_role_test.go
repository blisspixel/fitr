package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/role"
)

func TestRoleCommandsDefineReviewAndEditWithoutBackend(t *testing.T) {
	t.Setenv("FITR_RESULTS", t.TempDir())
	t.Setenv("FITR_BACKEND", "invalid-must-not-be-used")
	output, code := captureTopStdout(t, func() int {
		return commandHandler("role")(context.Background(), []string{"init", "coding", "--quality", "user_tasks", "--memory-gb", "22", "--display", "json"})
	})
	if code != exitOK || !strings.Contains(output, role.LibrarySchema) {
		t.Fatalf("init code=%d output=%s", code, output)
	}
	output, code = captureTopStdout(t, func() int { return cmdRole(context.Background(), []string{"show", "coding"}) })
	var spec role.Spec
	if code != exitOK || json.Unmarshal([]byte(output), &spec) != nil || spec.Validate() != nil {
		t.Fatalf("show did not export an editable spec: %s", output)
	}
	spec.Description = "updated preferences"
	data, _ := json.Marshal(spec)
	path := filepath.Join(t.TempDir(), "role.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"define", path}, {"list", "--display", "json"}, {"list", "--display", "none"}, nil} {
		_, code := captureTopStdout(t, func() int { return cmdRole(context.Background(), args) })
		if code != exitOK {
			t.Fatalf("%v code=%d", args, code)
		}
	}
	output, code = captureTopStdout(t, func() int { return cmdRole(context.Background(), []string{"review", "coding", "--display", "json"}) })
	if code != exitUnresolved || !strings.Contains(output, `"state": "empty"`) {
		t.Fatalf("empty role became qualified: code=%d %s", code, output)
	}
}

func TestRoleRejectsIncompleteRequirementsAndInvalidActions(t *testing.T) {
	t.Setenv("FITR_RESULTS", t.TempDir())
	for _, args := range [][]string{
		{"invalid"}, {"init", "coding"}, {"init", "coding", "--quality", "user_tasks"},
		{"list", "extra"}, {"show"}, {"review", "coding", "--memory-gb", "22"},
		{"attach", "coding"}, {"detach", "coding"}, {"list", "--display", "unknown"},
		{"init", "../escape", "--quality", "user_tasks", "--memory-gb", "22"},
		{"init", "coding", "--quality", "user_tasks", "--memory-gb", "NaN"},
		{"init", "coding", "--quality", "coding", "--memory-gb", "22"},
		{"init", "agent", "--quality", "unattended_agentic", "--memory-gb", "22"},
	} {
		_, code := captureTopStderr(t, func() int { return cmdRole(context.Background(), args) })
		if code != exitUsage {
			t.Fatalf("%v code=%d", args, code)
		}
	}
	for _, args := range [][]string{{"--help"}, {"init", "--help"}, {"review", "--help"}} {
		_, code := captureTopStderr(t, func() int { return cmdRole(context.Background(), args) })
		if code != exitOK {
			t.Fatalf("help %v code=%d", args, code)
		}
	}
	for _, args := range [][]string{{"review", "missing"}, {"define", "missing.json"}, {"attach", "missing", "missing.json"}, {"detach", "missing", "invalid"}} {
		_, code := captureTopStderr(t, func() int { return cmdRole(context.Background(), args) })
		if code != exitError {
			t.Fatalf("%v code=%d", args, code)
		}
	}
}

func TestRoleShippedExampleDefinesEditablePolicy(t *testing.T) {
	t.Setenv("FITR_RESULTS", t.TempDir())
	_, code := captureTopStdout(t, func() int {
		return cmdRole(context.Background(), []string{"define", "../../examples/roles/coding.json", "--display", "none"})
	})
	if code != exitOK {
		t.Fatalf("shipped role example failed: %d", code)
	}
	output, code := captureTopStdout(t, func() int {
		return cmdRole(context.Background(), []string{"show", "coding"})
	})
	var spec role.Spec
	if code != exitOK || json.Unmarshal([]byte(output), &spec) != nil || len(spec.Preferences) != 3 {
		t.Fatalf("example lost its editable preference policy: %s", output)
	}
}
