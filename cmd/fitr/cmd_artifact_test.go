package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/artifact"
)

type artifactCLIFixture struct {
	sourcePath, mappingPath, localPath, outputPath string
	spec                                           artifact.Spec
}

func artifactFixture(t *testing.T, state string) artifactCLIFixture {
	t.Helper()
	content := []byte("tiny local weights\n")
	directory := sourceTestDirectory(t)
	localPath := filepath.Join(directory, "model.gguf")
	if state != "unavailable" {
		if err := os.WriteFile(localPath, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files := []string{"model.gguf"}
	if state == "incomplete" {
		files = append(files, "extra.gguf")
	}
	metadata := artifactFixtureMetadata(content, state)
	sourcePath, resolution := discoveryCLIResolvedReceipt(t, metadata, files...)
	fixture := artifactCLIFixture{
		sourcePath: sourcePath, mappingPath: filepath.Join(directory, "mapping.json"),
		localPath: localPath, outputPath: filepath.Join(directory, "binding.json"),
		spec: artifact.Spec{Schema: artifact.SpecSchema, ResolutionSHA256: resolution.ResolutionSHA256,
			Files: []artifact.Mapping{{SourcePath: "model.gguf", LocalPath: localPath, ComponentRole: "weights"}}},
	}
	writeArtifactFixtureSpec(t, fixture.mappingPath, fixture.spec)
	return fixture
}

func artifactFixtureMetadata(content []byte, state string) string {
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	if state == "mismatch" {
		digest = strings.Repeat("c", 64)
	}
	lfs := fmt.Sprintf(`,"lfs":{"size":%d,"sha256":%q,"pointerSize":128}`, len(content), digest)
	if state == "locally_hashed" {
		lfs = ""
	}
	return fmt.Sprintf(`{"id":"owner/model","sha":%q,"siblings":[{"rfilename":"model.gguf","size":%d,"blobId":%q%s},{"rfilename":"extra.gguf","size":%d,"blobId":%q%s}]}`,
		strings.Repeat("a", 40), len(content), strings.Repeat("b", 40), lfs, len(content), strings.Repeat("b", 40), lfs)
}

func writeArtifactFixtureSpec(t *testing.T, path string, spec artifact.Spec) {
	t.Helper()
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (fixture artifactCLIFixture) args(mode string) []string {
	return []string{"bind", "--source", fixture.sourcePath, "--mapping", fixture.mappingPath,
		"--out", fixture.outputPath, "--max-bytes", "1024", "--timeout", "1s", "--display", mode}
}

func blockArtifactNetwork(t *testing.T) {
	t.Helper()
	t.Setenv("FITR_BACKEND", "invalid-must-not-be-used")
	previous := http.DefaultTransport
	http.DefaultTransport = sourceTestTransport(func(*http.Request) (*http.Response, error) {
		t.Error("artifact command attempted a network request")
		return nil, errors.New("network prohibited")
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
}

func TestArtifactCLIBindsTinyFilesOffline(t *testing.T) {
	for _, state := range []string{"matched", "locally_hashed", "mismatch", "incomplete", "unavailable"} {
		t.Run(state, func(t *testing.T) {
			fixture := artifactFixture(t, state)
			blockArtifactNetwork(t)
			output, code := captureTopStdout(t, func() int { return cmdArtifact(t.Context(), fixture.args("json")) })
			var binding artifact.Binding
			if err := json.Unmarshal([]byte(output), &binding); err != nil {
				t.Fatalf("decode binding: %v; code=%d output=%s", err, code, output)
			}
			wantCode := exitUnresolved
			if state == "matched" {
				wantCode = exitOK
			}
			wantState := state
			if state == "unavailable" {
				wantState = "incomplete"
			}
			if code != wantCode || binding.State != wantState || binding.RuntimeState != "unbound" || binding.QualityState != "unmeasured" {
				t.Fatalf("code=%d binding=%+v", code, binding)
			}
			if binding.Limits.MaxBytes != 1024 || binding.Limits.TimeoutMillis != 1000 {
				t.Fatalf("explicit CLI limits lost: %+v", binding.Limits)
			}
			if saved, err := artifact.LoadBinding(fixture.outputPath); err != nil || saved.BindingSHA256 != binding.BindingSHA256 {
				t.Fatalf("saved receipt differs: %v", err)
			}
			assertArtifactShowModes(t, fixture.outputPath, wantCode)
		})
	}
}

func assertArtifactShowModes(t *testing.T, path string, wantCode int) {
	t.Helper()
	for _, mode := range []string{"json", "plain", "rich", "none"} {
		output, code := captureTopStdout(t, func() int {
			return cmdArtifact(t.Context(), []string{"show", path, "--display", mode})
		})
		if code != wantCode || (mode == "none" && output != "") {
			t.Fatalf("show %s: code=%d output=%s", mode, code, output)
		}
	}
}

func TestArtifactCLIShowIsHistoricalAndValidatesSilentReceipts(t *testing.T) {
	fixture := artifactFixture(t, "matched")
	if code := cmdArtifact(t.Context(), fixture.args("none")); code != exitOK {
		t.Fatalf("bind exited %d", code)
	}
	for _, path := range []string{fixture.localPath, fixture.sourcePath, fixture.mappingPath} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	moved := filepath.Join(sourceTestDirectory(t), "moved-binding.json")
	if err := os.Rename(fixture.outputPath, moved); err != nil {
		t.Fatal(err)
	}
	fixture.outputPath = moved
	blockArtifactNetwork(t)
	if code := cmdArtifact(t.Context(), []string{"show", fixture.outputPath, "--display", "none"}); code != exitOK {
		t.Fatalf("historical receipt unexpectedly reopened its weights: code=%d", code)
	}
	data, err := os.ReadFile(fixture.outputPath)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(data), `"state": "matched"`, `"state": "locally_hashed"`, 1)
	if tampered == string(data) {
		t.Fatal("fixture tampering did not change the receipt")
	}
	if err := os.WriteFile(fixture.outputPath, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := cmdArtifact(t.Context(), []string{"show", fixture.outputPath, "--display", "none"}); code != exitError {
		t.Fatalf("silent show accepted a tampered receipt: code=%d", code)
	}
	if code := writeArtifactBinding(artifact.Binding{}, "none"); code != exitError {
		t.Fatal("silent writer accepted an invalid receipt")
	}
}

func TestArtifactCLIOutputFailuresAreErrors(t *testing.T) {
	fixture := artifactFixture(t, "matched")
	if code := cmdArtifact(t.Context(), fixture.args("none")); code != exitOK {
		t.Fatalf("bind exited %d", code)
	}
	closed, err := os.CreateTemp(t.TempDir(), "closed-stdout")
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = closed
	t.Cleanup(func() { os.Stdout = previous })
	for _, mode := range []string{"plain", "rich", "auto", "json"} {
		if code := cmdArtifact(t.Context(), []string{"show", fixture.outputPath, "--display", mode}); code != exitError {
			t.Fatalf("closed %s output exited %d", mode, code)
		}
	}
	if code := cmdArtifact(t.Context(), []string{"show", fixture.outputPath, "--display", "none"}); code != exitOK {
		t.Fatalf("silent output wrote to stdout: code=%d", code)
	}
}

func TestArtifactCLIHonorsByteBudget(t *testing.T) {
	fixture := artifactFixture(t, "matched")
	args := append(fixture.args("json"), "--max-bytes", "1")
	output, code := captureTopStdout(t, func() int { return cmdArtifact(t.Context(), args) })
	var binding artifact.Binding
	if err := json.Unmarshal([]byte(output), &binding); err != nil {
		t.Fatal(err)
	}
	if code != exitUnresolved || binding.State != "budget_exceeded" || binding.BytesRead > 1 {
		t.Fatalf("byte budget failed: code=%d state=%s read=%d", code, binding.State, binding.BytesRead)
	}
}
