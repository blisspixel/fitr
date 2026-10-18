package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/autoruntime"
	"github.com/blisspixel/fitr/internal/fitting"
)

type fatalReader struct{ t *testing.T }

func (r fatalReader) Read([]byte) (int, error) {
	r.t.Helper()
	r.t.Fatal("stdin was read")
	return 0, io.EOF
}

func sessionCeilingArgs(extra ...string) []string {
	args := []string{
		"--role", "daily", "--outcome", "structured_output", "--candidate", "qwen3:30b",
		"--ctx", "32768", "--capacity-budget-gb", "20",
	}
	return append(args, extra...)
}

func sessionOwnedArgs(extra ...string) []string {
	args := []string{
		"--role", "daily", "--outcome", "structured_output",
		"--candidate", "qwen3:30b", "--candidate", "devstral:24b",
		"--ctx", "32768", "--capacity-reserve-gb", "4", "--memory-gb", "24",
		"--approve-owned-runtime",
		"--runtime-executable", `C:\runtime\ollama.exe`, "--models", `C:\runtime\models`,
	}
	return append(args, extra...)
}

func assertEmptyResults(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("results %v %d", err, len(entries))
	}
}

func TestTailorPlanApproveFlagDoesNotMeasure(t *testing.T) {
	tailorEnv(t)
	var calls int
	deps := tailorDeps{runPoint: func(context.Context, tailorJob) (string, string, error) {
		calls++
		return "", "", errors.New("ran")
	}}
	stdout, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), ceilingArgs("--approve-plan"), deps)
	})
	if code != exitOK || calls != 0 {
		t.Fatalf("exit %d calls %d\n%s\n%s", code, calls, stdout, stderr)
	}
	session := loadFitting(t, fittingID(t, stdout))
	if session.Phase != fitting.PhasePreviewed {
		t.Fatalf("phase %s", session.Phase)
	}
}

func TestTailorSessionMeasuresOneNamedCandidate(t *testing.T) {
	root := tailorEnv(t)
	var calls int
	deps := tailorDeps{runPoint: func(_ context.Context, job tailorJob) (string, string, error) {
		calls++
		assertCeilingOpts(t, job)
		return "run-00001", tailorDigest("a"), nil
	}}
	stdout, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), sessionCeilingArgs("--approve-plan"), deps)
	})
	if code != exitUnresolved || calls != 1 {
		t.Fatalf("exit %d calls %d\n%s\n%s", code, calls, stdout, stderr)
	}
	requireText(t, stdout, "non-comparative", "was not invented", "downloads off", "next:")
	session := loadFitting(t, fittingID(t, stdout))
	if session.Phase != fitting.PhaseMeasured || len(session.Points) != 1 || session.ConfirmationSeed != "" {
		t.Fatalf("phase %s points %d seed %q", session.Phase, len(session.Points), session.ConfirmationSeed)
	}
	assertNoOrdinaryResult(t, root)
}

func TestTailorSessionStopsOnEOFBeforeAPlan(t *testing.T) {
	root := tailorEnv(t)
	deps := tailorDeps{interactive: true, in: strings.NewReader("")}
	_, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), []string{"--display", "plain"}, deps)
	})
	if code != exitUsage {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	requireText(t, stderr, "stopped before the plan was approved")
	assertEmptyResults(t, root)
}

func TestTailorSessionStopsOnEOFBeforeApproval(t *testing.T) {
	tailorEnv(t)
	var calls int
	deps := tailorDeps{
		interactive: true, in: strings.NewReader(""),
		runPoint: func(context.Context, tailorJob) (string, string, error) {
			calls++
			return "", "", errors.New("ran")
		},
	}
	stdout, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), sessionCeilingArgs(), deps)
	})
	if code != exitUsage || calls != 0 {
		t.Fatalf("exit %d calls %d\n%s\n%s", code, calls, stdout, stderr)
	}
	requireText(t, stderr, "stopped before the sealed plan was approved")
	session := loadFitting(t, fittingID(t, stdout))
	if session.Phase != fitting.PhasePreviewed {
		t.Fatalf("phase %s", session.Phase)
	}
}

func TestTailorSessionYesIsTheOnlyApproval(t *testing.T) {
	tailorEnv(t)
	var calls int
	deps := tailorDeps{
		interactive: true, in: strings.NewReader("y\n"),
		runPoint: func(context.Context, tailorJob) (string, string, error) {
			calls++
			return "run-00001", tailorDigest("a"), nil
		},
	}
	_, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), sessionCeilingArgs(), deps)
	})
	if code != exitUsage || calls != 0 {
		t.Fatalf("y exit %d calls %d\n%s", code, calls, stderr)
	}
	deps.in = strings.NewReader("yes\n")
	deps.prompt = nil
	stdout, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), sessionCeilingArgs(), deps)
	})
	if code != exitUnresolved || calls != 1 {
		t.Fatalf("yes exit %d calls %d\n%s\n%s", code, calls, stdout, stderr)
	}
}

func TestTailorSessionInterruptDoesNotReadOrSeal(t *testing.T) {
	root := tailorEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	deps := tailorDeps{interactive: true, in: fatalReader{t}}
	_, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailorWith(ctx, []string{"--display", "plain"}, deps)
	})
	if code != exitInterrupt {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	requireText(t, stderr, "fitting interrupted")
	assertEmptyResults(t, root)
}

func TestTailorSessionJSONDoesNotReadStdin(t *testing.T) {
	tailorEnv(t)
	deps := tailorDeps{interactive: true, in: fatalReader{t}}
	_, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), sessionCeilingArgs("--display", "json"), deps)
	})
	if code != exitUsage {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	requireText(t, stderr, "--approve-plan")
}

func TestTailorSessionNonInteractiveQuestionDoesNotRead(t *testing.T) {
	root := tailorEnv(t)
	deps := tailorDeps{interactive: false, in: fatalReader{t}}
	_, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), []string{"--display", "plain", "--role", "daily"}, deps)
	})
	if code != exitUsage {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	requireText(t, stderr, "--outcome")
	assertEmptyResults(t, root)
}

func TestTailorSessionRefusesOwnedLaunchWhenUnsupported(t *testing.T) {
	root := tailorEnv(t)
	var calls int
	deps := tailorDeps{
		ownedSupported: func() error {
			return errors.New("owned Ollama launch currently requires Windows process-tree and listener ownership support")
		},
		runPoint: func(context.Context, tailorJob) (string, string, error) {
			calls++
			return "", "", errors.New("ran")
		},
	}
	_, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), sessionOwnedArgs("--approve-plan"), deps)
	})
	if code != exitUnresolved || calls != 0 {
		t.Fatalf("exit %d calls %d\n%s", code, calls, stderr)
	}
	requireText(t, stderr, "requires Windows", "omit --approve-owned-runtime", "does not switch")
	assertEmptyResults(t, root)
}

func TestTailorSessionMissingRuntimeDoesNotInspectOrStart(t *testing.T) {
	root := tailorEnv(t)
	var inspected, starts int
	deps := tailorDeps{
		ownedSupported: func() error { return nil },
		inspectRuntime: func(context.Context, string, string) (autoruntime.Spec, error) {
			inspected++
			return runtimeSpec(8192, 2<<30), nil
		},
		startOwned: func(context.Context, autoCommand) int {
			starts++
			return exitOK
		},
	}
	args := []string{
		"--role", "daily", "--outcome", "structured_output",
		"--candidate", "qwen3:30b", "--candidate", "devstral:24b",
		"--ctx", "32768", "--capacity-reserve-gb", "4", "--memory-gb", "24",
		"--approve-owned-runtime", "--approve-plan",
	}
	_, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), args, deps)
	})
	if code != exitUsage || inspected != 0 || starts != 0 {
		t.Fatalf("exit %d inspected %d starts %d\n%s", code, inspected, starts, stderr)
	}
	requireText(t, stderr, "--runtime-executable")
	assertEmptyResults(t, root)
}

func TestTailorSessionRefusesAChangedInstallation(t *testing.T) {
	root := tailorEnv(t)
	var n, starts int
	deps := tailorDeps{
		ownedSupported: func() error { return nil },
		inspectRuntime: func(context.Context, string, string) (autoruntime.Spec, error) {
			n++
			spec := runtimeSpec(8192, 2<<30)
			if n > 1 {
				spec.ExecutableSHA256 = tailorDigest("f")
			}
			return spec, nil
		},
		startOwned: func(context.Context, autoCommand) int {
			starts++
			return exitOK
		},
	}
	_, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), sessionOwnedArgs("--approve-plan"), deps)
	})
	if code != exitUnresolved || starts != 0 || n != 2 {
		t.Fatalf("exit %d starts %d inspects %d\n%s", code, starts, n, stderr)
	}
	requireText(t, stderr, "changed between preview and start")
	if runtimeFiles(t, root) != 0 {
		t.Fatal("runtime file was written after the installation changed")
	}
}

func TestTailorSessionRefusesStaleInventory(t *testing.T) {
	tailorEnv(t)
	var n, calls int
	deps := tailorDeps{
		listModels: func(context.Context, string) ([]string, error) {
			n++
			if n == 1 {
				return []string{"qwen3:30b"}, nil
			}
			return []string{"other:7b"}, nil
		},
		runPoint: func(context.Context, tailorJob) (string, string, error) {
			calls++
			return "", "", errors.New("ran")
		},
	}
	_, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), sessionCeilingArgs("--approve-plan"), deps)
	})
	if code != exitUnresolved || calls != 0 || n != 2 {
		t.Fatalf("exit %d calls %d lists %d\n%s", code, calls, n, stderr)
	}
	requireText(t, stderr, "inventory changed")
}

func TestTailorSessionWritesANewRuntimeFile(t *testing.T) {
	root := tailorEnv(t)
	unrelated := filepath.Join(root, "unrelated.json")
	if err := os.WriteFile(unrelated, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got string
	deps := tailorDeps{
		ownedSupported: func() error { return nil },
		inspectRuntime: func(context.Context, string, string) (autoruntime.Spec, error) {
			return runtimeSpec(8192, 2<<30), nil
		},
		startOwned: func(_ context.Context, command autoCommand) int {
			got = command.runtimePath
			return exitOK
		},
	}
	stdout, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), sessionOwnedArgs("--approve-plan"), deps)
	})
	if code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
	kept, err := os.ReadFile(unrelated)
	if err != nil || string(kept) != "keep\n" {
		t.Fatalf("unrelated file %q %v", kept, err)
	}
	session := loadFitting(t, fittingID(t, stdout))
	written := readRuntime(t, got)
	if written.NumCtx != session.Plan.ContextTokens || written.NumCtx == 8192 || written.ReserveBytes != session.Plan.CapacityBytes {
		t.Fatalf("runtime %+v plan ctx %d reserve %d", written, session.Plan.ContextTokens, session.Plan.CapacityBytes)
	}
	refuseText(t, stdout, `C:\runtime`)
}

func TestTailorSessionRestartsAfterACompletedPoint(t *testing.T) {
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
		return cmdTailorWith(context.Background(), []string{id}, deps)
	})
	if code != exitUnresolved || len(seen) != 1 || seen[0] != "devstral:24b" {
		t.Fatalf("restart exit %d seen %v\n%s", code, seen, stderr)
	}
	session := loadFitting(t, id)
	if session.ConfirmationSeed != "" || session.Points[0].RunID != "run-00001" || session.Points[1].RunID != "run-00002" {
		t.Fatalf("points %+v seed %q", session.Points, session.ConfirmationSeed)
	}
}

func TestTailorSessionAcceptsAPrefilledAnswer(t *testing.T) {
	tailorEnv(t)
	var calls int
	deps := tailorDeps{
		interactive: true, in: strings.NewReader("daily\nyes\n"),
		runPoint: func(context.Context, tailorJob) (string, string, error) {
			calls++
			return "run-00001", tailorDigest("a"), nil
		},
	}
	args := []string{
		"--outcome", "structured_output", "--candidate", "qwen3:30b",
		"--ctx", "32768", "--capacity-budget-gb", "20",
	}
	_, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), args, deps)
	})
	if code != exitUnresolved || calls != 1 {
		t.Fatalf("exit %d calls %d\n%s", code, calls, stderr)
	}
}

func runtimeFiles(t *testing.T, root string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(root, func(path string, info os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(info.Name(), ".runtime.json") {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func readRuntime(t *testing.T, path string) autoruntime.Spec {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var spec autoruntime.Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	return spec
}
