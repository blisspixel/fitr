package workload

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/ollama"
)

func TestPiPlanSealsSplitSummariesIntoOneBudget(t *testing.T) {
	sealed := piTestPlan(t, PiProviderLocal, 1)
	if sealed.Plan.RequestBudget != piRequestBudget || sealed.Plan.MaxTurns != piRequestBudget ||
		sealed.Plan.PiSchedule == nil || sealed.Plan.PiSchedule.SummaryCalls != piSummaryCalls ||
		sealed.Plan.Driver == nil || sealed.Plan.Driver.Commit != PiDriverCommit {
		t.Fatalf("budget = %d schedule = %+v", sealed.Plan.RequestBudget, sealed.Plan.PiSchedule)
	}
	sealed.Plan.RequestBudget = piOrdinaryCap + piReopenTurns
	sealed.Plan.MaxTurns = sealed.Plan.RequestBudget
	sealed.Plan.PlanSHA256, _ = planDigest(sealed.Plan)
	if err := sealed.Plan.Validate(); err == nil {
		t.Fatal("a budget that drops the split summaries was accepted")
	}
}

func TestPiSessionIssuesBothSplitSummaries(t *testing.T) {
	sealed := piTestPlan(t, PiProviderLocal, 1)
	backend := &scriptedWorkflowBackend{responses: piHappyMessages()}
	bundle, err := sealed.Run(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	trial := bundle.Trials[0]
	if backend.calls != 5 || trial.Outcome != OutcomeAccepted || trial.SummaryCalls != piSummaryCalls {
		t.Fatalf("calls = %d outcome = %s summaries = %d", backend.calls, trial.Outcome, trial.SummaryCalls)
	}
	if bundle.Report.Coverage != "observed" {
		t.Fatalf("coverage = %s, one acceptance is not an established measurement", bundle.Report.Coverage)
	}
}

func TestHarborRewardIsNotThePiReceipt(t *testing.T) {
	sealed := piTestPlan(t, PiProviderLocal, 1)
	sealed.Plan.Contract.Verifier = "harbor"
	sealed.Plan.Contract.ExternalProtocol = "harbor-reward"
	sealed.Plan.Contract.Proof = EvidenceExternalProtocol
	sealed.Plan.PlanSHA256, _ = planDigest(sealed.Plan)
	err := sealed.Plan.Validate()
	if err == nil || !strings.Contains(err.Error(), "Harbor") {
		t.Fatalf("harbor reward: %v", err)
	}
}

func TestFakeProviderCannotEstablishTheMeasurement(t *testing.T) {
	sealed := piTestPlan(t, PiProviderFake, 3)
	backend := &scriptedWorkflowBackend{responses: piHappyMessages()}
	bundle, err := sealed.Run(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Report.Coverage != "not_established" || bundle.Report.AcceptedOutcomesPerHour.Estimate != nil {
		t.Fatalf("fake coverage = %s rate = %+v", bundle.Report.Coverage, bundle.Report.AcceptedOutcomesPerHour)
	}
	if !piGap(bundle.Report.Gaps, piFakeProviderGap) {
		t.Fatalf("gaps = %v", bundle.Report.Gaps)
	}
}

func TestLocalPiSessionEstablishesAfterThreeAcceptances(t *testing.T) {
	sealed := piTestPlan(t, PiProviderLocal, 3)
	bundle, err := sealed.Run(context.Background(), &scriptedWorkflowBackend{responses: piHappyMessages()})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Report.Coverage != "established" || piGap(bundle.Report.Gaps, piFakeProviderGap) {
		t.Fatalf("coverage = %s gaps = %v", bundle.Report.Coverage, bundle.Report.Gaps)
	}
}

func TestSecondWriteFailsTheFileVerifier(t *testing.T) {
	messages := piHappyMessages()
	messages[4] = toolMessage("write_file", map[string]any{"path": piTaskFile, "content": piTaskDone})
	bundle := piRun(t, messages)
	trial := bundle.Trials[0]
	if trial.Outcome != OutcomeRejected || trial.Effects != 2 || piPassed(trial.Verifier, "single_effect") {
		t.Fatalf("outcome = %s effects = %d checks = %+v", trial.Outcome, trial.Effects, trial.Verifier.Checks)
	}
}

func TestSingleEffectPassRequiresOneRecordedWrite(t *testing.T) {
	messages := piHappyMessages()
	messages[4] = toolMessage("write_file", map[string]any{"path": piTaskFile, "content": piTaskDone})
	trial := piRun(t, messages).Trials[0]
	if trial.Effects != 2 {
		t.Fatalf("setup effects = %d", trial.Effects)
	}
	trial.Effects = 1
	setPiCheck(&trial, "single_effect", true)
	trial.Verifier.Accepted = true
	// The verifier event status is part of the sequence, separate from the
	// effect counter the check is about to disagree with.
	trial.Events[len(trial.Events)-2].Status = boolStatus(true)
	stats, err := piValidationStats(t, &trial)
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePiTrial(trial, stats); err == nil || !strings.Contains(err.Error(), "write events") {
		t.Fatal("single_effect passed while two successful writes were recorded")
	}
}

func TestCheckpointPassRequiresTheResumeEvent(t *testing.T) {
	sealed := piTestPlan(t, PiProviderLocal, 1)
	bundle, err := sealed.Run(context.Background(), &scriptedWorkflowBackend{chatErr: errors.New("backend down")})
	if err != nil {
		t.Fatal(err)
	}
	trial := bundle.Trials[0]
	if trial.Outcome != OutcomeInfrastructure || piPassed(trial.Verifier, "checkpoint") {
		t.Fatalf("setup outcome = %s checks = %+v", trial.Outcome, trial.Verifier.Checks)
	}
	digest, err := hashValue("fitr.workload.pi-checkpoint.v1", "unresumed")
	if err != nil {
		t.Fatal(err)
	}
	trial.Verifier.CheckpointSHA256 = digest
	setPiCheck(&trial, "checkpoint", true)
	stats, err := piValidationStats(t, &trial)
	if err != nil {
		t.Fatal(err)
	}
	if stats.resumes != 0 {
		t.Fatalf("setup resumes = %d", stats.resumes)
	}
	if err := validatePiTrial(trial, stats); err == nil || !strings.Contains(err.Error(), "resume") {
		t.Fatal("checkpoint check passed without a resume event")
	}
}

func setPiCheck(trial *Trial, code string, passed bool) {
	for index := range trial.Verifier.Checks {
		if trial.Verifier.Checks[index].Code == code {
			trial.Verifier.Checks[index].Passed = passed
		}
	}
}

func piValidationStats(t *testing.T, trial *Trial) (eventStats, error) {
	t.Helper()
	digest, err := hashValue("fitr.workload.event.v1", trial.Verifier)
	if err != nil {
		return eventStats{}, err
	}
	trial.Events[len(trial.Events)-2].EvidenceSHA256 = digest
	trial.Events[len(trial.Events)-1].EvidenceSHA256 = digest
	return validateEvents(trial.Events, trial.Outcome, trial.Verifier)
}

func TestDoneWithoutTheTaskFileIsRejected(t *testing.T) {
	bundle := piRun(t, []ollama.Message{
		{Role: "assistant", Content: "DONE"},
		{Role: "assistant", Content: "history"},
		{Role: "assistant", Content: "prefix"},
		{Role: "assistant", Content: "DONE"},
	})
	trial := bundle.Trials[0]
	if trial.Outcome == OutcomeAccepted || piPassed(trial.Verifier, "task_file") || trial.SummaryCalls != piSummaryCalls {
		t.Fatalf("outcome = %s summaries = %d checks = %+v", trial.Outcome, trial.SummaryCalls, trial.Verifier.Checks)
	}
}

func TestBlankSummaryDigestCannotPass(t *testing.T) {
	trial := piRun(t, piHappyMessages()).Trials[0]
	blank, err := hashValue("fitr.workload.pi-summary.v1", "")
	if err != nil {
		t.Fatal(err)
	}
	trial.Verifier.SummarySHA256 = []string{blank, blank}
	if err := validatePiChecks(trial); err == nil {
		t.Fatal("blank summary digests satisfied the summaries check")
	}
}

func TestSummaryPassRequiresStoredDigests(t *testing.T) {
	trial := piRun(t, piHappyMessages()).Trials[0]
	if !piPassed(trial.Verifier, "summaries") {
		t.Fatal("setup")
	}
	trial.Verifier.SummarySHA256 = nil
	if err := validatePiChecks(trial); err == nil {
		t.Fatal("summaries check passed without two stored digests")
	}
}

func TestTaskFilePassRequiresTheDoneDigest(t *testing.T) {
	trial := piRun(t, piHappyMessages()).Trials[0]
	open, err := hashValue("fitr.workload.policy.v1", piTaskOpen)
	if err != nil {
		t.Fatal(err)
	}
	trial.Verifier.PolicySHA256 = open
	if err := validatePiChecks(trial); err == nil {
		t.Fatal("task file check passed for a digest other than status=done")
	}
}

func TestBlankSummaryDoesNotCompleteTheSplit(t *testing.T) {
	messages := piHappyMessages()
	messages[2].Content = " \n\t"
	messages[3].Content = ""
	bundle := piRun(t, messages)
	trial := bundle.Trials[0]
	if trial.Outcome == OutcomeAccepted || piPassed(trial.Verifier, "summaries") || trial.AuthorityViolations != 0 {
		t.Fatalf("outcome = %s authority = %d checks = %+v", trial.Outcome, trial.AuthorityViolations, trial.Verifier.Checks)
	}
	if trial.SummaryCalls != piSummaryCalls {
		t.Fatalf("summary requests = %d", trial.SummaryCalls)
	}
}

func TestSummaryToolCallLeavesAuthority(t *testing.T) {
	messages := piHappyMessages()
	messages[2] = toolMessage("write_file", map[string]any{"path": piTaskFile, "content": piTaskDone})
	bundle := piRun(t, messages)
	trial := bundle.Trials[0]
	if trial.Outcome == OutcomeAccepted || piPassed(trial.Verifier, "authority") || piPassed(trial.Verifier, "summaries") {
		t.Fatalf("outcome = %s checks = %+v", trial.Outcome, trial.Verifier.Checks)
	}
}

func TestPiCheckpointRejectsTamperAndRestoresFiles(t *testing.T) {
	state := newPiState()
	state.files[piTaskFile] = piTaskDone
	state.summaries = []string{"history", "prefix"}
	if err := state.sealCheckpoint(); err != nil {
		t.Fatal(err)
	}
	state.files[piTaskFile] = "dirty"
	if err := state.resume(); err != nil {
		t.Fatal(err)
	}
	if state.files[piTaskFile] != piTaskDone || !state.resumed {
		t.Fatal("resume kept the dirty working copy")
	}
	state.sealedFiles[piTaskFile] = "tampered"
	state.resumed = false
	if err := state.resume(); err == nil {
		t.Fatal("tampered checkpoint was reopened")
	}
}

func TestUnresumedCheckpointCannotPass(t *testing.T) {
	state := newPiState()
	state.files[piTaskFile] = piTaskDone
	state.effects = 1
	state.summaries = []string{"history", "prefix"}
	if err := state.sealCheckpoint(); err != nil {
		t.Fatal(err)
	}
	receipt := verifyPi(&state)
	if receipt.Accepted || piPassed(receipt, "checkpoint") {
		t.Fatal("sealed but unresumed checkpoint passed")
	}
}

func TestPolicyRepairOmitsThePiSession(t *testing.T) {
	sealed := workloadTestPlan(t, 1)
	data, err := json.Marshal(sealed.Plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"request_budget", "\"driver\"", "\"provider\"", "pi_schedule"} {
		if strings.Contains(string(data), key) {
			t.Fatalf("policy-repair plan carries %s", key)
		}
	}
	trial, err := sealed.runTrial(context.Background(), &scriptedWorkflowBackend{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(trial)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"summary_calls", "\"effects\"", "checkpoint_sha256", "\"class\"", "summary_sha256"} {
		if strings.Contains(string(encoded), key) {
			t.Fatalf("policy-repair trial carries %s", key)
		}
	}
	sealed.Plan.RequestBudget = piRequestBudget
	sealed.Plan.PlanSHA256, _ = planDigest(sealed.Plan)
	if err := sealed.Plan.Validate(); err == nil {
		t.Fatal("policy-repair plan accepted a pi request budget")
	}
}

func piTestPlan(t *testing.T, provider string, trials int) *SealedPlan {
	t.Helper()
	sealed, err := NewPiPlan(workloadIdentity(t), "device-key", trials, 30, 8192, provider)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func piRun(t *testing.T, messages []ollama.Message) Bundle {
	t.Helper()
	bundle, err := piTestPlan(t, PiProviderLocal, 1).Run(context.Background(), &scriptedWorkflowBackend{responses: messages})
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func piHappyMessages() []ollama.Message {
	write := toolMessage("write_file", map[string]any{"path": piTaskFile, "content": piTaskDone})
	return []ollama.Message{
		write,
		{Role: "assistant", Content: "DONE"},
		{Role: "assistant", Content: "history summary"},
		{Role: "assistant", Content: "turn prefix summary"},
		{Role: "assistant", Content: "DONE"},
	}
}

func piPassed(receipt VerifierReceipt, code string) bool {
	for _, item := range receipt.Checks {
		if item.Code == code {
			return item.Passed
		}
	}
	return false
}

func piGap(gaps []string, want string) bool {
	for _, gap := range gaps {
		if gap == want {
			return true
		}
	}
	return false
}
