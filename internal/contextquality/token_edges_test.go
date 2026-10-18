package contextquality

import (
	"strings"
	"testing"
)

func rejectDraft(t *testing.T, mutate func(*TokenPlan), fragment string) {
	t.Helper()
	draft := draftTokenPlan(AccountingNative, nil)
	mutate(&draft)
	_, err := NewTokenPlan(draft)
	if err == nil || !strings.Contains(err.Error(), fragment) {
		t.Fatalf("%v, want %q", err, fragment)
	}
}

func TestTokenPlanRejectsABrokenSchedule(t *testing.T) {
	rejectDraft(t, func(p *TokenPlan) { p.Semantics = "" }, "unsupported")
	rejectDraft(t, func(p *TokenPlan) { p.TargetUsableInputTokens = 0 }, "outside")
	rejectDraft(t, func(p *TokenPlan) { p.GenerationReserveTokens = 0 }, "reserve")
	rejectDraft(t, func(p *TokenPlan) { p.ReasoningReserveTokens = -1 }, "reserve")
	rejectDraft(t, func(p *TokenPlan) { p.RequestedWindowTokens = 10 }, "window")
	rejectDraft(t, func(p *TokenPlan) { p.RequestedWindowTokens = MaxOperatingWindowTokens + 1 }, "window")
	rejectDraft(t, func(p *TokenPlan) { p.OutputBudgetTokens = p.RequestedWindowTokens + 1 }, "output")
	rejectDraft(t, func(p *TokenPlan) { p.Identity.ArtifactDigest = "SHA256:nope" }, "digest")
	rejectDraft(t, func(p *TokenPlan) { p.Identity.WeightQuant = "" }, "quant")
	rejectDraft(t, func(p *TokenPlan) { p.Identity.TemplateDigest = "" }, "template")
	rejectDraft(t, func(p *TokenPlan) { p.Identity.AccountingSource = "guessed" }, "unsupported token accounting")
	rejectDraft(t, func(p *TokenPlan) { p.Order = "shuffled" }, "order")
	rejectDraft(t, func(p *TokenPlan) { p.Repeats = 0 }, "repeats")
	rejectDraft(t, func(p *TokenPlan) { p.Cases = nil }, "empty")
	rejectDraft(t, func(p *TokenPlan) { p.RequestBudget = 257 }, "request budget")
	rejectDraft(t, func(p *TokenPlan) { p.TimeBudgetSeconds = 24*60*60 + 1 }, "time budget")
	rejectDraft(t, func(p *TokenPlan) { p.Cases[0].Family = "nope" }, "family")
}

func TestTokenPlanRejectsRepeatedAndMismatchedCases(t *testing.T) {
	repeated := draftTokenPlan(AccountingNative, []TokenCase{
		{Family: IndirectRetrieval, Position: Middle, Seed: 1},
		{Family: IndirectRetrieval, Position: Middle, Seed: 1},
	})
	repeated.Repeats = 2
	repeated.RequestBudget = 2
	if _, err := NewTokenPlan(repeated); err == nil || !strings.Contains(err.Error(), "repeats a seed") {
		t.Fatal(err)
	}
	mismatched := draftTokenPlan(AccountingNative, []TokenCase{
		{Family: DistantDependency, Position: Beginning, Seed: 1},
		{Family: DistantDependency, Position: End, Seed: 2},
	})
	mismatched.Repeats = 2
	mismatched.RequestBudget = 2
	if _, err := NewTokenPlan(mismatched); err == nil || !strings.Contains(err.Error(), "do not match") {
		t.Fatal(err)
	}
	unknown := draftTokenPlan(AccountingUnknown, nil)
	unknown.Identity.TemplateDigest = "not-a-digest"
	if _, err := NewTokenPlan(unknown); err == nil || !strings.Contains(err.Error(), "template") {
		t.Fatal(err)
	}
	bytePack := draftTokenPlan(AccountingBytePack, nil)
	bytePack.Identity.TemplateDigest = ""
	if _, err := NewTokenPlan(bytePack); err != nil {
		t.Fatal(err)
	}
}

func TestTokenPlanDigestAndDecodeStaySealed(t *testing.T) {
	plan := sealedTokenPlan(t, AccountingNative, nil)
	if _, err := plan.Digest(); err != nil {
		t.Fatal(err)
	}
	raw, err := plan.JSON()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeTokenPlan(raw)
	if err != nil || decoded.PlanSHA256 != plan.PlanSHA256 {
		t.Fatalf("%v %s", err, decoded.PlanSHA256)
	}
	broken := plan
	broken.PlanSHA256 = "sha256:" + strings.Repeat("cd", 32)
	if err := broken.Validate(); err == nil {
		t.Fatal("a rewritten digest validated")
	}
	if _, err := broken.Digest(); err == nil {
		t.Fatal("a rewritten digest was returned")
	}
	if _, err := broken.JSON(); err == nil {
		t.Fatal("a rewritten digest was encoded")
	}
	cloned := plan.Clone()
	cloned.Cases[0].Seed = 99
	if plan.Cases[0].Seed == 99 {
		t.Fatal("clone aliased the case slice")
	}
	if _, err := ParseUsableInputTarget("999999999"); err == nil {
		t.Fatal("a target above the sealed range parsed")
	}
	if _, err := UsableInputLabel(0); err == nil {
		t.Fatal("zero labeled")
	}
	claim, err := QualifyBytePack(Report{}, 0)
	if err == nil || claim.State == TokenQualified {
		t.Fatal(err)
	}
	claim, err = QualifyBytePack(Report{}, 64000)
	if err != nil || claim.State != TokenRefused || !strings.Contains(claim.Reason, "UTF-8 bytes") || strings.Contains(claim.Reason, "65536") {
		t.Fatalf("%v %+v", err, claim)
	}
}

func qualifyNative(t *testing.T, mutate func(*TokenObservationSet), state, fragment string) {
	t.Helper()
	plan := sealedTokenPlan(t, AccountingNative, nil)
	observed := passingTokenSet(plan)
	mutate(&observed)
	assertTokenState(t, plan, observed, state, fragment)
}

func qualifyErr(t *testing.T, mutate func(*TokenObservationSet), fragment string) {
	t.Helper()
	plan := sealedTokenPlan(t, AccountingNative, nil)
	observed := passingTokenSet(plan)
	mutate(&observed)
	_, err := Qualify(plan, observed)
	if err == nil || !strings.Contains(err.Error(), fragment) {
		t.Fatalf("%v, want %q", err, fragment)
	}
}

func TestTokenQualificationFailClosedDispositions(t *testing.T) {
	qualifyNative(t, func(o *TokenObservationSet) { o.Capacity.ConcurrentLoad = ConcurrentContaminated }, TokenContaminated, "contaminated")
	qualifyNative(t, func(o *TokenObservationSet) {
		o.Capacity.AppliedBeforeAllocation = false
		o.Capacity.AppliedBeforePhase = false
		o.Capacity.FreeBytes = nil
		o.Capacity.ObservedAt = ""
	}, TokenRefused, "free-capacity")
	qualifyNative(t, func(o *TokenObservationSet) { o.Cases[0].Identity.ArtifactDigest = digestByte(9) }, TokenRefused, "artifact")
	qualifyNative(t, func(o *TokenObservationSet) { o.Cases[0].Identity.WeightQuant = "Q8_0" }, TokenRefused, "quant")
	qualifyNative(t, func(o *TokenObservationSet) { o.Cases[0].Identity.AccountingSource = AccountingUnknown }, TokenRefused, "accounting")
	qualifyNative(t, func(o *TokenObservationSet) { o.Cases[0].Identity.RuntimeBuild = "other-build" }, TokenRefused, "runtime")
	qualifyNative(t, func(o *TokenObservationSet) { o.Cases[0].Identity.EndpointIdentity = digestByte(9) }, TokenRefused, "endpoint")
	qualifyNative(t, func(o *TokenObservationSet) { o.Cases[0].Identity.KVDtype = "q8_0" }, TokenRefused, "KV")
	qualifyNative(t, func(o *TokenObservationSet) { o.Cases[0].Truncated = true }, TokenRefused, "truncation")
	qualifyNative(t, func(o *TokenObservationSet) { o.Cases[0].Shifted = true }, TokenRefused, "shift")
	qualifyNative(t, func(o *TokenObservationSet) { o.Cases[0].EnforcedWindowTokens = testInt(200000) }, TokenRefused, "not the sealed")
	qualifyNative(t, func(o *TokenObservationSet) { o.Cases[0].Outcome = CaseCapacityLimited }, TokenCapacityLimited, "capacity")
	qualifyNative(t, func(o *TokenObservationSet) { o.Cases[0].Outcome = CaseTransportFailed }, TokenTransportFailed, "transport")
	qualifyNative(t, func(o *TokenObservationSet) { o.Cases[0].Outcome = CaseNotTested }, TokenNotTested, "not run")
	qualifyNative(t, func(o *TokenObservationSet) { o.Capacity.ConcurrentLoad = ConcurrentUnobserved }, TokenMissingAccounting, "unobserved")
	qualifyNative(t, func(o *TokenObservationSet) { o.Cases[0].PayloadTokens = nil }, TokenMissingAccounting, "payload")
	qualifyNative(t, func(o *TokenObservationSet) { o.Cases[0].EnforcedWindowTokens = nil }, TokenMissingAccounting, "enforced window")
}

func TestTokenQualificationRejectsMalformedObservations(t *testing.T) {
	qualifyErr(t, func(o *TokenObservationSet) { o.Capacity.ConcurrentLoad = "busy" }, "concurrent")
	qualifyErr(t, func(o *TokenObservationSet) { o.Capacity.ObservedAt = "yesterday" }, "RFC3339Nano")
	qualifyErr(t, func(o *TokenObservationSet) {
		negative := int64(-1)
		o.Capacity.FreeBytes = &negative
	}, "negative")
	qualifyErr(t, func(o *TokenObservationSet) { o.Cases = append(o.Cases, o.Cases[0]) }, "outside")
	qualifyErr(t, func(o *TokenObservationSet) { o.Cases[0].CaseID = "not-sealed" }, "not in the sealed")
	qualifyErr(t, func(o *TokenObservationSet) { o.Cases[0].Outcome = "maybe" }, "outcome")
	qualifyErr(t, func(o *TokenObservationSet) { o.Cases[0].PayloadTokens = testInt(-1) }, "negative")
	qualifyErr(t, func(o *TokenObservationSet) { o.Cases[0].PayloadUTF8Bytes = testInt((1 << 30) + 1) }, "unbounded")
	repeated := sealedTokenPlan(t, AccountingNative, []TokenCase{
		{Family: IndirectRetrieval, Position: Beginning, Seed: 1},
		{Family: IndirectRetrieval, Position: End, Seed: 2},
	})
	duplicated := passingTokenSet(repeated)
	duplicated.Cases[1].CaseID = duplicated.Cases[0].CaseID
	if _, err := Qualify(repeated, duplicated); err == nil || !strings.Contains(err.Error(), "repeats") {
		t.Fatal(err)
	}
	plan := sealedTokenPlan(t, AccountingUnknown, nil)
	assertTokenState(t, plan, passingTokenSet(plan), TokenMissingAccounting, "unknown")
	draft := draftTokenPlan(AccountingNative, nil)
	draft.Identity.KVDtype = "unobserved"
	sealed, err := NewTokenPlan(draft)
	if err != nil {
		t.Fatal(err)
	}
	assertTokenState(t, sealed, passingTokenSet(sealed), TokenMissingAccounting, "KV dtype")
	native := sealedTokenPlan(t, AccountingNative, nil)
	byteObserved := passingTokenSet(native)
	byteObserved.Cases[0].Identity.AccountingSource = AccountingBytePack
	assertTokenState(t, native, byteObserved, TokenRefused, "byte")
}

func TestTokenWindowsAndClonesDoNotShareCounts(t *testing.T) {
	plan := sealedTokenPlan(t, AccountingNative, []TokenCase{
		{Family: InstructionRetention, Position: Beginning, Seed: 1},
		{Family: InstructionRetention, Position: End, Seed: 1},
	})
	observed := passingTokenSet(plan)
	observed.Cases[0].EnforcedWindowTokens = testInt(131072)
	observed.Cases[1].EnforcedWindowTokens = testInt(65536)
	assertTokenState(t, plan, observed, TokenRefused, "changed between cases")
	reserve := int64(10)
	peak := int64(20)
	observed = passingTokenSet(plan)
	observed.Cases[1].EnforcedWindowTokens = testInt(131072)
	observed.Capacity.ReserveBytes = &reserve
	observed.Capacity.PeakBytes = &peak
	observed.Capacity.FreeBytes = nil
	report, err := Qualify(plan, observed)
	if err != nil || !strings.Contains(report.FreeMemory, "unmeasured") {
		t.Fatalf("%v %q", err, report.FreeMemory)
	}
	cloned := observed.Clone()
	cloned.Cases[0].PayloadTokens = testInt(1)
	if observed.Cases[0].PayloadTokens == nil || *observed.Cases[0].PayloadTokens == 1 {
		t.Fatal("observation clone aliased a count")
	}
	copied := report.Clone()
	copied.Cases[0].Outcome = CaseQualityFailed
	if report.Cases[0].Outcome == CaseQualityFailed {
		t.Fatal("report clone aliased a case")
	}
}
