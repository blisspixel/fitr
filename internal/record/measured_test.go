package record

import (
	"testing"

	"github.com/blisspixel/fitr/internal/eval"
)

func TestCodeDispositionSeparatesExtractionFromAnIsolationSkip(t *testing.T) {
	extracted := &Record{
		TaskPlan: TaskPlan{CodeTrials: 2},
		CodeWrite: []eval.ExecResult{{
			Outcome: eval.OutcomeInconclusive,
			Detail:  "no executable code was extracted",
		}},
		CodeFix: []eval.ExecResult{{
			Outcome: eval.OutcomeInconclusive,
			Detail:  "no executable code was extracted",
		}},
	}
	got := extracted.Measured()
	if !got.CodePlanned || !got.CodeAttempted || !got.CodeExtractionMiss || got.CodeKnown {
		t.Fatalf("extraction miss = %+v", got)
	}

	skipped := &Record{
		TaskPlan: TaskPlan{CodeTrials: 2},
		CodeWrite: []eval.ExecResult{{
			Outcome: eval.OutcomeSkipped,
			Detail:  "isolated execution is unavailable: bubblewrap (bwrap) is not installed",
		}},
		CodeFix: []eval.ExecResult{{
			Outcome: eval.OutcomeSkipped,
			Detail:  "isolated execution is unavailable: bubblewrap (bwrap) is not installed",
		}},
	}
	skip := skipped.Measured()
	if !skip.CodePlanned || skip.CodeAttempted || skip.CodeExtractionMiss || skip.CodeKnown {
		t.Fatalf("isolation skip = %+v", skip)
	}
	if skip.CodeSkipDetail != "isolated execution is unavailable: bubblewrap (bwrap) is not installed" {
		t.Fatalf("skip detail = %q", skip.CodeSkipDetail)
	}

	mixed := &Record{
		TaskPlan: TaskPlan{CodeTrials: 2},
		CodeWrite: []eval.ExecResult{{
			Outcome: eval.OutcomeInconclusive,
			Detail:  "no executable code was extracted",
		}},
		CodeFix: []eval.ExecResult{{Outcome: eval.OutcomePass, Pass: true}},
	}
	if mixed.Measured().CodeExtractionMiss {
		t.Fatal("a measured fix was called an extraction miss")
	}
}

func TestDisagreeingSkipReasonsStayUnmeasured(t *testing.T) {
	disagreed := &Record{
		TaskPlan: TaskPlan{CodeTrials: 2},
		CodeWrite: []eval.ExecResult{{
			Outcome: eval.OutcomeSkipped,
			Detail:  "isolated execution is unavailable: bubblewrap (bwrap) is not installed",
		}},
		CodeFix: []eval.ExecResult{{
			Outcome: eval.OutcomeSkipped,
			Detail:  "isolated execution is unavailable: /usr/bin/prlimit is not installed",
		}},
	}
	if detail := disagreed.Measured().CodeSkipDetail; detail != "" {
		t.Fatalf("disagreeing skips became one reason: %q", detail)
	}
}

func TestAgenticAttemptIsDistinctFromAnIsolationSkip(t *testing.T) {
	ran := &Record{
		TaskPlan: TaskPlan{AgenticTrials: 1},
		Agentic:  &eval.ToolLoopResult{Outcome: eval.OutcomeInconclusive, Detail: "verifier output"},
	}
	if !ran.Measured().AgenticAttempted || ran.Measured().AgenticRan {
		t.Fatalf("attempted agent = %+v", ran.Measured())
	}
	skipped := &Record{
		TaskPlan: TaskPlan{AgenticTrials: 1},
		Agentic: &eval.ToolLoopResult{
			Outcome: eval.OutcomeSkipped,
			Ended:   "isolated_execution_unavailable",
		},
	}
	if skipped.Measured().AgenticAttempted || skipped.Measured().AgenticRan {
		t.Fatalf("skipped agent = %+v", skipped.Measured())
	}
}
