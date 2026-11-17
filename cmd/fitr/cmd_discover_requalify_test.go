package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverRequalifyPrintsTheStaleClaim(t *testing.T) {
	t.Setenv("FITR_BACKEND", "invalid-must-not-be-used")
	t.Setenv("FITR_RESULTS", filepath.Join(t.TempDir(), "absent-results"))
	path := writeRequalifyInput(t, requalifyDocument(t, "ollama 0.40.8"))
	output, code := captureTopStdout(t, func() int {
		return cmdDiscover(context.Background(), []string{"requalify", path, "--display", "plain"})
	})
	if code != exitUnresolved {
		t.Fatalf("exit %d, output %s", code, output)
	}
	for _, want := range []string{
		"fitr.requalification.v1",
		"valid artifact",
		"stale runtime changed runtime",
		"next fresh-confirmation runtime",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q in %s", want, output)
		}
	}
	if strings.Contains(output, "please upgrade") || strings.Contains(output, "pass") ||
		strings.Contains(output, "rerun") || strings.Contains(output, "fresh-confirmation artifact") {
		t.Fatalf("report carried a name, a pass, or the unaffected claim: %s", output)
	}
}

func TestDiscoverRequalifyJSONAndSilenceKeepTheExit(t *testing.T) {
	path := writeRequalifyInput(t, requalifyDocument(t, "ollama 0.40.8"))
	jsonOut, jsonCode := captureTopStdout(t, func() int {
		return cmdDiscover(context.Background(), []string{"requalify", path, "--display", "json"})
	})
	if jsonCode != exitUnresolved || !strings.Contains(jsonOut, `"schema": "fitr.requalification.v1"`) ||
		strings.Contains(jsonOut, `"pass"`) {
		t.Fatalf("json exit %d, output %s", jsonCode, jsonOut)
	}
	silent, silentCode := captureTopStdout(t, func() int {
		return cmdDiscover(context.Background(), []string{"requalify", path, "--display", "none"})
	})
	if silentCode != exitUnresolved || silent != "" {
		t.Fatalf("none exit %d, output %q", silentCode, silent)
	}
}

func TestDiscoverRequalifyAcceptsAnUnchangedInstallation(t *testing.T) {
	path := writeRequalifyInput(t, requalifyDocument(t, "ollama 0.40.7"))
	output, code := captureTopStdout(t, func() int {
		return cmdDiscover(context.Background(), []string{"requalify", path, "--display", "plain"})
	})
	if code != exitOK || !strings.Contains(output, "valid runtime") || strings.Contains(output, "next ") {
		t.Fatalf("exit %d, output %s", code, output)
	}
}

func TestDiscoverRequalifyRejectsAMalformedDocument(t *testing.T) {
	body := []byte(`{"schema":"fitr.requalification-input.v1","schema":"fitr.requalification-input.v1","prior":{},"current":{},"claims":[{"id":"fit","depends_on":["artifact_digest"]}]}`)
	path := writeRequalifyInput(t, body)
	_, code := captureTopStderr(t, func() int {
		return cmdDiscover(context.Background(), []string{"requalify", path, "--display", "plain"})
	})
	if code != exitError {
		t.Fatalf("duplicate key exit = %d", code)
	}
	numeric := bytesReplacing(t, requalifyDocument(t, "ollama 0.40.7"), `"sha256:abc"`, `1`)
	path = writeRequalifyInput(t, numeric)
	_, code = captureTopStderr(t, func() int {
		return cmdDiscover(context.Background(), []string{"requalify", path, "--display", "plain"})
	})
	if code != exitError {
		t.Fatalf("numeric identity exit = %d", code)
	}
}

func TestDiscoverRequalifyStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := writeRequalifyInput(t, requalifyDocument(t, "ollama 0.40.7"))
	output, code := captureTopStdout(t, func() int {
		return cmdDiscover(ctx, []string{"requalify", path, "--display", "plain"})
	})
	if code != exitInterrupt || output != "" {
		t.Fatalf("exit %d, output %q", code, output)
	}
}

func TestDiscoverRequalifyUsageDoesNotReadADocument(t *testing.T) {
	_, code := captureTopStderr(t, func() int {
		return cmdDiscover(context.Background(), []string{"requalify"})
	})
	if code != exitUsage {
		t.Fatalf("exit %d", code)
	}
	output, code := captureTopStdout(t, func() int {
		return cmdDiscover(context.Background(), []string{"requalify", "--help"})
	})
	if code != exitOK || !strings.Contains(output, "fitr discover requalify") {
		t.Fatalf("help exit %d, output %s", code, output)
	}
}

func requalifyDocument(t *testing.T, currentRuntime string) []byte {
	t.Helper()
	fact := func(value string) map[string]string {
		return map[string]string{"state": "observed", "value": value}
	}
	stack := func(runtime string) map[string]any {
		return map[string]any{
			"artifact_digest":   fact("sha256:abc"),
			"quant":             fact("Q4_K_M"),
			"runtime":           fact(runtime),
			"compute_backend":   fact("rocm"),
			"driver":            fact("amdgpu"),
			"os":                fact("linux"),
			"kernel":            fact("6.12.0"),
			"hardware":          fact("apu"),
			"effective_context": fact("2048"),
			"concurrency":       fact("1"),
			"residency":         fact("one-model"),
			"harness":           fact("none"),
		}
	}
	current := stack(currentRuntime)
	current["presentation"] = "narrow"
	current["model_name"] = "library-name"
	current["announcement"] = "please upgrade"
	encoded, err := json.Marshal(map[string]any{
		"schema":  "fitr.requalification-input.v1",
		"prior":   stack("ollama 0.40.7"),
		"current": current,
		"claims": []map[string]any{
			{"id": "artifact", "depends_on": []string{"artifact_digest"}},
			{"id": "runtime", "depends_on": []string{"runtime"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func writeRequalifyInput(t *testing.T, body []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "requalify.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func bytesReplacing(t *testing.T, body []byte, old, replacement string) []byte {
	t.Helper()
	if !strings.Contains(string(body), old) {
		t.Fatalf("fixture missing %s", old)
	}
	return []byte(strings.Replace(string(body), old, replacement, 1))
}
