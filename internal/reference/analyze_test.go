package reference

import (
	"strings"
	"testing"
)

func TestAgentClaimDoesNotBecomeTheOutcome(t *testing.T) {
	plan := sealedPlan(t, "2024-01-01", fakeSHA("artifact"))
	rows := rowsLike(plan, OutcomeUnresolved)
	rows[0].AgentClaim = OutcomeResolved
	result := mustAnalyze(t, plan, rows)
	if result.Tasks[0].Outcome != OutcomeUnresolved || !result.Tasks[0].AgentClaimIgnored {
		t.Fatalf("claim rewrote the row: %+v", result.Tasks[0])
	}
	if result.Fresh || !result.RecordedFixture {
		t.Fatal("a recorded verifier file was marked fresh")
	}
	if result.Clean.Unresolved != len(plan.Instances) || result.CleanRate.Denominator != len(plan.Instances) {
		t.Fatalf("clean counts = %+v rate %+v", result.Clean, result.CleanRate)
	}
}

func TestErrorsStayOutOfTheRateDenominator(t *testing.T) {
	plan := sealedPlan(t, "2024-01-01", fakeSHA("artifact"))
	rows := rowsLike(plan, OutcomeResolved)
	rows[0].Verdict = OutcomeTimeout
	rows[1].Verdict = OutcomeHarness
	rows[2].Verdict = OutcomeEnvironment
	result := mustAnalyze(t, plan, rows)
	want := len(plan.Instances) - 3
	if result.CleanRate.Denominator != want || result.Clean.Timeout != 1 || result.Clean.HarnessError != 1 {
		t.Fatalf("errors entered the rate or were dropped: counts %+v rate %+v", result.Clean, result.CleanRate)
	}
	if result.CleanRate.Status != rateMeasured || result.CleanRate.Estimate == nil {
		t.Fatal("a positive clean denominator stayed unmeasured")
	}
}

func TestContaminationIsReportedSeparately(t *testing.T) {
	plan := sealedPlan(t, "2026-01-01", fakeSHA("artifact"))
	result := mustAnalyze(t, plan, rowsLike(plan, OutcomeResolved))
	if result.CleanRate.Status != rateUnmeasured || result.CleanRate.Denominator != 0 {
		t.Fatalf("contaminated rows entered the clean rate: %+v", result.CleanRate)
	}
	if result.Contaminated.PossiblyContaminated != len(plan.Instances) || result.ContaminatedRate.Denominator != len(plan.Instances) {
		t.Fatalf("contaminated report = %+v rate %+v", result.Contaminated, result.ContaminatedRate)
	}
	if contamination("2025-04-02", "2025-04-02") != contaminationClean {
		t.Fatal("a task created on the release day was labeled contaminated")
	}
	if contamination("not-a-date", "2025-04-02") != ContaminationAbsent {
		t.Fatal("a bad date became a contamination label")
	}
}

func TestStoredRateThatDisagreesIsRejected(t *testing.T) {
	plan := sealedPlan(t, "2024-01-01", fakeSHA("artifact"))
	result := mustAnalyze(t, plan, rowsLike(plan, OutcomeResolved))
	result.Clean.Resolved = 0
	if err := result.Validate(); err == nil {
		t.Fatal("a stored zero matched a resolved verifier file")
	}
}

func TestProblemTextIsRejectedByTheVerifierDecoder(t *testing.T) {
	body := `{"schema":"fitr.external-reference.verifier.v1","tasks":[{"instance_id":"x","problem_statement":"secret"}]}`
	if _, err := DecodeVerifier([]byte(body)); err == nil {
		t.Fatal("problem text was accepted as a verifier row")
	}
	dup := `{"schema":"fitr.external-reference.verifier.v1","schema":"fitr.external-reference.verifier.v1","tasks":[]}`
	if _, err := DecodeVerifier([]byte(dup)); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate key error = %v", err)
	}
}

func TestDriftAndPartialEnergyStayUnmeasured(t *testing.T) {
	plan := sealedPlan(t, "2024-01-01", fakeSHA("artifact"))
	rows := rowsLike(plan, OutcomeResolved)
	rows[0].InstanceID = "not-in-the-plan"
	if _, err := Analyze(plan, ready(plan), rows); err == nil || !strings.Contains(err.Error(), "drift") {
		t.Fatalf("drift error = %v", err)
	}
	rows = rowsLike(plan, OutcomeResolved)
	one := 1.0
	rows[0].EnergyJoules = &one
	result := mustAnalyze(t, plan, rows)
	if result.EnergyJoules != nil {
		t.Fatal("one energy reading became a total")
	}
	zero := 0.0
	for i := range rows {
		rows[i].APICostUSD = &zero
	}
	result = mustAnalyze(t, plan, rows)
	if result.APICostUSD == nil || *result.APICostUSD != 0 {
		t.Fatalf("declared zero cost = %v", result.APICostUSD)
	}
}

func TestMissingConditionIsNotAnIdleGPU(t *testing.T) {
	body := `{"schema":"fitr.external-reference.conditions.v1","other_model":false,"served_context":8192}`
	if _, err := DecodeConditions([]byte(body)); err == nil {
		t.Fatal("a missing gpu_busy field became false")
	}
}
