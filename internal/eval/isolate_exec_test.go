package eval

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/blisspixel/fitr/internal/isolate"
)

func TestIsolationStaysUnavailableWithoutTheRunOptIn(t *testing.T) {
	previous := isolationStatus
	isolationStatus = func() isolate.Status { return isolate.Status{Available: true, Reason: "ready"} }
	t.Cleanup(func() { isolationStatus = previous })
	if executionKind(context.Background()) != "unavailable" {
		t.Fatal("bubblewrap must stay unused unless the run opts in")
	}
	if executionKind(WithIsolatedExecution(context.Background())) != "isolated" {
		t.Fatal("an opted-in run should use the sandbox")
	}
	if executionKind(WithUnsafeExecution(context.Background())) != "unsafe" {
		t.Fatal("the unisolated flag still wins over the sandbox")
	}
}

func TestSandboxLaunchFailureIsNotAModelScore(t *testing.T) {
	launch := VerificationReceipt{Failure: failure(FailureExecutorLaunch, "verify", context.Canceled)}
	if isolatedOutcome(false, launch) != OutcomeError {
		t.Fatalf("launch failure = %s", isolatedOutcome(false, launch))
	}
	ran := VerificationReceipt{
		ProcessStarted: true, ExitCode: 1,
		Failure: failure(FailureExecutorExit, "verify", context.Canceled),
	}
	if isolatedOutcome(false, ran) != OutcomeFail {
		t.Fatalf("failed verifier = %s", isolatedOutcome(false, ran))
	}
}

func TestIsolatedVerifierPassIsAMeasuredOutcome(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("bubblewrap worker is Linux-only")
	}
	if !isolate.Probe().Available {
		t.Skip(isolate.Probe().Reason)
	}
	previous := isolationStatus
	isolationStatus = isolate.Probe
	t.Cleanup(func() { isolationStatus = previous })

	dir := t.TempDir()
	script := "print('PASS_TOKEN')\n"
	if err := os.WriteFile(filepath.Join(dir, "check.py"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	runner, err := resolveTaskRunner(context.Background(), []string{"python3", "check.py"}, map[string]string{"check.py": script})
	if err != nil {
		t.Fatal(err)
	}
	spec := ExecSpec{PassIfStdoutContains: "PASS_TOKEN"}
	result := ExecResult{}
	ctx := WithIsolatedExecution(context.Background())
	gradeExecResult(ctx, dir, runner, spec, &result)
	if result.Outcome != OutcomePass || !result.Pass {
		t.Fatalf("isolated pass = %+v", result)
	}
	if _, measured := MeasuredOutcome(result.Outcome, false); !measured {
		t.Fatal("an isolated pass was not a measured outcome")
	}

	if err := os.WriteFile(filepath.Join(dir, "check.py"), []byte("print('nope')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	failed := ExecResult{}
	gradeExecResult(ctx, dir, runner, spec, &failed)
	if failed.Outcome != OutcomeFail || failed.Pass {
		t.Fatalf("isolated fail = %+v", failed)
	}
}
