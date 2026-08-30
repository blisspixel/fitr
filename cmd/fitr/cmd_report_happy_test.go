package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/eval"
)

func TestCompareReportsCompatibleMeasuredRuns(t *testing.T) {
	t.Setenv("FITR_RESULTS", t.TempDir())
	a, b := golden(t), golden(t)
	a.Model, a.Scorecard.Model = "first", "first"
	b.Model, b.Scorecard.Model = "second", "second"
	sealCurrentResult(t, a, b)
	saveCurrentResults(t, a, b)

	out, code := captureTopStdout(t, func() int {
		return cmdCompare(context.Background(), []string{"first", "second"})
	})
	if code != exitOK {
		t.Fatalf("compare exit=%d\n%s", code, out)
	}
	for _, want := range []string{
		"first  vs  second", "decode tok/s", "prefill tok/s", "paired",
		"95% interval", "cannot separate",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("compatible comparison missing %q:\n%s", want, out)
		}
	}
}

func TestCompareKeepsUnprovenUncachedPrefillDescriptive(t *testing.T) {
	t.Setenv("FITR_RESULTS", t.TempDir())
	a, b := golden(t), golden(t)
	a.Model, a.Scorecard.Model = "first", "first"
	b.Model, b.Scorecard.Model = "second", "second"
	if len(a.Speed) == 0 || len(b.Speed) == 0 {
		t.Fatal("golden result has no speed evidence")
	}
	for i := range a.Speed {
		a.Speed[i].PrefillTPS = 300
	}
	for i := range b.Speed {
		b.Speed[i].PrefillTPS = 100
	}
	// A positive receipt keeps the observation visible but prevents an
	// uncached-prefill winner claim.
	a.Speed[0].CachedPromptTok = 1
	a.Speed[0].PromptTok = 100
	sealCurrentResult(t, a, b)
	saveCurrentResults(t, a, b)

	out, code := captureTopStdout(t, func() int {
		return cmdCompare(context.Background(), []string{"first", "second"})
	})
	if code != exitOK {
		t.Fatalf("compare exit=%d\n%s", code, out)
	}
	if !strings.Contains(out, "prefill tok/s") || !strings.Contains(out, "uncached prefill not proven") {
		t.Fatalf("comparison hid the descriptive prefill caveat:\n%s", out)
	}
	prefillLine := ""
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "prefill tok/s") {
			prefillLine = line
			break
		}
	}
	if strings.Contains(prefillLine, "faster") || strings.Contains(prefillLine, "[") {
		t.Fatalf("unproven uncached prefill retained a winner or interval: %q", prefillLine)
	}
}

func TestPairedClaimRequiresACompleteMatchingPlan(t *testing.T) {
	a, b := golden(t), golden(t)
	a.Model, b.Model = "first", "second"
	sealCurrentResult(t, a, b)
	flips := eval.PairFlips(a.Checks, b.Checks)
	if !completePairedPlan(a, b, flips) {
		t.Fatal("complete matching mock plans were not claimable")
	}
	b.Checks[0].Outcome = eval.OutcomeSkipped
	if completePairedPlan(a, b, eval.PairFlips(a.Checks, b.Checks)) {
		t.Fatal("a skipped planned pair remained claimable")
	}
	b.Checks[0].Outcome = mockBinaryOutcome(b.Checks[0].Pass)
	b.TaskPlan.CheckPlanSHA256 = strings.Repeat("f", len(b.TaskPlan.CheckPlanSHA256))
	if completePairedPlan(a, b, eval.PairFlips(a.Checks, b.Checks)) {
		t.Fatal("different sealed plans remained claimable")
	}
}

func TestBoardJSONProjectsOnlyClaimableRunsAndSupportsFullEvidence(t *testing.T) {
	t.Setenv("FITR_RESULTS", t.TempDir())
	clean, contaminated := golden(t), golden(t)
	clean.Model, clean.Scorecard.Model = "clean", "clean"
	contaminated.Model, contaminated.Scorecard.Model = "contaminated", "contaminated"
	contaminated.Contamination = []string{"leftover:7b"}
	sealCurrentResult(t, clean, contaminated)
	saveCurrentResults(t, clean, contaminated)

	readBoard := func(t *testing.T, full bool) map[string]any {
		t.Helper()
		args := []string{"--display=json"}
		if full {
			args = append(args, "--full")
		}
		out, code := captureTopStdout(t, func() int {
			return cmdBoard(context.Background(), args)
		})
		if code != exitOK {
			t.Fatalf("board exit=%d\n%s", code, out)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(out), &payload); err != nil {
			t.Fatalf("board JSON: %v\n%s", err, out)
		}
		if payload["inconclusive_excluded"] != float64(1) {
			t.Fatalf("board exclusion receipt = %v", payload["inconclusive_excluded"])
		}
		return payload
	}

	brief := readBoard(t, false)
	if brief["schema"] != "fitr.board.v1" || brief["results"] != float64(1) {
		t.Fatalf("brief board identity = %+v", brief)
	}
	full := readBoard(t, true)
	if full["schema"] != "fitr.board.full.v1" {
		t.Fatalf("full board identity = %+v", full)
	}
}
