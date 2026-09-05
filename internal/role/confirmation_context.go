package role

import (
	"errors"

	"github.com/blisspixel/fitr/internal/record"
)

// ValidateConfirmationContextPoint checks the freshly observed placement and
// effective context before the battery runs. A context probe may be needed to
// obtain these observations. Final confirmation analysis repeats this same
// check against sealed evidence; preparation alone does not qualify a point.
func ValidateConfirmationContextPoint(plan ConfirmationPlan, result *record.Record) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	if result == nil || result.DeviceV2 == nil || result.DeviceV2.Context.EffectiveTokens == nil {
		return errors.New("confirmation effective context is unverified")
	}
	key, err := result.DeviceV2.ComparabilityKey()
	if err != nil || key != plan.Protocol.ComparabilityKey || *result.DeviceV2.Context.EffectiveTokens != plan.Protocol.EffectiveContext {
		return errors.New("confirmation verified device or context differs")
	}
	return nil
}
