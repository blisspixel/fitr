package workload

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/record"
)

const workloadArtifactDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

func TestBoundedWorkflowProducesSignedIndependentAcceptance(t *testing.T) {
	sealed := workloadTestPlan(t, 3)
	bundle, err := sealed.Run(context.Background(), &scriptedWorkflowBackend{})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Report.Counts.Accepted != 3 || bundle.Report.Coverage != "established" ||
		bundle.Report.MedianAcceptedMillis == nil {
		t.Fatalf("workload report = %+v", bundle.Report)
	}
	for _, trial := range bundle.Trials {
		if trial.Outcome != OutcomeAccepted || trial.Verifier.EvidenceClass != "deterministic_assertion" ||
			trial.AuthorityViolations != 0 || len(trial.Signature) == 0 {
			t.Fatalf("accepted trial = %+v", trial)
		}
		if err := trial.Validate(bundle.Plan); err != nil {
			t.Fatal(err)
		}
	}
	roundTripWorkloadBundle(t, bundle)
}

func TestWorkflowDeniesAuthorityExpansionAndRejectsSelfReportedDone(t *testing.T) {
	sealed := workloadTestPlan(t, 1)
	backend := &scriptedWorkflowBackend{responses: []ollama.Message{
		toolMessage("write_file", map[string]any{"path": requirementsFile, "content": "changed"}),
		{Role: "assistant", Content: "DONE"},
	}}
	bundle, err := sealed.Run(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	trial := bundle.Trials[0]
	if trial.Outcome != OutcomeRejected || trial.AuthorityViolations != 1 || trial.Verifier.Accepted {
		t.Fatalf("authority-violating trial = %+v", trial)
	}
	if bundle.Report.Counts.Rejected != 1 || bundle.Report.Coverage != "not_established" {
		t.Fatalf("rejection report = %+v", bundle.Report)
	}
}

func TestWorkflowBoundsToolCallsPerModelTurn(t *testing.T) {
	sealed := workloadTestPlan(t, 1)
	message := ollama.Message{Role: "assistant"}
	for range maximumToolCallsPerTurn + 1 {
		call := toolMessage("list_files", map[string]any{})
		message.ToolCalls = append(message.ToolCalls, call.ToolCalls[0])
	}
	bundle, err := sealed.Run(context.Background(), &scriptedWorkflowBackend{responses: []ollama.Message{message}})
	if err != nil {
		t.Fatal(err)
	}
	trial := bundle.Trials[0]
	if trial.Outcome != OutcomeRejected || trial.ToolCalls != 0 || trial.Turns != 1 {
		t.Fatalf("over-wide tool turn = %+v", trial)
	}
	worker := trial.Events[len(trial.Events)-5]
	if worker.Type != EventWorkerCompleted || worker.Status != "tool_call_cap" {
		t.Fatalf("over-wide tool turn worker event = %+v", worker)
	}
}

func TestWorkflowCanonicalizesDuplicateToolArguments(t *testing.T) {
	first := ollama.ToolCall{ID: "first"}
	first.Function.Name = "write_file"
	first.Function.Arguments = json.RawMessage(`{"path":"policy.json","content":"x"}`)
	second := ollama.ToolCall{ID: "second"}
	second.Function.Name = "write_file"
	second.Function.Arguments = json.RawMessage(`{"content":"x","path":"policy.json"}`)
	backend := &scriptedWorkflowBackend{responses: []ollama.Message{
		{Role: "assistant", ToolCalls: []ollama.ToolCall{first, second}},
		{Role: "assistant", Content: "DONE"},
	}}
	bundle, err := workloadTestPlan(t, 1).Run(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Trials[0].DuplicateCalls != 1 {
		t.Fatalf("semantic duplicate calls = %d, want 1", bundle.Trials[0].DuplicateCalls)
	}
}

func TestWorkflowKeepsInfrastructureFaultsOutOfModelFailureCount(t *testing.T) {
	sealed := workloadTestPlan(t, 1)
	bundle, err := sealed.Run(context.Background(), &scriptedWorkflowBackend{chatErr: errors.New("runtime unavailable")})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Trials[0].Outcome != OutcomeInfrastructure || bundle.Report.Counts.InfrastructureFault != 1 ||
		bundle.Report.Counts.Rejected != 0 || len(bundle.Report.Gaps) == 0 {
		t.Fatalf("infrastructure report = %+v", bundle.Report)
	}
}

func TestWorkflowRejectsSuccessReturnedAfterDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	recorder := newEventRecorder()
	state := newWorkflowState()
	execution := executeWorkflow(ctx, &scriptedWorkflowBackend{responses: []ollama.Message{{
		Role: "assistant", Content: "DONE",
	}}}, workloadTestPlan(t, 1).Plan, &state, recorder)
	if !execution.timedOut || execution.cleanStop || execution.infrastructureFault {
		t.Fatalf("late backend success = %+v", execution)
	}
	if got := recorder.events[len(recorder.events)-1]; got.Type != EventWorkerCompleted || got.Status != "timeout" {
		t.Fatalf("late backend terminal worker event = %+v", got)
	}
}

func TestWorkloadBundleRejectsEventAndReportTampering(t *testing.T) {
	sealed := workloadTestPlan(t, 1)
	bundle, err := sealed.Run(context.Background(), &scriptedWorkflowBackend{})
	if err != nil {
		t.Fatal(err)
	}
	bundle.Trials[0].Events[0].Actor = "worker"
	if err := bundle.Validate(); err == nil {
		t.Fatalf("event tampering error = %v", err)
	}

	sealed = workloadTestPlan(t, 1)
	bundle, err = sealed.Run(context.Background(), &scriptedWorkflowBackend{})
	if err != nil {
		t.Fatal(err)
	}
	bundle.Report.Counts.Accepted = 0
	if err := bundle.Validate(); err == nil || !strings.Contains(err.Error(), "report") {
		t.Fatalf("report tampering error = %v", err)
	}
}

func TestInterruptedWorkloadPlanCannotBeRetried(t *testing.T) {
	sealed := workloadTestPlan(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sealed.Run(ctx, &scriptedWorkflowBackend{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted workload = %v", err)
	}
	if _, err := sealed.Run(context.Background(), &scriptedWorkflowBackend{}); err == nil ||
		!strings.Contains(err.Error(), "signing key") {
		t.Fatalf("reused interrupted plan error = %v", err)
	}
}

func TestWorkloadTrialRejectsSignedInvalidEventState(t *testing.T) {
	for _, test := range invalidEventMutations() {
		t.Run(test.name, func(t *testing.T) {
			sealed := workloadTestPlan(t, 1)
			trial, err := sealed.runTrial(context.Background(), &scriptedWorkflowBackend{}, 1)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&trial)
			if err := sealed.signTrial(&trial); err != nil {
				t.Fatal(err)
			}
			if err := trial.Validate(sealed.Plan); err == nil {
				t.Fatal("signed structurally invalid workload trial was accepted")
			}
		})
	}
}

func invalidEventMutations() []struct {
	name   string
	mutate func(*Trial)
} {
	return []struct {
		name   string
		mutate func(*Trial)
	}{
		{"reordered verifier", reorderVerifierEvents},
		{"unbalanced tool", removeToolCompletion},
		{"duplicated tool pair", duplicateToolPair},
		{"counter mismatch", func(trial *Trial) { trial.ToolCalls++ }},
		{"missing event evidence", func(trial *Trial) { trial.Events[0].EvidenceSHA256 = "" }},
		{"wrong queued status", func(trial *Trial) { trial.Events[len(trial.Events)-4].Status = "started" }},
		{"detached verifier evidence", func(trial *Trial) {
			trial.Events[len(trial.Events)-2].EvidenceSHA256 =
				"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}},
		{"verifier self acceptance", func(trial *Trial) { trial.Verifier.Checks[0].Passed = false }},
	}
}

func TestWorkflowRefusesUnencodableBackendEvidence(t *testing.T) {
	sealed := workloadTestPlan(t, 1)
	backend := &scriptedWorkflowBackend{
		responses: []ollama.Message{{Role: "assistant", Content: "DONE"}},
		metrics:   ollama.Metrics{DecodeTPS: math.NaN()},
	}
	if _, err := sealed.Run(context.Background(), backend); err == nil ||
		!strings.Contains(err.Error(), "event evidence") {
		t.Fatalf("unencodable workload evidence error = %v", err)
	}
}

func TestVerifierReceiptBindsProtectedStateDigestToItsCheck(t *testing.T) {
	receipt := verifyWorkflow(newWorkflowState())
	receipt.ProtectedStateSHA256 = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := validateVerifier(receipt, 0); err == nil || !strings.Contains(err.Error(), "protected-state") {
		t.Fatalf("mismatched protected-state receipt error = %v", err)
	}
}

func reorderVerifierEvents(trial *Trial) {
	queued := len(trial.Events) - 4
	trial.Events[queued], trial.Events[queued+1] = trial.Events[queued+1], trial.Events[queued]
	resequenceEvents(trial.Events)
}

func removeToolCompletion(trial *Trial) {
	for index, event := range trial.Events {
		if event.Type == EventToolCompleted {
			trial.Events = append(trial.Events[:index], trial.Events[index+1:]...)
			resequenceEvents(trial.Events)
			return
		}
	}
}

func duplicateToolPair(trial *Trial) {
	for index, event := range trial.Events {
		if event.Type == EventToolStarted {
			pair := append([]Event(nil), trial.Events[index:index+2]...)
			trial.Events = append(trial.Events[:index], append(pair, trial.Events[index:]...)...)
			resequenceEvents(trial.Events)
			return
		}
	}
}

func TestWorkloadTrialRejectsMalformedCompletionKeyWithoutPanic(t *testing.T) {
	sealed := workloadTestPlan(t, 1)
	trial, err := sealed.runTrial(context.Background(), &scriptedWorkflowBackend{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	plan := sealed.Plan
	plan.CompletionKey = "AA"
	plan.PlanSHA256 = ""
	plan.PlanSHA256, err = planDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := trial.Validate(plan); err == nil {
		t.Fatal("malformed completion key was accepted")
	}
}

func TestWorkloadPlanRejectsIdentityAndBoundViolations(t *testing.T) {
	identity := workloadIdentity(t)
	for _, test := range []struct {
		name     string
		identity record.ModelIdentity
		device   string
		trials   int
		turns    int
		timeout  int
		context  int
	}{
		{name: "identity", identity: record.ModelIdentity{}, device: "device", trials: 1, turns: 1, timeout: 1, context: 1},
		{name: "device", identity: identity, trials: 1, turns: 1, timeout: 1, context: 1},
		{name: "trials", identity: identity, device: "device", turns: 1, timeout: 1, context: 1},
		{name: "turns", identity: identity, device: "device", trials: 1, timeout: 1, context: 1},
		{name: "timeout", identity: identity, device: "device", trials: 1, turns: 1, context: 1},
		{name: "timeout large", identity: identity, device: "device", trials: 1, turns: 1, timeout: 3601, context: 1},
		{name: "context large", identity: identity, device: "device", trials: 1, turns: 1, timeout: 1,
			context: maximumRequestedContext + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewPlan(test.identity, test.device, test.trials, test.turns, test.timeout, test.context); err == nil {
				t.Fatal("invalid workload plan was accepted")
			}
		})
	}
}

func workloadTestPlan(t *testing.T, trials int) *SealedPlan {
	t.Helper()
	sealed, err := NewPlan(workloadIdentity(t), "device-key", trials, 10, 30, 8192)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func workloadIdentity(t *testing.T) record.ModelIdentity {
	t.Helper()
	identity, err := record.NewModelIdentity("model", "model", "test", "test 1",
		workloadArtifactDigest, "", 1024)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func roundTripWorkloadBundle(t *testing.T, bundle Bundle) {
	t.Helper()
	data, err := bundle.JSON()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "workload.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadBundle(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.Validate(); err != nil {
		t.Fatal(err)
	}
}

func resequenceEvents(events []Event) {
	for index := range events {
		events[index].Sequence = index + 1
	}
}

type scriptedWorkflowBackend struct {
	calls     int
	responses []ollama.Message
	chatErr   error
	metrics   ollama.Metrics
}

func (backend *scriptedWorkflowBackend) Chat(_ context.Context, _ string, _ []ollama.Message,
	_ []ollama.Tool, _ ollama.Sampling) (ollama.Message, ollama.Metrics, error) {
	if backend.chatErr != nil {
		return ollama.Message{}, ollama.Metrics{}, backend.chatErr
	}
	if len(backend.responses) > 0 {
		message := backend.responses[backend.calls%len(backend.responses)]
		backend.calls++
		metrics := backend.metrics
		if metrics == (ollama.Metrics{}) {
			metrics.PromptTokens = 100
		}
		return message, metrics, nil
	}
	sequence := []ollama.Message{
		toolMessage("list_files", map[string]any{}),
		toolMessage("read_file", map[string]any{"path": requirementsFile}),
		toolMessage("write_file", map[string]any{"path": policyFile, "content": validPolicyJSON()}),
		toolMessage("run_checks", map[string]any{}),
		{Role: "assistant", Content: "DONE"},
	}
	message := sequence[backend.calls%len(sequence)]
	backend.calls++
	return message, ollama.Metrics{PromptTokens: 100}, nil
}

func toolMessage(name string, arguments map[string]any) ollama.Message {
	raw, _ := json.Marshal(arguments)
	call := ollama.ToolCall{ID: name + "-id"}
	call.Function.Name, call.Function.Arguments = name, raw
	return ollama.Message{Role: "assistant", ToolCalls: []ollama.ToolCall{call}}
}

func validPolicyJSON() string {
	return `{"version":2,"enabled":true,"retries":3,"timeout_ms":1500,"modes":["safe","fast"]}`
}

func (*scriptedWorkflowBackend) Name() string                   { return "test" }
func (*scriptedWorkflowBackend) URL() string                    { return "http://127.0.0.1" }
func (*scriptedWorkflowBackend) Version(context.Context) string { return "test 1" }
func (*scriptedWorkflowBackend) Reachable(context.Context) bool { return true }
func (*scriptedWorkflowBackend) Generate(context.Context, string, string, ollama.Sampling) (string, ollama.Metrics, error) {
	return "", ollama.Metrics{}, nil
}
func (*scriptedWorkflowBackend) Tags(context.Context) ([]ollama.ModelInfo, error) { return nil, nil }
func (*scriptedWorkflowBackend) Show(context.Context, string) (ollama.ModelInfo, error) {
	return ollama.ModelInfo{}, nil
}
func (*scriptedWorkflowBackend) PS(context.Context) ([]ollama.RunningModel, error) { return nil, nil }
func (*scriptedWorkflowBackend) StopAll(context.Context) ([]string, error)         { return nil, nil }
