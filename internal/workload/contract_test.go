package workload

import (
	"context"
	"math"
	"testing"
)

func TestEvidenceClassesDoNotPromoteWorkerOrProtocolSuccess(t *testing.T) {
	for _, class := range []EvidenceClass{EvidenceDeterministic, EvidenceExternalState, EvidenceIndependent,
		EvidenceHarness, EvidenceHeuristic, EvidenceModelJudged, EvidenceSelfReported, EvidenceNone, "invented"} {
		want := class == EvidenceDeterministic || class == EvidenceExternalState || class == EvidenceIndependent
		if class.CanEstablishCompletion() != want || class.Valid() != (class != "invented") {
			t.Fatalf("incorrect evidence policy for %q", class)
		}
		receipt := verifyWorkflow(newWorkflowState())
		receipt.EvidenceClass = class
		if err := validateVerifier(receipt, 0); (err == nil) != (class == EvidenceDeterministic) {
			t.Fatalf("fixed workflow allowed unsupported verifier %q: %v", class, err)
		}
	}
}

func TestWorkloadContractCannotDriftEvenWithRehashedPlan(t *testing.T) {
	sealed := workloadTestPlan(t, 1)
	sealed.Plan.Contract.Authority = "host-filesystem"
	sealed.Plan.PlanSHA256, _ = planDigest(sealed.Plan)
	if err := sealed.Plan.Validate(); err == nil {
		t.Fatal("unsupported authority was accepted")
	}
	sealed.Plan.Contract = nil
	sealed.Plan.PlanSHA256, _ = planDigest(sealed.Plan)
	if err := sealed.Plan.Validate(); err == nil {
		t.Fatal("missing v2 contract was accepted")
	}
}

func TestLegacyWorkloadBundleRoundTripWithoutInventedReceipts(t *testing.T) {
	sealed := workloadTestPlan(t, 1)
	sealed.Plan.Schema, sealed.Plan.Contract = LegacyPlanSchema, nil
	sealed.Plan.PlanSHA256, _ = planDigest(sealed.Plan)
	bundle, err := sealed.Run(context.Background(), &scriptedWorkflowBackend{})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Report.Schema != LegacyReportSchema || len(bundle.Report.TrialAnalysis) != 0 {
		t.Fatal("legacy bundle gained a new evidence claim")
	}
	roundTripWorkloadBundle(t, bundle)
}

func TestWorkloadTimingPartitionsElapsedWithoutDoubleCounting(t *testing.T) {
	sealed := workloadTestPlan(t, 1)
	trial, err := sealed.runTrial(context.Background(), &scriptedWorkflowBackend{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	for index := range trial.Events {
		trial.Events[index].ElapsedMillis = int64(index * 10)
	}
	trial.ElapsedMillis = trial.Events[len(trial.Events)-1].ElapsedMillis + 5
	if err := sealed.signTrial(&trial); err != nil {
		t.Fatal(err)
	}
	if err := trial.Validate(sealed.Plan); err != nil {
		t.Fatal(err)
	}
	timing := Analyze(sealed.Plan, []Trial{trial}).TrialAnalysis[0].Timing
	if timing == nil || timing.ModelMillis != 50 || timing.ToolMillis != 40 ||
		timing.VerifierQueueMillis != 10 || timing.VerifierMillis != 10 {
		t.Fatalf("timing = %+v", timing)
	}
	if timing.WorkerMillis != timing.ModelMillis+timing.ToolMillis+timing.WorkerOverheadMillis ||
		timing.ReleaseToTerminalMillis != timing.WorkerMillis+timing.VerifierQueueMillis+
			timing.VerifierMillis+timing.HarnessOverheadMillis ||
		timing.TimeToValidMillis == nil || *timing.TimeToValidMillis != timing.ReleaseToTerminalMillis {
		t.Fatalf("inconsistent time partition = %+v", timing)
	}
	trial.Events = nil
	if got := analyzeTrial(trial); got.Timing != nil || got.TimingStatus != "unavailable" {
		t.Fatal("missing receipts became timing")
	}
}

func TestWorkloadRateCannotOverflowAcrossLongTrials(t *testing.T) {
	plan := workloadTestPlan(t, 2).Plan
	report := Analyze(plan, []Trial{
		{Outcome: OutcomeAccepted, ElapsedMillis: math.MaxInt64},
		{Outcome: OutcomeRejected, ElapsedMillis: math.MaxInt64},
	})
	if rate := report.AcceptedOutcomesPerHour.Estimate; rate == nil || *rate <= 0 {
		t.Fatalf("overflowed exposure denominator: %+v", report)
	}
}

func TestWorkloadRejectsContradictoryRejectionAndPerTurnOverflow(t *testing.T) {
	sealed := workloadTestPlan(t, 1)
	trial, err := sealed.runTrial(context.Background(), &scriptedWorkflowBackend{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	trial.Outcome = OutcomeRejected
	if err := validateOutcomeSemantics(trial, "clean_stop"); err == nil {
		t.Fatal("independently accepted clean stop was relabeled as rejection")
	}
	trial.Outcome = OutcomeAccepted
	// Move sixteen extra calls into the first turn. Total calls still fit the
	// aggregate turns*limit bound, so only a per-turn check catches the defect.
	extra := []Event{}
	for range maximumToolCallsPerTurn {
		extra = append(extra, trial.Events[4:6]...)
	}
	trial.Events = append(trial.Events[:6], append(extra, trial.Events[6:]...)...)
	for index := range trial.Events {
		trial.Events[index].ElapsedMillis = int64(index)
	}
	resequenceEvents(trial.Events)
	if _, err := validateEvents(trial.Events, trial.Outcome, trial.Verifier); err == nil {
		t.Fatal("per-turn tool overflow was accepted")
	}
}
