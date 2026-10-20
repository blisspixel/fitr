package contextquality

import (
	"encoding/json"
	"strings"
	"testing"
)

func matchedConditionPlan(t *testing.T, retrieval string) TokenConditionPlan {
	t.Helper()
	plan, err := NewMatchedConditionPlan(TokenConditionPlan{
		TargetUsableInputTokens: 64000, Semantics: SemanticsUsableInput,
		Identity: tokenIdentity(AccountingNative), RetrievalPolicySHA256: retrieval,
	}, IndirectRetrieval, Middle, 7)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func passConditions(plan TokenConditionPlan, payload int) ConditionObservationSet {
	set := ConditionObservationSet{}
	for _, item := range plan.Cases {
		set.Cases = append(set.Cases, ConditionObservation{
			CaseID: item.ID, Outcome: CasePass, PayloadTokens: testInt(payload), OverheadTokens: testInt(12),
		})
	}
	return set
}

func TestConditionPackKeepsTheAnswerOutOfTheAbsentCell(t *testing.T) {
	focused, err := ConditionPack(ConditionFocused, "")
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := ConditionPack(ConditionUnrelated, "")
	if err != nil {
		t.Fatal(err)
	}
	absent, err := ConditionPack(ConditionAnswerAbsent, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(focused, ConditionAnswer) || strings.Contains(absent, ConditionAnswer) {
		t.Fatal("answer-absent pack contains the answer, or the focused pack does not")
	}
	if len(focused) >= len(unrelated) || strings.Contains(focused, "oak table") || !strings.Contains(unrelated, "oak table") {
		t.Fatal("focused baseline was padded to the unrelated pack")
	}
	if strings.Contains(absent, "oak table") {
		t.Fatal("answer-absent pack was padded with filler")
	}
}

func TestConditionIdentityIsNotFamilyPositionSeed(t *testing.T) {
	plan := matchedConditionPlan(t, "")
	if len(plan.Cases) != len(conditionOrder) {
		t.Fatalf("cases = %d", len(plan.Cases))
	}
	left, right := plan.Cases[0], plan.Cases[1]
	if left.Family != right.Family || left.Position != right.Position || left.Seed != right.Seed {
		t.Fatal("matched cells do not share the work")
	}
	if left.Condition == right.Condition || left.ContentSHA256 == right.ContentSHA256 || left.ID == right.ID || SameConditionCell(left, right) {
		t.Fatal("different conditions compared as one cell")
	}
	v1Left, err := tokenCaseID(plan.TargetUsableInputTokens, TokenCase{Sequence: 1, Family: left.Family, Position: left.Position, Seed: left.Seed})
	if err != nil {
		t.Fatal(err)
	}
	v1Right, err := tokenCaseID(plan.TargetUsableInputTokens, TokenCase{Sequence: 1, Family: right.Family, Position: right.Position, Seed: right.Seed})
	if err != nil {
		t.Fatal(err)
	}
	if v1Left != v1Right {
		t.Fatal("setup: v1 identity should collide when the condition is ignored")
	}
	sameSequence, err := conditionCaseID(plan.TargetUsableInputTokens, TokenConditionCase{
		Sequence: left.Sequence, Family: left.Family, Position: left.Position, Seed: left.Seed,
		Condition: right.Condition, ContentSHA256: right.ContentSHA256,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sameSequence == left.ID {
		t.Fatal("a different condition at the same sequence compared as one cell")
	}
}

func TestRetrievalPolicyChangesOnlyItsCell(t *testing.T) {
	without := matchedConditionPlan(t, "")
	first := strings.Repeat("ab", 32)
	second := strings.Repeat("cd", 32)
	withFirst := matchedConditionPlan(t, "sha256:"+first)
	withSecond := matchedConditionPlan(t, "sha256:"+second)
	if without.Cases[0].ID != withFirst.Cases[0].ID {
		t.Fatal("a retrieval policy changed the focused cell")
	}
	if len(withFirst.Cases) != len(without.Cases)+1 || withFirst.Cases[len(withFirst.Cases)-1].ID == withSecond.Cases[len(withSecond.Cases)-1].ID {
		t.Fatal("retrieval policy did not change its own cell")
	}
}

func TestSubstitutedConditionDigestIsRejected(t *testing.T) {
	plan := matchedConditionPlan(t, "")
	plan.Cases[0].ContentSHA256 = strings.Repeat("ab", 32)
	plan.PlanSHA256, _ = hashValue(TokenConditionSchema, plan)
	if err := plan.Validate(); err == nil {
		t.Fatal("a substituted pack digest still validated")
	}
}

func TestOneConditionFailureStaysVisible(t *testing.T) {
	plan := matchedConditionPlan(t, "")
	observed := passConditions(plan, 64000)
	observed.Cases[2].Outcome = CaseQualityFailed
	observed.Cases[0].PayloadTokens = nil
	report, err := QualifyConditions(plan, observed)
	if err != nil {
		t.Fatal(err)
	}
	if report.State != TokenQualityFailed || report.Cases[2].Outcome != CaseQualityFailed || report.Cases[0].Outcome != CaseMissingAccounting {
		t.Fatalf("report = %+v", report)
	}
	if strings.Contains(report.Uncertainty, "rate") && strings.Contains(report.Uncertainty, "confidence") {
		t.Fatal("uncertainty became a rate")
	}
}

func TestShortPayloadDoesNotQualifyTheTarget(t *testing.T) {
	plan := matchedConditionPlan(t, "")
	report, err := QualifyConditions(plan, passConditions(plan, 64))
	if err != nil {
		t.Fatal(err)
	}
	if report.State == TokenQualified || report.State != TokenRefused {
		t.Fatalf("state = %s", report.State)
	}
}

func TestConditionPlanIsNotAV1TokenPlan(t *testing.T) {
	plan := matchedConditionPlan(t, "")
	raw, err := plan.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeTokenPlan(raw); err == nil {
		t.Fatal("v2 condition plan decoded as a v1 token plan")
	}
	if _, err := DecodeTokenConditionPlan([]byte(`{"schema":"fitr.context-token-account.v2","schema":"dup"}`)); err == nil {
		t.Fatal("duplicate schema was accepted")
	}
}

func TestConditionReceiptRejectsABrokenSchedule(t *testing.T) {
	if _, err := ConditionPack(ConditionRetrieval, ""); err == nil {
		t.Fatal("retrieval pack accepted a missing policy")
	}
	if _, err := ConditionPack("nope", ""); err == nil {
		t.Fatal("unknown condition was packed")
	}
	if _, err := NewMatchedConditionPlan(TokenConditionPlan{
		TargetUsableInputTokens: 64000, Semantics: SemanticsUsableInput,
		Identity: tokenIdentity(AccountingNative), RetrievalPolicySHA256: "nope",
	}, IndirectRetrieval, Middle, 7); err == nil {
		t.Fatal("invalid retrieval policy sealed a plan")
	}
	plan := matchedConditionPlan(t, "")
	sum, err := plan.Digest()
	if err != nil || sum != plan.PlanSHA256 {
		t.Fatalf("digest %s err %v", sum, err)
	}
	cloned := plan.Clone()
	cloned.Cases[0].Seed = 1
	if plan.Cases[0].Seed == 1 {
		t.Fatal("plan clone aliased the cases")
	}
	broken := plan
	broken.Semantics = "bytes"
	if _, err := broken.Digest(); err == nil {
		t.Fatal("digest accepted a plan that does not validate")
	}
	if _, err := broken.JSON(); err == nil {
		t.Fatal("json accepted a plan that does not validate")
	}
	if _, err := DecodeTokenConditionPlan(nil); err == nil {
		t.Fatal("empty condition plan decoded")
	}
	if _, err := DecodeTokenConditionPlan([]byte(`{"schema":"fitr.context-token-account.v2"}`)); err == nil {
		t.Fatal("incomplete condition plan decoded")
	}
	shifted := plan
	shifted.Cases[0].ID = "sha256:" + strings.Repeat("ab", 32)
	if err := shifted.Validate(); err == nil {
		t.Fatal("a substituted case id still validated")
	}
}

func TestConditionShapeNamesTheMismatch(t *testing.T) {
	base := matchedConditionPlan(t, "")
	cases := []TokenConditionPlan{
		func() TokenConditionPlan { p := base; p.TargetUsableInputTokens = 0; return p }(),
		func() TokenConditionPlan {
			p := base
			p.Identity.ArtifactDigest = ""
			return p
		}(),
		func() TokenConditionPlan { p := base; p.RetrievalPolicySHA256 = "nope"; return p }(),
		func() TokenConditionPlan { p := base; p.Cases = p.Cases[:4]; return p }(),
		func() TokenConditionPlan {
			p := base.Clone()
			p.Cases[0].Family = "nope"
			return p
		}(),
		func() TokenConditionPlan {
			p := base.Clone()
			p.Cases[1].Seed = 9
			return p
		}(),
	}
	for _, plan := range cases {
		if err := plan.Validate(); err == nil {
			t.Fatalf("broken plan validated: %+v", plan)
		}
	}
	if _, err := QualifyConditions(TokenConditionPlan{}, ConditionObservationSet{}); err == nil {
		t.Fatal("an unsealed condition plan qualified")
	}
	unsealed := base.Clone()
	unsealed.Semantics = "bytes"
	raw, err := json.Marshal(unsealed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeTokenConditionPlan(raw); err == nil {
		t.Fatal("a decoded plan with the wrong semantics validated")
	}
	if sameConditionCases(base.Cases, base.Cases[:len(base.Cases)-1]) {
		t.Fatal("a shorter case list compared as the same schedule")
	}
}

func TestConditionDispositionsStayDistinct(t *testing.T) {
	plan := matchedConditionPlan(t, "")
	set := passConditions(plan, 64000)
	set.Cases[0].Outcome = CaseQualityFailed
	set.Cases[0].PayloadTokens = nil
	failed, err := QualifyConditions(plan, set)
	if err != nil || failed.State != TokenQualityFailed || failed.Cases[0].Outcome != CaseQualityFailed {
		t.Fatalf("nil-count quality failure = %+v err %v", failed, err)
	}
	missing := passConditions(plan, 64000)
	missing.Cases[1].OverheadTokens = nil
	report, err := QualifyConditions(plan, missing)
	if err != nil || report.State != TokenMissingAccounting {
		t.Fatalf("missing accounting = %+v err %v", report, err)
	}
	for _, outcome := range []string{CaseCapacityLimited, CaseTransportFailed, CaseNotTested, "nope"} {
		observed := passConditions(plan, 64000)
		observed.Cases[3].Outcome = outcome
		report, err = QualifyConditions(plan, observed)
		if err != nil || report.State == TokenQualified {
			t.Fatalf("outcome %s qualified: %+v err %v", outcome, report, err)
		}
	}
	short := passConditions(plan, 64000)
	short.Cases = short.Cases[:4]
	if _, err := QualifyConditions(plan, short); err == nil {
		t.Fatal("a short observation set qualified")
	}
	repeated := passConditions(plan, 64000)
	repeated.Cases[1].CaseID = repeated.Cases[0].CaseID
	if _, err := QualifyConditions(plan, repeated); err == nil {
		t.Fatal("a repeated cell qualified")
	}
	absent := passConditions(plan, 64000)
	absent.Cases[1].CaseID = "sha256:" + strings.Repeat("ef", 32)
	if _, err := QualifyConditions(plan, absent); err == nil {
		t.Fatal("an unknown cell qualified")
	}
}

func TestConditionCopiesDoNotShareTheMeasuredCell(t *testing.T) {
	plan := matchedConditionPlan(t, "")
	set := passConditions(plan, 64000)
	set.Cases[0].Outcome = CaseQualityFailed
	set.Cases[0].PayloadTokens = nil
	failed, err := QualifyConditions(plan, set)
	if err != nil {
		t.Fatal(err)
	}
	cloned := set.Clone()
	cloned.Cases[0].PayloadTokens = testInt(1)
	if set.Cases[0].PayloadTokens != nil {
		t.Fatal("observation clone aliased the payload")
	}
	copied := failed.Clone()
	copied.Cases[0].Outcome = CasePass
	if failed.Cases[0].Outcome == CasePass {
		t.Fatal("report clone aliased the cases")
	}
	if !payloadBelowTarget(ConditionReport{Cases: []ConditionCaseResult{{CaseID: "id", Outcome: CasePass}}}, map[string]ConditionObservation{"id": {}}, 10) {
		t.Fatal("a pass with no payload was treated as the sealed target")
	}
	if conditionState(ConditionReport{}, CasePass) {
		t.Fatal("an empty report contained a pass")
	}
}

func TestMatchedConditionsQualifyAsAFixedBattery(t *testing.T) {
	plan := matchedConditionPlan(t, "")
	report, err := QualifyConditions(plan, passConditions(plan, 64000))
	if err != nil {
		t.Fatal(err)
	}
	if report.State != TokenQualified || report.TargetLabel != "64000" || report.Failed != 0 || report.Unresolved != 0 {
		t.Fatalf("report = %+v", report)
	}
	if !strings.Contains(report.Uncertainty, "not a confidence interval") {
		t.Fatalf("uncertainty = %s", report.Uncertainty)
	}
}
