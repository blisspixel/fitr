package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/render"
)

type generatedCheckBackend struct {
	*runIntegrationBackend
	reply string
	err   error
}

func (b *generatedCheckBackend) Generate(context.Context, string, string, ollama.Sampling) (string, ollama.Metrics, error) {
	return b.reply, ollama.Metrics{}, b.err
}

func staticCheck(id, need string) eval.CheckSpec {
	return eval.CheckSpec{
		ID: id, Kind: "check", Need: need, Family: "static", Origin: "builtin", NumPredict: 8,
		Params: map[string]any{
			"prompt": "reply OK",
			"grader": map[string]any{"type": "exact", "expected": "OK"},
		},
	}
}

func TestMeasureGeneratedChecksRunsTheSealedFixedPlan(t *testing.T) {
	spec := &eval.Spec{Checks: []eval.CheckSpec{
		staticCheck("structured", "structured_output"),
		staticCheck("reasoning", "reasoning"),
	}}
	backend := &generatedCheckBackend{runIntegrationBackend: &runIntegrationBackend{}, reply: "OK"}
	step := func(_, _ string, run func() error) error { return run() }
	display := render.New("none")
	defer display.Close()
	result := &Result{SeedSet: "test-seedset"}
	if err := measureGeneratedChecks(context.Background(), backend, "model", spec,
		result, 2, display, step); err != nil {
		t.Fatal(err)
	}
	if len(result.Checks) != 4 || len(result.AdaptiveDecisions) != 0 {
		t.Fatalf("checks=%d decisions=%d, want 4 and 0", len(result.Checks), len(result.AdaptiveDecisions))
	}
	for _, outcome := range result.Checks {
		if !outcome.Pass {
			t.Fatalf("unexpected failed outcome: %+v", outcome)
		}
	}
}

func TestMeasureGeneratedChecksPropagatesStepAndBackendErrors(t *testing.T) {
	spec := &eval.Spec{Checks: []eval.CheckSpec{staticCheck("structured", "structured_output")}}
	display := render.New("none")
	defer display.Close()

	want := errors.New("step failed")
	stepErr := func(_, _ string, _ func() error) error { return want }
	if err := measureGeneratedChecks(context.Background(), &generatedCheckBackend{
		runIntegrationBackend: &runIntegrationBackend{}, reply: "OK",
	}, "model", spec, &Result{}, 1, display, stepErr); !errors.Is(err, want) {
		t.Fatalf("step error = %v", err)
	}

	backendErr := errors.New("backend failed")
	runStep := func(_, _ string, run func() error) error { return run() }
	err := measureGeneratedChecks(context.Background(), &generatedCheckBackend{
		runIntegrationBackend: &runIntegrationBackend{}, err: backendErr,
	}, "model", spec, &Result{}, 1, display, runStep)
	if err == nil || !strings.Contains(err.Error(), backendErr.Error()) {
		t.Fatalf("backend error = %v", err)
	}
}
