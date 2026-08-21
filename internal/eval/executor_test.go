package eval

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/ollama"
)

func TestMain(m *testing.M) {
	if os.Getenv("FITR_EXECUTOR_HELPER") == "1" {
		if len(os.Args) > 1 && os.Args[1] == "--version" {
			version := os.Getenv("FITR_EXECUTOR_HELPER_VERSION")
			if version == "" {
				version = "Python 3.12.7"
			}
			_, _ = fmt.Fprintln(os.Stdout, version)
			os.Exit(0)
		}
		if len(os.Args) > 1 && os.Args[1] == "-I" {
			path := os.Getenv("FITR_EXECUTOR_HELPER_REPORTED_PATH")
			if path == "" {
				path, _ = os.Executable()
			}
			_, _ = fmt.Fprintln(os.Stdout, path)
			os.Exit(0)
		}
		path, _ := os.Executable()
		_, _ = fmt.Fprintln(os.Stdout, path)
		_, _ = fmt.Fprintln(os.Stdout, "PASS_TOKEN")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func installHelperPython(t *testing.T, dir string) string {
	t.Helper()
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := "python"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	target := filepath.Join(dir, name)
	in, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		_ = in.Close()
		t.Fatal(err)
	}
	_, copyErr := io.Copy(out, in)
	closeOutErr, closeInErr := out.Close(), in.Close()
	if copyErr != nil || closeOutErr != nil || closeInErr != nil {
		t.Fatalf("copy helper: copy=%v close output=%v close input=%v", copyErr, closeOutErr, closeInErr)
	}
	return target
}

func TestExactFinalLineRejectsSubstringAndDuplicateMarkers(t *testing.T) {
	for name, tc := range map[string]struct {
		output string
		want   bool
	}{
		"exact final":      {"diagnostic\nPASS_TOKEN\n", true},
		"substring":        {"diagnostic PASS_TOKEN\n", false},
		"not final":        {"PASS_TOKEN\nmore output\n", false},
		"duplicate":        {"PASS_TOKEN\nPASS_TOKEN\n", false},
		"carriage newline": {"diagnostic\r\nPASS_TOKEN\r\n", true},
		"missing":          {"diagnostic\n", false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := exactFinalLine(tc.output, "PASS_TOKEN"); got != tc.want {
				t.Fatalf("exactFinalLine(%q) = %v, want %v", tc.output, got, tc.want)
			}
		})
	}
}

func TestFailurePreservesCancellation(t *testing.T) {
	err := failure(FailureTransport, "chat", context.Canceled)
	if err.Kind != FailureCancelled {
		t.Fatalf("kind = %q, want %q", err.Kind, FailureCancelled)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatal("typed failure must preserve context cancellation")
	}
}

func TestTaskRunnerPreflightRejectsIndirectAndMissingScripts(t *testing.T) {
	if err := preflightTaskRunner([]string{"python", "-c", "print('x')"}, nil); err == nil {
		t.Fatal("python -c bypassed the task-local script contract")
	}
	if err := preflightTaskRunner([]string{"python", "missing.py"}, map[string]string{}); err == nil {
		t.Fatal("missing verifier script passed preflight")
	}
}

func TestResolvedRunnerIsPinnedAndReceiptedAcrossPATHChange(t *testing.T) {
	t.Setenv("FITR_EXECUTOR_HELPER", "1")
	firstDir, secondDir := t.TempDir(), t.TempDir()
	first := installHelperPython(t, firstDir)
	second := installHelperPython(t, secondDir)
	t.Setenv("PATH", firstDir)
	runnerName := filepath.Base(first)
	runner, err := resolveTaskRunner(context.Background(), []string{runnerName, "test.py"}, map[string]string{"test.py": ""})
	if err != nil {
		t.Fatal(err)
	}
	wantPath, err := filepath.EvalSymlinks(first)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(wantPath) {
		wantPath, err = filepath.Abs(wantPath)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", secondDir)
	receipt := verifyIn(context.Background(), t.TempDir(), runner, "PASS_TOKEN")
	if receipt.Failure != nil || !receipt.SuccessfulExit || !receipt.ExactFinalMarker {
		t.Fatalf("verification receipt = %+v", receipt)
	}
	if receipt.InterpreterPath != filepath.Clean(wantPath) || receipt.InterpreterVer != "Python 3.12.7" ||
		!strings.HasPrefix(receipt.InterpreterHash, "sha256:") {
		t.Fatalf("interpreter receipt = %+v", receipt)
	}
	if !strings.Contains(receipt.Output, filepath.Clean(wantPath)) || strings.Contains(receipt.Output, second) {
		t.Fatalf("PATH replacement changed the launched interpreter: %q", receipt.Output)
	}
}

func TestPythonLauncherResolvesAndRunsReportedExecutable(t *testing.T) {
	t.Setenv("FITR_EXECUTOR_HELPER", "1")
	launcher := installHelperPython(t, t.TempDir())
	interpreter := installHelperPython(t, t.TempDir())
	t.Setenv("FITR_EXECUTOR_HELPER_REPORTED_PATH", interpreter)
	runner, err := resolveTaskRunner(context.Background(), []string{launcher, "test.py"}, map[string]string{"test.py": ""})
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(interpreter)
	if err != nil {
		t.Fatal(err)
	}
	receipt := verifyIn(context.Background(), t.TempDir(), runner, "PASS_TOKEN")
	if receipt.Failure != nil || receipt.InterpreterPath != filepath.Clean(want) ||
		!strings.Contains(receipt.Output, filepath.Clean(want)) || strings.Contains(receipt.Output, launcher) {
		t.Fatalf("launcher was mistaken for the reported interpreter: %+v", receipt)
	}
}

func TestVerifierRejectsInterpreterChangedAfterPreflight(t *testing.T) {
	t.Setenv("FITR_EXECUTOR_HELPER", "1")
	dir := t.TempDir()
	path := installHelperPython(t, dir)
	runner, err := resolveTaskRunner(context.Background(), []string{path, "test.py"}, map[string]string{"test.py": ""})
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("changed")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	receipt := verifyIn(context.Background(), t.TempDir(), runner, "PASS_TOKEN")
	if receipt.ProcessStarted || receipt.Failure == nil || receipt.Failure.Kind != FailureExecutorPreflight {
		t.Fatalf("changed interpreter was launched: %+v", receipt)
	}
}

func TestPythonVersionPreflightRejectsInjectionAndUnboundedOutput(t *testing.T) {
	for _, raw := range []string{
		"Python 3.12.7\nmalicious", "Python 3.12.7; rm -rf data", "Python three",
	} {
		if _, err := normalizePythonVersion([]byte(raw)); err == nil {
			t.Fatalf("malformed version %q was accepted", raw)
		}
	}
	if got, err := normalizePythonVersion([]byte("  Python\t3.13.0rc2\r\n")); err != nil || got != "Python 3.13.0rc2" {
		t.Fatalf("normalized version = %q, %v", got, err)
	}

	t.Setenv("FITR_EXECUTOR_HELPER", "1")
	t.Setenv("FITR_EXECUTOR_HELPER_VERSION", strings.Repeat("x", interpreterVersionMax+1))
	path := installHelperPython(t, t.TempDir())
	_, err := resolveTaskRunner(context.Background(), []string{path, "test.py"}, map[string]string{"test.py": ""})
	if err == nil || !strings.Contains(err.Error(), "output exceeds") {
		t.Fatalf("oversized version output error = %v", err)
	}
}

func TestPreflightVersionFailureOccursBeforeModelOutput(t *testing.T) {
	t.Setenv("FITR_EXECUTOR_HELPER", "1")
	t.Setenv("FITR_EXECUTOR_HELPER_VERSION", "Python 3.12.7\nforged")
	path := installHelperPython(t, t.TempDir())
	spec, err := LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	spec.CodeWrite.Runner[0] = path
	backend := &fakeBackend{}
	result, err := RunExec(WithUnsafeExecution(context.Background()), backend, "model", spec.CodeWrite, t.TempDir())
	var typed *Failure
	if !errors.As(err, &typed) || typed.Kind != FailureExecutorPreflight {
		t.Fatalf("preflight error = %#v", err)
	}
	if backend.generateCalls != 0 || result.Outcome != OutcomeError {
		t.Fatalf("malicious version reached model generation: calls=%d result=%+v", backend.generateCalls, result)
	}
}

func TestToolLoopPersistsEveryVerifierInterpreterReceipt(t *testing.T) {
	t.Setenv("FITR_EXECUTOR_HELPER", "1")
	path := installHelperPython(t, t.TempDir())
	specs, err := LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	spec := specs.Tools
	spec.Verify.Runner[0] = path
	runner, err := resolveTaskRunner(context.Background(), spec.Verify.Runner, spec.Files)
	if err != nil {
		t.Fatal(err)
	}
	executor := runner.receipt()
	backend := &fakeBackend{turns: []fakeTurn{
		callTurn(callTo("run_tests", `{}`)),
		callTurn(ollama.Message{Role: "assistant", Content: "DONE"}),
	}}
	result, err := RunToolLoop(WithUnsafeExecutor(context.Background(), executor), backend, "model", spec, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != OutcomeInconclusive || result.Verifier == nil || len(result.VerifierObservations) != 2 {
		t.Fatalf("tool verifier evidence = %+v", result)
	}
	for i := range result.VerifierObservations {
		if err := result.VerifierObservations[i].ValidateExecutor(executor); err != nil {
			t.Fatalf("verifier observation %d: %v", i, err)
		}
	}
}

func TestMeasuredOutcomeExcludesUnavailableEvidence(t *testing.T) {
	for _, outcome := range []Outcome{OutcomeInconclusive, OutcomeError, OutcomeSkipped} {
		if _, measured := MeasuredOutcome(outcome, true); measured {
			t.Fatalf("%q entered a binary denominator", outcome)
		}
	}
	if pass, measured := MeasuredOutcome(OutcomePass, false); !measured || !pass {
		t.Fatal("explicit pass must be measured")
	}
	if pass, measured := MeasuredOutcome(OutcomeFail, true); !measured || pass {
		t.Fatal("explicit fail must be measured")
	}
}

func TestOutcomeCountsRequireThePlannedDenominator(t *testing.T) {
	c := CountOutcomes(4, OutcomePass, OutcomeFail, OutcomeInconclusive, OutcomeSkipped)
	if !c.Complete() || c.Scorable != 2 || c.Attempted != 3 {
		t.Fatalf("complete counts = %+v", c)
	}
	partial := CountOutcomes(4, OutcomePass, OutcomeFail)
	if partial.Complete() {
		t.Fatalf("partial observations completed the planned denominator: %+v", partial)
	}
}

func TestVerifierReceiptFailsClosedAcrossInjectedProcessFailures(t *testing.T) {
	marker := "PASS_TOKEN"
	spoofed := verifierReceiptFromProcess(
		[]byte("diagnostic\nPASS_TOKEN\n"), true, 1, errors.New("exit status 1"), nil, marker,
	)
	if spoofed.SuccessfulExit || spoofed.ExactFinalMarker || spoofed.Failure == nil ||
		spoofed.Failure.Kind != FailureExecutorExit {
		t.Fatalf("failed process forged a receipt: %+v", spoofed)
	}

	launch := verifierReceiptFromProcess(nil, false, -1, errors.New("access denied"), nil, marker)
	if launch.Failure == nil || launch.Failure.Kind != FailureExecutorLaunch {
		t.Fatalf("launch failure = %+v", launch)
	}

	timedOut := verifierReceiptFromProcess(nil, true, -1, errors.New("killed"), context.DeadlineExceeded, marker)
	if timedOut.Failure == nil || timedOut.Failure.Kind != FailureExecutorTimeout {
		t.Fatalf("timeout = %+v", timedOut)
	}

	valid := verifierReceiptFromProcess([]byte("diagnostic\nPASS_TOKEN\n"), true, 0, nil, nil, marker)
	if !valid.SuccessfulExit || !valid.ExactFinalMarker || valid.Failure != nil ||
		!strings.HasPrefix(valid.OutputSHA256, "sha256:") {
		t.Fatalf("valid parent receipt = %+v", valid)
	}

	protocol := verifierReceiptFromProcess([]byte("PASS_TOKEN\nextra\n"), true, 0, nil, nil, marker)
	if protocol.Failure == nil || protocol.Failure.Kind != FailureVerifierProtocol {
		t.Fatalf("protocol failure = %+v", protocol)
	}
}
