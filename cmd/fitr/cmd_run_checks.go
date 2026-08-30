package main

import (
	"context"
	"fmt"

	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/render"
)

// measureGeneratedChecks owns the fixed generated-check sampling plan. Every
// declared spec runs in every requested round, which keeps the denominator
// sealed and comparisons paired when a seed set is shared.
func measureGeneratedChecks(ctx context.Context, backend llm.Backend, model string, spec *eval.Spec,
	result *Result, rounds int, display render.Display,
	step func(string, string, func() error) error) error {
	detail := fmt.Sprintf("%d generated tasks", len(spec.Checks)*rounds)
	return step("checks", detail, func() error {
		completed := 0
		for round := range rounds {
			for _, check := range spec.Checks {
				seed := eval.InstanceSeed(result.SeedSet, check.ID, round)
				outcome, runErr := eval.RunCheck(ctx, backend, model, check, seed)
				if runErr != nil {
					return runErr
				}
				result.Checks = append(result.Checks, outcome)
				completed++
				if live, ok := display.(liveTelemetry); ok {
					live.LiveProgress(completed, len(spec.Checks)*rounds,
						fmt.Sprintf("%d of %d generated tasks", completed, len(spec.Checks)*rounds))
				}
			}
		}
		return nil
	})
}
