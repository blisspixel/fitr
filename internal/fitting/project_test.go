package fitting

import (
	"testing"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/score"
)

func classState(observations []EvidenceObservation, class string) (string, bool) {
	for _, o := range observations {
		if o.Class == class {
			return o.State, true
		}
	}
	return "", false
}

func evSession() Session {
	return Session{
		Schema: SessionSchema, Phase: PhaseMeasured,
		Plan: Plan{Schema: PlanSchema, RoleName: "coding", Candidates: []string{"qwen3.6:35b"},
			ContextTokens: 65536, KVCacheType: "f16"},
	}
}

func passingSource() ObservationSource {
	effective, prefix := 65536, 4096
	return ObservationSource{
		ArtifactDigest: "sha256:abc", Runtime: "0.33.3", DeviceKey: "dev-key",
		Report: analysis.Report{
			Context: analysis.Context{Effective: &effective},
			Capacity: analysis.Capacity{
				Budget:     &analysis.CapacityBudgetObservation{State: analysis.CapacityBudgetFit, Status: analysis.StatusAvailable},
				Resident:   &analysis.ResidentObservation{Status: analysis.StatusAvailable},
				Prediction: &analysis.CapacityPredictionObservation{KVDataType: "f16", Status: analysis.StatusAvailable},
			},
			Performance:  analysis.Performance{DecodeTPS: analysis.PerformanceObservation{Status: analysis.StatusAvailable, SampleCount: 3}},
			ContextTasks: &analysis.ContextTasks{Status: analysis.StatusAvailable, Outcome: "pass", Complete: true, VerifiedPrefixBytes: &prefix},
		},
		Scorecard: score.Scorecard{Needs: map[string]score.Verdict{
			"fast_and_decent":   {State: score.Pass},
			"structured_output": {State: score.Pass}, "instruction_precision": {State: score.Pass},
			"uncensored": {State: score.Pass}, "output_health": {State: score.Pass},
			"coding": {State: score.Pass}, "tool_calling": {State: score.Pass}, "tool_restraint": {State: score.Pass},
			"unattended_agentic": {State: score.Pass},
		}},
	}
}

// Every class passes only on its own direct evidence, and scope identity comes
// from the observation, not from the session.
func TestProjectObservationsPassesEachClassOnDirectEvidence(t *testing.T) {
	observations, scope, conflicts := ProjectObservations([]ObservationSource{passingSource()})
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %v", conflicts)
	}
	for _, class := range []string{ClassMemoryProjection, ClassLoad, ClassSimplePrompt,
		ClassPerformance, ClassBehavior, ClassToolWorkflow, ClassLongContext, ClassHarness} {
		if state, ok := classState(observations, class); !ok || state != StatePass {
			t.Fatalf("class %s = %q ok=%v, want pass", class, state, ok)
		}
	}
	if scope.TestedContext != 65536 || scope.Artifact != "sha256:abc" || scope.Device != "dev-key" || scope.Runtime != "0.33.3" {
		t.Fatalf("scope not taken from observations: %+v", scope)
	}
}

// No evidence is no observations and no invented identity.
func TestProjectObservationsFailsClosedWithoutSources(t *testing.T) {
	observations, scope, _ := ProjectObservations(nil)
	if len(observations) != 0 {
		t.Fatalf("empty sources produced observations: %+v", observations)
	}
	if scope.TestedContext != 0 || scope.Artifact != "" || scope.Device != "" {
		t.Fatalf("empty sources invented identity: %+v", scope)
	}
}

// A skipped need is never a pass, so the class stays unresolved.
func TestProjectObservationsNeverPassesOnSkippedNeed(t *testing.T) {
	src := passingSource()
	src.Scorecard.Needs["tool_restraint"] = score.Verdict{State: score.Skip}
	observations, _, _ := ProjectObservations([]ObservationSource{src})
	if state, ok := classState(observations, ClassToolWorkflow); ok {
		t.Fatalf("tool-or-coding emitted %q while a need was skipped", state)
	}
}

// A descriptive-only observation cannot establish a capacity class.
func TestProjectObservationsDescriptiveOnlyNeverPasses(t *testing.T) {
	src := passingSource()
	src.Report.Capacity.Budget.Status = analysis.StatusDescriptiveOnly
	observations, _, _ := ProjectObservations([]ObservationSource{src})
	if state, ok := classState(observations, ClassMemoryProjection); ok {
		t.Fatalf("memory-projection emitted %q from a descriptive-only budget", state)
	}
}

// A failed document pack fails the long-context class.
func TestProjectObservationsLongContextFailure(t *testing.T) {
	src := passingSource()
	src.Report.ContextTasks = &analysis.ContextTasks{Status: analysis.StatusAvailable, Outcome: "fail"}
	observations, _, _ := ProjectObservations([]ObservationSource{src})
	if state, ok := classState(observations, ClassLongContext); !ok || state != StateFail {
		t.Fatalf("long-context = %q ok=%v, want fail", state, ok)
	}
}

// Sources that disagree on device identity must not be joined into one scope.
func TestProjectObservationsScopeConflict(t *testing.T) {
	first, second := passingSource(), passingSource()
	second.DeviceKey = "other-key"
	_, scope, conflicts := ProjectObservations([]ObservationSource{first, second})
	if scope.Device != "" {
		t.Fatalf("disagreeing devices were joined: %q", scope.Device)
	}
	if len(conflicts) == 0 {
		t.Fatal("disagreeing devices produced no conflict")
	}
}

func TestNeedClassMapping(t *testing.T) {
	cases := map[string]string{
		"structured_output": ClassBehavior, "instruction_precision": ClassBehavior,
		"output_health": ClassBehavior, "uncensored": ClassBehavior,
		"coding": ClassToolWorkflow, "tool_calling": ClassToolWorkflow, "tool_restraint": ClassToolWorkflow,
		"unattended_agentic": ClassHarness, "fast_and_decent": ClassPerformance,
		"vision": "",
	}
	for need, want := range cases {
		if got := NeedClass(need); got != want {
			t.Errorf("NeedClass(%q) = %q, want %q", need, got, want)
		}
	}
}

// Exploration evidence cannot certify a winner: even when every required class
// passes, an exploration recommendation stays unresolved.
func TestExplorationCannotCertifyWinner(t *testing.T) {
	rec := AssessRecommendation(RecommendationInput{
		Role: "coding", Required: []string{ClassLoad},
		Observations: []EvidenceObservation{{Class: ClassLoad, State: StatePass}},
		Exploration:  true,
	})
	if rec.Qualified || rec.Outcome != "unresolved" {
		t.Fatalf("exploration certified a winner: outcome=%q qualified=%v", rec.Outcome, rec.Qualified)
	}
}

// Only a source from resolved fresh confirmation clears the exploration hold;
// the fitting's own exploration does not.
func TestConfirmedSourceCanCertify(t *testing.T) {
	confirmed := passingSource()
	confirmed.Confirmed = true
	rec := evSession().Recommendation(confirmed)
	if !rec.Qualified || rec.Outcome != "supported" {
		t.Fatalf("confirmed evidence did not certify: outcome=%q qualified=%v", rec.Outcome, rec.Qualified)
	}
	if rec := evSession().Recommendation(passingSource()); rec.Qualified || rec.Outcome != "unresolved" {
		t.Fatalf("exploration certified a winner: outcome=%q", rec.Outcome)
	}
}
