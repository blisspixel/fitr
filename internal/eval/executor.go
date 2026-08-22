package eval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// Windows race builds and antivirus scanning can spend several seconds in
	// process startup before the interpreter writes a single line. Keep the
	// preflight bounded without making valid local interpreters fail randomly.
	interpreterVersionTimeout = 15 * time.Second
	interpreterVersionMax     = 4 << 10
	interpreterHashMax        = 512 << 20
)

var (
	pythonVersionPattern = regexp.MustCompile(`^Python[ \t]+([0-9]+\.[0-9]+(?:\.[0-9]+)?(?:[A-Za-z0-9.+-]*))$`)
	sha256Pattern        = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type unsafeExecutionKey struct{}

type unsafeExecutionConfig struct {
	executor *ExecutorReceipt
}

// WithUnsafeExecution opts into executable task diagnostics. Generated code is
// not isolated in Release A, so these observations are always inconclusive and
// never enter model scoring.
func WithUnsafeExecution(ctx context.Context) context.Context {
	return context.WithValue(ctx, unsafeExecutionKey{}, unsafeExecutionConfig{})
}

// WithUnsafeExecutor opts in to unisolated diagnostics and pins every launch
// to the interpreter identified before the run manifest is sealed.
func WithUnsafeExecutor(ctx context.Context, executor ExecutorReceipt) context.Context {
	copy := executor
	return context.WithValue(ctx, unsafeExecutionKey{}, unsafeExecutionConfig{executor: &copy})
}

func unsafeExecutionEnabled(ctx context.Context) bool {
	_, enabled := ctx.Value(unsafeExecutionKey{}).(unsafeExecutionConfig)
	return enabled
}

func pinnedUnsafeExecutor(ctx context.Context) (*ExecutorReceipt, bool) {
	config, enabled := ctx.Value(unsafeExecutionKey{}).(unsafeExecutionConfig)
	return config.executor, enabled && config.executor != nil
}

// ToolLoopRequiresExecution reports whether a tool-loop specification can
// launch generated code, either during the loop or in its final verifier.
func ToolLoopRequiresExecution(spec ToolLoopSpec) bool {
	if len(spec.Verify.Runner) > 0 {
		return true
	}
	for _, tool := range spec.Tools {
		if tool.Function.Name == "run_tests" {
			return true
		}
	}
	return false
}

// PreflightUnsafeExecutors resolves every executable before any model output is
// requested. A missing or indirect runner cannot turn into a model result.
func PreflightUnsafeExecutors(spec *Spec) error {
	_, err := PreflightUnsafeExecutor(spec)
	return err
}

// ExecutorReceipt is the exact unisolated Python interpreter identity sealed
// into an unsafe run manifest. It is reproducibility evidence, not a sandbox
// or a trust assertion about the interpreter.
type ExecutorReceipt struct {
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

func (r ExecutorReceipt) Validate() error {
	if r.Kind != "python" {
		return fmt.Errorf("unsupported executor kind %q", r.Kind)
	}
	if !filepath.IsAbs(r.Path) || filepath.Clean(r.Path) != r.Path {
		return errors.New("executor path is not clean and absolute")
	}
	version, err := normalizePythonVersion([]byte(r.Version))
	if err != nil || version != r.Version {
		return errors.New("executor version is not a normalized Python version")
	}
	if !sha256Pattern.MatchString(r.SHA256) {
		return errors.New("executor sha256 is not a canonical SHA-256 token")
	}
	return nil
}

// PreflightUnsafeExecutor resolves and interrogates all executable task
// runners before model output. Release A supports one pinned Python identity
// for the complete built-in executable battery.
func PreflightUnsafeExecutor(spec *Spec) (ExecutorReceipt, error) {
	if spec == nil {
		return ExecutorReceipt{}, failure(FailureExecutorPreflight, "spec", errors.New("nil task specification"))
	}
	checks := []struct {
		name   string
		runner []string
		files  map[string]string
	}{
		{spec.CodeWrite.ID, spec.CodeWrite.Runner, spec.CodeWrite.Files},
		{spec.CodeFix.ID, spec.CodeFix.Runner, spec.CodeFix.Files},
		{spec.Tools.ID, spec.Tools.Verify.Runner, spec.Tools.Files},
		{spec.Agentic.ID, spec.Agentic.Verify.Runner, spec.Agentic.Files},
	}
	var receipt ExecutorReceipt
	for i, check := range checks {
		if err := validateTaskRunner(check.runner, check.files); err != nil {
			return ExecutorReceipt{}, failure(FailureExecutorPreflight, check.name, err)
		}
		path, err := resolvePythonExecutablePath(context.Background(), check.runner[0])
		if err != nil {
			return ExecutorReceipt{}, failure(FailureExecutorPreflight, check.name, err)
		}
		if i == 0 {
			version, err := queryPythonVersion(context.Background(), path)
			if err != nil {
				return ExecutorReceipt{}, failure(FailureExecutorPreflight, check.name, err)
			}
			digest, err := hashInterpreter(path)
			if err != nil {
				return ExecutorReceipt{}, failure(FailureExecutorPreflight, check.name, err)
			}
			receipt = ExecutorReceipt{Kind: "python", Path: path, Version: version, SHA256: digest}
		} else if path != receipt.Path {
			return ExecutorReceipt{}, failure(FailureExecutorPreflight, check.name,
				errors.New("executable tasks resolved to different interpreter identities"))
		}
	}
	return receipt, nil
}

func preflightTaskRunner(argv []string, files map[string]string) error {
	_, err := resolveTaskRunner(context.Background(), argv, files)
	return err
}

type resolvedTaskRunner struct {
	path    string
	args    []string
	version string
	sha256  string
}

func resolveTaskRunner(ctx context.Context, argv []string, files map[string]string) (resolvedTaskRunner, error) {
	if err := validateTaskRunner(argv, files); err != nil {
		return resolvedTaskRunner{}, err
	}
	if pinned, ok := pinnedUnsafeExecutor(ctx); ok {
		return resolvedRunnerFromReceipt(*pinned, argv[1:])
	}
	path, err := resolvePythonExecutablePath(ctx, argv[0])
	if err != nil {
		return resolvedTaskRunner{}, err
	}
	version, err := queryPythonVersion(ctx, path)
	if err != nil {
		return resolvedTaskRunner{}, err
	}
	digest, err := hashInterpreter(path)
	if err != nil {
		return resolvedTaskRunner{}, err
	}
	return resolvedTaskRunner{
		path: path, args: append([]string(nil), argv[1:]...),
		version: version, sha256: digest,
	}, nil
}

func resolvedRunnerFromReceipt(receipt ExecutorReceipt, args []string) (resolvedTaskRunner, error) {
	if err := receipt.Validate(); err != nil {
		return resolvedTaskRunner{}, err
	}
	runner := resolvedTaskRunner{
		path: receipt.Path, args: append([]string(nil), args...),
		version: receipt.Version, sha256: receipt.SHA256,
	}
	if err := runner.validateIdentity(); err != nil {
		return resolvedTaskRunner{}, err
	}
	return runner, nil
}

func (r resolvedTaskRunner) receipt() ExecutorReceipt {
	return ExecutorReceipt{Kind: "python", Path: r.path, Version: r.version, SHA256: r.sha256}
}

func validateTaskRunner(argv []string, files map[string]string) error {
	if err := validateRunnerName(argv); err != nil {
		return err
	}
	if len(argv) != 2 {
		return fmt.Errorf("runner must name exactly one task-local Python file")
	}
	script := argv[1]
	if filepath.Base(script) != script || strings.ToLower(filepath.Ext(script)) != ".py" {
		return fmt.Errorf("runner script %q must be a task-local .py file", script)
	}
	if _, ok := files[script]; !ok {
		return fmt.Errorf("runner script %q is not present in the immutable fixture", script)
	}
	return nil
}

func validateRunnerName(argv []string) error {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return errors.New("no runner defined")
	}
	base := strings.ToLower(filepath.Base(argv[0]))
	switch base {
	case "python", "python.exe", "python3", "python3.exe":
	default:
		return fmt.Errorf("runner %q is not on the direct-interpreter allowlist", argv[0])
	}
	return nil
}

func resolveRunnerPath(name string) (string, error) {
	resolved, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("resolve runner %q: %w", name, err)
	}
	if !filepath.IsAbs(resolved) {
		if resolved, err = filepath.Abs(resolved); err != nil {
			return "", fmt.Errorf("resolve absolute runner path: %w", err)
		}
	}
	return canonicalRunnerPath(resolved)
}

func canonicalRunnerPath(resolved string) (string, error) {
	resolved = filepath.Clean(resolved)
	canonical, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", fmt.Errorf("resolve canonical runner path %q: %w", resolved, err)
	}
	if !filepath.IsAbs(canonical) {
		canonical, err = filepath.Abs(canonical)
		if err != nil {
			return "", fmt.Errorf("resolve canonical absolute runner path: %w", err)
		}
	}
	return filepath.Clean(canonical), nil
}

func resolvePythonExecutablePath(ctx context.Context, name string) (string, error) {
	launcher, err := resolveRunnerPath(name)
	if err != nil {
		return "", err
	}
	raw, err := boundedCommandOutput(ctx, launcher, "-I", "-S", "-c",
		"import os,sys; print(os.path.realpath(sys.executable))")
	if err != nil {
		return "", fmt.Errorf("resolve Python executable through %q: %w", launcher, err)
	}
	reported := strings.TrimSpace(string(raw))
	if reported == "" || strings.ContainsAny(reported, "\r\n") || !filepath.IsAbs(reported) {
		return "", errors.New("python reported a malformed executable path")
	}
	canonical, err := canonicalRunnerPath(reported)
	if err != nil {
		return "", fmt.Errorf("resolve Python-reported executable %q: %w", reported, err)
	}
	return canonical, nil
}

type boundedOutput struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (w *boundedOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	remaining := w.limit - w.buf.Len()
	if remaining > 0 {
		keep := len(p)
		if keep > remaining {
			keep = remaining
		}
		_, _ = w.buf.Write(p[:keep])
	}
	if len(p) > remaining {
		w.overflow = true
	}
	return len(p), nil
}

func (w *boundedOutput) result() ([]byte, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buf.Bytes()...), w.overflow
}

func queryPythonVersion(ctx context.Context, path string) (string, error) {
	raw, err := boundedCommandOutput(ctx, path, "--version")
	if err != nil {
		return "", fmt.Errorf("query interpreter version: %w", err)
	}
	version, err := normalizePythonVersion(raw)
	if err != nil {
		return "", fmt.Errorf("query interpreter version: %w", err)
	}
	return version, nil
}

func boundedCommandOutput(ctx context.Context, path string, args ...string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, interpreterVersionTimeout)
	defer cancel()
	output := &boundedOutput{limit: interpreterVersionMax}
	cmd := exec.CommandContext(cctx, path, args...)
	cmd.Stdout, cmd.Stderr = output, output
	err := cmd.Run()
	raw, overflow := output.result()
	if cctx.Err() != nil {
		return nil, cctx.Err()
	}
	if overflow {
		return nil, fmt.Errorf("output exceeds %d bytes", interpreterVersionMax)
	}
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func normalizePythonVersion(raw []byte) (string, error) {
	value := strings.TrimSpace(string(raw))
	if len(value) > 128 || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("interpreter returned a malformed Python version")
	}
	match := pythonVersionPattern.FindStringSubmatch(value)
	if match == nil {
		return "", fmt.Errorf("interpreter returned unsupported version %q", value)
	}
	return "Python " + match[1], nil
}

func hashInterpreter(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open interpreter for hashing: %w", err)
	}
	defer f.Close() //nolint:errcheck // a read-only close cannot alter the receipt
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect interpreter for hashing: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("interpreter %q is not a regular file", path)
	}
	if info.Size() < 1 || info.Size() > interpreterHashMax {
		return "", fmt.Errorf("interpreter size %d is outside the supported receipt range", info.Size())
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, interpreterHashMax+1))
	if err != nil {
		return "", fmt.Errorf("hash interpreter: %w", err)
	}
	if n != info.Size() || n > interpreterHashMax {
		return "", errors.New("interpreter changed while its identity was being recorded")
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func (r resolvedTaskRunner) validateIdentity() error {
	digest, err := hashInterpreter(r.path)
	if err != nil {
		return err
	}
	if digest != r.sha256 {
		return errors.New("resolved interpreter changed after preflight")
	}
	return nil
}

// VerificationReceipt is created by the parent harness after the process
// terminates. Generated code can influence its own output, but cannot replace
// the parent's exit observation or convert an unavailable observation into a
// scoreable outcome.
type VerificationReceipt struct {
	Protocol         string `json:"protocol"`
	InterpreterPath  string `json:"interpreter_path"`
	InterpreterVer   string `json:"interpreter_version"`
	InterpreterHash  string `json:"interpreter_sha256"`
	ProcessStarted   bool   `json:"process_started"`
	SuccessfulExit   bool   `json:"successful_exit"`
	ExitCode         int    `json:"exit_code"`
	ExactFinalMarker bool   `json:"exact_final_marker"`
	OutputSHA256     string `json:"output_sha256"`

	Output  string   `json:"-"`
	Failure *Failure `json:"-"`
}

// ValidateExecutor binds a verifier observation to the interpreter identity
// sealed before evaluation. It does not make an unisolated observation
// scoreable.
func (r VerificationReceipt) ValidateExecutor(expected ExecutorReceipt) error {
	if r.Protocol != "fitr.verifier.v2" {
		return fmt.Errorf("unsupported verifier protocol %q", r.Protocol)
	}
	actual := ExecutorReceipt{
		Kind: "python", Path: r.InterpreterPath, Version: r.InterpreterVer, SHA256: r.InterpreterHash,
	}
	if err := actual.Validate(); err != nil {
		return fmt.Errorf("verifier executor: %w", err)
	}
	if actual != expected {
		return errors.New("verifier observation used an interpreter that differs from the run manifest")
	}
	return nil
}

// verifyIn requires both a successful process exit and an exact final marker.
// Raw model output cannot override a failed process. Until the isolated worker
// exists, even a matching receipt remains unverified and is scored as
// inconclusive by callers.
func verifyIn(ctx context.Context, dir string, runner resolvedTaskRunner, marker string) VerificationReceipt {
	r := VerificationReceipt{
		Protocol: "fitr.verifier.v2", ExitCode: -1,
		InterpreterPath: runner.path, InterpreterVer: runner.version, InterpreterHash: runner.sha256,
	}
	if err := runner.validateIdentity(); err != nil {
		r.Failure = failure(FailureExecutorPreflight, "verify", err)
		return r
	}
	cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, runner.path, runner.args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	started, exitCode := false, -1
	if cmd.ProcessState != nil {
		started, exitCode = true, cmd.ProcessState.ExitCode()
	}
	r = verifierReceiptFromProcess(out, started, exitCode, err, cctx.Err(), marker)
	r.InterpreterPath, r.InterpreterVer, r.InterpreterHash = runner.path, runner.version, runner.sha256
	return r
}

func verifierReceiptFromProcess(output []byte, started bool, exitCode int, runErr, contextErr error, marker string) VerificationReceipt {
	r := VerificationReceipt{
		Protocol: "fitr.verifier.v2", ProcessStarted: started, ExitCode: exitCode,
		Output: string(output),
	}
	sum := sha256.Sum256(output)
	r.OutputSHA256 = "sha256:" + hex.EncodeToString(sum[:])
	if contextErr != nil {
		r.Failure = failure(FailureExecutorTimeout, "verify", contextErr)
		return r
	}
	if runErr != nil {
		kind := FailureExecutorLaunch
		if started {
			kind = FailureExecutorExit
		}
		r.Failure = failure(kind, "verify", runErr)
		return r
	}
	r.SuccessfulExit = started && exitCode == 0
	if !r.SuccessfulExit {
		r.Failure = &Failure{
			Kind: FailureExecutorExit, Operation: "verify",
			Message: fmt.Sprintf("runner ended with exit code %d", exitCode),
		}
		return r
	}
	r.ExactFinalMarker = exactFinalLine(r.Output, marker)
	if !r.ExactFinalMarker {
		r.Failure = &Failure{
			Kind: FailureVerifierProtocol, Operation: "verify",
			Message: fmt.Sprintf("successful runner did not end with exact marker %q", marker),
		}
	}
	return r
}

func exactFinalLine(output, marker string) bool {
	if marker == "" {
		return false
	}
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 || strings.TrimSpace(lines[len(lines)-1]) != marker {
		return false
	}
	matches := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == marker {
			matches++
		}
	}
	return matches == 1
}
