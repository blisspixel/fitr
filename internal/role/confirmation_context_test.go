package role

import (
	"testing"

	"github.com/blisspixel/fitr/internal/record"
)

func TestConfirmationContextGuardRunsBeforeCompletion(t *testing.T) {
	plan, points, _ := roleConfirmationFixture(t)
	point := roleConfirmationClone(t, points[0])
	point.Completion = nil
	if err := ValidateConfirmationContextPoint(plan, point); err != nil {
		t.Fatalf("matching freshly observed context rejected before completion: %v", err)
	}
	for name, mutate := range map[string]func(*record.Record){
		"missing fingerprint": func(result *record.Record) { result.DeviceV2 = nil },
		"missing context":     func(result *record.Record) { result.DeviceV2.Context.EffectiveTokens = nil },
		"changed effective context": func(result *record.Record) {
			*result.DeviceV2.Context.EffectiveTokens /= 2
		},
		"changed requested context": func(result *record.Record) {
			result.DeviceV2.Context.RequestedTokens /= 2
		},
		"invalid fingerprint": func(result *record.Record) { result.DeviceV2.Schema = "invalid" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := roleConfirmationClone(t, point)
			mutate(changed)
			if ValidateConfirmationContextPoint(plan, changed) == nil {
				t.Fatal("known context mismatch permitted the battery")
			}
		})
	}
	if ValidateConfirmationContextPoint(plan, nil) == nil {
		t.Fatal("missing point permitted the battery")
	}
	plan.Protocol.ComparabilityKey = "changed-device"
	plan.PlanSHA256 = ""
	plan.PlanSHA256, _ = confirmationDigest(ConfirmationPlanSchema, plan)
	if ValidateConfirmationContextPoint(plan, point) == nil {
		t.Fatal("different device comparability key accepted")
	}
	plan.Schema = "invalid"
	if ValidateConfirmationContextPoint(plan, point) == nil {
		t.Fatal("invalid issued plan accepted")
	}
}
