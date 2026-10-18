package experiment

import (
	"errors"
	"fmt"
	"math"
	"reflect"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/stats"
)

// TimedObjective reports whether a confirmation winner for this metric is
// decided by non-overlapping t intervals. Exact byte metrics are not.
func TimedObjective(metric string) bool {
	_, _, ok := metricObservation(metric)
	return ok
}

// ClaimableVariation reads the coefficient of variation for metric from one
// claimable sealed result. A display-only or descriptive observation stays
// unmeasured. The value is this result's spread, not a license to treat
// another device or context as the same noise.
func ClaimableVariation(result *record.Record, metric string) (float64, error) {
	if result == nil {
		return 0, errors.New("noise source is unavailable")
	}
	if issue := result.EvidenceIntegrityIssue(); issue != "" {
		return 0, fmt.Errorf("noise source is not claimable: %s", issue)
	}
	report, err := analysis.FromRecord(result)
	if err != nil {
		return 0, err
	}
	return Variation(report, metric)
}

// Variation reads cv from a report that already passed claim checks. The
// samples have to be present: a stored standard deviation without them is
// not reused.
func Variation(report analysis.Report, metric string) (float64, error) {
	pick, claim, ok := metricObservation(metric)
	if !ok {
		return 0, errors.New("objective metric has no repeated-mean interval")
	}
	observation := pick(report.Performance)
	if observation.Status != analysis.StatusAvailable || !analysis.Supports(observation, claim) {
		return 0, errors.New("noise source does not establish this metric")
	}
	if observation.SampleCount != len(observation.Samples) {
		return 0, errors.New("noise source sample count does not match its samples")
	}
	cv := stats.SampleCV(observation.Samples)
	if !positiveCV(cv) {
		return 0, errors.New("noise source coefficient of variation is unmeasured")
	}
	return cv, nil
}

// NoiseContext requires the sealed requested and effective context to be the
// confirmation's requested context. An unverified effective context cannot
// supply the variation for that plan.
func NoiseContext(result *record.Record, requested int) error {
	if result == nil || result.DeviceV2 == nil {
		return errors.New("noise source is missing fingerprint v2")
	}
	if result.NumCtx != requested || result.DeviceV2.Context.RequestedTokens != requested {
		return errors.New("noise source requested context differs from this confirmation")
	}
	effective := result.DeviceV2.Context.EffectiveTokens
	if effective == nil || *effective != requested {
		return errors.New("noise source effective context is unverified or differs from this confirmation")
	}
	return nil
}

// NoiseDevice requires the live fingerprint to be the one sealed in the
// noise result. A coefficient of variation does not travel across devices.
func NoiseDevice(live device.Fingerprint, result *record.Record) error {
	if result == nil || result.DeviceV2 == nil {
		return errors.New("noise source is missing fingerprint v2")
	}
	if !reflect.DeepEqual(live, result.DeviceV2.Device) {
		return errors.New("noise source device fingerprint differs from this confirmation")
	}
	return nil
}

func metricObservation(metric string) (func(analysis.Performance) analysis.PerformanceObservation, analysis.SupportClaim, bool) {
	switch metric {
	case "decode_tps":
		return func(p analysis.Performance) analysis.PerformanceObservation { return p.DecodeTPS },
			analysis.ClaimObservedDecode, true
	case "prefill_tps":
		return func(p analysis.Performance) analysis.PerformanceObservation { return p.PrefillTPS },
			analysis.ClaimUncachedPrefill, true
	case "request_ttft_seconds":
		return func(p analysis.Performance) analysis.PerformanceObservation { return p.TTFTSeconds },
			analysis.ClaimObservedRequestTTFT, true
	case "loaded_ttft_seconds":
		return func(p analysis.Performance) analysis.PerformanceObservation { return p.TTFTSeconds },
			analysis.ClaimLoadedUncachedTTFT, true
	default:
		return nil, "", false
	}
}

func positiveCV(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
