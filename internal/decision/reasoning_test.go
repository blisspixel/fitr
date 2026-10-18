package decision

import (
	"strings"
	"testing"
)

func TestOmittedEffortIsNotADefaultAndUnknownCannotCertify(t *testing.T) {
	omitted := reasoningAccount("")
	if omitted.Requested != "" || omitted.Application != "unknown" || strings.Contains(omitted.Note, "xhigh") {
		t.Fatalf("omitted effort was filled in: %+v", omitted)
	}
	explicit := reasoningAccount("low")
	if explicit.Requested != "low" || explicit.Application != "unknown" {
		t.Fatalf("explicit effort changed shape: %+v", explicit)
	}
	labeled := ReasoningAccount{Schema: ReasoningAccountSchema, Requested: "low", Application: "observed"}
	err := CertifyEffortComparison(labeled, labeled)
	if err == nil || !strings.Contains(err.Error(), "not certified") || !strings.Contains(err.Error(), "no observation protocol") {
		t.Fatalf("a labeled observation certified an effort comparison: %v", err)
	}
	err = CertifyEffortComparison(omitted, explicit)
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown applications certified a comparison: %v", err)
	}
}

func TestRequestedEffortStaysUnresolved(t *testing.T) {
	result := sealedDecisionRecord(t)
	spec := DecisionSpec{
		Schema: SpecSchema, Name: "effort", Evidence: EvidenceDecide,
		Requirements: []Requirement{{
			ID: "context", Context: &ContextRequirement{MinimumEffectiveTokens: 4096},
		}},
		Reasoning: &ReasoningRequest{Effort: "low"},
	}
	evaluation, err := Evaluate(result, spec)
	if err != nil {
		t.Fatal(err)
	}
	if evaluation.State == DecisionEligible || evaluation.Eligibility == DecisionEligible {
		t.Fatalf("an unobserved effort became eligible: %+v", evaluation)
	}
	if evaluation.Reasoning == nil || evaluation.Reasoning.Requested != "low" ||
		evaluation.Reasoning.Application != "unknown" || strings.Contains(evaluation.Reasoning.Note, "xhigh") {
		t.Fatalf("requested effort was not retained: %+v", evaluation.Reasoning)
	}
	joined := evaluation.Gaps
	text := strings.Join(joined, " ")
	if !strings.Contains(text, "not observed") || !strings.Contains(text, "capability") {
		t.Fatalf("evaluation did not name the missing application: %s", text)
	}
}
