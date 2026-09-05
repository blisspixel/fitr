package workload

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/strictjson"
)

type eventRecorder struct {
	started time.Time
	events  []Event
	err     error
}

func newEventRecorder() *eventRecorder {
	return &eventRecorder{started: time.Now()}
}

func (recorder *eventRecorder) add(eventType EventType, actor, status, tool string, evidence any) string {
	event := Event{
		Sequence: len(recorder.events) + 1, ElapsedMillis: time.Since(recorder.started).Milliseconds(),
		Type: eventType, Actor: actor, Attempt: 1, Tool: tool, Status: status,
	}
	if evidence != nil {
		digest, err := hashValue("fitr.workload.event.v1", evidence)
		if err != nil && recorder.err == nil {
			recorder.err = fmt.Errorf("encode workload event evidence: %w", err)
		}
		event.EvidenceSHA256 = digest
	}
	recorder.events = append(recorder.events, event)
	return event.EvidenceSHA256
}

func (sealed *SealedPlan) Run(ctx context.Context, backend llm.Backend) (Bundle, error) {
	if sealed == nil || len(sealed.privateKey) != ed25519.PrivateKeySize {
		return Bundle{}, errors.New("workload plan signing key is unavailable")
	}
	if backend == nil {
		return Bundle{}, errors.New("workload backend is required")
	}
	if err := sealed.Plan.Validate(); err != nil {
		return Bundle{}, err
	}
	defer func() {
		clear(sealed.privateKey)
		sealed.privateKey = nil
	}()
	trials := make([]Trial, 0, sealed.Plan.Trials)
	for index := 1; index <= sealed.Plan.Trials; index++ {
		if err := ctx.Err(); err != nil {
			return Bundle{}, err
		}
		trial, err := sealed.runTrial(ctx, backend, index)
		if err != nil {
			return Bundle{}, err
		}
		trials = append(trials, trial)
	}
	report := Analyze(sealed.Plan, trials)
	bundle := Bundle{Schema: BundleSchema, Plan: sealed.Plan, Trials: trials, Report: report}
	if err := bundle.Validate(); err != nil {
		return Bundle{}, err
	}
	return bundle, nil
}

func (sealed *SealedPlan) runTrial(ctx context.Context, backend llm.Backend, index int) (Trial, error) {
	recorder := newEventRecorder()
	state := newWorkflowState()
	recorder.add(EventScenarioReleased, "harness", "released", "", map[string]any{
		"workflow": WorkflowID, "version": WorkflowVersion, "trial": index,
	})
	recorder.add(EventWorkerStarted, "worker", "started", "", nil)
	trialCtx, cancel := context.WithTimeout(ctx, time.Duration(sealed.Plan.TimeoutSeconds)*time.Second)
	defer cancel()
	execution := executeWorkflow(trialCtx, backend, sealed.Plan, &state, recorder)
	if ctx.Err() != nil {
		return Trial{}, ctx.Err()
	}
	recorder.add(EventVerifierQueued, "harness", "queued", "", nil)
	recorder.add(EventVerifierStarted, "verifier", "started", "", nil)
	verification := verifyWorkflow(state)
	recorder.add(EventVerifierCompleted, "verifier", boolStatus(verification.Accepted), "", verification)
	outcome := terminalOutcome(execution, verification, state.authorityViolations)
	recorder.add(terminalEvent(outcome), "harness", string(outcome), "", verification)
	if recorder.err != nil {
		return Trial{}, recorder.err
	}
	trial := Trial{
		Schema: TrialSchema, PlanSHA256: sealed.Plan.PlanSHA256,
		TrialID: fmt.Sprintf("%s:%d", sealed.Plan.PlanSHA256, index), Index: index,
		Events: recorder.events, Outcome: outcome, ElapsedMillis: time.Since(recorder.started).Milliseconds(),
		Attempts: 1, Turns: execution.turns, ToolCalls: execution.toolCalls,
		DuplicateCalls: execution.duplicateCalls, AuthorityViolations: state.authorityViolations,
		Verifier: verification,
	}
	if err := sealed.signTrial(&trial); err != nil {
		return Trial{}, err
	}
	return trial, nil
}

type workflowExecution struct {
	turns, toolCalls, duplicateCalls int
	cleanStop                        bool
	timedOut, infrastructureFault    bool
}

const maximumToolCallsPerTurn = 16

func executeWorkflow(ctx context.Context, backend llm.Backend, plan Plan,
	state *workflowState, recorder *eventRecorder) workflowExecution {
	execution := workflowExecution{}
	messages := []ollama.Message{{Role: "user", Content: workflowPrompt}}
	sampling := ollama.Deterministic(800, plan.RequestedContext)
	seenCalls := make(map[string]int)
	for turn := 1; turn <= plan.MaxTurns; turn++ {
		execution.turns = turn
		recorder.add(EventModelStarted, "worker", "started", "", map[string]int{"turn": turn})
		message, metrics, err := backend.Chat(ctx, plan.Model.Resolved, messages, workflowTools(), sampling)
		contextErr := ctx.Err()
		if err != nil || contextErr != nil {
			return failedModelRequest(execution, contextErr, err, recorder)
		}
		recorder.add(EventModelCompleted, "worker", "completed", "", struct {
			Message ollama.Message `json:"message"`
			Metrics ollama.Metrics `json:"metrics"`
		}{message, metrics})
		if len(message.ToolCalls) > maximumToolCallsPerTurn {
			recorder.add(EventWorkerCompleted, "worker", "tool_call_cap", "", nil)
			return execution
		}
		if len(message.ToolCalls) == 0 {
			execution.cleanStop = strings.EqualFold(strings.TrimSpace(message.Content), "DONE")
			recorder.add(EventWorkerCompleted, "worker", cleanStopStatus(execution.cleanStop), "", nil)
			return execution
		}
		messages = append(messages, message)
		for _, call := range message.ToolCalls {
			execution.toolCalls++
			arguments := canonicalToolArguments(call.Function.Arguments)
			name := retainedToolName(call.Function.Name)
			evidence := recorder.add(EventToolStarted, "harness", "started", name, arguments)
			signature := name + "\x00" + evidence
			seenCalls[signature]++
			if seenCalls[signature] > 1 {
				execution.duplicateCalls++
			}
			result, status := state.invoke(call.Function.Name, call.Function.Arguments)
			recorder.add(EventToolCompleted, "harness", status, name, result)
			messages = append(messages, ollama.Message{
				Role: "tool", ToolName: call.Function.Name, ToolCallID: call.ID, Content: result,
			})
		}
	}
	recorder.add(EventWorkerCompleted, "worker", "turn_cap", "", nil)
	return execution
}

// Only harness-owned names are retained verbatim. A digest keeps unknown calls
// distinct for duplicate accounting without persisting arbitrary model text.
func retainedToolName(name string) string {
	switch name {
	case "list_files", "read_file", "write_file", "run_checks":
		return name
	default:
		digest, _ := hashValue("fitr.workload.tool-name.v1", name)
		return "unknown:" + digest
	}
}

func canonicalToolArguments(raw json.RawMessage) any {
	var arguments any
	if len(raw) > 0 && strictjson.Unmarshal(raw, &arguments) == nil {
		return arguments
	}
	return struct {
		Raw string `json:"raw"`
	}{Raw: string(raw)}
}

func failedModelRequest(execution workflowExecution, contextErr, requestErr error,
	recorder *eventRecorder) workflowExecution {
	execution.timedOut = errors.Is(contextErr, context.DeadlineExceeded)
	execution.infrastructureFault = !execution.timedOut
	status := "error"
	if execution.timedOut {
		status = "timeout"
	}
	if requestErr == nil {
		requestErr = contextErr
	}
	recorder.add(EventModelCompleted, "worker", status, "", errorEvidence(requestErr))
	recorder.add(EventWorkerCompleted, "worker", status, "", nil)
	return execution
}

func terminalOutcome(execution workflowExecution, verification VerifierReceipt, authorityViolations int) Outcome {
	switch {
	case execution.timedOut:
		return OutcomeTimedOut
	case execution.infrastructureFault:
		return OutcomeInfrastructure
	case execution.cleanStop && verification.Accepted && authorityViolations == 0:
		return OutcomeAccepted
	default:
		return OutcomeRejected
	}
}

func terminalEvent(outcome Outcome) EventType {
	switch outcome {
	case OutcomeAccepted:
		return EventAccepted
	case OutcomeTimedOut:
		return EventTimedOut
	case OutcomeInfrastructure:
		return EventInfrastructure
	default:
		return EventRejected
	}
}

func (sealed *SealedPlan) signTrial(trial *Trial) error {
	evidence, err := trialDigest(*trial)
	if err != nil {
		return err
	}
	trial.EvidenceSHA256 = evidence
	trial.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(sealed.privateKey, []byte(evidence)))
	return nil
}

func trialDigest(trial Trial) (string, error) {
	trial.EvidenceSHA256, trial.Signature = "", ""
	return hashValue("fitr.workload.trial.v1", trial)
}

func boolStatus(value bool) string {
	if value {
		return "passed"
	}
	return "failed"
}

func cleanStopStatus(value bool) string {
	if value {
		return "clean_stop"
	}
	return "stopped_without_done"
}

func errorEvidence(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
