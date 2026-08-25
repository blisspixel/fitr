package main

// Opt-in live smoke against a running Ollama. CI never runs this - every CI
// test is pure logic - but a human with a model can:
//
//	FITR_LIVE=qwen3-coder:30b go test ./cmd/fitr -run TestLive -v
//
// It runs one check from each need pool against the real model, and walks the
// whole documented loop. Both assert the MACHINERY (generation, grading,
// outcome shape, command wiring), not the model's score - a weak model failing
// a check is a correct result, not a test failure.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/ollama"
)

func TestLiveChecksSmoke(t *testing.T) {
	model := os.Getenv("FITR_LIVE")
	if model == "" {
		t.Skip("set FITR_LIVE=<model> to run the live smoke")
	}
	ctx := context.Background()
	c := ollama.New()
	if !c.Reachable(ctx) {
		t.Fatal("FITR_LIVE is set but Ollama is not reachable")
	}
	spec, err := eval.LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	wantOne := map[string]bool{"structured_output": true, "instruction_precision": true, "reasoning": true}
	for _, cs := range spec.Checks {
		if !wantOne[cs.Need] {
			continue
		}
		delete(wantOne, cs.Need)
		seed := eval.InstanceSeed("live-smoke", cs.ID, 0)
		o, err := eval.RunCheck(ctx, c, model, cs, seed)
		if err != nil {
			t.Fatalf("%s: %v", cs.ID, err)
		}
		if o.TaskID != cs.ID || o.Seed != seed || o.Need != cs.Need {
			t.Fatalf("%s: outcome shape wrong: %+v", cs.ID, o)
		}
		if o.Detail == "" {
			t.Fatalf("%s: outcome has no detail - a bare pass/fail is uninterpretable", cs.ID)
		}
		t.Logf("%-22s pass=%v  %s", cs.ID, o.Pass, o.Detail)
	}
	if len(wantOne) > 0 {
		t.Fatalf("no check found for pools: %v", wantOne)
	}
}

// TestLiveLoopSmoke walks every command in the README's everyday loop table
// against a real runtime. CI cannot do this: it has no GPU and no serving
// process, so it covers protocol and rendering logic thoroughly and covers the
// product not at all. That blind spot shipped 0.9.6 with `advise` returning
// SKIP for every Ollama model -- the fit verdict, unavailable on the primary
// backend, with a green build and a green acceptance row.
//
// Each step asserts the command produced its documented artifact. A negative
// verdict is a real answer here: exitGates means the loop worked and a gate
// failed, which is the outcome fitr exists to report.
func TestLiveLoopSmoke(t *testing.T) {
	model := os.Getenv("FITR_LIVE")
	if model == "" {
		t.Skip("set FITR_LIVE=<model> to run the live loop smoke")
	}
	ctx := context.Background()
	if !ollama.New().Reachable(ctx) {
		t.Fatal("FITR_LIVE is set but Ollama is not reachable")
	}
	// Never read or write the operator's real evidence.
	t.Setenv("FITR_RESULTS", t.TempDir())

	ranLoop := func(t *testing.T, name string, code int, stdout, stderr string) {
		t.Helper()
		if code != exitOK && code != exitGates {
			t.Fatalf("%s: exit = %d, want %d or %d; stderr=%q", name, code, exitOK, exitGates, stderr)
		}
		if strings.TrimSpace(stdout) == "" {
			t.Fatalf("%s: produced no output", name)
		}
	}

	t.Run("inventory", func(t *testing.T) {
		out, code := captureTopStdout(t, func() int { return cmdStatus(ctx, nil) })
		ranLoop(t, "inventory", code, out, "")
		if !strings.Contains(out, baseTag(model)) {
			t.Fatalf("bare fitr did not list the installed model %q:\n%s", model, out)
		}
	})

	t.Run("advise", func(t *testing.T) {
		out, code := captureTopStdout(t, func() int { return cmdAdvise(ctx, []string{model}) })
		ranLoop(t, "advise", code, out, "")
		// The exact 0.9.6 regression: weights unknown suppresses the context
		// table and the verdict degrades to SKIP. The table is the artifact.
		if strings.Contains(out, "weights were not measured") {
			t.Fatalf("advise could not size the weights of an installed model:\n%s", out)
		}
		if !strings.Contains(out, "WEIGHTS") {
			t.Fatalf("advise printed no context-fit table:\n%s", out)
		}
	})

	t.Run("run", func(t *testing.T) {
		// The scorecard is stdout; the save receipt is a stderr diagnostic, so
		// proving a measurement persisted needs both streams.
		stdout := ""
		stderr, code := captureTopStderr(t, func() int {
			var inner int
			stdout, inner = captureTopStdout(t, func() int {
				return cmdRun(ctx, []string{model, "--quick"})
			})
			return inner
		})
		ranLoop(t, "run", code, stdout, stderr)
		if !strings.Contains(stderr, "saved") {
			t.Fatalf("run printed no save receipt:\nstdout=%s\nstderr=%s", stdout, stderr)
		}
	})

	// board and apply both read the evidence run just wrote, so they prove the
	// save/reopen seam, not only their own rendering.
	t.Run("board", func(t *testing.T) {
		out, code := captureTopStdout(t, func() int { return cmdBoard(ctx, nil) })
		ranLoop(t, "board", code, out, "")
		if !strings.Contains(out, baseTag(model)) {
			t.Fatalf("board did not reopen the run just measured:\n%s", out)
		}
	})

	t.Run("apply", func(t *testing.T) {
		out, code := captureTopStdout(t, func() int { return cmdApply(ctx, []string{model}) })
		ranLoop(t, "apply", code, out, "")
		if !strings.Contains(out, "does not restart") {
			t.Fatalf("apply dropped its non-mutation promise:\n%s", out)
		}
	})
}

// baseTag trims a registry host and namespace so an inventory row rendered as
// a short tag still matches a fully qualified FITR_LIVE value.
func baseTag(model string) string {
	if i := strings.LastIndex(model, "/"); i >= 0 {
		return model[i+1:]
	}
	return model
}
