package main

// Opt-in live smoke against a running Ollama. CI never runs this - every CI
// test is pure logic - but a human with a model can:
//
//	FITR_LIVE=qwen3-coder:30b go test ./cmd/fitr -run TestLive -v
//
// It runs one check from each need pool against the real model. It asserts
// the MACHINERY (generation, grading, outcome shape), not the model's score -
// a weak model failing a check is a correct result, not a test failure.

import (
	"context"
	"os"
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
