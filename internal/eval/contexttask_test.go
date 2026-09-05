package eval

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/contextquality"
	"github.com/blisspixel/fitr/internal/ollama"
)

const contextSeedSet = "0123456789abcdef0123456789abcdef"

type contextCall struct {
	model    string
	prompt   string
	sampling ollama.Sampling
}

// contextFake never sees an expected answer, exactly as the real adapter never
// does. A reply is chosen by cell index alone.
type contextFake struct {
	policy ollama.ContextRequestPolicy
	calls  []contextCall
	reply  func(index int) (string, ollama.Metrics, error)
}

func (f *contextFake) ContextRequestPolicy() ollama.ContextRequestPolicy { return f.policy }

func (f *contextFake) Generate(_ context.Context, model, prompt string, s ollama.Sampling) (string, ollama.Metrics, error) {
	index := len(f.calls)
	f.calls = append(f.calls, contextCall{model: model, prompt: prompt, sampling: s})
	return f.reply(index)
}

func contextAccounting(prompt, cached, output int) *ollama.ContextTokenAccounting {
	return &ollama.ContextTokenAccounting{PromptTokens: &prompt, CachedPromptTokens: &cached, OutputTokens: &output}
}

// answered is a well-formed but wrong reply that leaves the full reserve free.
func answered() (string, ollama.Metrics, error) {
	return `{"answer":"none"}`, ollama.Metrics{ContextAccounting: contextAccounting(1000, 0, 6)}, nil
}

func contextPlan(t *testing.T, tiers ...int) contextquality.Plan {
	t.Helper()
	policy, err := contextquality.NewPolicy(8192, tiers)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := contextquality.NewPlan(policy, contextSeedSet)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func runContextFake(t *testing.T, plan contextquality.Plan, reply func(int) (string, ollama.Metrics, error)) (ContextTaskRun, *contextFake) {
	t.Helper()
	fake := &contextFake{policy: ollama.PreserveContextV1, reply: reply}
	run, err := RunContextTasks(t.Context(), fake, "candidate", plan)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	return run, fake
}

func countReasons(run ContextTaskRun) map[string]int {
	reasons := map[string]int{}
	for _, cell := range run.Report.Cells {
		reasons[cell.Reason]++
	}
	return reasons
}

// The adapter must submit the sealed prompt unchanged at the declared window
// and reserve. It holds no expected answer, so it cannot leak one into a
// request even by accident.
func TestContextRunSubmitsTheSealedPromptAtTheDeclaredWindow(t *testing.T) {
	plan := contextPlan(t, 2048, 4096)
	run, fake := runContextFake(t, plan, func(int) (string, ollama.Metrics, error) { return answered() })
	if len(fake.calls) != len(plan.Cells) || len(run.Observations) != len(plan.Cells) {
		t.Fatalf("calls=%d observations=%d cells=%d", len(fake.calls), len(run.Observations), len(plan.Cells))
	}
	want := ollama.Deterministic(contextquality.OutputReserveTokens, plan.Policy.OperatingWindowTokens)
	for index, call := range fake.calls {
		task, err := contextquality.Generate(plan, index+1)
		if err != nil {
			t.Fatal(err)
		}
		if call.model != "candidate" || call.prompt != task.Prompt || call.sampling != want {
			t.Fatalf("cell %d sent model=%q sampling=%+v prompt_match=%v", index+1, call.model, call.sampling, call.prompt == task.Prompt)
		}
		if strings.Contains(call.prompt, "expected") && !strings.Contains(task.Payload, "expected") {
			t.Fatalf("cell %d prompt gained content the plan did not seal", index+1)
		}
	}
}

// The report is the pure analyzer's, not the adapter's. Nothing a backend
// claims about its own success reaches a verdict.
func TestContextRunReportIsRederivedFromObservationsAlone(t *testing.T) {
	plan := contextPlan(t, 2048, 4096)
	run, _ := runContextFake(t, plan, func(int) (string, ollama.Metrics, error) { return answered() })
	independent, err := contextquality.Analyze(plan, run.Observations)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(run.Report, independent) {
		t.Fatal("report is not the analyzer's conclusion about the recorded observations")
	}
	if run.Report.Outcome != contextquality.Fail || run.Report.Counts.Pass != 0 || !run.Report.Complete {
		t.Fatalf("a wrong answer was not graded as a complete failure: %+v", run.Report.Counts)
	}
	if run.Report.VerifiedPrefixUTF8Bytes != nil || run.Report.KnownPrefixUTF8Bytes != nil || run.Report.AtLeastLargestTested {
		t.Fatalf("failing cells produced a qualified prefix: %+v", run.Report)
	}
}

// A terminal success whose prompt leaves no room for the whole declared
// reserve fails, even though the model returned a complete short answer.
func TestContextReserveViolationFailsAnOtherwiseCompleteAnswer(t *testing.T) {
	plan := contextPlan(t, 2048, 4096)
	window, reserve := plan.Policy.OperatingWindowTokens, plan.Policy.OutputReserveTokens
	for _, tc := range []struct {
		name           string
		prompt, output int
		reason         string
	}{
		{"exactly fits", window - reserve, 6, "answer_fields"},
		{"one token over", window - reserve + 1, 6, "context_limit"},
		{"short answer hides the overflow", window - 1, 1, "context_limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run, _ := runContextFake(t, plan, func(int) (string, ollama.Metrics, error) {
				return `{"answer":"none"}`, ollama.Metrics{ContextAccounting: contextAccounting(tc.prompt, 0, tc.output)}, nil
			})
			if got := countReasons(run)[tc.reason]; got != len(plan.Cells) {
				t.Fatalf("reason %q covered %d of %d cells: %v", tc.reason, got, len(plan.Cells), countReasons(run))
			}
			if run.Report.Counts.Unavailable != 0 || run.Report.Counts.Fail != len(plan.Cells) {
				t.Fatalf("reserve outcome was not a complete failure: %+v", run.Report.Counts)
			}
		})
	}
}

// Missing or inconsistent native counts are an evidence gap, never a zero.
func TestContextUnknownAccountingIsUnavailableNotFailure(t *testing.T) {
	plan := contextPlan(t, 2048, 4096)
	for name, metrics := range map[string]ollama.Metrics{
		"no receipt":        {},
		"absent cache":      {ContextAccounting: &ollama.ContextTokenAccounting{PromptTokens: intPtr(10), OutputTokens: intPtr(1)}},
		"absent prompt":     {ContextAccounting: &ollama.ContextTokenAccounting{CachedPromptTokens: intPtr(0), OutputTokens: intPtr(1)}},
		"cache over prompt": {ContextAccounting: contextAccounting(10, 11, 1)},
		"output over cap":   {ContextAccounting: contextAccounting(10, 0, contextquality.OutputReserveTokens+1)},
	} {
		t.Run(name, func(t *testing.T) {
			run, fake := runContextFake(t, plan, func(int) (string, ollama.Metrics, error) {
				return `{"answer":"none"}`, metrics, nil
			})
			if len(fake.calls) != len(plan.Cells) || run.Ended != nil {
				t.Fatalf("unknown accounting stopped the phase: calls=%d ended=%v", len(fake.calls), run.Ended)
			}
			if run.Report.Counts.Unavailable != len(plan.Cells) || run.Report.Counts.Fail != 0 || run.Report.Complete {
				t.Fatalf("accounting gap became a quality result: %+v", run.Report)
			}
			if countReasons(run)["accounting_unknown"] != len(plan.Cells) {
				t.Fatalf("unexpected reasons: %v", countReasons(run))
			}
			if run.Report.VerifiedPrefixUTF8Bytes != nil {
				t.Fatal("incomplete phase established a verified prefix")
			}
		})
	}
}

func intPtr(n int) *int { return &n }

// Output-cap termination is a failed cell under the fixed reserve, and its
// partial reply is retained only within the declared answer bound.
func TestContextOutputLimitFailsAndBoundsItsPartialAnswer(t *testing.T) {
	plan := contextPlan(t, 2048, 4096)
	long := strings.Repeat("x", contextquality.MaxAnswerBytes*3)
	run, _ := runContextFake(t, plan, func(int) (string, ollama.Metrics, error) {
		return long, ollama.Metrics{Truncated: true, ContextAccounting: contextAccounting(1000, 0, contextquality.OutputReserveTokens)}, nil
	})
	if run.Report.Counts.Fail != len(plan.Cells) || countReasons(run)["output_limit"] != len(plan.Cells) {
		t.Fatalf("output limit was not a complete failure: %+v %v", run.Report.Counts, countReasons(run))
	}
	for _, observation := range run.Observations {
		if len(observation.Answer) > contextquality.MaxAnswerBytes {
			t.Fatalf("retained %d answer bytes past the bound", len(observation.Answer))
		}
	}
}

// An oversized complete answer fails on its size without copying its bytes.
func TestContextOversizedAnswerFailsWithoutUnboundedRetention(t *testing.T) {
	plan := contextPlan(t, 2048, 4096)
	long := strings.Repeat("y", contextquality.MaxAnswerBytes*4)
	run, _ := runContextFake(t, plan, func(int) (string, ollama.Metrics, error) {
		return long, ollama.Metrics{ContextAccounting: contextAccounting(1000, 0, 6)}, nil
	})
	if countReasons(run)["answer_too_large"] != len(plan.Cells) {
		t.Fatalf("unexpected reasons: %v", countReasons(run))
	}
	for _, observation := range run.Observations {
		if len(observation.Answer) != contextquality.MaxAnswerBytes+1 {
			t.Fatalf("retained %d answer bytes rather than the failing bound", len(observation.Answer))
		}
	}
}

// A refused reservation, cancellation, a runtime that cannot be shown local,
// and an invalid request policy each end the phase. Every later cell is
// explicitly not attempted rather than silently missing.
func TestContextFatalFaultsEndThePhaseAndMarkTheRestNotAttempted(t *testing.T) {
	plan := contextPlan(t, 2048, 4096)
	for _, tc := range []struct {
		name   string
		err    error
		reason string
	}{
		{"admission refused", ollama.ErrInferenceAdmission, "not_attempted"},
		{"cancelled", context.Canceled, "cancelled"},
		{"deadline", context.DeadlineExceeded, "cancelled"},
		{"remote execution", ollama.ErrRemoteExecution, "runtime_unverified"},
		{"invalid policy", ollama.ErrContextRequestPolicy, "runtime_unverified"},
		{"wrapped", fmt.Errorf("dispatch: %w", ollama.ErrInferenceAdmission), "not_attempted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run, fake := runContextFake(t, plan, func(index int) (string, ollama.Metrics, error) {
				if index == 3 {
					return "", ollama.Metrics{}, tc.err
				}
				return answered()
			})
			if len(fake.calls) != 4 {
				t.Fatalf("phase continued past a fatal fault: %d calls", len(fake.calls))
			}
			if !errors.Is(run.Ended, tc.err) {
				t.Fatalf("Ended = %v, want %v", run.Ended, tc.err)
			}
			for index, cell := range run.Report.Cells {
				want := "not_attempted"
				switch {
				case index < 3:
					want = "answer_fields"
				case index == 3:
					want = tc.reason
				}
				if cell.Reason != want {
					t.Fatalf("cell %d reason = %q, want %q", index+1, cell.Reason, want)
				}
			}
			if run.Report.Complete || run.Report.VerifiedPrefixUTF8Bytes != nil {
				t.Fatalf("an ended phase qualified: %+v", run.Report)
			}
		})
	}
}

// A transport fault is recorded for its own cell and the phase continues, so
// the remaining cells still produce diagnostics. It never becomes a failure.
func TestContextTransportFaultIsRecordedWithoutEndingThePhase(t *testing.T) {
	plan := contextPlan(t, 2048, 4096)
	for name, err := range map[string]error{
		"network":    &net.OpError{Op: "dial", Err: errors.New("refused")},
		"http":       errors.New("ollama 500: internal error"),
		"accounting": ollama.ErrContextAccounting,
	} {
		t.Run(name, func(t *testing.T) {
			run, fake := runContextFake(t, plan, func(index int) (string, ollama.Metrics, error) {
				if index%2 == 0 {
					return "", ollama.Metrics{}, err
				}
				return answered()
			})
			if len(fake.calls) != len(plan.Cells) || run.Ended != nil {
				t.Fatalf("phase stopped early: calls=%d ended=%v", len(fake.calls), run.Ended)
			}
			if run.Report.Counts.Unavailable != len(plan.Cells)/2 || run.Report.Complete {
				t.Fatalf("transport faults were not held unavailable: %+v", run.Report.Counts)
			}
			if run.Report.VerifiedPrefixUTF8Bytes != nil {
				t.Fatal("a phase with unavailable cells established a verified prefix")
			}
		})
	}
}

// A client that would not send the declared overflow controls dispatches
// nothing at all. The same holds for an unusable plan or model.
func TestContextRunRefusesBeforeAnyRequest(t *testing.T) {
	plan := contextPlan(t, 2048, 4096)
	for _, tc := range []struct {
		name   string
		policy ollama.ContextRequestPolicy
		model  string
		plan   contextquality.Plan
	}{
		{"legacy client", "", "candidate", plan},
		{"other policy", "ollama.something_else.v1", "candidate", plan},
		{"no model", ollama.PreserveContextV1, "", plan},
		{"empty plan", ollama.PreserveContextV1, "candidate", contextquality.Plan{}},
		{"tampered cells", ollama.PreserveContextV1, "candidate", tamperedPlan(plan)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &contextFake{policy: tc.policy, reply: func(int) (string, ollama.Metrics, error) {
				t.Fatal("a refused run reached the runtime")
				return "", ollama.Metrics{}, nil
			}}
			run, err := RunContextTasks(t.Context(), fake, tc.model, tc.plan)
			if err == nil || len(fake.calls) != 0 || run.Observations != nil || run.Ended != nil {
				t.Fatalf("run=%+v calls=%d error=%v", run, len(fake.calls), err)
			}
			if !reflect.DeepEqual(run.Report, contextquality.Report{}) {
				t.Fatalf("a refused run produced a report: %+v", run.Report)
			}
		})
	}
	if _, err := RunContextTasks(t.Context(), nil, "candidate", plan); !errors.Is(err, ollama.ErrContextRequestPolicy) {
		t.Fatalf("a missing backend was accepted: %v", err)
	}
}

func tamperedPlan(plan contextquality.Plan) contextquality.Plan {
	tampered := plan
	tampered.Cells = append([]contextquality.Cell(nil), plan.Cells...)
	tampered.Cells[0].PayloadSHA256 = "sha256:" + strings.Repeat("0", 64)
	return tampered
}

// The real client satisfies the narrow surface, so the adapter cannot be
// wired to a backend that silently drops the declared controls.
func TestOllamaClientReportsItsConfiguredContextPolicy(t *testing.T) {
	var backend ContextBackend = &ollama.Client{ContextPolicy: ollama.PreserveContextV1}
	if backend.ContextRequestPolicy() != ollama.PreserveContextV1 {
		t.Fatal("configured client did not report its policy")
	}
	if (&ollama.Client{}).ContextRequestPolicy() != "" {
		t.Fatal("unconfigured client claimed a context policy")
	}
}
