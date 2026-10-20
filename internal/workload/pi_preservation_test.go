package workload

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNewPiPlanSealsFactPreservation(t *testing.T) {
	sealed := piTestPlan(t, PiProviderLocal, 1)
	pinned := pinnedPreservation()
	if sealed.Plan.Preservation == nil || *sealed.Plan.Preservation != pinned || pinned.Reductions != 1 {
		t.Fatalf("preservation = %+v", sealed.Plan.Preservation)
	}
	if sealed.Plan.RequestBudget != piRequestBudget {
		t.Fatalf("budget = %d", sealed.Plan.RequestBudget)
	}
	for _, prompt := range []string{piPreservedPrompt, piPreservedSummaryPrompt, piPreservedReopenPrompt} {
		if strings.Contains(prompt, "8443") || strings.Contains(prompt, "8080") {
			t.Fatalf("prompt carries the verifier fact: %s", prompt)
		}
	}
}

func TestHistoricalPiPlanOmitsPreservation(t *testing.T) {
	sealed := piTestPlan(t, PiProviderLocal, 1)
	historical, err := piHistoricalContract()
	if err != nil {
		t.Fatal(err)
	}
	sealed.Plan.Preservation = nil
	sealed.Plan.Contract = &historical
	sealed.Plan.PlanSHA256, _ = planDigest(sealed.Plan)
	if err := sealed.Plan.Validate(); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(sealed.Plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "preservation") {
		t.Fatal("historical plan carries preservation")
	}
}

func TestFluentSummaryDoesNotPreserveTheFact(t *testing.T) {
	messages := piHappyMessages()
	messages[0] = toolMessage("write_file", map[string]any{"path": piTaskFile, "content": piTaskDone})
	messages[2].Content = "The current port is 8443 and the recall stays unresolved."
	messages[3].Content = "Task completed successfully."
	trial := piRun(t, messages).Trials[0]
	if trial.Outcome == OutcomeAccepted || piPassed(trial.Verifier, "corrected_fact") || !piPassed(trial.Verifier, "summaries") {
		t.Fatalf("outcome = %s checks = %+v", trial.Outcome, trial.Verifier.Checks)
	}
}

func TestCorrectPortWithResolvedRecallFails(t *testing.T) {
	body := "status=done\nport=8443\nrecall=ok\n"
	messages := piHappyMessages()
	messages[0] = toolMessage("write_file", map[string]any{"path": piTaskFile, "content": body})
	trial := piRun(t, messages).Trials[0]
	if trial.Outcome == OutcomeAccepted || !piPassed(trial.Verifier, "corrected_fact") || piPassed(trial.Verifier, "unresolved_failure") {
		t.Fatalf("outcome = %s checks = %+v", trial.Outcome, trial.Verifier.Checks)
	}
}

func TestPreservedSessionAcceptsTheFileNotTheSummary(t *testing.T) {
	trial := piRun(t, piHappyMessages()).Trials[0]
	if trial.Outcome != OutcomeAccepted || trial.Verifier.ContextAction != piContextActionSummary {
		t.Fatalf("outcome = %s action = %s", trial.Outcome, trial.Verifier.ContextAction)
	}
	if !piPassed(trial.Verifier, "corrected_fact") || !piPassed(trial.Verifier, "unresolved_failure") || !piPassed(trial.Verifier, "context_action") {
		t.Fatalf("checks = %+v", trial.Verifier.Checks)
	}
	for _, summary := range []string{"history summary", "turn prefix summary"} {
		if strings.Contains(summary, "8443") {
			t.Fatal("happy-path summary carries the fact")
		}
	}
}

func TestPlantedFactPassConflictsWithTheDigest(t *testing.T) {
	bundle := piRun(t, piHappyMessages())
	trial := bundle.Trials[0]
	trial.Verifier.CorrectedFactSHA256 = ""
	setPiCheck(&trial, "corrected_fact", true)
	if err := validatePiChecks(trial, bundle.Plan); err == nil || !strings.Contains(err.Error(), "corrected fact") {
		t.Fatal("planted corrected-fact pass was accepted")
	}
}

func TestPreservationContractRejectsAMix(t *testing.T) {
	preserved, err := piPreservedContract()
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePiPreservation(Plan{Contract: &preserved}); err == nil {
		t.Fatal("preserved contract without the schedule validated")
	}
	historical, err := piHistoricalContract()
	if err != nil {
		t.Fatal(err)
	}
	pinned := pinnedPreservation()
	if err := validatePiPreservation(Plan{Contract: &historical, Preservation: &pinned}); err == nil {
		t.Fatal("historical contract carrying preservation validated")
	}
	if err := validatePiPreservation(Plan{}); err == nil {
		t.Fatal("a plan with no contract validated")
	}
	ordinary, summary, reopen := piSessionPrompts(Plan{})
	if ordinary != piPrompt || summary != piSummaryPrompt || reopen != piReopenPrompt || piDoneBody(Plan{}) != piTaskDone {
		t.Fatal("a historical plan did not keep the original prompts and done body")
	}
	if line, ok := prefixedLine("port=1\nport=2\n", "port="); ok || line != "" {
		t.Fatalf("two port lines hashed as one fact: %q %v", line, ok)
	}
}

func TestWorkspaceToolsReadTheFileNotTheSummary(t *testing.T) {
	state := newPiState(true)
	list, status := state.invoke("list_files", nil)
	if status != "ok" || !strings.Contains(list, "CORRECTION.txt") || !strings.Contains(list, "NOTE.txt") {
		t.Fatalf("list %q %s", list, status)
	}
	note, status := state.invoke("read_file", json.RawMessage(`{"path":"NOTE.txt"}`))
	if status != "ok" || note != piNoteBody {
		t.Fatalf("read %q %s", note, status)
	}
	if _, status = state.invoke("read_file", json.RawMessage(`{"path":"missing"}`)); status != "denied" {
		t.Fatalf("missing file status %s", status)
	}
	if _, status = state.invoke("list_files", json.RawMessage(`[`)); status != "malformed" {
		t.Fatalf("malformed status %s", status)
	}
	if _, status = state.invoke("shell", nil); status != "denied" {
		t.Fatalf("unknown tool status %s", status)
	}
	if _, status = state.invoke("write_file", json.RawMessage(`{"path":1}`)); status != "malformed" {
		t.Fatalf("malformed write status %s", status)
	}
	if digests := storedSummaryDigests([]string{"  ", "one"}); len(digests) != 1 || piSummariesEstablished(digests) {
		t.Fatalf("blank summary became evidence: %v", digests)
	}
}

func TestCorrectionWriteLeavesAuthority(t *testing.T) {
	messages := piHappyMessages()
	messages[0] = toolMessage("write_file", map[string]any{"path": piCorrectionFile, "content": piCorrectionBody})
	trial := piRun(t, messages).Trials[0]
	if trial.Outcome == OutcomeAccepted || piPassed(trial.Verifier, "authority") || trial.AuthorityViolations == 0 {
		t.Fatalf("outcome = %s authority = %d", trial.Outcome, trial.AuthorityViolations)
	}
}
