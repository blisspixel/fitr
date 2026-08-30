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
	smoke := liveLoopSmoke{ctx: ctx, model: model, backend: backend}
	t.Run("inventory", smoke.inventory)
	t.Run("advise", smoke.advise)
	t.Run("run", smoke.run)
	t.Run("board", smoke.board)
	t.Run("apply", smoke.apply)
	t.Run("doctor", smoke.doctor)
	t.Run("diag", smoke.diag)
	t.Run("device", smoke.device)
	t.Run("view", smoke.view)
	t.Run("export", smoke.export)
	t.Run("top_snapshot", smoke.topSnapshot)
	t.Run("compare", smoke.compare)
}

type liveLoopSmoke struct {
	ctx     context.Context
	model   string
	backend string
}

func (s liveLoopSmoke) assertRan(t *testing.T, name string, code int, stdout, stderr string) {
	t.Helper()
	if code != exitOK && code != exitGates {
		t.Fatalf("%s: exit = %d, want %d or %d; stderr=%q", name, code, exitOK, exitGates, stderr)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Fatalf("%s: produced no output", name)
	}
}

func (s liveLoopSmoke) inventory(t *testing.T) {
	out, code := captureTopStdout(t, func() int { return cmdStatus(s.ctx, []string{"--backend", s.backend}) })
	s.assertRan(t, "inventory", code, out, "")
	if !strings.Contains(out, baseTag(s.model)) {
		t.Fatalf("bare fitr did not list the installed model %q:\n%s", s.model, out)
	}
}

func (s liveLoopSmoke) advise(t *testing.T) {
	out, code := captureTopStdout(t, func() int {
		return cmdAdvise(s.ctx, []string{s.model, "--backend", s.backend})
	})
	s.assertRan(t, "advise", code, out, "")
	if strings.Contains(out, "weights were not measured") {
		t.Fatalf("advise could not size the weights of an installed model:\n%s", out)
	}
	if !strings.Contains(out, "WEIGHTS") {
		t.Fatalf("advise printed no context-fit table:\n%s", out)
	}
}

func (s liveLoopSmoke) run(t *testing.T) {
	stdout := ""
	stderr, code := captureTopStderr(t, func() int {
		var inner int
		stdout, inner = captureTopStdout(t, func() int {
			return cmdRun(s.ctx, []string{s.model, "--quick", "--backend", s.backend})
		})
		return inner
	})
	s.assertRan(t, "run", code, stdout, stderr)
	if !strings.Contains(stderr, "saved") {
		t.Fatalf("run printed no save receipt:\nstdout=%s\nstderr=%s", stdout, stderr)
	}
}

func (s liveLoopSmoke) board(t *testing.T) {
	if s.backend == "llama-server" {
		s.assertLlamaServerBoard(t)
		return
	}
	out, code := captureTopStdout(t, func() int { return cmdBoard(s.ctx, nil) })
	s.assertRan(t, "board", code, out, "")
	if !strings.Contains(out, baseTag(s.model)) {
		t.Fatalf("board did not reopen the run just measured:\n%s", out)
	}
}

func (s liveLoopSmoke) assertLlamaServerBoard(t *testing.T) {
	t.Helper()
	stdout := ""
	stderr, code := captureTopStderr(t, func() int {
		var inner int
		stdout, inner = captureTopStdout(t, func() int { return cmdBoard(s.ctx, nil) })
		return inner
	})
	if code != exitError {
		t.Fatalf("board exit = %d, want evidence refusal %d; stdout=%s\nstderr=%s", code, exitError, stdout, stderr)
	}
	if strings.Contains(stdout, baseTag(s.model)) {
		t.Fatalf("board ranked llama-server's observed-only artifact identity:\n%s", stdout)
	}
	if !strings.Contains(stderr, "valid evidence contract") {
		t.Fatalf("board did not explain llama-server's expected evidence exclusion:\n%s", stderr)
	}
}

func (s liveLoopSmoke) apply(t *testing.T) {
	out, code := captureTopStdout(t, func() int {
		return cmdApply(s.ctx, []string{s.model, "--backend", s.backend})
	})
	s.assertRan(t, "apply", code, out, "")
	if !strings.Contains(out, "does not restart") {
		t.Fatalf("apply dropped its non-mutation promise:\n%s", out)
	}
}

func (s liveLoopSmoke) runsAtAll(t *testing.T, name string, fn func() int) (string, string) {
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

func (s liveLoopSmoke) doctor(t *testing.T) {
	s.runsAtAll(t, "doctor", func() int {
		return cmdDoctor(s.ctx, []string{s.model, "--backend", s.backend})
	})
}

func (s liveLoopSmoke) diag(t *testing.T) {
	s.runsAtAll(t, "diag", func() int {
		return cmdDiag(s.ctx, []string{s.model, "--backend", s.backend})
	})
}

func (s liveLoopSmoke) device(t *testing.T) {
	out, code := captureTopStdout(t, func() int { return cmdDevice(s.ctx, []string{"--backend", s.backend}) })
	s.assertRan(t, "device", code, out, "")
	for _, field := range []string{"host", "os"} {
		if !strings.Contains(out, field) {
			t.Fatalf("device output omits %q:\n%s", field, out)
		}
	}
}

func (s liveLoopSmoke) view(t *testing.T) {
	out, code := captureTopStdout(t, func() int { return cmdView(s.ctx, []string{s.model}) })
	s.assertRan(t, "view", code, out, "")
	if !strings.Contains(out, baseTag(s.model)) {
		t.Fatalf("view did not reopen the measured model:\n%s", out)
	}
}

func (s liveLoopSmoke) export(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "scorecard.html")
	stderr, code := captureTopStderr(t, func() int {
		return cmdExport(s.ctx, []string{s.model, "--out", dest})
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
	for _, remote := range []string{`src="http`, `href="http`, `src="//`, `href="//`} {
		if bytes.Contains(body, []byte(remote)) {
			t.Fatalf("exported artifact references remote content (%s)", remote)
		}
	}
}

func (s liveLoopSmoke) topSnapshot(t *testing.T) {
	out, code := captureTopStdout(t, func() int { return cmdTop(s.ctx, []string{"--snapshot"}) })
	s.assertRan(t, "top --snapshot", code, out, "")
	var snap map[string]any
	if err := json.Unmarshal([]byte(out), &snap); err != nil {
		t.Fatalf("top --snapshot is not JSON: %v\n%s", err, out)
	}
	if snap["schema"] == nil {
		t.Fatalf("presentation snapshot carries no schema: %s", out)
	}
}

func (s liveLoopSmoke) compare(t *testing.T) {
	other := os.Getenv("FITR_LIVE_SECOND")
	if other == "" {
		t.Skip("set FITR_LIVE_SECOND=<model> to cover compare")
	}
	if s.backend == "llama-server" {
		t.Skip("llama-server serves one launch-time model; restart it and record a second run separately")
	}
	if _, code := captureTopStdout(t, func() int {
		return cmdRun(s.ctx, []string{other, "--quick", "--backend", s.backend})
	}); code != exitOK && code != exitGates {
		t.Fatalf("measuring the second model failed: exit %d", code)
	}
	s.runsAtAll(t, "compare", func() int { return cmdCompare(s.ctx, []string{s.model, other}) })
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
