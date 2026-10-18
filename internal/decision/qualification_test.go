package decision

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/device"
)

func TestOmittedEffortDoesNotPoolWithExplicit(t *testing.T) {
	omitted := EffortRef{Set: true, Omitted: true}
	explicit := EffortRef{Set: true, Value: "low"}
	filled := EffortRef{Set: true, Value: "xhigh"}
	err := PoolEfforts(omitted, explicit)
	if err == nil || !strings.Contains(err.Error(), "does not pool") {
		t.Fatalf("omitted pooled with explicit: %v", err)
	}
	if err = PoolEfforts(omitted, filled); err == nil || strings.Contains(filled.Value, "default") {
		t.Fatalf("omitted became xhigh: %v", err)
	}
	if omitted.Value != "" {
		t.Fatalf("omitted effort was filled: %+v", omitted)
	}
}

func TestExplicitEffortPoolsOnlyWithTheSameToken(t *testing.T) {
	low := EffortRef{Set: true, Value: "low"}
	if err := PoolEfforts(low, low); err != nil {
		t.Fatal(err)
	}
	if err := PoolEfforts(low, EffortRef{Set: true, Value: "high"}); err == nil {
		t.Fatal("different explicit efforts pooled")
	}
}

func TestAbsentEffortStaysUncompared(t *testing.T) {
	absent := EffortRef{}
	explicit := EffortRef{Set: true, Value: "low"}
	err := PoolEfforts(absent, explicit)
	if err == nil || !strings.Contains(err.Error(), "absent") {
		t.Fatalf("absent effort became a default: %v", err)
	}
	if err = PoolEfforts(absent, absent); err == nil {
		t.Fatal("two absent efforts pooled")
	}
}

func TestRejectedApplicationStates(t *testing.T) {
	for _, application := range []string{"observed", "unsupported", "ignored"} {
		err := SealApplication(application, applicationUnobserved)
		if err == nil || !strings.Contains(err.Error(), "rejected") {
			t.Fatalf("%s sealed: %v", application, err)
		}
	}
}

func TestUnknownWithoutProvenanceIsRejected(t *testing.T) {
	err := SealApplication("unknown", "")
	if err == nil || !strings.Contains(err.Error(), "missing provenance") {
		t.Fatalf("unknown without provenance sealed: %v", err)
	}
	if err = SealApplication("unknown", applicationUnobserved); err != nil {
		t.Fatal(err)
	}
}

func TestSignalsCannotWriteAnAppliedMode(t *testing.T) {
	got := ApplicationFromSignals(ApplicationSignals{
		Elapsed: 30, OutputLength: 4000, CapabilityFlag: true, SelfReport: "used xhigh",
	})
	if got != "unknown" {
		t.Fatalf("signals wrote %q", got)
	}
	if err := SealApplication(got, "elapsed 30s"); err == nil {
		t.Fatal("a signal provenance sealed unknown")
	}
}

func TestUnknownTemplateDoesNotPoolWithADigest(t *testing.T) {
	unknown := TemplateRef{Set: true}
	digested := TemplateRef{Set: true, Digest: strings.Repeat("ab", 32)}
	err := PoolTemplates(unknown, digested)
	if err == nil || !strings.Contains(err.Error(), "does not pool") {
		t.Fatalf("unknown template pooled with a digest: %v", err)
	}
}

func TestAbsentTemplateStaysUncompared(t *testing.T) {
	err := PoolTemplates(TemplateRef{}, TemplateRef{Set: true, Digest: strings.Repeat("ab", 32)})
	if err == nil || !strings.Contains(err.Error(), "absent") {
		t.Fatalf("absent template became a digest: %v", err)
	}
}

func TestTemplateDigestDisagreementStaysUncompared(t *testing.T) {
	left := TemplateRef{Set: true, Digest: strings.Repeat("ab", 32)}
	right := TemplateRef{Set: true, Digest: strings.Repeat("cd", 32)}
	if err := PoolTemplates(left, left); err != nil {
		t.Fatal(err)
	}
	err := PoolTemplates(left, right)
	if err == nil || !strings.Contains(err.Error(), "differ") {
		t.Fatalf("different digests pooled: %v", err)
	}
}

func fixedPair(outcome string) (PairLeg, PairLeg) {
	base := PairLeg{
		Task:     TaskRef{Set: true, ID: "explain-diff"},
		Reserves: Reserves{Set: true, User: "keep the public function", System: 200, Output: 800, Reasoning: 400},
		Budget:   Budget{Set: true, Limit: "120s"},
		Outcome:  outcome,
	}
	return base, base
}

func TestFastMandatoryFailureStaysFailed(t *testing.T) {
	base, variant := fixedPair("quality-failed")
	variant.Fast = true
	variant.Mandatory = true
	got := QualifyPair(base, variant)
	if !got.Qualified || got.State != "quality-failed" || got.State == "pass" {
		t.Fatalf("fast failure became a pass: %+v", got)
	}
}

func TestReducedReservesDoNotBecomeAPass(t *testing.T) {
	base, variant := fixedPair("pass")
	variant.Reserves.Output = base.Reserves.Output - 1
	got := QualifyPair(base, variant)
	if got.Qualified || got.State == "pass" || !strings.Contains(got.Missing, "reduced") {
		t.Fatalf("reduced reserves became a pass: %+v", got)
	}
	if got.Action == "" {
		t.Fatal("reduced reserves named no action")
	}
}

func TestTruncationIsNotAWrongAnswerOrAPass(t *testing.T) {
	base, variant := fixedPair("truncated")
	got := QualifyPair(base, variant)
	if !got.Qualified || got.State != "truncated" || got.State == "quality-failed" || got.State == "pass" {
		t.Fatalf("truncation changed disposition: %+v", got)
	}
}

func TestNoProgressIsNotAWrongAnswerOrAPass(t *testing.T) {
	base, variant := fixedPair("no-progress")
	got := QualifyPair(base, variant)
	if !got.Qualified || got.State != "no-progress" || got.State == "quality-failed" || got.State == "pass" {
		t.Fatalf("no-progress changed disposition: %+v", got)
	}
}

func TestPairOutcomesStayDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, outcome := range []string{"pass", "quality-failed", "truncated", "no-progress", "incomplete", "refused", "transport-failed"} {
		base, variant := fixedPair(outcome)
		got := QualifyPair(base, variant)
		encoded, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Qualified || got.State != outcome || seen[got.State] || !strings.Contains(string(encoded), `"comparison":"paired task"`) {
			t.Fatalf("outcome %s collapsed: %+v %s", outcome, got, encoded)
		}
		seen[got.State] = true
	}
	if len(seen) != 7 {
		t.Fatalf("dispositions collapsed: %v", seen)
	}
}

func TestQualificationDoesNotExtendTheDeviceFingerprint(t *testing.T) {
	const fingerprintFieldCount = 15
	got := reflect.TypeOf(device.Fingerprint{}).NumField()
	if got != fingerprintFieldCount {
		t.Fatalf("device fingerprint field count = %d, qualification must leave it at %d", got, fingerprintFieldCount)
	}
}
