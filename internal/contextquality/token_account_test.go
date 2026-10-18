package contextquality

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"unicode/utf8"
)

func digestByte(value byte) string {
	sum := sha256.Sum256([]byte{value})
	return "sha256:" + hex.EncodeToString(sum[:])
}

func tokenIdentity(source string) TokenIdentity {
	return TokenIdentity{
		ArtifactDigest: digestByte(1), WeightQuant: "Q4_K_M", TemplateDigest: digestByte(2),
		AccountingSource: source, RuntimeBuild: "ollama-0.33.3", EndpointIdentity: digestByte(3),
		DeviceFingerprint: digestByte(4), KVDtype: "f16",
	}
}

func draftTokenPlan(source string, cases []TokenCase) TokenPlan {
	if len(cases) == 0 {
		cases = []TokenCase{{Family: IndirectRetrieval, Position: Middle, Seed: 1}}
	}
	return TokenPlan{
		TargetUsableInputTokens: 64000, Semantics: SemanticsUsableInput, Identity: tokenIdentity(source),
		GenerationReserveTokens: 128, RequestedWindowTokens: 131072, Cases: cases, Repeats: 1,
		Order: TokenOrder, Threshold: TokenThreshold, StoppingRule: TokenStoppingRule,
		RequestBudget: len(cases), TimeBudgetSeconds: 600, OutputBudgetTokens: 128,
	}
}

func sealedTokenPlan(t *testing.T, source string, cases []TokenCase) TokenPlan {
	t.Helper()
	plan, err := NewTokenPlan(draftTokenPlan(source, cases))
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func testInt(value int) *int {
	copied := value
	return &copied
}

func passingTokenSet(plan TokenPlan) TokenObservationSet {
	free := int64(1 << 30)
	set := TokenObservationSet{Capacity: TokenCapacityObservation{
		ObservedAt: "2026-10-17T12:00:00.000000000Z", FreeBytes: &free,
		AppliedBeforeAllocation: true, AppliedBeforePhase: true, ConcurrentLoad: ConcurrentNone,
	}}
	for _, item := range plan.Cases {
		set.Cases = append(set.Cases, TokenCaseObservation{
			CaseID: item.ID, Outcome: CasePass, Identity: plan.Identity,
			PayloadTokens: testInt(64000), OverheadTokens: testInt(200), PayloadUTF8Bytes: testInt(80000),
			EnforcedWindowTokens: testInt(131072), CacheObserved: true, CachedTokens: testInt(0),
			GeneratedTokens: testInt(1),
		})
	}
	return set
}

func assertTokenState(t *testing.T, plan TokenPlan, observed TokenObservationSet, state, fragment string) {
	t.Helper()
	report, err := Qualify(plan, observed)
	if err != nil {
		t.Fatal(err)
	}
	if report.State != state || !strings.Contains(report.Reason, fragment) || strings.Contains(report.Reason, "%") {
		t.Fatalf("state %q reason %q, want %q containing %q", report.State, report.Reason, state, fragment)
	}
}

func TestSixtyFourKiBPackDoesNotCertify64000UsableTokens(t *testing.T) {
	prefix := 64 << 10
	report := Report{NativeTokenAccounting: "native", Outcome: Pass, VerifiedPrefixUTF8Bytes: &prefix}
	claim, err := QualifyBytePack(report, 64000)
	if err != nil {
		t.Fatal(err)
	}
	if claim.State == TokenQualified || claim.TargetLabel != "64000" || !strings.Contains(claim.Reason, "65536") {
		t.Fatalf("byte pack certified or mislabeled the target: %+v", claim)
	}
	if prefix == 64000 {
		t.Fatal("the fixture collapsed 64 KiB into 64000")
	}
	plan := sealedTokenPlan(t, AccountingBytePack, nil)
	observed := passingTokenSet(plan)
	observed.Cases[0].PayloadUTF8Bytes = testInt(prefix)
	observed.Cases[0].PayloadTokens = testInt(64000)
	assertTokenState(t, plan, observed, TokenRefused, "65536")
	if !strings.Contains(claim.Reason, "64000") {
		t.Fatalf("reason %q omitted the sealed integer", claim.Reason)
	}
}

func TestUsableInputTargetRejectsKAndByteSuffixes(t *testing.T) {
	for _, text := range []string{"64K", "64k", "64KiB", "64,000", "64000 tokens", "064000", ""} {
		if _, err := ParseUsableInputTarget(text); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
	got, err := ParseUsableInputTarget("64000")
	if err != nil || got != 64000 {
		t.Fatalf("64000 parsed as %d (%v)", got, err)
	}
	kiB, err := ParseUsableInputTarget("65536")
	if err != nil || kiB != 65536 || kiB == got {
		t.Fatalf("65536 parsed as %d (%v), which must stay distinct from 64000", kiB, err)
	}
	label, err := UsableInputLabel(64000)
	if err != nil || label != "64000" {
		t.Fatalf("label %q (%v)", label, err)
	}
}

func TestMultiByteTextIsNotATokenCount(t *testing.T) {
	text := strings.Repeat("é", 1000)
	if len(text) == utf8.RuneCountInString(text) {
		t.Fatal("fixture is not multi-byte")
	}
	if _, err := UsableInputTokensFromText(text); err == nil || !strings.Contains(err.Error(), "not usable-input tokens") {
		t.Fatal(err)
	}
	if _, err := UsableInputTokensFromText(strings.Repeat("a", 64<<10)); err == nil {
		t.Fatal("a 64 KiB string produced a token count")
	}
}

func TestTotalWindowIsNotUsableInput(t *testing.T) {
	draft := draftTokenPlan(AccountingNative, nil)
	draft.Semantics = SemanticsTotalWindow
	if _, err := NewTokenPlan(draft); err == nil || !strings.Contains(err.Error(), "not usable input") {
		t.Fatal(err)
	}
}

func TestNativeAccountQualifiesAFittingReservation(t *testing.T) {
	plan := sealedTokenPlan(t, AccountingNative, nil)
	report, err := Qualify(plan, passingTokenSet(plan))
	if err != nil {
		t.Fatal(err)
	}
	if report.State != TokenQualified || report.TargetLabel != "64000" || report.Passed != 1 || report.Failed != 0 {
		t.Fatalf("qualified report = %+v", report)
	}
	if report.Uncertainty != TokenUncertainty || strings.Contains(report.Uncertainty, "%") {
		t.Fatalf("uncertainty = %q", report.Uncertainty)
	}
	if report.ObservedPayloadTokens == nil || *report.ObservedPayloadTokens != 64000 {
		t.Fatalf("payload = %v", report.ObservedPayloadTokens)
	}
}

func TestObservedPayloadDoesNotRelabelTheTarget(t *testing.T) {
	plan := sealedTokenPlan(t, AccountingNative, nil)
	observed := passingTokenSet(plan)
	observed.Cases[0].PayloadTokens = testInt(70000)
	report, err := Qualify(plan, observed)
	if err != nil {
		t.Fatal(err)
	}
	if report.State != TokenQualified || report.TargetLabel != "64000" || report.TargetUsableInputTokens != 64000 {
		t.Fatalf("target was relabeled: %+v", report)
	}
	if report.ObservedPayloadTokens == nil || *report.ObservedPayloadTokens != 70000 {
		t.Fatalf("observed payload = %v", report.ObservedPayloadTokens)
	}
}

func TestTokenizerMismatchRefusesTheTokenClaim(t *testing.T) {
	plan := sealedTokenPlan(t, AccountingNative, nil)
	observed := passingTokenSet(plan)
	observed.Cases[0].Identity.TemplateDigest = digestByte(9)
	assertTokenState(t, plan, observed, TokenRefused, "template")
}

func TestChangedFingerprintRefusesTheTokenClaim(t *testing.T) {
	plan := sealedTokenPlan(t, AccountingNative, nil)
	observed := passingTokenSet(plan)
	observed.Cases[0].Identity.DeviceFingerprint = digestByte(9)
	assertTokenState(t, plan, observed, TokenRefused, "fingerprint")
}

func TestUnknownOverheadStaysUnmeasured(t *testing.T) {
	plan := sealedTokenPlan(t, AccountingNative, nil)
	observed := passingTokenSet(plan)
	observed.Cases[0].OverheadTokens = nil
	assertTokenState(t, plan, observed, TokenMissingAccounting, "overhead")
}

func TestUnknownCacheCannotQualify(t *testing.T) {
	plan := sealedTokenPlan(t, AccountingNative, nil)
	unobserved := passingTokenSet(plan)
	unobserved.Cases[0].CacheObserved = false
	assertTokenState(t, plan, unobserved, TokenMissingAccounting, "cache")
	missing := passingTokenSet(plan)
	missing.Cases[0].CachedTokens = nil
	assertTokenState(t, plan, missing, TokenMissingAccounting, "cache")
}

func TestInsufficientReserveDoesNotPass(t *testing.T) {
	// The enforced window matches the sealed window and can hold the one
	// emitted token. It cannot hold the sealed 128-token reserve.
	draft := draftTokenPlan(AccountingNative, nil)
	draft.RequestedWindowTokens = 64000 + 200 + 1
	plan, err := NewTokenPlan(draft)
	if err != nil {
		t.Fatal(err)
	}
	observed := passingTokenSet(plan)
	observed.Cases[0].GeneratedTokens = testInt(1)
	observed.Cases[0].EnforcedWindowTokens = testInt(draft.RequestedWindowTokens)
	assertTokenState(t, plan, observed, TokenCapacityLimited, "reserve")
}

func TestReducedWindowDoesNotQualify(t *testing.T) {
	plan := sealedTokenPlan(t, AccountingNative, nil)
	observed := passingTokenSet(plan)
	observed.Cases[0].EnforcedWindowTokens = testInt(8192)
	assertTokenState(t, plan, observed, TokenRefused, "smaller")
}

func TestOnePositionFailureIsNotErased(t *testing.T) {
	plan := sealedTokenPlan(t, AccountingNative, []TokenCase{
		{Family: IndirectRetrieval, Position: Middle, Seed: 1},
		{Family: IndirectRetrieval, Position: End, Seed: 2},
	})
	observed := passingTokenSet(plan)
	observed.Cases[1].Outcome = CaseQualityFailed
	assertTokenState(t, plan, observed, TokenQualityFailed, "does not erase")
}

func TestMissingCaseIsNotTested(t *testing.T) {
	plan := sealedTokenPlan(t, AccountingNative, []TokenCase{
		{Family: DistantDependency, Position: Beginning, Seed: 1},
		{Family: DistantDependency, Position: End, Seed: 2},
	})
	observed := passingTokenSet(plan)
	observed.Cases = observed.Cases[:1]
	assertTokenState(t, plan, observed, TokenNotTested, "not run")
}

func TestPayloadBelowTheTargetIsNotShrunk(t *testing.T) {
	plan := sealedTokenPlan(t, AccountingNative, nil)
	observed := passingTokenSet(plan)
	observed.Cases[0].PayloadTokens = testInt(63999)
	assertTokenState(t, plan, observed, TokenRefused, "below the sealed target")
}

func TestTokenPlanDoesNotAliasCallerCases(t *testing.T) {
	draft := draftTokenPlan(AccountingNative, nil)
	sealed, err := NewTokenPlan(draft)
	if err != nil {
		t.Fatal(err)
	}
	draft.Cases[0].Family = DistantDependency
	if sealed.Cases[0].Family != IndirectRetrieval || sealed.PlanSHA256 == "" {
		t.Fatalf("sealed plan followed the caller: %+v", sealed.Cases[0])
	}
	if _, err := DecodeTokenPlan([]byte(`{"schema":"fitr.context-token-account.v1"}`)); err == nil {
		t.Fatal("partial plan decoded")
	}
}
