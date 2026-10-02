package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/blisspixel/fitr/internal/experiment"
	"github.com/blisspixel/fitr/internal/record"
)

func TestCmdExperimentServingValidation(t *testing.T) {
	ctx := context.Background()

	// 1. No arguments
	if code := cmdExperimentServing(ctx, []string{}); code != exitUsage {
		t.Fatalf("code = %d, want %d for empty args", code, exitUsage)
	}

	// 2. Too many arguments
	if code := cmdExperimentServing(ctx, []string{"m1", "m2"}); code != exitUsage {
		t.Fatalf("code = %d, want %d for multiple args", code, exitUsage)
	}

	// 3. Out of bounds flags
	if code := cmdExperimentServing(ctx, []string{"m", "--concurrency", "0"}); code != exitUsage {
		t.Fatalf("code = %d, want %d for concurrency 0", code, exitUsage)
	}
	if code := cmdExperimentServing(ctx, []string{"m", "--concurrency", "65"}); code != exitUsage {
		t.Fatalf("code = %d, want %d for concurrency 65", code, exitUsage)
	}
	if code := cmdExperimentServing(ctx, []string{"m", "-n", "0"}); code != exitUsage {
		t.Fatalf("code = %d, want %d for requests 0", code, exitUsage)
	}
	if code := cmdExperimentServing(ctx, []string{"m", "--warmup", "-1"}); code != exitUsage {
		t.Fatalf("code = %d, want %d for negative warmup", code, exitUsage)
	}
	if code := cmdExperimentServing(ctx, []string{"m", "--display", "invalid"}); code != exitUsage {
		t.Fatalf("code = %d, want %d for invalid display", code, exitUsage)
	}
}

func createTestServingBundleFile(t *testing.T) string {
	t.Helper()
	model := record.ModelIdentity{
		Requested:        "qwen3:8b",
		Resolved:         "qwen3:8b-q4_k_m",
		Backend:          "ollama",
		Runtime:          "0.5.0",
		Kind:             "digest",
		Value:            "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ContentAddressed: true,
	}
	plan, err := experiment.NewServingPlan(model, "dev-123", 4, 20, 2, 4096)
	if err != nil {
		t.Fatal(err)
	}
	concurrency := experiment.ConcurrencyObservation{
		DeclaredLevel: 4,
		State:         experiment.ConcurrencyDeclared,
		Backend:       "ollama",
		Evidence:      "ollama does not expose slot state",
	}

	var reqs []experiment.ServingRequestObservation
	for i := 1; i <= 22; i++ {
		ttft := int64(40 + i)
		reqs = append(reqs, experiment.ServingRequestObservation{
			Index:                i,
			IsWarmup:             i <= 2,
			QueueDurationMillis:  int64(i * 5),
			ServerDurationMillis: int64(100 + i*10),
			TTFTMillis:           &ttft,
			PromptTokens:         12,
			CompletionTokens:     24,
			AcceptedOutcome:      true,
		})
	}

	bundle, err := experiment.NewServingBundle(plan, concurrency, reqs, 2500)
	if err != nil {
		t.Fatal(err)
	}
	bundleData, err := bundle.JSON()
	if err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(t.TempDir(), "serving-bundle.json")
	if err := os.WriteFile(bundlePath, bundleData, 0o600); err != nil {
		t.Fatal(err)
	}
	return bundlePath
}

func TestCmdExperimentServingReopenBundle(t *testing.T) {
	ctx := context.Background()
	bundlePath := createTestServingBundleFile(t)

	// 1. Reopen with auto mode
	if code := cmdExperimentServing(ctx, []string{bundlePath}); code != exitOK {
		t.Fatalf("code = %d, want %d", code, exitOK)
	}

	// 2. Reopen with json mode
	if code := cmdExperimentServing(ctx, []string{bundlePath, "--display", "json"}); code != exitOK {
		t.Fatalf("code = %d, want %d", code, exitOK)
	}

	// 3. Reopen with none mode
	if code := cmdExperimentServing(ctx, []string{bundlePath, "--display", "none"}); code != exitOK {
		t.Fatalf("code = %d, want %d", code, exitOK)
	}

	// 4. Reopen with live flags should fail
	if code := cmdExperimentServing(ctx, []string{bundlePath, "--concurrency", "8"}); code != exitUsage {
		t.Fatalf("code = %d, want %d when applying live flags to saved bundle", code, exitUsage)
	}
}
