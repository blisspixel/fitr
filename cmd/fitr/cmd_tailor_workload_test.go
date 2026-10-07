package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/top"
)

func TestTailor250KIsNotSatisfiedBy32K(t *testing.T) {
	tailorEnv(t)
	args := ceilingArgs("--desired-context", "250000", "--context-meaning", "total-window")
	stdout, stderr, code := captureCommandOutput(t, func() int { return cmdTailor(context.Background(), args) })
	if code != exitUnresolved {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
	requireText(t, stdout, "desired context unmet", "does not satisfy that workload", "stopped before inference")
	refuseText(t, stdout, "workload satisfied")
	var calls int
	deps := tailorDeps{runPoint: func(context.Context, tailorJob) (string, string, error) {
		calls++
		return "", "", nil
	}}
	_, _, code = captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), []string{"start", fittingID(t, stdout), "--approve-plan"}, deps)
	})
	if code != exitUnresolved || calls != 0 {
		t.Fatalf("start exit %d calls %d", code, calls)
	}
}

func TestTailorScreenKeepsTheBudgetAndTheGoal(t *testing.T) {
	tailorEnv(t)
	args := ceilingArgs("--scope", "screen", "--desired-context", "250000", "--context-meaning", "total-window")
	stdout, stderr, code := captureCommandOutput(t, func() int { return cmdTailor(context.Background(), args) })
	if code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
	requireText(t, stdout, "does not satisfy that workload", "20.00 GiB", "cannot enlarge")
	id := fittingID(t, stdout)
	var seen int
	deps := tailorDeps{runPoint: func(_ context.Context, job tailorJob) (string, string, error) {
		seen = job.Opts.numCtx
		assertCeilingOpts(t, job)
		return "run-00001", tailorDigest("a"), nil
	}}
	stdout, _, code = captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), []string{"start", id, "--approve-plan"}, deps)
	})
	if code != exitUnresolved || seen != 32768 {
		t.Fatalf("exit %d ctx %d\n%s", code, seen, stdout)
	}
	session := loadFitting(t, id)
	if session.Plan.Workload.DesiredContextTokens != 250000 || session.Plan.CapacityBytes == 0 {
		t.Fatalf("%+v", session.Plan.Workload)
	}
}

func TestTailorHermesRejects59392(t *testing.T) {
	tailorEnv(t)
	args := []string{"plan", "--role", "daily", "--outcome", "structured_output", "--candidate", "qwen3:30b", "--ctx", "59392", "--capacity-budget-gb", "20", "--harness", "hermes"}
	stdout, stderr, code := captureCommandOutput(t, func() int { return cmdTailor(context.Background(), args) })
	if code != exitUnresolved {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
	requireText(t, stdout, "59392", "64000", "not an eligible recommendation")
	refuseText(t, stdout, "workload satisfied")
}

func TestTailorWeightAndKVStayDistinct(t *testing.T) {
	tailorEnv(t)
	stdout, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailor(context.Background(), ceilingArgs("--weight-quant", "Q4_K_M", "--kv", "q8_0", "--weight-preference", "Q4-or-better"))
	})
	if code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
	requireText(t, stdout, "model weights", "cache precision", "does not halve total", "does not set or prove the server")
	session := loadFitting(t, fittingID(t, stdout))
	if !session.Plan.FlashAttention || session.Plan.Workload.WeightQuant != "Q4_K_M" || session.Plan.KVCacheType != "q8_0" || session.Plan.Workload.KVServerVerified {
		t.Fatalf("%+v kv %s", session.Plan.Workload, session.Plan.KVCacheType)
	}
}

func TestTailorClientKVEnvIsNotProof(t *testing.T) {
	tailorEnv(t)
	t.Setenv("OLLAMA_KV_CACHE_TYPE", "q8_0")
	stdout, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailor(context.Background(), ceilingArgs("--kv", "q8_0"))
	})
	if code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
	requireText(t, stdout, "does not prove", "does not set or prove the server")
	session := loadFitting(t, fittingID(t, stdout))
	if !session.Plan.FlashAttention || session.Plan.Workload == nil || session.Plan.Workload.KVServerVerified {
		t.Fatalf("%+v", session.Plan.Workload)
	}
}

func TestTailorPresetStopsBeforeInference(t *testing.T) {
	tailorEnv(t)
	args := []string{"plan", "--role", "daily", "--workload", "repository-planning", "--candidate", "qwen3:30b", "--ctx", "32768", "--capacity-budget-gb", "20"}
	stdout, stderr, code := captureCommandOutput(t, func() int { return cmdTailor(context.Background(), args) })
	if code != exitUnresolved {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
	requireText(t, stdout, "mandatory:", "populated-context", "stopped before inference")
	var calls int
	deps := tailorDeps{runPoint: func(context.Context, tailorJob) (string, string, error) {
		calls++
		return "", "", nil
	}}
	_, _, code = captureCommandOutput(t, func() int {
		return cmdTailorWith(context.Background(), []string{"start", fittingID(t, stdout), "--approve-plan"}, deps)
	})
	if code != exitUnresolved || calls != 0 {
		t.Fatalf("start exit %d calls %d", code, calls)
	}
}

func TestTailorClaimsAgreeAcrossSurfaces(t *testing.T) {
	tailorEnv(t)
	args := ceilingArgs("--scope", "screen", "--desired-context", "250000", "--context-meaning", "total-window", "--harness", "hermes", "--weight-quant", "Q4_K_M", "--kv", "q8_0")
	stdout, stderr, code := captureCommandOutput(t, func() int { return cmdTailor(context.Background(), args) })
	if code != exitUnresolved {
		t.Fatalf("plan exit %d\n%s\n%s", code, stdout, stderr)
	}
	id := fittingID(t, stdout)
	text, stderr, code := captureCommandOutput(t, func() int {
		return cmdTailor(context.Background(), []string{"status", id, "--display", "plain"})
	})
	raw, jsonErr, jsonCode := captureCommandOutput(t, func() int {
		return cmdTailor(context.Background(), []string{"status", id, "--display", "json"})
	})
	if code != exitUnresolved || jsonCode != exitUnresolved || stderr != "" || jsonErr != "" {
		t.Fatalf("text %d json %d\n%s\n%s", code, jsonCode, stderr, jsonErr)
	}
	var view tailorView
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatal(err)
	}
	lines := view.Lines
	if strings.Join(strings.Fields(text), " ") != strings.Join(strings.Fields(strings.Join(view.Lines, " ")), " ") {
		t.Fatalf("text and json diverged")
	}
	claims := []string{
		"desired context unmet", "does not satisfy that workload", "not an eligible recommendation",
		"model weights", "cache precision", "does not prove", "does not halve total model and runtime memory",
		"not populated-context qualification", "measured peak is unknown",
		"a memory projection is not tested long-context quality", "we do not have enough evidence",
		"unmeasured cache type", "skipped executable tests", "unknown runtime overhead",
		"fitr.recommendation.v1",
	}
	html := renderClaimHTML(t, lines)
	canvas := renderClaimTUI(lines)
	for _, claim := range claims {
		for _, surface := range []struct {
			name string
			body string
		}{{"text", text}, {"json", strings.Join(view.Lines, " ")}, {"html", html}, {"tui", canvas}} {
			if !strings.Contains(strings.Join(strings.Fields(surface.body), " "), claim) {
				t.Fatalf("claim %q missing from %s", claim, surface.name)
			}
		}
	}
}

func renderClaimHTML(t *testing.T, lines []string) string {
	t.Helper()
	var buf bytes.Buffer
	artifact := render.Artifact{FitrVersion: "0.11.1", Model: "qwen3:30b", Level: "full", Claims: lines}
	if err := render.WriteHTML(&buf, artifact); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func renderClaimTUI(lines []string) string {
	snapshot := top.Snapshot{History: []top.Run{{ID: "run-1", Model: "qwen3:30b", Claims: lines}}}
	state := top.NewState(snapshot)
	state.View = top.ViewResult
	state.Width = 180
	state.Height = 220
	state.Selected[top.ViewResult] = "run-1"
	return top.Render(state, top.DefaultGlyphs(false)).Plain()
}
