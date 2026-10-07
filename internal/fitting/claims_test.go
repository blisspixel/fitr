package fitting

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/eval"
)

func jsonMarshal(value any) (string, error) {
	encoded, err := json.Marshal(value)
	return string(encoded), err
}

func TestUntestedContextProjectionDoesNotClaimAFit(t *testing.T) {
	got := UntestedContextProjection(124672, 32768)
	if !strings.Contains(got, "untested projection") || !strings.Contains(got, "124672") || !strings.Contains(got, "32768") {
		t.Fatal(got)
	}
	if strings.Contains(got, "this device fits") {
		t.Fatal(got)
	}
	if UntestedContextProjection(32768, 32768) != "" || UntestedContextProjection(0, 32768) != "" {
		t.Fatal("measured or unknown window produced a projection")
	}
}

func TestComparisonRequiresTheSharedSeed(t *testing.T) {
	req := ceilingRequest(t)
	req.Candidates = []string{"qwen3:30b", "devstral:24b"}
	plan, err := Draft(req, testTasks(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := Paired(plan, plan.SeedSet, plan.SeedSet); err != nil {
		t.Fatal(err)
	}
	if err := Paired(plan, plan.SeedSet, "other-seed"); err == nil {
		t.Fatal("different cases were paired")
	}
	one, err := Draft(ceilingRequest(t), testTasks(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := Paired(one, one.SeedSet, one.SeedSet); err == nil {
		t.Fatal("one candidate became a comparison")
	}
	if err := FreshConfirmation(plan, plan.SeedSet); err == nil {
		t.Fatal("exploration evidence became confirmation")
	}
	if err := FreshConfirmation(one, "fresh-seed"); err == nil {
		t.Fatal("assessment gained a confirmation winner")
	}
}

func TestHistoryDoesNotBecomeFreshConfirmation(t *testing.T) {
	plan, err := Draft(ceilingRequest(t), testTasks(t))
	if err != nil {
		t.Fatal(err)
	}
	match, err := ConsiderHistory(plan, plan.Candidates[0], plan.SeedSet, plan.ContextTokens, plan.CapacityKind, plan.CapacityBytes, plan.Endpoint)
	if err != nil || !match.Reuse || match.Fresh {
		t.Fatal(match, err)
	}
	mismatch, err := ConsiderHistory(plan, plan.Candidates[0], "other", plan.ContextTokens, plan.CapacityKind, plan.CapacityBytes, plan.Endpoint)
	if err != nil || mismatch.Reuse {
		t.Fatal(mismatch, err)
	}
}

func TestFailureLineDoesNotReconstructAMissingResponse(t *testing.T) {
	line := FailureLine(eval.CheckOutcome{
		TaskID: "tool-args", Family: "tool_args", Seed: 7, Pass: false, Outcome: eval.OutcomeFail,
		ParserOutcome: "missing_param", FailingField: "batch", Verifier: "fitr.eval.grade/v1",
		InputSHA256: "sha256:aa",
	})
	for _, want := range []string{"tool-args", "tool_args", "missing_param", "batch", "fitr.eval.grade/v1", "sha256:aa", "not the native tool-call channel", "not retained"} {
		if !strings.Contains(line, want) {
			t.Fatalf("missing %q in %s", want, line)
		}
	}
	if strings.Contains(line, "native tool support is broken") {
		t.Fatal(line)
	}
	if FailureLine(eval.CheckOutcome{Pass: false, Outcome: eval.OutcomeSkipped}) != "" {
		t.Fatal("a skip became a model failure")
	}
	old := FailureLine(eval.CheckOutcome{TaskID: "old", Family: "format", Outcome: eval.OutcomeFail})
	if !strings.Contains(old, "not recorded") || !strings.Contains(old, "not retained") {
		t.Fatal(old)
	}
}

func TestResumeDoesNotMintAConfirmationSeed(t *testing.T) {
	plan, err := Draft(ceilingRequest(t), testTasks(t))
	if err != nil {
		t.Fatal(err)
	}
	session := Session{Schema: SessionSchema, Phase: PhaseMeasured, Plan: plan}
	action, err := session.ResumeAction()
	if err != nil || action != "status" {
		t.Fatal(action, err)
	}
	if _, err := session.AdoptionAction(); err == nil || !strings.Contains(err.Error(), "No Ollama alias") {
		t.Fatal(err)
	}
	session.Phase = PhaseAdoptionClosed
	if _, err := session.ResumeAction(); err == nil || !strings.Contains(err.Error(), "confirmation seed") {
		t.Fatal(err)
	}
	session.Phase = PhaseMeasuring
	session.ConfirmationSeed = "fresh"
	if err := session.Validate(); err == nil {
		t.Fatal("replacement confirmation seed was stored")
	}
}

func TestLoadAndPromptDoNotQualifyAFailedToolWorkflow(t *testing.T) {
	rec := AssessRecommendation(RecommendationInput{
		Role:     "coding",
		Required: []string{ClassLoad, ClassSimplePrompt, ClassToolWorkflow},
		Scope: RecommendationScope{
			Artifact: "sha256:abc model:demo", Runtime: "ollama http://127.0.0.1:11434",
			Configuration: "context 8192, kv f16", Device: "local", Workload: "agentic-coding",
			TestedContext: 8192,
			Uncertainty:   []string{"unknown runtime overhead", "unmeasured cache type", "skipped executable tests", "small sample"},
		},
		Observations: []EvidenceObservation{
			{Class: ClassMemoryProjection, State: StatePass, Detail: "weights plus KV suggest 124672"},
			{Class: ClassLoad, State: StatePass},
			{Class: ClassSimplePrompt, State: StatePass},
			{Class: ClassPerformance, State: StatePass},
			{Class: ClassToolWorkflow, State: StateFail, Detail: "required tool call failed"},
		},
		ProjectionTokens: 124672, MeasuredTokens: 8192, RequestedContext: 8192,
	})
	text := strings.Join(rec.Lines, "\n")
	if rec.Qualified || rec.Outcome != "no-qualified-candidate" || rec.PaidSubstitute || rec.LongContextTested {
		t.Fatalf("%+v", rec)
	}
	for _, want := range []string{"passed: memory-projection, load, simple-prompt, performance", "failed: tool-or-coding", "role coding remains unsupported", "unknown runtime overhead", "unmeasured cache type", "skipped executable tests", "small sample", "no qualified candidate", "not a failed product experience", "a memory projection is not tested long-context quality", "not demonstrated task quality"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q\n%s", want, text)
		}
	}
	for _, forbidden := range []string{"recommended", "this device fits", "lowered the requirement", "switched to paid"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("summary hid a limit with %q\n%s", forbidden, text)
		}
	}
	encoded, err := jsonMarshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(encoded, `"qualified":false`) || !strings.Contains(encoded, `"long_context_tested":false`) || !strings.Contains(encoded, `"paid_substitute":false`) {
		t.Fatal(encoded)
	}
}

func TestMemoryProjectionIsNotTestedLongContext(t *testing.T) {
	rec := AssessRecommendation(RecommendationInput{
		Role: "research", Required: []string{ClassLongContext},
		Scope: RecommendationScope{Artifact: "sha256:def", TestedContext: 32768},
		Observations: []EvidenceObservation{
			{Class: ClassMemoryProjection, State: StatePass},
			{Class: ClassLoad, State: StatePass},
		},
		ProjectionTokens: 124672, MeasuredTokens: 32768, RequestedContext: 124672,
	})
	text := strings.Join(rec.Lines, "\n")
	if rec.LongContextTested || rec.Qualified || sliceHas(rec.Passed, ClassLongContext) {
		t.Fatalf("projection became tested long-context quality: %+v", rec)
	}
	if rec.RequestedContext != 124672 {
		t.Fatalf("requested context changed to %d", rec.RequestedContext)
	}
	for _, want := range []string{"a memory projection is not tested long-context quality", "untested projection", "124672", "32768", "we do not have enough evidence"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q\n%s", want, text)
		}
	}
}

func TestConflictsStayVisibleAndDoNotLowerTheRequest(t *testing.T) {
	rec := AssessRecommendation(RecommendationInput{
		Role: "daily", Required: []string{ClassLoad},
		Observations:     []EvidenceObservation{{Class: ClassLoad, State: StatePass}},
		RequestedContext: 64000, ProjectionTokens: 32768, MeasuredTokens: 8192,
		Conflicts: []string{"desired context 64000 exceeds the projected memory"},
	})
	text := strings.Join(rec.Lines, "\n")
	if rec.Qualified || rec.Outcome == "supported" || rec.RequestedContext != 64000 || rec.PaidSubstitute {
		t.Fatalf("%+v", rec)
	}
	for _, want := range []string{"tradeoff: desired context 64000 exceeds the projected memory", "The requirement was not lowered", "Cloud is not an automatic substitute", "A quality requirement was not relaxed"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q\n%s", want, text)
		}
	}
}

func TestNoEvidenceIsNotAProductFailure(t *testing.T) {
	rec := AssessRecommendation(RecommendationInput{Role: "daily"})
	text := strings.Join(rec.Lines, "\n")
	if rec.Qualified || rec.Outcome != "unresolved" || !strings.Contains(text, "we do not have enough evidence") {
		t.Fatalf("%+v\n%s", rec, text)
	}
	if strings.Contains(text, "failed product") || strings.Contains(text, "recommended") {
		t.Fatal(text)
	}
}

func TestSessionPhaseCannotEstablishTestedContextOrDevice(t *testing.T) {
	plan, err := Draft(ceilingRequest(t), testTasks(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{PhaseMeasured, PhaseDelegated, PhaseAdoptionClosed} {
		rec := (Session{Schema: SessionSchema, Plan: plan, Phase: phase}).Recommendation()
		if rec.Scope.TestedContext != 0 || rec.Scope.Device != "" {
			t.Fatalf("phase %s invented measurement identity: %+v", phase, rec.Scope)
		}
	}
}
