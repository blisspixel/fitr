package analysis

import (
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/contextquality"
	"github.com/blisspixel/fitr/internal/record"
)

func conditionReport() contextquality.ConditionReport {
	return contextquality.ConditionReport{
		TargetLabel: "64000", Semantics: contextquality.SemanticsUsableInput,
		State: contextquality.TokenQualified, Uncertainty: contextquality.TokenUncertainty,
		Reason:     "the sealed conditions passed as a fixed battery",
		NextAction: "larger targets and untested positions remain unmeasured",
		Passed:     5, Cases: []contextquality.ConditionCaseResult{{
			Condition: contextquality.ConditionFocused, Family: contextquality.IndirectRetrieval,
			Position: contextquality.Middle, Seed: 7, Outcome: contextquality.CasePass,
		}, {
			Condition: contextquality.ConditionUnrelated, Family: contextquality.IndirectRetrieval,
			Position: contextquality.Middle, Seed: 7, Outcome: contextquality.CaseQualityFailed,
		}},
	}
}

func TestBytePackAndTokenAccountDoNotProjectConditions(t *testing.T) {
	prefix := 64 << 10
	evidence := contextTaskEvidence(t, passingContextTaskReport())
	evidence.Report.VerifiedPrefixUTF8Bytes = &prefix
	if tokenConditionsFrom(&record.Record{ContextQuality: evidence}, false) != nil {
		t.Fatal("a byte pack projected token conditions")
	}
	if tokenConditionsFrom(&record.Record{ContextTokenAccount: &record.ContextTokenAccount{}}, false) != nil {
		t.Fatal("a v1 token account projected token conditions")
	}
	if tokenConditionsFrom(nil, false) != nil || tokenConditionsFrom(&record.Record{}, false) != nil {
		t.Fatal("an absent receipt was projected")
	}
}

func TestTokenConditionsProjectionCopiesTheDerivedClaim(t *testing.T) {
	view := tokenConditionsFrom(&record.Record{ContextTokenConditions: &record.ContextTokenConditions{
		Report: conditionReport(),
	}}, false)
	if view == nil || view.State != contextquality.TokenQualified || view.Tone != TokenToneAccepted || view.TargetLabel != "64000" {
		t.Fatalf("%+v", view)
	}
	if len(view.Cases) != 2 || view.Cases[0].Condition != contextquality.ConditionFocused || view.Cases[1].Outcome != contextquality.CaseQualityFailed {
		t.Fatalf("cases %+v", view.Cases)
	}
	if strings.Contains(view.TargetLabel, "K") || strings.Contains(view.Uncertainty, "%") || strings.Contains(view.Uncertainty, "rate") {
		t.Fatalf("projection abbreviated the target or invented a rate: %+v", view)
	}
}

func TestUnclaimableTokenConditionsDoNotCertify(t *testing.T) {
	view := tokenConditionsFrom(&record.Record{ContextTokenConditions: &record.ContextTokenConditions{
		Report: conditionReport(),
	}}, true)
	if view == nil || view.State == contextquality.TokenQualified || view.Status != StatusDescriptiveOnly || view.Tone != TokenToneGap {
		t.Fatalf("%+v", view)
	}
	if !strings.Contains(view.Reason, "cannot certify") || view.Cases[1].Outcome != contextquality.CaseQualityFailed {
		t.Fatalf("%+v", view)
	}
}
