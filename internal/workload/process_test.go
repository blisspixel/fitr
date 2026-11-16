package workload

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAcceptedTrialKeepsAMalformedToolObservation(t *testing.T) {
	trial := acceptedTrialWithToolStatus(t, "write_file", "malformed")
	analysis := analyzeTrial(trial)
	if analysis.Process == nil {
		t.Fatal("validated trial left the process observation unavailable")
	}
	if analysis.Process.Malformed != 1 || analysis.Process.Failed != 0 || analysis.Process.Denied != 0 {
		t.Fatalf("process = %+v", analysis.Process)
	}
	if trial.Outcome != OutcomeAccepted || !trial.Verifier.Accepted {
		t.Fatalf("outcome %s verifier accepted %v", trial.Outcome, trial.Verifier.Accepted)
	}
	text := analysis.ProcessText()
	if !strings.Contains(text, "malformed 1") || !strings.Contains(text, "write_file") {
		t.Fatalf("process text = %q", text)
	}
	if strings.Contains(text, "flawless") || strings.Contains(text, "unnecessary") || strings.Contains(text, "wasted") {
		t.Fatalf("process text labels the observation: %q", text)
	}
	if strings.Contains(text, "timeout_ms") || strings.Contains(text, "policy.json") || strings.Contains(text, "REQUIREMENTS") {
		t.Fatalf("process text carries tool content: %q", text)
	}
	for _, event := range trial.Events {
		if event.EvidenceSHA256 != "" && strings.Contains(text, event.EvidenceSHA256) {
			t.Fatalf("process text carries an argument digest: %q", text)
		}
	}
}

func TestFailedInterimCheckStaysDistinctFromVerifierRejection(t *testing.T) {
	trial := acceptedTrialWithToolStatus(t, "run_checks", "failed")
	analysis := analyzeTrial(trial)
	if analysis.Process == nil || analysis.Process.Failed != 1 {
		t.Fatalf("process = %+v", analysis.Process)
	}
	if trial.Outcome != OutcomeAccepted || !trial.Verifier.Accepted {
		t.Fatalf("failed tool became a verifier rejection: outcome %s accepted %v", trial.Outcome, trial.Verifier.Accepted)
	}
	if strings.Contains(analysis.ProcessText(), "rejected") {
		t.Fatalf("process text relabels the outcome: %q", analysis.ProcessText())
	}
}

func TestRepeatedCallIsCountedWithoutAPenaltyLabel(t *testing.T) {
	sealed := workloadTestPlan(t, 1)
	trial, err := sealed.runTrial(context.Background(), &scriptedWorkflowBackend{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicateNamedToolPair(&trial, "run_checks") {
		t.Fatal("run_checks pair missing")
	}
	trial.ToolCalls++
	trial.DuplicateCalls++
	if err := sealed.signTrial(&trial); err != nil {
		t.Fatal(err)
	}
	if err := trial.Validate(sealed.Plan); err != nil {
		t.Fatalf("repeated call failed validation: %v", err)
	}
	analysis := analyzeTrial(trial)
	if analysis.Process == nil || analysis.Process.Repeated != 1 {
		t.Fatalf("process = %+v", analysis.Process)
	}
	text := analysis.ProcessText()
	if !strings.Contains(text, "repeated 1") || !strings.Contains(text, "run_checks") ||
		strings.Contains(text, "unnecessary") || strings.Contains(text, "wasted") || strings.Contains(text, "penalty") {
		t.Fatalf("process text = %q", text)
	}
}

func duplicateNamedToolPair(trial *Trial, tool string) bool {
	for index, event := range trial.Events {
		if event.Type == EventToolStarted && event.Tool == tool {
			pair := append([]Event(nil), trial.Events[index:index+2]...)
			trial.Events = append(trial.Events[:index], append(pair, trial.Events[index:]...)...)
			resequenceEvents(trial.Events)
			return true
		}
	}
	return false
}

func TestInvalidToolSequenceLeavesProcessUnavailable(t *testing.T) {
	sealed := workloadTestPlan(t, 1)
	trial, err := sealed.runTrial(context.Background(), &scriptedWorkflowBackend{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	removeToolCompletion(&trial)
	analysis := analyzeTrial(trial)
	if analysis.Process != nil || analysis.ProcessText() != "unavailable" {
		t.Fatalf("invalid sequence process = %+v text %q", analysis.Process, analysis.ProcessText())
	}
}

func acceptedTrialWithToolStatus(t *testing.T, tool, status string) Trial {
	t.Helper()
	sealed := workloadTestPlan(t, 1)
	trial, err := sealed.runTrial(context.Background(), &scriptedWorkflowBackend{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for index := range trial.Events {
		event := &trial.Events[index]
		if event.Type == EventToolCompleted && event.Tool == tool {
			event.Status = status
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("trial has no %s completion", tool)
	}
	if err := sealed.signTrial(&trial); err != nil {
		t.Fatal(err)
	}
	if err := trial.Validate(sealed.Plan); err != nil {
		t.Fatalf("mutated trial failed validation: %v", err)
	}
	if trial.Outcome != OutcomeAccepted {
		t.Fatalf("scripted trial outcome = %s", trial.Outcome)
	}
	return trial
}

func TestDeniedToolStaysAProcessCount(t *testing.T) {
	trial := acceptedTrialWithToolStatus(t, "read_file", "denied")
	analysis := analyzeTrial(trial)
	process := analysis.Process
	if process == nil || process.Denied != 1 || process.Malformed != 0 || process.Failed != 0 {
		t.Fatalf("process = %+v", process)
	}
	text := analysis.ProcessText()
	if !strings.Contains(text, "denied 1") || !strings.Contains(text, "read_file") {
		t.Fatalf("process text = %q", text)
	}
}

func TestCleanTrialKeepsAnObservedZero(t *testing.T) {
	sealed := workloadTestPlan(t, 1)
	trial, err := sealed.runTrial(context.Background(), &scriptedWorkflowBackend{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	signature := trial.Signature
	analysis := analyzeTrial(trial)
	if trial.Signature != signature || trial.EvidenceSHA256 == "" {
		t.Fatal("analysis changed the signed trial")
	}
	process := analysis.Process
	if process == nil || process.Status != "observed" || process.Malformed != 0 || process.Denied != 0 ||
		process.Failed != 0 || process.Repeated != 0 || len(process.Observations) != 0 {
		t.Fatalf("process = %+v", process)
	}
	text := analysis.ProcessText()
	if !strings.Contains(text, "malformed 0") || !strings.Contains(text, "repeated 0") || strings.Contains(text, "flawless") {
		t.Fatalf("process text = %q", text)
	}
}

func TestHistoricalPlanKeepsTheV2ReportBytes(t *testing.T) {
	sealed := workloadTestPlan(t, 1)
	sealed.Plan.Analysis = ""
	digest, err := planDigest(sealed.Plan)
	if err != nil {
		t.Fatal(err)
	}
	sealed.Plan.PlanSHA256 = digest
	bundle, err := sealed.Run(context.Background(), &scriptedWorkflowBackend{})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Report.Schema != ReportSchema {
		t.Fatalf("schema = %s", bundle.Report.Schema)
	}
	data, err := json.Marshal(bundle.Report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "tool_process") {
		t.Fatalf("historical report gained tool_process: %s", data)
	}
	roundTripWorkloadBundle(t, bundle)

	current := workloadTestPlan(t, 1)
	if current.Plan.Analysis != ProcessReportSchema {
		t.Fatalf("analysis = %s", current.Plan.Analysis)
	}
	fresh, err := current.Run(context.Background(), &scriptedWorkflowBackend{})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Report.Schema != ProcessReportSchema || len(fresh.Report.TrialAnalysis) != 1 || fresh.Report.TrialAnalysis[0].Process == nil {
		t.Fatalf("schema %s analysis %+v", fresh.Report.Schema, fresh.Report.TrialAnalysis)
	}
	trialJSON, err := json.Marshal(fresh.Trials[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(trialJSON), "tool_process") {
		t.Fatal("process counts were written into the signed trial")
	}
	roundTripWorkloadBundle(t, fresh)
}

func TestUnknownAnalysisContractIsRejected(t *testing.T) {
	sealed := workloadTestPlan(t, 1)
	sealed.Plan.Analysis = "fitr.workload.analysis.v9"
	digest, err := planDigest(sealed.Plan)
	if err != nil {
		t.Fatal(err)
	}
	sealed.Plan.PlanSHA256 = digest
	if err := sealed.Plan.Validate(); err == nil {
		t.Fatal("unknown analysis contract was accepted")
	}
}
