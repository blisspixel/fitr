package workload

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/ollama"
)

type piState struct {
	files        map[string]string
	sealedFiles  map[string]string
	summaries    []string
	seen         map[string]int
	checkpoint   string
	noteSHA      string
	resumed      bool
	authority    int
	effects      int
	summaryCalls int
}

func newPiState() piState {
	note, _ := hashValue("fitr.workload.protected.v1", piNoteBody)
	return piState{
		files: map[string]string{piNoteFile: piNoteBody, piTaskFile: piTaskOpen},
		seen:  map[string]int{}, noteSHA: note,
	}
}

func (sealed *SealedPlan) runPiTrial(ctx context.Context, backend llm.Backend, index int) (Trial, error) {
	recorder := newEventRecorder()
	state := newPiState()
	recorder.add(EventScenarioReleased, "harness", "released", "", map[string]any{
		"workflow": PiWorkflowID, "version": WorkflowVersion, "trial": index,
	})
	recorder.add(EventWorkerStarted, "worker", "started", "", nil)
	trialCtx, cancel := context.WithTimeout(ctx, time.Duration(sealed.Plan.TimeoutSeconds)*time.Second)
	defer cancel()
	execution, err := executePiSession(trialCtx, backend, sealed.Plan, &state, recorder)
	if err != nil {
		return Trial{}, err
	}
	if ctx.Err() != nil {
		return Trial{}, ctx.Err()
	}
	if recorder.err != nil {
		return Trial{}, recorder.err
	}
	receipt := verifyPi(&state)
	return sealed.finishPiTrial(recorder, execution, receipt, state, index)
}

func (sealed *SealedPlan) finishPiTrial(recorder *eventRecorder, execution workflowExecution, receipt VerifierReceipt, state piState, index int) (Trial, error) {
	recorder.add(EventVerifierQueued, "harness", "queued", "", nil)
	recorder.add(EventVerifierStarted, "verifier", "started", "", nil)
	recorder.add(EventVerifierCompleted, "verifier", boolStatus(receipt.Accepted), "", receipt)
	outcome := terminalOutcome(execution, receipt, state.authority)
	recorder.add(terminalEvent(outcome), "harness", string(outcome), "", receipt)
	if recorder.err != nil {
		return Trial{}, recorder.err
	}
	trial := Trial{
		Schema: TrialSchema, PlanSHA256: sealed.Plan.PlanSHA256,
		TrialID: fmt.Sprintf("%s:%d", sealed.Plan.PlanSHA256, index), Index: index,
		Events: recorder.events, Outcome: outcome, ElapsedMillis: time.Since(recorder.started).Milliseconds(),
		Attempts: 1, Turns: execution.turns, ToolCalls: execution.toolCalls,
		DuplicateCalls: execution.duplicateCalls, AuthorityViolations: state.authority,
		Verifier: receipt, SummaryCalls: state.summaryCalls, Effects: state.effects,
	}
	if err := sealed.signTrial(&trial); err != nil {
		return Trial{}, err
	}
	return trial, nil
}

func executePiSession(ctx context.Context, backend llm.Backend, plan Plan, state *piState, recorder *eventRecorder) (workflowExecution, error) {
	execution := workflowExecution{}
	messages := []ollama.Message{{Role: "user", Content: piPrompt}}
	if !piOrdinaryTurns(ctx, backend, plan, state, recorder, &execution, &messages) {
		return execution, nil
	}
	if !piSummaryTurns(ctx, backend, plan, state, recorder, &execution, messages) {
		return execution, nil
	}
	return piReopenTurn(ctx, backend, plan, state, recorder, execution)
}

func piOrdinaryTurns(ctx context.Context, backend llm.Backend, plan Plan, state *piState, recorder *eventRecorder, execution *workflowExecution, messages *[]ollama.Message) bool {
	for range piOrdinaryCap {
		message, ok := piModelRequest(ctx, backend, plan, state, recorder, execution, *messages, "", true)
		if !ok {
			return false
		}
		if len(message.ToolCalls) == 0 {
			return true
		}
		*messages = append(*messages, message)
		*messages = appendPiTools(state, recorder, execution, *messages, message)
	}
	return true
}

func piSummaryTurns(ctx context.Context, backend llm.Backend, plan Plan, state *piState, recorder *eventRecorder, execution *workflowExecution, messages []ollama.Message) bool {
	for index := 1; index <= piSummaryCalls; index++ {
		prompt := piSummaryPrompt
		if index > 1 && len(state.summaries) > 0 {
			prompt += "\n\nPrevious summary:\n" + state.summaries[len(state.summaries)-1]
		}
		history := append(append([]ollama.Message{}, messages...), ollama.Message{Role: "user", Content: prompt})
		if _, ok := piModelRequest(ctx, backend, plan, state, recorder, execution, history, piSummaryClass, false); !ok {
			return false
		}
	}
	return true
}

func piReopenTurn(ctx context.Context, backend llm.Backend, plan Plan, state *piState, recorder *eventRecorder, execution workflowExecution) (workflowExecution, error) {
	if err := state.sealCheckpoint(); err != nil {
		return execution, err
	}
	recorder.add(EventCompactionStarted, "harness", "compacting", "", nil)
	recorder.add(EventCompactionCompleted, "harness", "compacted", "", map[string]string{
		"checkpoint_sha256": state.checkpoint,
	})
	if err := state.resume(); err != nil {
		return execution, err
	}
	recorder.add(EventCheckpointResumed, "harness", "resumed", "", nil)
	messages := []ollama.Message{{Role: "user", Content: piPrompt}}
	for _, summary := range state.summaries {
		messages = append(messages, ollama.Message{Role: "user", Content: "Summary:\n" + summary})
	}
	messages = append(messages, ollama.Message{Role: "user", Content: piReopenPrompt})
	message, ok := piModelRequest(ctx, backend, plan, state, recorder, &execution, messages, "", true)
	if !ok {
		return execution, nil
	}
	if len(message.ToolCalls) == 0 {
		execution.cleanStop = strings.EqualFold(strings.TrimSpace(message.Content), "DONE")
	} else {
		appendPiTools(state, recorder, &execution, nil, message)
	}
	recorder.add(EventWorkerCompleted, "worker", cleanStopStatus(execution.cleanStop), "", nil)
	return execution, nil
}

func piModelRequest(ctx context.Context, backend llm.Backend, plan Plan, state *piState, recorder *eventRecorder, execution *workflowExecution, messages []ollama.Message, class string, allowTools bool) (ollama.Message, bool) {
	if execution.turns >= plan.RequestBudget {
		recorder.add(EventWorkerCompleted, "worker", "turn_cap", "", nil)
		return ollama.Message{}, false
	}
	execution.turns++
	if class == piSummaryClass {
		state.summaryCalls++
	}
	recorder.add(EventModelStarted, "worker", "started", "", map[string]string{"class": class})
	markLastClass(recorder, class)
	tools := []ollama.Tool{}
	if allowTools {
		tools = piWorkspaceTools()
	}
	sampling := ollama.Deterministic(800, plan.RequestedContext)
	message, metrics, err := backend.Chat(ctx, plan.Model.Resolved, messages, tools, sampling)
	if err != nil || ctx.Err() != nil {
		failed := failedModelRequest(*execution, ctx.Err(), err, recorder)
		*execution = failed
		markModelCompletedClass(recorder, class)
		return ollama.Message{}, false
	}
	recorder.add(EventModelCompleted, "worker", "completed", "", struct {
		Message ollama.Message `json:"message"`
		Metrics ollama.Metrics `json:"metrics"`
	}{message, metrics})
	markLastClass(recorder, class)
	if class == piSummaryClass {
		return message, acceptSummary(state, message)
	}
	return message, true
}

func acceptSummary(state *piState, message ollama.Message) bool {
	// Tool calls and oversize text leave authority. A blank reply is only a
	// missing summary: it is not stored and it is not an authority breach.
	if len(message.ToolCalls) > 0 || len(message.Content) > piSummaryLimit {
		state.authority++
		return true
	}
	if strings.TrimSpace(message.Content) == "" {
		return true
	}
	state.summaries = append(state.summaries, message.Content)
	return true
}

func appendPiTools(state *piState, recorder *eventRecorder, execution *workflowExecution, messages []ollama.Message, message ollama.Message) []ollama.Message {
	if len(message.ToolCalls) > maximumToolCallsPerTurn {
		state.authority++
		return messages
	}
	for _, call := range message.ToolCalls {
		execution.toolCalls++
		name := retainedToolName(call.Function.Name)
		evidence := recorder.add(EventToolStarted, "harness", "started", name, canonicalToolArguments(call.Function.Arguments))
		signature := name + "\x00" + evidence
		state.seen[signature]++
		if state.seen[signature] > 1 {
			execution.duplicateCalls++
		}
		result, status := state.invoke(call.Function.Name, call.Function.Arguments)
		recorder.add(EventToolCompleted, "harness", status, name, result)
		messages = append(messages, ollama.Message{
			Role: "tool", ToolName: call.Function.Name, ToolCallID: call.ID, Content: result,
		})
	}
	return messages
}

func markLastClass(recorder *eventRecorder, class string) {
	if class == "" || len(recorder.events) == 0 {
		return
	}
	recorder.events[len(recorder.events)-1].Class = class
}

func markModelCompletedClass(recorder *eventRecorder, class string) {
	if class == "" {
		return
	}
	for index := len(recorder.events) - 1; index >= 0; index-- {
		if recorder.events[index].Type == EventModelCompleted {
			recorder.events[index].Class = class
			return
		}
	}
}

func (state *piState) sealCheckpoint() error {
	state.sealedFiles = cloneStringMap(state.files)
	digest, err := piCheckpointDigest(state.sealedFiles, state.summaries)
	if err != nil {
		return err
	}
	state.checkpoint = digest
	state.resumed = false
	return nil
}

func (state *piState) resume() error {
	if len(state.sealedFiles) == 0 || state.checkpoint == "" {
		return errors.New("pi checkpoint does not match the sealed session")
	}
	digest, err := piCheckpointDigest(state.sealedFiles, state.summaries)
	if err != nil {
		return err
	}
	if digest != state.checkpoint {
		return errors.New("pi checkpoint does not match the sealed session")
	}
	state.files = cloneStringMap(state.sealedFiles)
	state.resumed = true
	return nil
}

func piCheckpointDigest(files map[string]string, summaries []string) (string, error) {
	type entry struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	payload := struct {
		Files     []entry  `json:"files"`
		Summaries []string `json:"summaries"`
	}{Summaries: append([]string{}, summaries...)}
	if payload.Summaries == nil {
		payload.Summaries = []string{}
	}
	names := make([]string, 0, len(files))
	for path := range files {
		names = append(names, path)
	}
	sort.Strings(names)
	for _, path := range names {
		payload.Files = append(payload.Files, entry{Path: path, Content: files[path]})
	}
	return hashValue("fitr.workload.pi-checkpoint.v1", payload)
}

func cloneStringMap(files map[string]string) map[string]string {
	copied := make(map[string]string, len(files))
	for path, content := range files {
		copied[path] = content
	}
	return copied
}
