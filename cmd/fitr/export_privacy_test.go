package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/render"
)

// The export contract is "opt-in, self-contained, fingerprint visible, raw
// model output omitted". Raw generations ARE persisted in a saved result, so
// the omission has to happen when a shareable artifact is built -- otherwise a
// scorecard carries whatever the prompt elicited to wherever the file is sent.
//
// The test first proves the canary is genuinely in the saved evidence. Without
// that half it would pass on data that never reached the pipeline, which is a
// test that cannot fail.
func TestExportedArtifactCarriesNoRawModelOutput(t *testing.T) {
	const canary = "CANARY-RAW-MODEL-OUTPUT-9d4f2a"
	r := &Result{
		Model:   "probe:1b",
		Profile: "default",
		CodeWrite: []eval.ExecResult{{
			Pass: false, Detail: "generated code was not executed",
			Raw: "def solve():\n    # " + canary + "\n    return 1\n",
		}},
		CodeFix: []eval.ExecResult{{
			Pass: false, Detail: "generated code was not executed",
			Raw: canary + " second occurrence",
		}},
	}
	r.Device.Host, r.Device.OS, r.Device.GPU = "testhost", "linux", "TEST GPU"

	// Half one: the raw text really is in the stored evidence.
	stored, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stored), canary) {
		t.Fatal("canary is not in the saved result; the test would prove nothing")
	}

	// Half two: nothing that leaves the machine carries it.
	a, err := artifactFrom(r)
	if err != nil {
		t.Fatalf("artifact could not be built: %v", err)
	}
	var html strings.Builder
	if err := render.WriteHTML(&html, a); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html.String(), canary) {
		t.Fatalf("exported HTML contains raw model output (%q)", canary)
	}
	if encoded, err := json.Marshal(a); err == nil && strings.Contains(string(encoded), canary) {
		t.Fatal("the export artifact structure carries raw model output")
	}

	evPath := filepath.Join(t.TempDir(), record.ArtifactStem(r.Model)+".retonr.json")
	if err := writeRetonrEvidence(r, evPath, ""); err == nil {
		body, readErr := os.ReadFile(evPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(body), canary) {
			t.Fatal("retonr evidence contains raw model output")
		}
	}
}
