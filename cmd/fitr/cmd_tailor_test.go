package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/autoruntime"
	"github.com/blisspixel/fitr/internal/fitting"
	"github.com/blisspixel/fitr/internal/lock"
)

func TestTailorStartRequiresTheSessionLease(t *testing.T) {
	_, id, _ := draftCeiling(t)
	guard, err := lock.Acquire("fitting-"+id, "test fitting")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = guard.Release() })
	calls := 0
	deps := tailorDeps{runPoint: func(context.Context, tailorJob) (string, string, error) {
		calls++
		return "run", tailorDigest("c"), nil
	}}
	_, _, code := captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), []string{"start", id, "--approve-plan"}, deps)
	})
	if code != exitError || calls != 0 {
		t.Fatalf("concurrent session mutation permitted: exit %d calls %d", code, calls)
	}
}

func tailorEnv(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("FITR_RESULTS", root)
	t.Setenv("FITR_TASKS", t.TempDir())
	t.Setenv("OLLAMA_BASE_URL", "")
	t.Setenv("OLLAMA_HOST", "")
	t.Setenv("FITR_BACKEND", "invalid-must-not-be-used")
	previous := machineErrors
	t.Cleanup(func() { machineErrors = previous })
	return root
}

func ceilingArgs(extra ...string) []string {
	args := []string{
		"plan", "--role", "daily", "--outcome", "structured_output", "--candidate", "qwen3:30b",
		"--ctx", "32768", "--capacity-budget-gb", "20",
	}
	return append(args, extra...)
}

func requireText(t *testing.T, got string, wants ...string) {
	t.Helper()
	got = strings.Join(strings.Fields(got), " ")
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

func refuseText(t *testing.T, got, banned string) {
	t.Helper()
	if strings.Contains(got, banned) {
		t.Fatalf("found %q in:\n%s", banned, got)
	}
}

func fittingID(t *testing.T, stdout string) string {
	t.Helper()
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "fitting" && strings.HasPrefix(fields[1], "fit-") {
			return fields[1]
		}
	}
	t.Fatalf("no fitting id in:\n%s", stdout)
	return ""
}

func loadFitting(t *testing.T, id string) fitting.Session {
	t.Helper()
	session, err := (fitting.Store{Results: os.Getenv("FITR_RESULTS")}).Load(id)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func draftCeiling(t *testing.T) (string, string, string) {
	t.Helper()
	root := tailorEnv(t)
	stdout, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailor(context.Background(), ceilingArgs())
	})
	if code != exitOK {
		t.Fatalf("plan exit %d\n%s\n%s", code, stdout, stderr)
	}
	return root, fittingID(t, stdout), stdout
}

func assertNoOrdinaryResult(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != ".fitting" {
			t.Fatalf("result outside the fitting store: %s", entry.Name())
		}
	}
}

func assertCeilingOpts(t *testing.T, job tailorJob) {
	t.Helper()
	plan := job.Session.Plan
	if job.Opts.numCtx != 32768 || job.Opts.seedSet != plan.SeedSet || plan.SeedSet != plan.ID || job.Opts.capacityReserveGB != nil || job.Opts.capacityBudgetGB == nil {
		t.Fatalf("opts numCtx=%d seed=%q reserve=%v budget=%v", job.Opts.numCtx, job.Opts.seedSet, job.Opts.capacityReserveGB, job.Opts.capacityBudgetGB)
	}
	got, err := gibBytes(*job.Opts.capacityBudgetGB)
	if err != nil || got != plan.CapacityBytes || len(plan.Candidates) != 1 || job.Model != "qwen3:30b" {
		t.Fatalf("budget bytes %d err %v want %d candidates %v", got, err, plan.CapacityBytes, plan.Candidates)
	}
}

func tailorDigest(char string) string { return "sha256:" + strings.Repeat(char, 64) }

func TestTailorPlanSealsOneCeiling(t *testing.T) {
	root, _, stdout := draftCeiling(t)
	requireText(t, stdout, "32768", "20.00 GiB", "absolute ceiling", "not written to OLLAMA_GPU_OVERHEAD", "non-comparative", "not percent complete")
	refuseText(t, stdout, "8192")
	assertNoOrdinaryResult(t, root)
}

func TestFittingMemoryProbeUsesThePlannedContext(t *testing.T) {
	_, id, _ := draftCeiling(t)
	plan := loadFitting(t, id).Plan
	opts, err := fittingRunOpts(plan)
	if err != nil {
		t.Fatal(err)
	}
	if opts.memoryCtx != plan.ContextTokens {
		t.Fatalf("memory probe context %d differs from plan %d", opts.memoryCtx, plan.ContextTokens)
	}
}

func TestTailorRefusesChangedTasksBeforeInference(t *testing.T) {
	_, id, _ := draftCeiling(t)
	path := filepath.Join(os.Getenv("FITR_TASKS"), "late.json")
	data := []byte(`{"id":"late","kind":"check","need":"structured_output","family":"static","num_predict":8,"params":{"prompt":"Say yes","grader":{"type":"exact","expected":"yes"}}}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	deps := tailorDeps{runPoint: func(context.Context, tailorJob) (string, string, error) {
		calls++
		return "run", tailorDigest("b"), nil
	}}
	_, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), []string{"start", id, "--approve-plan"}, deps)
	})
	if code != exitError || calls != 0 || !strings.Contains(stderr, "task schedule changed") {
		t.Fatalf("changed schedule exit %d calls %d: %s", code, calls, stderr)
	}
}

func TestTailorStatusJSONMatchesText(t *testing.T) {
	_, id, _ := draftCeiling(t)
	text, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailor(context.Background(), []string{"status", id, "--display", "plain"})
	})
	if code != exitOK || stderr != "" {
		t.Fatalf("text status %d %s", code, stderr)
	}
	raw, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailor(context.Background(), []string{"status", id, "--display", "json"})
	})
	if code != exitOK || stderr != "" {
		t.Fatalf("json status %d %s", code, stderr)
	}
	var view tailorView
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatalf("json: %v\n%s", err, raw)
	}
	if view.Schema != fitting.ViewSchema || view.Plan.ContextTokens != 32768 || strings.Join(strings.Fields(text), " ") != strings.Join(strings.Fields(strings.Join(view.Lines, " ")), " ") {
		t.Fatalf("view schema %s ctx %d\ntext %q\njson %#v", view.Schema, view.Plan.ContextTokens, text, view.Lines)
	}
}

func TestTailorAsksForTheMissingOutcome(t *testing.T) {
	tailorEnv(t)
	args := []string{"plan", "--role", "daily", "--candidate", "qwen3:30b", "--ctx", "32768", "--capacity-budget-gb", "20"}
	_, stderr, code := captureCommandOutput(t, func() int { return cmdTailor(context.Background(), args) })
	if code != exitUsage {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	requireText(t, stderr, "--outcome")
}

func TestTailorRejectsTwoCapacityPolicies(t *testing.T) {
	tailorEnv(t)
	_, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailor(context.Background(), ceilingArgs("--capacity-reserve-gb", "4"))
	})
	if code != exitUsage {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	requireText(t, stderr, "--capacity-budget-gb", "--capacity-reserve-gb")
}

func TestTailorPlanReportsEndpointConflict(t *testing.T) {
	tailorEnv(t)
	t.Setenv("OLLAMA_BASE_URL", "http://127.0.0.1:11434")
	t.Setenv("OLLAMA_HOST", "http://10.1.2.3:11434")
	_, stderr, code := captureCommandOutput(t, func() int { return cmdTailor(context.Background(), ceilingArgs()) })
	if code != exitUsage {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	requireText(t, stderr, "OLLAMA_BASE_URL", "OLLAMA_HOST")
}

func TestTailorPlanKeepsAnExplicitEndpoint(t *testing.T) {
	tailorEnv(t)
	t.Setenv("OLLAMA_BASE_URL", "http://127.0.0.1:11434")
	t.Setenv("OLLAMA_HOST", "http://10.1.2.3:11434")
	stdout, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailor(context.Background(), ceilingArgs("--endpoint", "http://127.0.0.1:11435"))
	})
	if code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
	session := loadFitting(t, fittingID(t, stdout))
	if session.Plan.Endpoint != "http://127.0.0.1:11435" || session.Plan.EndpointSource != "explicit" {
		t.Fatalf("endpoint %+v", session.Plan.Endpoint)
	}
}

func TestTailorPlanStopsCodingBeforeInference(t *testing.T) {
	tailorEnv(t)
	args := []string{"plan", "--role", "daily", "--outcome", "coding", "--candidate", "qwen3:30b", "--ctx", "32768", "--capacity-budget-gb", "20"}
	stdout, stderr, code := captureCommandOutput(t, func() int { return cmdTailor(context.Background(), args) })
	if code != exitUnresolved {
		t.Fatalf("plan exit %d\n%s\n%s", code, stdout, stderr)
	}
	requireText(t, stdout, "independently checked", "--scope screen")
	var calls int
	deps := tailorDeps{runPoint: func(context.Context, tailorJob) (string, string, error) {
		calls++
		return "", "", errors.New("inference ran")
	}}
	_, _, code = captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), []string{"start", fittingID(t, stdout), "--approve-plan"}, deps)
	})
	if code != exitUnresolved || calls != 0 {
		t.Fatalf("start exit %d calls %d", code, calls)
	}
}

func TestTailorDoesNotLowerAnImpossibleRate(t *testing.T) {
	tailorEnv(t)
	stdout, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailor(context.Background(), ceilingArgs("--minimum-rate", "1"))
	})
	if code != exitUnresolved {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
	requireText(t, stdout, "not lowered")
}

func TestTailorScreenDoesNotPromiseACoder(t *testing.T) {
	tailorEnv(t)
	args := []string{"plan", "--role", "daily", "--scope", "screen", "--outcome", "coding", "--candidate", "qwen3:30b", "--ctx", "32768", "--capacity-budget-gb", "20", "--minimum-rate", "1"}
	stdout, stderr, code := captureCommandOutput(t, func() int { return cmdTailor(context.Background(), args) })
	if code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
	requireText(t, stdout, "does not qualify a coder", "not copied")
	refuseText(t, stdout, "qualified coder")
}

func TestTailorRejectsACeilingForAnOwnedRuntime(t *testing.T) {
	root := tailorEnv(t)
	_, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailor(context.Background(), ceilingArgs("--approve-owned-runtime"))
	})
	if code != exitUsage {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	requireText(t, stderr, "OLLAMA_GPU_OVERHEAD")
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("session written: %v %d", err, len(entries))
	}
}

func TestTailorReserveWithoutOwnedDoesNotClaimGPUOverhead(t *testing.T) {
	tailorEnv(t)
	args := []string{"plan", "--role", "daily", "--outcome", "structured_output", "--candidate", "qwen3:30b", "--ctx", "32768", "--capacity-reserve-gb", "2", "--memory-gb", "20"}
	stdout, stderr, code := captureCommandOutput(t, func() int { return cmdTailor(context.Background(), args) })
	if code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
	requireText(t, stdout, "does not by itself change")
	refuseText(t, stdout, "OLLAMA_GPU_OVERHEAD")
}

func TestTailorStartRequiresApproval(t *testing.T) {
	_, id, _ := draftCeiling(t)
	var calls int
	deps := tailorDeps{runPoint: func(context.Context, tailorJob) (string, string, error) {
		calls++
		return "", "", errors.New("ran")
	}}
	stdout, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), []string{"start", id}, deps)
	})
	if code != exitUsage || calls != 0 {
		t.Fatalf("exit %d calls %d\n%s\n%s", code, calls, stdout, stderr)
	}
	requireText(t, stdout, "32768")
	requireText(t, stderr, "--approve-plan")
}

func TestTailorStartKeepsTheCeilingAndDoesNotInventACandidate(t *testing.T) {
	root, id, _ := draftCeiling(t)
	var calls int
	deps := tailorDeps{runPoint: func(_ context.Context, job tailorJob) (string, string, error) {
		calls++
		assertCeilingOpts(t, job)
		return "run-00001", tailorDigest("a"), nil
	}}
	stdout, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), []string{"start", id, "--approve-plan"}, deps)
	})
	if code != exitUnresolved || calls != 1 {
		t.Fatalf("exit %d calls %d\n%s\n%s", code, calls, stdout, stderr)
	}
	requireText(t, stdout, "non-comparative", "A second candidate was not invented", "No role selection is recorded yet")
	assertNoOrdinaryResult(t, root)
	session := loadFitting(t, id)
	if session.Phase != fitting.PhaseMeasured || session.ConfirmationSeed != "" || len(session.Points) != 1 {
		t.Fatalf("phase %s seed %q points %d", session.Phase, session.ConfirmationSeed, len(session.Points))
	}
}

func TestTailorResumeSkipsAFinishedPoint(t *testing.T) {
	tailorEnv(t)
	stdout, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailor(context.Background(), ceilingArgs("--candidate", "devstral:24b"))
	})
	if code != exitOK {
		t.Fatalf("plan exit %d\n%s\n%s", code, stdout, stderr)
	}
	id := fittingID(t, stdout)
	seen := runUntilSecondStops(t, id)
	if len(seen) != 2 || seen[0] != "qwen3:30b" || seen[1] != "devstral:24b" {
		t.Fatalf("first pass %v", seen)
	}
	seen = nil
	deps := tailorDeps{runPoint: func(_ context.Context, job tailorJob) (string, string, error) {
		seen = append(seen, job.Model)
		return "run-00002", tailorDigest("b"), nil
	}}
	_, stderr, code = captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), []string{"resume", id}, deps)
	})
	if code != exitUnresolved || len(seen) != 1 || seen[0] != "devstral:24b" {
		t.Fatalf("resume exit %d seen %v\n%s", code, seen, stderr)
	}
	session := loadFitting(t, id)
	if session.ConfirmationSeed != "" || session.Points[0].RunID != "run-00001" || session.Points[1].RunID != "run-00002" {
		t.Fatalf("points %+v seed %q", session.Points, session.ConfirmationSeed)
	}
}

func runUntilSecondStops(t *testing.T, id string) []string {
	t.Helper()
	var seen []string
	deps := tailorDeps{runPoint: func(_ context.Context, job tailorJob) (string, string, error) {
		seen = append(seen, job.Model)
		if job.Model == "devstral:24b" {
			return "", "", errors.New("second stopped")
		}
		return "run-00001", tailorDigest("a"), nil
	}}
	_, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), []string{"start", id, "--approve-plan"}, deps)
	})
	if code != exitError {
		t.Fatalf("start exit %d\n%s", code, stderr)
	}
	return seen
}

func TestTailorSkipsWorkWhenHistoryMatches(t *testing.T) {
	_, id, _ := draftCeiling(t)
	var calls int
	deps := tailorDeps{
		history: func(plan fitting.Plan, model string) (tailorHistory, bool) {
			return tailorHistory{
				Point: fitting.Point{Model: model, RunID: "run-hist01", EvidenceSHA256: tailorDigest("c")},
				Seed:  plan.SeedSet, ContextTokens: plan.ContextTokens, Kind: plan.CapacityKind,
				CapacityBytes: plan.CapacityBytes, Endpoint: plan.Endpoint,
			}, true
		},
		runPoint: func(context.Context, tailorJob) (string, string, error) {
			calls++
			return "", "", errors.New("ran")
		},
	}
	_, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), []string{"start", id, "--approve-plan"}, deps)
	})
	session := loadFitting(t, id)
	if code != exitUnresolved || calls != 0 || session.Points[0].RunID != "run-hist01" || session.ConfirmationSeed != "" {
		t.Fatalf("exit %d calls %d point %+v\n%s", code, calls, session.Points, stderr)
	}
	_, stderr, code = captureCommandOutput(t, func() int {
		return cmdTailor(context.Background(), []string{"adopt", id})
	})
	if code != exitUnresolved {
		t.Fatalf("adopt exit %d\n%s", code, stderr)
	}
	requireText(t, stderr, "No Ollama alias")
}

func TestTailorRejectsUnpairedHistory(t *testing.T) {
	_, id, _ := draftCeiling(t)
	var calls int
	deps := tailorDeps{
		history: func(fitting.Plan, string) (tailorHistory, bool) {
			return tailorHistory{Point: fitting.Point{Model: "qwen3:30b", RunID: "run-hist01", EvidenceSHA256: tailorDigest("c")}, Seed: "other-seed"}, true
		},
		runPoint: func(context.Context, tailorJob) (string, string, error) {
			calls++
			return "run-00001", tailorDigest("a"), nil
		},
	}
	_, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), []string{"start", id, "--approve-plan"}, deps)
	})
	session := loadFitting(t, id)
	if code != exitUnresolved || calls != 1 || session.Points[0].RunID != "run-00001" {
		t.Fatalf("exit %d calls %d point %+v\n%s", code, calls, session.Points, stderr)
	}
}

func TestTailorCancelKeepsTheReceipt(t *testing.T) {
	_, id, _ := draftCeiling(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls int
	deps := tailorDeps{runPoint: func(context.Context, tailorJob) (string, string, error) {
		calls++
		return "", "", errors.New("ran")
	}}
	_, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailorWith(ctx, []string{"start", id, "--approve-plan"}, deps)
	})
	session := loadFitting(t, id)
	if code != exitInterrupt || calls != 0 || session.ConfirmationSeed != "" || session.Phase != fitting.PhaseMeasuring {
		t.Fatalf("exit %d calls %d phase %s seed %q\n%s", code, calls, session.Phase, session.ConfirmationSeed, stderr)
	}
}

func TestTailorCleanupFailureIsNotAModelResult(t *testing.T) {
	_, id, _ := draftCeiling(t)
	deps := tailorDeps{
		runPoint:      func(context.Context, tailorJob) (string, string, error) { return "run-00001", tailorDigest("a"), nil },
		closeEvidence: func(fitting.Session) error { return errors.New("disk full") },
	}
	_, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), []string{"start", id, "--approve-plan"}, deps)
	})
	if code != exitError {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	requireText(t, stderr, "cleanup of the fitting evidence store failed")
	refuseText(t, stderr, "model-quality")
	session := loadFitting(t, id)
	if session.Phase == fitting.PhaseMeasured || session.ConfirmationSeed != "" {
		t.Fatalf("phase %s seed %q", session.Phase, session.ConfirmationSeed)
	}
}

func TestTailorGuide(t *testing.T) {
	stdout, _, code := captureCommandOutput(t, func() int { return cmdTailor(context.Background(), nil) })
	if code != exitUsage {
		t.Fatalf("bare exit %d", code)
	}
	requireText(t, stdout, "fitr tailor", "OLLAMA_BASE_URL", "OLLAMA_HOST")
	stdout, _, code = captureCommandOutput(t, func() int { return cmdTailor(context.Background(), []string{"--help"}) })
	if code != exitOK {
		t.Fatalf("help exit %d", code)
	}
	requireText(t, stdout, "absolute ceiling")
}

func runtimeSpec(numCtx int, reserve int64) autoruntime.Spec {
	return autoruntime.Spec{
		Schema: autoruntime.SpecSchema, Executable: `C:\runtime\ollama.exe`,
		ExecutableSHA256: tailorDigest("a"), LibrariesSHA256: tailorDigest("b"),
		RuntimeVersion: "0.34.3", ModelStore: `C:\runtime\models`,
		NumCtx: numCtx, KVCacheType: "f16", FlashAttention: true, ReserveBytes: reserve,
	}
}

func writeRuntimeFile(t *testing.T, spec autoruntime.Spec) (string, []byte) {
	t.Helper()
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(t.TempDir(), "runtime.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, data
}

func draftOwned(t *testing.T) (string, fitting.Session) {
	t.Helper()
	root := tailorEnv(t)
	args := []string{
		"plan", "--role", "daily", "--outcome", "structured_output",
		"--candidate", "qwen3:30b", "--candidate", "devstral:24b",
		"--ctx", "32768", "--capacity-reserve-gb", "4", "--memory-gb", "24", "--approve-owned-runtime",
	}
	stdout, stderr, code := captureCommandOutput(t, func() int { return cmdTailor(context.Background(), args) })
	if code != exitOK {
		t.Fatalf("plan exit %d\n%s\n%s", code, stdout, stderr)
	}
	return root, loadFitting(t, fittingID(t, stdout))
}

func TestTailorOwnedStartRefusesInspectionDefaults(t *testing.T) {
	root, session := draftOwned(t)
	path, before := writeRuntimeFile(t, runtimeSpec(8192, 2<<30))
	var calls int
	deps := tailorDeps{
		startOwned: func(context.Context, autoCommand) int { calls++; return exitOK },
		runPoint: func(context.Context, tailorJob) (string, string, error) {
			t.Fatal("endpoint measurement ran")
			return "", "", errors.New("ran")
		},
	}
	_, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), []string{"start", session.Plan.ID, "--approve-plan", "--runtime", path}, deps)
	})
	if code != exitUsage || calls != 0 {
		t.Fatalf("exit %d calls %d\n%s", code, calls, stderr)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".roles")); !os.IsNotExist(err) {
		t.Fatalf("role library: %v", err)
	}
}

func TestTailorOwnedStartDelegatesTheMatchingRuntime(t *testing.T) {
	_, session := draftOwned(t)
	path, before := writeRuntimeFile(t, runtimeSpec(session.Plan.ContextTokens, session.Plan.CapacityBytes))
	autoID := "auto-" + strings.Repeat("a", 32)
	var got autoCommand
	deps := tailorDeps{startOwned: func(ctx context.Context, command autoCommand) int {
		got = command
		note, ok := ctx.Value(autoNotifyKey{}).(autoSessionNotifier)
		if !ok || note == nil {
			return exitError
		}
		if err := note(autoID); err != nil {
			return exitError
		}
		return exitOK
	}}
	_, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), []string{"start", session.Plan.ID, "--approve-plan", "--runtime", path}, deps)
	})
	if code != exitOK {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	reloaded := loadFitting(t, session.Plan.ID)
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) || reloaded.Phase != fitting.PhaseDelegated || reloaded.AutoSessionID != autoID {
		t.Fatalf("phase %s auto %s err %v", reloaded.Phase, reloaded.AutoSessionID, err)
	}
	if got.mode != "establish" || got.adoption != "manual" || got.runtimePath != path || len(got.candidates) != 2 || got.repeats != 3 {
		t.Fatalf("command %+v", got)
	}
}
