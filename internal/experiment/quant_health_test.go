package experiment

import (
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/record"
)

func TestQuantHealthWithholdsAnUnreadCacheAndAMissingSelfPair(t *testing.T) {
	left := healthRecord(t, quantDigestA, "", nil)
	right := healthRecord(t, quantDigestB, "", nil)
	health := quantHealth([]*record.Record{left, right})
	if health.Schema != QuantHealthSchema || health.State != "withheld" ||
		!strings.Contains(health.Reason, "not measured") || strings.Contains(health.Reason, "equal") {
		t.Fatalf("unread cache became a comparison: %+v", health)
	}
	same := healthRecord(t, quantDigestA, "f16", nil)
	other := healthRecord(t, quantDigestB, "f16", nil)
	health = quantHealth([]*record.Record{same, other})
	if health.State != "withheld" || !strings.Contains(health.Reason, "self-against-self") {
		t.Fatalf("two artifacts without a repeat became a health claim: %+v", health)
	}
	changed := healthRecord(t, quantDigestB, "q8_0", nil)
	health = quantHealth([]*record.Record{same, changed})
	if health.State != "withheld" || !strings.Contains(health.Reason, "differs") {
		t.Fatalf("a cache dtype change was ignored: %+v", health)
	}
}

func TestQuantHealthSeparatesOnlyAboveTheSameArtifact(t *testing.T) {
	crossLeft := healthRecord(t, quantDigestA, "f16", familyChecks("structured_output", 6, true))
	crossRight := healthRecord(t, quantDigestB, "f16", familyChecks("structured_output", 6, false))
	crossRight.Checks = append(crossRight.Checks, eval.CheckOutcome{
		TaskID: "tools", Family: "call", Need: "tool_calling", Seed: 1, Outcome: eval.OutcomeInconclusive,
	})
	selfAgain := healthRecord(t, quantDigestA, "f16", familyChecks("structured_output", 6, true))
	health := quantHealth([]*record.Record{crossLeft, selfAgain, crossRight})
	if health.State != "above_self" || !needAboveSelf(health, "structured_output") {
		t.Fatalf("a zero self floor did not leave the cross pair above it: %+v", health)
	}
	if fail, other := channelCounts(health, quantDigestB, "tool_calling"); fail != 0 || other != 1 {
		t.Fatalf("an inconclusive tool check became a failure: fail=%d other=%d", fail, other)
	}
	noisy := healthRecord(t, quantDigestA, "f16", familyChecks("structured_output", 6, false))
	health = quantHealth([]*record.Record{crossLeft, noisy, crossRight})
	if health.State == "above_self" || needAboveSelf(health, "structured_output") {
		t.Fatalf("the same artifact already moved as much as the other quant: %+v", health)
	}
}

func healthRecord(t *testing.T, digest, cache string, checks []eval.CheckOutcome) *record.Record {
	t.Helper()
	identity, err := record.NewModelIdentity("model", "model", "ollama", "test", digest, "", 8)
	if err != nil {
		t.Fatal(err)
	}
	result := &record.Record{Checks: checks, Manifest: &record.RunManifest{Model: identity}}
	if cache != "" {
		result.DeviceV2 = &device.FingerprintV2{Device: device.Fingerprint{
			Config: map[string]string{"OLLAMA_KV_CACHE_TYPE": cache},
		}}
	}
	return result
}

func familyChecks(need string, families int, pass bool) []eval.CheckOutcome {
	checks := make([]eval.CheckOutcome, families)
	outcome := eval.OutcomePass
	if !pass {
		outcome = eval.OutcomeFail
	}
	for i := range checks {
		checks[i] = eval.CheckOutcome{
			TaskID: need, Family: "f" + string(rune('a'+i)), Need: need,
			Seed: uint64(i + 1), Pass: pass, Outcome: outcome,
		}
	}
	return checks
}

func channelCounts(health QuantHealth, digest, need string) (int, int) {
	for _, row := range health.Channels {
		if row.ArtifactDigest == digest && row.Need == need {
			return row.Fail, row.Other
		}
	}
	return -1, -1
}

func needAboveSelf(health QuantHealth, need string) bool {
	for _, row := range health.Needs {
		if row.Need == need && row.AboveSelf {
			return true
		}
	}
	return false
}
