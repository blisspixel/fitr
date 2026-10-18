package analysis

import (
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/contextquality"
	"github.com/blisspixel/fitr/internal/record"
)

func TestBytePackDoesNotProjectAsTokenAccount(t *testing.T) {
	prefix := 64 << 10
	evidence := contextTaskEvidence(t, passingContextTaskReport())
	evidence.Report.VerifiedPrefixUTF8Bytes = &prefix
	evidence.Report.NativeTokenAccounting = "native"
	view := tokenAccountFrom(&record.Record{ContextQuality: evidence}, false)
	if view != nil {
		t.Fatalf("64 KiB byte pack projected a token account: %+v", view)
	}
}

func TestTokenAccountProjectionCopiesTheDerivedClaim(t *testing.T) {
	payload := 64000
	window := 131072
	view := tokenAccountFrom(&record.Record{ContextTokenAccount: &record.ContextTokenAccount{
		Report: contextquality.TokenReport{
			TargetLabel: "64000", Semantics: contextquality.SemanticsUsableInput,
			State: contextquality.TokenQualified, Uncertainty: contextquality.TokenUncertainty,
			Reason:                "the sealed usable-input target passed as a fixed battery",
			NextAction:            "larger targets and untested families remain unmeasured",
			ObservedPayloadTokens: &payload, GenerationReserveTokens: 128,
			RequestedWindowTokens: window, EnforcedWindowTokens: &window, Passed: 1, Seeds: 1,
			FreeMemory: "free memory was unmeasured at 2026-10-17T12:00:00Z",
		},
	}}, false)
	if view == nil || view.State != contextquality.TokenQualified || view.Tone != TokenToneAccepted || view.TargetLabel != "64000" {
		t.Fatalf("%+v", view)
	}
	if view.PayloadLabel != "64000" || view.WindowLabel != "enforced 131072; requested 131072" {
		t.Fatalf("payload %q window %q", view.PayloadLabel, view.WindowLabel)
	}
	if strings.Contains(view.TargetLabel, "K") || strings.Contains(view.Uncertainty, "%") {
		t.Fatalf("projection abbreviated the target: %+v", view)
	}
}

func TestUnclaimableTokenAccountDoesNotCertify(t *testing.T) {
	view := tokenAccountFrom(&record.Record{ContextTokenAccount: &record.ContextTokenAccount{
		Report: contextquality.TokenReport{
			TargetLabel: "64000", Semantics: contextquality.SemanticsUsableInput,
			State: contextquality.TokenQualified,
		},
	}}, true)
	if view == nil || view.State == contextquality.TokenQualified || view.Status != StatusDescriptiveOnly || view.Tone != TokenToneGap {
		t.Fatalf("%+v", view)
	}
	if !strings.Contains(view.Reason, "cannot certify") {
		t.Fatal(view.Reason)
	}
}

func TestTokenAccountLabelsLeaveUnmeasuredCountsUnmeasured(t *testing.T) {
	if tokenAccountFrom(nil, false) != nil || tokenAccountFrom(&record.Record{}, false) != nil {
		t.Fatal("an absent receipt was projected")
	}
	view := tokenAccountFrom(&record.Record{ContextTokenAccount: &record.ContextTokenAccount{
		Report: contextquality.TokenReport{
			State: "refused", GenerationReserveTokens: 128, ReasoningReserveTokens: 32,
			RequestedWindowTokens: 131072,
			Cases: []contextquality.TokenCaseResult{{
				Family: contextquality.IndirectRetrieval, Position: contextquality.Middle, Seed: 1, Outcome: "refused",
			}},
		},
	}}, false)
	if view == nil || view.Tone != TokenToneGap || view.PayloadLabel != "unmeasured" || view.OverheadLabel != "unmeasured" {
		t.Fatalf("%+v", view)
	}
	if !strings.Contains(view.ReserveLabel, "reasoning 32") || !strings.Contains(view.WindowLabel, "unmeasured") {
		t.Fatalf("reserve %q window %q", view.ReserveLabel, view.WindowLabel)
	}
	if len(view.Cases) != 1 || view.Cases[0].Outcome != "refused" {
		t.Fatalf("cases %+v", view.Cases)
	}
}
