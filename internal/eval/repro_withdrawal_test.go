package eval

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/blisspixel/fitr/internal/ollama"
)

// Opt-in repeat harness for the withdrawal tool loop. CI never runs it.
//
//	FITR_REPRO=qwen2.5:14b FITR_REPRO_N=20 go test ./internal/eval -run Repro -v
//
// It exists for two jobs. The first is reproducing the intermittent
// "chat response is missing the terminal receipt" transport failure observed
// once during a default battery; ten consecutive runs did not reproduce it, so
// the harness is left here rather than the finding being written off.
//
// The second is repeatability. The loop is seeded and temperature zero, yet
// ten runs of the same model took between four and eight turns and split nine
// to one on outcome. A serving runtime is not deterministic across loads, and
// a behavioural task's turn count is not a stable measurement. That is a fact
// about the instrument, and it belongs in the test-retest work, not in a
// single run's scorecard.
//
// It asserts nothing about the model: every outcome here is a real answer.
func TestReproWithdrawalTransport(t *testing.T) {
	model := os.Getenv("FITR_REPRO")
	if model == "" {
		t.Skip("set FITR_REPRO=<model> to run the reproduction harness")
	}
	n := 10
	if v := os.Getenv("FITR_REPRO_N"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			n = parsed
		}
	}
	ctx := context.Background()
	c := ollama.New()
	if !c.Reachable(ctx) {
		t.Fatal("ollama not reachable")
	}
	spec, err := LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	fails := 0
	for i := 0; i < n; i++ {
		res, failure := RunToolLoop(ctx, c, model, spec.Withdrawal, t.TempDir())
		if failure != nil {
			fails++
			t.Logf("iter %2d: FAILURE outcome=%s ended=%s turns=%d maxPrompt=%d err=%v detail=%+v",
				i, res.Outcome, res.Ended, res.Turns, res.MaxPromptTok, failure, res.Failure)
			continue
		}
		t.Logf("iter %2d: ok outcome=%s ended=%s turns=%d maxPrompt=%d deadCalls=%d",
			i, res.Outcome, res.Ended, res.Turns, res.MaxPromptTok, res.DeadCalls)
	}
	t.Logf("=== %d/%d iterations hit a transport failure ===", fails, n)
}
