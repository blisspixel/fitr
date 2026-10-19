package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/reference"
)

func TestReferencePlanSealsAndRunRefusesBeforeTheVerifier(t *testing.T) {
	machineErrors = false
	t.Cleanup(func() { machineErrors = false })
	t.Setenv("FITR_RESULTS", t.TempDir())
	root := t.TempDir()
	images := filepath.Join(root, "images.json")
	planPath := filepath.Join(root, "plan.json")
	writeReferenceImages(t, images)
	stdout, code := captureTopStdout(t, func() int {
		return cmdExperiment(context.Background(), referencePlanArgs(images, planPath))
	})
	if code != exitOK || !strings.Contains(stdout, "sha256:") {
		t.Fatalf("plan exit %d\n%s", code, stdout)
	}
	if _, err := reference.DecodePlan(mustRead(t, planPath)); err != nil {
		t.Fatal(err)
	}
	conditions := filepath.Join(root, "conditions.json")
	if err := os.WriteFile(conditions, []byte(`{"schema":"fitr.external-reference.conditions.v1","gpu_busy":false,"other_model":false,"served_context":8192}`), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(root, "missing-verifier.json")
	stderr, runCode := captureTopStderr(t, func() int {
		return cmdExperiment(context.Background(), []string{
			"reference", "run", "--plan", planPath, "--verifier", missing, "--conditions", conditions, "--display", "plain",
		})
	})
	if runCode != exitError || !strings.Contains(stderr, "confinement probe") {
		t.Fatalf("run exit %d\n%s", runCode, stderr)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("run created or required the verifier file before preflight")
	}
}

func TestBoardShowsReferenceDiscordanceWithoutAFitRow(t *testing.T) {
	machineErrors = false
	t.Cleanup(func() { machineErrors = false })
	root := t.TempDir()
	t.Setenv("FITR_RESULTS", root)
	writeComparableReference(t, root, "a", reference.OutcomeResolved)
	writeComparableReference(t, root, "b", reference.OutcomeUnresolved)
	stdout, code := captureTopStdout(t, func() int {
		return cmdBoard(context.Background(), []string{"--display", "plain"})
	})
	if code != exitOK || !strings.Contains(stdout, "discordant") || !strings.Contains(stdout, "not a fit or performance claim") {
		t.Fatalf("board exit %d\n%s", code, stdout)
	}
}

func referencePlanArgs(images, out string) []string {
	return []string{
		"reference", "plan", "--images", images, "--out", out, "--display", "plain",
		"--harness-name", "mini-swe-agent", "--harness-version", "1.0.0",
		"--harness-config", referenceSHA("config"), "--steps", "20", "--task-wall", "60",
		"--ctx", "8192", "--k", "1", "--seedset", "0123456789abcdef0123456789abcdef",
		"--wall-budget", "3600", "--disk-budget", "1073741824", "--required-disk", "1048576",
		"--prefill", "1000", "--decode", "100", "--prompt-tokens", "100", "--completion-tokens", "50",
		"--artifact", referenceSHA("artifact"), "--artifact-release", "2024-01-01",
		"--quant", "Q4_K_M", "--runtime", "ollama", "--runtime-build", "0.11.4",
		"--device-key", referenceSHA("device"),
	}
}

func writeReferenceImages(t *testing.T, path string) {
	t.Helper()
	images := map[string]string{}
	for _, item := range reference.DefaultSubset() {
		images[item.ID] = referenceSHA(item.ID)
	}
	body, err := json.Marshal(map[string]any{"schema": reference.ImagesSchema, "images": images})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeComparableReference(t *testing.T, root, name, verdict string) {
	t.Helper()
	plan := mustReferencePlan(t, referenceSHA(name))
	rows := make([]reference.Observation, 0, len(plan.Instances))
	for index, instance := range plan.Instances {
		rowVerdict := reference.OutcomeResolved
		if index == 0 {
			rowVerdict = verdict
		}
		rows = append(rows, reference.Observation{
			InstanceID: instance.ID, Repeat: 1, Verdict: rowVerdict, Steps: 1,
			PromptTokens: 10, CompletionTokens: 5, WallMillis: 100,
			VerifierLogSHA256: referenceSHA(instance.ID + name),
		})
	}
	result, err := reference.Analyze(plan, reference.RunConditions{WorkerProbeAvailable: true, ServedContext: plan.ContextCap}, rows)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, ".experiments")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "reference-"+name+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustReferencePlan(t *testing.T, artifact string) reference.Plan {
	t.Helper()
	instances := make([]reference.Instance, 0, 24)
	for _, item := range reference.DefaultSubset() {
		instances = append(instances, reference.Instance{ID: item.ID, Created: item.Created, ImageDigest: referenceSHA(item.ID)})
	}
	plan, err := reference.NewPlan(reference.Plan{
		Dataset: reference.DatasetID, DatasetRevision: reference.DatasetRevision,
		DatasetSplit: reference.DatasetSplit, SubsetID: reference.SubsetID, Instances: instances,
		HarnessName: "mini-swe-agent", HarnessVersion: "1.0.0", HarnessConfigSHA256: referenceSHA("config"),
		StepLimit: 20, TaskWallSeconds: 60, ContextCap: 8192, K: 1,
		SeedSet: "0123456789abcdef0123456789abcdef", WallBudgetSeconds: 3600,
		DiskBudgetBytes: 1 << 30, RequiredDiskBytes: 1 << 20,
		PrefillTokensPerSecond: 1000, DecodeTokensPerSecond: 100,
		PromptTokens: 100, CompletionTokens: 50, ArtifactSHA256: artifact, ArtifactRelease: "2024-01-01",
		Quant: "Q4_K_M", Runtime: "ollama", RuntimeBuild: "0.11.4", DeviceKey: referenceSHA("device"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func referenceSHA(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
