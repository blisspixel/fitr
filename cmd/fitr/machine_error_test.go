package main

import (
	"encoding/json"
	"flag"
	"strings"
	"testing"
)

func withDisplay(t *testing.T, mode string) {
	t.Helper()
	previous := machineErrors
	t.Cleanup(func() { machineErrors = previous })
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("display", mode, "")
	noteDisplayMode(fs)
}

// A caller that asked for a machine-readable surface should not have to match
// prose with a regular expression to learn why there is no document.
func TestJSONDisplayReportsFailuresAsOneDocument(t *testing.T) {
	withDisplay(t, "json")
	_, stderr, _ := captureCommandOutput(t, func() int {
		errPrint("model is not installed", "14 model(s) available", "pull it first")
		return exitError
	})
	var failure machineError
	if err := json.Unmarshal([]byte(strings.TrimSpace(stderr)), &failure); err != nil {
		t.Fatalf("stderr was not one JSON document: %v\n%s", err, stderr)
	}
	if failure.Schema != ErrorSchema {
		t.Fatalf("schema = %q, want %q", failure.Schema, ErrorSchema)
	}
	if failure.Error != "model is not installed" || failure.Note == "" || failure.Hint == "" {
		t.Fatalf("failure lost its parts: %+v", failure)
	}
}

// Every other mode keeps the prose a person reads. The exit code remains the
// class in both.
func TestOtherDisplayModesKeepProse(t *testing.T) {
	for _, mode := range []string{"plain", "auto", "rich", "none"} {
		t.Run(mode, func(t *testing.T) {
			withDisplay(t, mode)
			_, stderr, _ := captureCommandOutput(t, func() int {
				errPrint("model is not installed", "a note", "a hint")
				return exitError
			})
			if !strings.HasPrefix(strings.TrimSpace(stderr), "error:") {
				t.Fatalf("%s mode did not print prose:\n%s", mode, stderr)
			}
			if strings.Contains(stderr, ErrorSchema) {
				t.Fatalf("%s mode emitted a machine document:\n%s", mode, stderr)
			}
		})
	}
}

// An empty note or hint is absent rather than an empty string, so a reader
// cannot mistake "nothing to add" for "added nothing".
func TestAbsentPartsAreOmitted(t *testing.T) {
	withDisplay(t, "json")
	_, stderr, _ := captureCommandOutput(t, func() int {
		errPrint("bad invocation", "", "")
		return exitUsage
	})
	if strings.Contains(stderr, `"note"`) || strings.Contains(stderr, `"hint"`) {
		t.Fatalf("empty parts were encoded: %s", stderr)
	}
}
