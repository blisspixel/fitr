package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModelSetPlanAndClassifyRefuseCoResidency(t *testing.T) {
	machineErrors = false
	t.Cleanup(func() { machineErrors = false })
	root := t.TempDir()
	members := filepath.Join(root, "members.json")
	observation := filepath.Join(root, "observation.json")
	planPath := filepath.Join(root, "plan.json")
	bundlePath := filepath.Join(root, "bundle.json")
	writeModelSetInputs(t, members, observation)
	stdout, code := captureTopStdout(t, func() int {
		return cmdExperiment(context.Background(), []string{
			"modelset", "plan", "--members", members, "--runtime", "ollama",
			"--device-key", "fp2:" + strings.Repeat("ab", 32), "--out", planPath, "--display", "plain",
		})
	})
	if code != exitOK || !strings.Contains(stdout, "sha256:") || !strings.Contains(stdout, "does not contact a runtime") {
		t.Fatalf("plan exit %d\n%s", code, stdout)
	}
	stdout, code = captureTopStdout(t, func() int {
		return cmdExperiment(context.Background(), []string{
			"modelset", "classify", "--plan", planPath, "--observation", observation,
			"--out", bundlePath, "--display", "plain",
		})
	})
	if code != exitOK || !strings.Contains(stdout, "not_one_observation") || !strings.Contains(stdout, "co-resident") ||
		!strings.Contains(stdout, "no") || !strings.Contains(stdout, "does not enter the Board") {
		t.Fatalf("classify exit %d\n%s", code, stdout)
	}
	body, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"co_resident": false`) || strings.Contains(string(body), "333333") {
		t.Fatalf("bundle claimed a sum or a residency:\n%s", body)
	}
	forged := strings.Replace(string(body), `"co_resident": false`, `"co_resident": true`, 1)
	if err := os.WriteFile(bundlePath, []byte(forged), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr, reopen := captureTopStderr(t, func() int {
		return cmdExperiment(context.Background(), []string{"modelset", bundlePath, "--display", "plain"})
	})
	if reopen != exitError || !strings.Contains(stderr, "does not match") {
		t.Fatalf("reopen exit %d\n%s", reopen, stderr)
	}
}

func writeModelSetInputs(t *testing.T, members, observation string) {
	t.Helper()
	memberBody := `[
	  {"artifact_digest":"sha256:` + strings.Repeat("a", 64) + `","role":"coding","requested_context":8192,"placement_intention":"accelerator"},
	  {"artifact_digest":"sha256:` + strings.Repeat("b", 64) + `","role":"embed","requested_context":8192,"placement_intention":"accelerator"}
	]`
	observationBody := `{
	  "schema":"fitr.experiment.modelset.observation.v1",
	  "kind":"ollama_ps",
	  "bodies":1,
	  "rows":[
	    {"name":"left","size_bytes":111111,"state":"resident"},
	    {"name":"right","size_bytes":222222,"state":"resident"}
	  ]
	}`
	if err := os.WriteFile(members, []byte(memberBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(observation, []byte(observationBody), 0o600); err != nil {
		t.Fatal(err)
	}
}
