package record

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/contextquality"
)

func recordDigest(value byte) string {
	sum := sha256.Sum256([]byte{value})
	return "sha256:" + hex.EncodeToString(sum[:])
}

func recordTokenPlan(t *testing.T) contextquality.TokenPlan {
	t.Helper()
	plan, err := contextquality.NewTokenPlan(contextquality.TokenPlan{
		TargetUsableInputTokens: 64000, Semantics: contextquality.SemanticsUsableInput,
		Identity: contextquality.TokenIdentity{
			ArtifactDigest: recordDigest(1), WeightQuant: "Q4_K_M", TemplateDigest: recordDigest(2),
			AccountingSource: contextquality.AccountingNative, RuntimeBuild: "ollama-0.33.3",
			EndpointIdentity: recordDigest(3), DeviceFingerprint: recordDigest(4), KVDtype: "f16",
		},
		GenerationReserveTokens: 128, RequestedWindowTokens: 131072,
		Cases:   []contextquality.TokenCase{{Family: contextquality.IndirectRetrieval, Position: contextquality.Middle, Seed: 1}},
		Repeats: 1, Order: contextquality.TokenOrder, Threshold: contextquality.TokenThreshold,
		StoppingRule: contextquality.TokenStoppingRule, RequestBudget: 1, TimeBudgetSeconds: 600,
		OutputBudgetTokens: 128,
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func recordTokenCount(value int) *int {
	copied := value
	return &copied
}

func recordTokenObservations(plan contextquality.TokenPlan) contextquality.TokenObservationSet {
	free := int64(1 << 30)
	item := plan.Cases[0]
	return contextquality.TokenObservationSet{
		Capacity: contextquality.TokenCapacityObservation{
			ObservedAt: "2026-10-17T12:00:00.000000000Z", FreeBytes: &free,
			AppliedBeforeAllocation: true, AppliedBeforePhase: true,
			ConcurrentLoad: contextquality.ConcurrentNone,
		},
		Cases: []contextquality.TokenCaseObservation{{
			CaseID: item.ID, Outcome: contextquality.CasePass, Identity: plan.Identity,
			PayloadTokens: recordTokenCount(64000), OverheadTokens: recordTokenCount(200),
			EnforcedWindowTokens: recordTokenCount(131072), CacheObserved: true,
			CachedTokens: recordTokenCount(0), GeneratedTokens: recordTokenCount(1),
		}},
	}
}

func TestAbsentTokenAccountKeepsSignedBytes(t *testing.T) {
	record, err := json.Marshal(&Record{SchemaVersion: EvidenceSchemaVersion, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(completedEvidencePayload{})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(record, []byte("context_token_account")) || bytes.Contains(payload, []byte("context_token_account")) {
		t.Fatal("absent token account changed a record or signed payload")
	}
}

func TestTokenAccountReDerivesAndRejectsAStoredClaim(t *testing.T) {
	plan := recordTokenPlan(t)
	fresh := &Record{}
	if err := fresh.PlanContextTokenAccount(plan); err != nil {
		t.Fatal(err)
	}
	if err := fresh.AttachContextTokenAccount(plan, recordTokenObservations(plan)); err != nil {
		t.Fatal(err)
	}
	if fresh.ContextTokenAccount.Report.State != contextquality.TokenQualified || fresh.ContextTokenAccount.Report.TargetLabel != "64000" {
		t.Fatalf("report = %+v", fresh.ContextTokenAccount.Report)
	}
	fresh.ContextTokenAccount.Report.State = contextquality.TokenQualityFailed
	if err := fresh.validateContextTokenAccount(); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatal(err)
	}
}

func TestTokenAccountAttachRequiresTheSeal(t *testing.T) {
	plan := recordTokenPlan(t)
	fresh := &Record{}
	if err := fresh.AttachContextTokenAccount(plan, recordTokenObservations(plan)); err == nil || fresh.ContextTokenAccount != nil {
		t.Fatal(err)
	}
	late := &Record{Manifest: &RunManifest{}}
	if err := late.PlanContextTokenAccount(plan); err == nil || !strings.Contains(err.Error(), "before the run manifest") {
		t.Fatal(err)
	}
}

func TestTokenAccountSealIsOnceAndDerived(t *testing.T) {
	plan := recordTokenPlan(t)
	observed := recordTokenObservations(plan)
	var missing *Record
	if err := missing.PlanContextTokenAccount(plan); err == nil {
		t.Fatal("planned on nil")
	}
	if err := missing.AttachContextTokenAccount(plan, observed); err == nil {
		t.Fatal("attached to nil")
	}
	fresh := &Record{}
	if err := fresh.PlanContextTokenAccount(plan); err != nil {
		t.Fatal(err)
	}
	if err := fresh.PlanContextTokenAccount(plan); err == nil || !strings.Contains(err.Error(), "already sealed") {
		t.Fatal(err)
	}
	if err := fresh.validateContextTokenAccount(); err == nil || !strings.Contains(err.Error(), "no attached") {
		t.Fatal(err)
	}
	negative := recordTokenObservations(plan)
	negative.Cases[0].PayloadTokens = recordTokenCount(-1)
	if err := fresh.AttachContextTokenAccount(plan, negative); err == nil || fresh.ContextTokenAccount != nil {
		t.Fatal(err)
	}
	if err := fresh.AttachContextTokenAccount(plan, observed); err != nil {
		t.Fatal(err)
	}
	if err := fresh.AttachContextTokenAccount(plan, observed); err == nil || !strings.Contains(err.Error(), "already attached") {
		t.Fatal(err)
	}
	fresh.TaskPlan.TokenAccountCases = 2
	if err := fresh.validateContextTokenAccount(); err == nil || !strings.Contains(err.Error(), "cases") {
		t.Fatal(err)
	}
	fresh.TaskPlan.TokenAccountCases = 1
	fresh.TaskPlan.TokenAccountPlanSHA256 = "sha256:" + strings.Repeat("cd", 32)
	if err := fresh.validateContextTokenAccount(); err == nil || !strings.Contains(err.Error(), "sealed plan") {
		t.Fatal(err)
	}
	if cloneContextTokenAccount(nil) != nil {
		t.Fatal("nil clone")
	}
	dup := cloneContextTokenAccount(fresh.ContextTokenAccount)
	dup.Plan.Cases[0].Seed = 9
	if fresh.ContextTokenAccount.Plan.Cases[0].Seed == 9 {
		t.Fatal("clone aliased the plan")
	}
}

func TestLegacyRecordRejectsATokenAccount(t *testing.T) {
	legacy := &Record{SchemaVersion: EvidenceSchemaVersion - 1, ContextTokenAccount: &ContextTokenAccount{}}
	err := legacy.ValidateEvidenceContract()
	if err == nil || !strings.Contains(err.Error(), "legacy") {
		t.Fatal(err)
	}
}
