package main

// Opt-in live smoke against an explicitly selected serving runtime. CI never
// runs this - every CI test is pure logic - but a human with a model can:
//
//	FITR_LIVE=qwen3-coder:30b go test ./cmd/fitr -run TestLive -v
//	FITR_LIVE=served.gguf FITR_LIVE_BACKEND=llama-server go test ./cmd/fitr -run TestLive -v
//
// FITR_LIVE_BACKEND defaults to Ollama so the original invocation remains the
// ordinary path. Live acceptance never auto-discovers a different runtime.
// It runs one check from each need pool against the real model, and walks the
// whole documented loop. Both assert the MACHINERY (generation, grading,
// outcome shape, command wiring), not the model's score - a weak model failing
// a check is a correct result, not a test failure.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/llm"
)

func TestLiveBackendSelectionIsExplicit(t *testing.T) {
	t.Run("Ollama remains the default", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/tags" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"models":[{"name":"model:latest"}]}`))
		}))
		defer server.Close()
		t.Setenv("FITR_LIVE", "model:latest")
		t.Setenv("FITR_LIVE_BACKEND", "")
		t.Setenv("OLLAMA_BASE_URL", server.URL)

		model, backend, c := liveBackend(t, context.Background())
		if model != "model:latest" || backend != "ollama" || c.URL() != server.URL {
			t.Fatalf("live backend = %q, %q, %q", model, backend, c.URL())
		}
	})

	t.Run("llama-server uses only its configured endpoint", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/health":
				_, _ = w.Write([]byte(`{"status":"ok"}`))
			case "/props":
				_, _ = w.Write([]byte(`{"build_info":"test","model_path":"C:\\models\\served.gguf"}`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()
		t.Setenv("FITR_LIVE", "operator-alias")
		t.Setenv("FITR_LIVE_BACKEND", "llama-server")
		t.Setenv("LLAMA_SERVER_URL", server.URL)

		model, backend, c := liveBackend(t, context.Background())
		if model != "served.gguf" || backend != "llama-server" || c.URL() != server.URL {
			t.Fatalf("live backend = %q, %q, %q", model, backend, c.URL())
		}
	})
}

func TestLiveChecksSmoke(t *testing.T) {
	ctx := context.Background()
	model, _, c := liveBackend(t, ctx)
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
	ctx := context.Background()
	model, backend, _ := liveBackend(t, ctx)
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
		out, code := captureTopStdout(t, func() int {
			return cmdStatus(ctx, []string{"--backend", backend})
		})
		ranLoop(t, "inventory", code, out, "")
		if !strings.Contains(out, baseTag(model)) {
			t.Fatalf("bare fitr did not list the installed model %q:\n%s", model, out)
		}
	})

	t.Run("advise", func(t *testing.T) {
		out, code := captureTopStdout(t, func() int {
			return cmdAdvise(ctx, []string{model, "--backend", backend})
		})
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
				return cmdRun(ctx, []string{model, "--quick", "--backend", backend})
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
		if backend == "llama-server" {
			stdout := ""
			stderr, code := captureTopStderr(t, func() int {
				var inner int
				stdout, inner = captureTopStdout(t, func() int { return cmdBoard(ctx, nil) })
				return inner
			})
			if code != exitError {
				t.Fatalf("board exit = %d, want evidence refusal %d; stdout=%s\nstderr=%s", code, exitError, stdout, stderr)
			}
			if strings.Contains(stdout, baseTag(model)) {
				t.Fatalf("board ranked llama-server's observed-only artifact identity:\n%s", stdout)
			}
			if !strings.Contains(stderr, "valid evidence contract") {
				t.Fatalf("board did not explain llama-server's expected evidence exclusion:\n%s", stderr)
			}
			return
		}
		out, code := captureTopStdout(t, func() int { return cmdBoard(ctx, nil) })
		ranLoop(t, "board", code, out, "")
		if !strings.Contains(out, baseTag(model)) {
			t.Fatalf("board did not reopen the run just measured:\n%s", out)
		}
	})

	t.Run("apply", func(t *testing.T) {
		out, code := captureTopStdout(t, func() int {
			return cmdApply(ctx, []string{model, "--backend", backend})
		})
		ranLoop(t, "apply", code, out, "")
		if !strings.Contains(out, "does not restart") {
			t.Fatalf("apply dropped its non-mutation promise:\n%s", out)
		}
	})

	// The loop above is the README's everyday table. The commands below are the
	// rest of the documented surface. An acceptance path that walks only the
	// happy five is how a broken advise shipped behind a green row.
	//
	// Doctor and diag report whether this box can be measured at all, so a
	// refusal is a real answer; only a crash or a rejected invocation fails.
	runsAtAll := func(t *testing.T, name string, fn func() int) (string, string) {
		t.Helper()
		stdout := ""
		stderr, code := captureTopStderr(t, func() int {
			var inner int
			stdout, inner = captureTopStdout(t, fn)
			return inner
		})
		if code == exitUsage {
			t.Fatalf("%s rejected its own documented invocation: %s", name, stderr)
		}
		if strings.TrimSpace(stdout+stderr) == "" {
			t.Fatalf("%s produced no output", name)
		}
		return stdout, stderr
	}

	t.Run("doctor", func(t *testing.T) {
		runsAtAll(t, "doctor", func() int {
			return cmdDoctor(ctx, []string{model, "--backend", backend})
		})
	})

	t.Run("diag", func(t *testing.T) {
		runsAtAll(t, "diag", func() int {
			return cmdDiag(ctx, []string{model, "--backend", backend})
		})
	})

	t.Run("device", func(t *testing.T) {
		out, code := captureTopStdout(t, func() int {
			return cmdDevice(ctx, []string{"--backend", backend})
		})
		ranLoop(t, "device", code, out, "")
		// The fingerprint decides comparability, so it has to name the machine
		// rather than print blanks.
		for _, field := range []string{"host", "os"} {
			if !strings.Contains(out, field) {
				t.Fatalf("device output omits %q:\n%s", field, out)
			}
		}
	})

	t.Run("view", func(t *testing.T) {
		out, code := captureTopStdout(t, func() int { return cmdView(ctx, []string{model}) })
		ranLoop(t, "view", code, out, "")
		if !strings.Contains(out, baseTag(model)) {
			t.Fatalf("view did not reopen the measured model:\n%s", out)
		}
	})

	t.Run("export", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "scorecard.html")
		stderr, code := captureTopStderr(t, func() int {
			return cmdExport(ctx, []string{model, "--out", dest})
		})
		if code != exitOK {
			t.Fatalf("export exit = %d; stderr=%s", code, stderr)
		}
		body, err := os.ReadFile(dest)
		if err != nil {
			t.Fatalf("export wrote no artifact at %s: %v", dest, err)
		}
		if !bytes.Contains(body, []byte("<html")) {
			t.Fatalf("exported artifact is not HTML (%d bytes)", len(body))
		}
		// Self-contained is part of the export contract: a shared scorecard
		// must not fetch anything when it is opened.
		for _, remote := range []string{`src="http`, `href="http`, `src="//`, `href="//`} {
			if bytes.Contains(body, []byte(remote)) {
				t.Fatalf("exported artifact references remote content (%s)", remote)
			}
		}
	})

	t.Run("top_snapshot", func(t *testing.T) {
		out, code := captureTopStdout(t, func() int { return cmdTop(ctx, []string{"--snapshot"}) })
		ranLoop(t, "top --snapshot", code, out, "")
		var snap map[string]any
		if err := json.Unmarshal([]byte(out), &snap); err != nil {
			t.Fatalf("top --snapshot is not JSON: %v\n%s", err, out)
		}
		if snap["schema"] == nil {
			t.Fatalf("presentation snapshot carries no schema: %s", out)
		}
	})

	// compare needs two measured models, so it runs only when the operator
	// names a second one. A skipped row that reads as green is the failure
	// this whole test exists to prevent, so the skip is explicit.
	t.Run("compare", func(t *testing.T) {
		other := os.Getenv("FITR_LIVE_SECOND")
		if other == "" {
			t.Skip("set FITR_LIVE_SECOND=<model> to cover compare")
		}
		if backend == "llama-server" {
			t.Skip("llama-server serves one launch-time model; restart it and record a second run separately")
		}
		if _, code := captureTopStdout(t, func() int {
			return cmdRun(ctx, []string{other, "--quick", "--backend", backend})
		}); code != exitOK && code != exitGates {
			t.Fatalf("measuring the second model failed: exit %d", code)
		}
		runsAtAll(t, "compare", func() int { return cmdCompare(ctx, []string{model, other}) })
	})
}

func liveBackend(t *testing.T, ctx context.Context) (string, string, llm.Backend) {
	t.Helper()
	requested := strings.TrimSpace(os.Getenv("FITR_LIVE"))
	if requested == "" {
		t.Skip("set FITR_LIVE=<model> to run the live smoke")
	}
	rawBackend := strings.TrimSpace(os.Getenv("FITR_LIVE_BACKEND"))
	if rawBackend == "" {
		rawBackend = "ollama"
	}
	backend, ok := canonicalBackendKind(rawBackend)
	if !ok || backend != "ollama" && backend != "llama-server" {
		t.Fatalf("FITR_LIVE_BACKEND=%q is not a supported native live backend; use ollama or llama-server", rawBackend)
	}
	c, err := backendAt(backend, "")
	if err != nil {
		t.Fatalf("configure %s live backend: %v", backend, err)
	}
	if !c.Reachable(ctx) {
		t.Fatalf("FITR_LIVE is set but %s is not reachable at %s", backend, c.URL())
	}
	models, err := c.Tags(ctx)
	if err != nil {
		t.Fatalf("list models from %s: %v", backend, err)
	}
	selected, err := selectResolvedModel(backend, requested, models)
	if err != nil {
		t.Fatalf("resolve FITR_LIVE model through %s: %v", backend, err)
	}
	return selected.Name, backend, c
}

// baseTag trims a registry host and namespace so an inventory row rendered as
// a short tag still matches a fully qualified FITR_LIVE value.
func baseTag(model string) string {
	if i := strings.LastIndex(model, "/"); i >= 0 {
		return model[i+1:]
	}
	return model
}
