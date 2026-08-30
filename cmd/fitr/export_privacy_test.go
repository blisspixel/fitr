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
	secrets := exportPrivacySecrets{
		canary: "CANARY-RAW-MODEL-OUTPUT-9d4f2a", host: "PRIVATE-HOST-7f31",
		path: `C:\Users\private-user\.ollama\models`,
		url:  "https://token@private.example.internal/models/secret-model.gguf",
		unc:  `\\private-fileserver\models\secret.gguf`,
		ssh:  "ssh://private-user@private-host.internal/models",
	}
	r := exportPrivacyResult(secrets)
	assertRawCanaryWasStored(t, r, secrets.canary)
	assertShareableArtifactIsPrivate(t, r, secrets)
	assertRetonrEvidenceIsPrivate(t, r, secrets.canary)
}

type exportPrivacySecrets struct {
	canary, host, path, url, unc, ssh string
}

func exportPrivacyResult(secrets exportPrivacySecrets) *Result {
	r := &Result{
		Model:     secrets.url,
		Profile:   "default",
		Scorecard: score.Scorecard{Model: secrets.url},
		CodeWrite: []eval.ExecResult{{
			Pass: false, Detail: "generated code was not executed",
			Raw: "def solve():\n    # " + secrets.canary + "\n    return 1\n",
		}},
		CodeFix: []eval.ExecResult{{
			Pass: false, Detail: "generated code was not executed",
			Raw: secrets.canary + " second occurrence",
		}},
	}
	r.Device.Host, r.Device.OS, r.Device.GPU = secrets.host, "linux", "TEST GPU"
	r.Device.CPU, r.Device.GPUDriver = secrets.ssh, secrets.unc
	r.Device.Runtime, r.Device.InferenceDevice = secrets.url, secrets.path
	r.Device.Config = map[string]string{
		"OLLAMA_MODELS":         secrets.path,
		"OLLAMA_KV_CACHE_TYPE":  "token=" + secrets.path,
		"OLLAMA_CONTEXT_LENGTH": "8192",
	}
	return r
}

func assertRawCanaryWasStored(t *testing.T, r *Result, canary string) {
	t.Helper()
	// Half one: the raw text really is in the stored evidence.
	stored, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stored), canary) {
		t.Fatal("canary is not in the saved result; the test would prove nothing")
	}
}

func assertShareableArtifactIsPrivate(t *testing.T, r *Result, secrets exportPrivacySecrets) {
	t.Helper()
	// Half two: nothing that leaves the machine carries it.
	a, err := artifactFrom(r)
	if err != nil {
		t.Fatalf("artifact could not be built: %v", err)
	}
	var html strings.Builder
	if err := render.WriteHTML(&html, a); err != nil {
		t.Fatal(err)
	}
	assertNoPrivateValues(t, html.String(), secrets, "exported HTML contains private value %q")
	if encoded, err := json.Marshal(a); err == nil {
		assertNoPrivateValues(t, string(encoded), secrets, "the export artifact structure carries private value %q")
	}
}

func assertNoPrivateValues(t *testing.T, body string, secrets exportPrivacySecrets, message string) {
	t.Helper()
	for _, secret := range []string{secrets.canary, secrets.host, secrets.path, secrets.url, secrets.unc, secrets.ssh,
		"private.example.internal", "private-fileserver", "private-host.internal", "private-user"} {
		if strings.Contains(body, secret) {
			t.Fatalf(message, secret)
		}
	}
}

func assertRetonrEvidenceIsPrivate(t *testing.T, r *Result, canary string) {
	t.Helper()
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
