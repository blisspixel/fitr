package record

import (
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/contextquality"
)

func recordConditionPlan(t *testing.T) contextquality.TokenConditionPlan {
	t.Helper()
	plan, err := contextquality.NewMatchedConditionPlan(contextquality.TokenConditionPlan{
		TargetUsableInputTokens: 64000, Semantics: contextquality.SemanticsUsableInput,
		Identity: contextquality.TokenIdentity{
			ArtifactDigest: recordDigest(1), WeightQuant: "Q4_K_M", TemplateDigest: recordDigest(2),
			AccountingSource: contextquality.AccountingNative, RuntimeBuild: "ollama-0.33.3",
			EndpointIdentity: recordDigest(3), DeviceFingerprint: recordDigest(4), KVDtype: "f16",
		},
	}, contextquality.IndirectRetrieval, contextquality.Middle, 7)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func recordConditionObservations(plan contextquality.TokenConditionPlan, payload int, outcome string) contextquality.ConditionObservationSet {
	set := contextquality.ConditionObservationSet{}
	for _, item := range plan.Cases {
		set.Cases = append(set.Cases, contextquality.ConditionObservation{
			CaseID: item.ID, Outcome: outcome, PayloadTokens: recordTokenCount(payload), OverheadTokens: recordTokenCount(12),
		})
	}
	return set
}

func TestTokenConditionsReDeriveAndRejectAStoredClaim(t *testing.T) {
	plan := recordConditionPlan(t)
	fresh := &Record{}
	if err := fresh.PlanContextTokenConditions(plan); err != nil {
		t.Fatal(err)
	}
	if err := fresh.AttachContextTokenConditions(plan, recordConditionObservations(plan, 64000, contextquality.CasePass)); err != nil {
		t.Fatal(err)
	}
	if fresh.ContextTokenConditions.Report.State != contextquality.TokenQualified || fresh.ContextTokenConditions.Report.TargetLabel != "64000" {
		t.Fatalf("report = %+v", fresh.ContextTokenConditions.Report)
	}
	fresh.ContextTokenConditions.Report.State = contextquality.TokenQualityFailed
	if err := fresh.validateContextTokenConditions(); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatal(err)
	}
}

func TestTokenConditionsKeepAShortPayloadUnqualified(t *testing.T) {
	plan := recordConditionPlan(t)
	fresh := &Record{}
	if err := fresh.PlanContextTokenConditions(plan); err != nil {
		t.Fatal(err)
	}
	if err := fresh.AttachContextTokenConditions(plan, recordConditionObservations(plan, 64, contextquality.CasePass)); err != nil {
		t.Fatal(err)
	}
	if fresh.ContextTokenConditions.Report.State != contextquality.TokenRefused || fresh.ContextTokenConditions.Report.TargetLabel != "64000" {
		t.Fatalf("short payload qualified the sealed target: %+v", fresh.ContextTokenConditions.Report)
	}
}

func TestTokenConditionsAttachRequiresTheSeal(t *testing.T) {
	plan := recordConditionPlan(t)
	observed := recordConditionObservations(plan, 64000, contextquality.CasePass)
	fresh := &Record{}
	if err := fresh.AttachContextTokenConditions(plan, observed); err == nil || fresh.ContextTokenConditions != nil {
		t.Fatal(err)
	}
	late := &Record{Manifest: &RunManifest{}}
	if err := late.PlanContextTokenConditions(plan); err == nil || !strings.Contains(err.Error(), "before the run manifest") {
		t.Fatal(err)
	}
	var missing *Record
	if err := missing.PlanContextTokenConditions(plan); err == nil {
		t.Fatal("planned on nil")
	}
	if err := missing.AttachContextTokenConditions(plan, observed); err == nil {
		t.Fatal("attached to nil")
	}
	if err := fresh.PlanContextTokenConditions(plan); err != nil {
		t.Fatal(err)
	}
	if err := fresh.PlanContextTokenConditions(plan); err == nil || !strings.Contains(err.Error(), "already sealed") {
		t.Fatal(err)
	}
	if err := fresh.validateContextTokenConditions(); err == nil || !strings.Contains(err.Error(), "no attached") {
		t.Fatal(err)
	}
	if err := fresh.AttachContextTokenConditions(plan, observed); err != nil {
		t.Fatal(err)
	}
	if err := fresh.AttachContextTokenConditions(plan, observed); err == nil || !strings.Contains(err.Error(), "already attached") {
		t.Fatal(err)
	}
	fresh.TaskPlan.TokenConditionCases = 1
	if err := fresh.validateContextTokenConditions(); err == nil || !strings.Contains(err.Error(), "cases") {
		t.Fatal(err)
	}
	fresh.TaskPlan.TokenConditionCases = len(plan.Cases)
	fresh.TaskPlan.TokenConditionPlanSHA256 = "sha256:" + strings.Repeat("cd", 32)
	if err := fresh.validateContextTokenConditions(); err == nil || !strings.Contains(err.Error(), "sealed plan") {
		t.Fatal(err)
	}
	if cloneContextTokenConditions(nil) != nil {
		t.Fatal("nil clone")
	}
	dup := cloneContextTokenConditions(fresh.ContextTokenConditions)
	dup.Plan.Cases[0].Seed = 9
	dup.Observed.Cases[0].Outcome = contextquality.CaseQualityFailed
	if fresh.ContextTokenConditions.Plan.Cases[0].Seed == 9 || fresh.ContextTokenConditions.Observed.Cases[0].Outcome == contextquality.CaseQualityFailed {
		t.Fatal("clone aliased the receipt")
	}
}

func TestTokenConditionPlanRejectsADigestThatDoesNotValidate(t *testing.T) {
	fresh := &Record{}
	if err := fresh.PlanContextTokenConditions(contextquality.TokenConditionPlan{}); err == nil {
		t.Fatal("an unsealed condition plan was planned")
	}
	plan := recordConditionPlan(t)
	if err := fresh.PlanContextTokenConditions(plan); err != nil {
		t.Fatal(err)
	}
	if err := fresh.AttachContextTokenConditions(plan, recordConditionObservations(plan, 64000, contextquality.CasePass)); err != nil {
		t.Fatal(err)
	}
	fresh.ContextTokenConditions.Plan.Schema = "nope"
	if err := fresh.validateContextTokenConditions(); err == nil {
		t.Fatal("a broken plan digest still matched")
	}
	fresh.ContextTokenConditions.Plan.Schema = plan.Schema
	fresh.ContextTokenConditions.Observed.Cases[1].CaseID = fresh.ContextTokenConditions.Observed.Cases[0].CaseID
	if err := fresh.validateContextTokenConditions(); err == nil {
		t.Fatal("repeated observations still matched the report")
	}
}

func TestLegacyRecordRejectsTokenConditions(t *testing.T) {
	legacy := &Record{SchemaVersion: EvidenceSchemaVersion - 1, ContextTokenConditions: &ContextTokenConditions{}}
	err := legacy.ValidateEvidenceContract()
	if err == nil || !strings.Contains(err.Error(), "legacy") {
		t.Fatal(err)
	}
}
