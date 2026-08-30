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
	"github.com/blisspixel/fitr/internal/score"
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
	const privateHost = "PRIVATE-HOST-7f31"
	const privatePath = `C:\Users\private-user\.ollama\models`
	const privateURL = "https://token@private.example.internal/models/secret-model.gguf"
	const privateUNC = `\\private-fileserver\models\secret.gguf`
	const privateSSH = "ssh://private-user@private-host.internal/models"
	r := &Result{
		Model:     privateURL,
		Profile:   "default",
		Scorecard: score.Scorecard{Model: privateURL},
		CodeWrite: []eval.ExecResult{{
			Pass: false, Detail: "generated code was not executed",
			Raw: "def solve():\n    # " + canary + "\n    return 1\n",
		}},
		CodeFix: []eval.ExecResult{{
			Pass: false, Detail: "generated code was not executed",
			Raw: canary + " second occurrence",
		}},
	}
	r.Device.Host, r.Device.OS, r.Device.GPU = privateHost, "linux", "TEST GPU"
	r.Device.CPU, r.Device.GPUDriver = privateSSH, privateUNC
	r.Device.Runtime, r.Device.InferenceDevice = privateURL, privatePath
	r.Device.Config = map[string]string{
		"OLLAMA_MODELS":         privatePath,
		"OLLAMA_KV_CACHE_TYPE":  "token=" + privatePath,
		"OLLAMA_CONTEXT_LENGTH": "8192",
	}

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
	for _, secret := range []string{canary, privateHost, privatePath, privateURL, privateUNC, privateSSH,
		"private.example.internal", "private-fileserver", "private-host.internal", "private-user"} {
		if strings.Contains(html.String(), secret) {
			t.Fatalf("exported HTML contains private value %q", secret)
		}
	}
	if encoded, err := json.Marshal(a); err == nil {
		for _, secret := range []string{canary, privateHost, privatePath, privateURL, privateUNC, privateSSH,
			"private.example.internal", "private-fileserver", "private-host.internal", "private-user"} {
			if strings.Contains(string(encoded), secret) {
				t.Fatalf("the export artifact structure carries private value %q", secret)
			}
		}
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
