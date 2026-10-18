package experiment

import (
	"math"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/stats"
)

func TestVariationFollowsTheTimedClaim(t *testing.T) {
	for _, metric := range []string{"decode_tps", "prefill_tps", "request_ttft_seconds", "loaded_ttft_seconds"} {
		if !TimedObjective(metric) {
			t.Fatal(metric)
		}
		if _, err := Variation(analysis.Report{}, metric); err == nil || !strings.Contains(err.Error(), "does not establish") {
			t.Fatalf("%s empty report: %v", metric, err)
		}
	}
	if TimedObjective("artifact_bytes") || TimedObjective("resident_bytes") {
		t.Fatal("exact byte metrics were treated as repeated means")
	}
	if _, err := Variation(analysis.Report{}, "artifact_bytes"); err == nil || !strings.Contains(err.Error(), "no repeated-mean") {
		t.Fatal(err)
	}
}

func TestClaimableDecodeVariationUsesSealedSamples(t *testing.T) {
	result := sealedSpreadRecord(t, 8192, []float64{19, 20, 21})
	cv, err := ClaimableVariation(result, "decode_tps")
	if err != nil {
		t.Fatal(err)
	}
	want := stats.SampleCV([]float64{19, 20, 21})
	if cv != want || want <= 0 {
		t.Fatalf("cv = %v, want %v", cv, want)
	}
	for _, metric := range []string{"prefill_tps", "request_ttft_seconds", "loaded_ttft_seconds"} {
		_, err := ClaimableVariation(result, metric)
		if err == nil || !strings.Contains(err.Error(), "unmeasured") {
			t.Fatalf("%s identical samples: %v", metric, err)
		}
	}
	if _, err := ClaimableVariation(nil, "decode_tps"); err == nil {
		t.Fatal("missing noise source supplied a coefficient of variation")
	}
	result.SchemaVersion = 0
	if _, err := ClaimableVariation(result, "decode_tps"); err == nil || !strings.Contains(err.Error(), "not claimable") {
		t.Fatal(err)
	}
}

func TestVariationRefusesAnUnsupportedClaimAndAMismatchedCount(t *testing.T) {
	mean := 20.0
	// A cache hit keeps the observed-prefill claim and drops the uncached one.
	// The stored standard deviation must not become the coefficient of variation.
	hit := analysis.Report{Performance: analysis.Performance{PrefillTPS: analysis.PerformanceObservation{
		Estimate: &mean, Status: analysis.StatusAvailable, SampleCount: 3,
		Samples:  []float64{19, 20, 21},
		Supports: []analysis.SupportClaim{analysis.ClaimObservedPrefill},
	}}}
	if _, err := Variation(hit, "prefill_tps"); err == nil || !strings.Contains(err.Error(), "does not establish") {
		t.Fatal(err)
	}
	mismatch := analysis.Report{Performance: analysis.Performance{DecodeTPS: analysis.PerformanceObservation{
		Estimate: &mean, Status: analysis.StatusAvailable, SampleCount: 3,
		Samples:  []float64{19, 20},
		Supports: []analysis.SupportClaim{analysis.ClaimObservedDecode},
	}}}
	if _, err := Variation(mismatch, "decode_tps"); err == nil || !strings.Contains(err.Error(), "sample count") {
		t.Fatal(err)
	}
}

func TestNoiseStaysOnOneDeviceAndContext(t *testing.T) {
	result := sealedSpreadRecord(t, 8192, []float64{19, 20, 21})
	if err := NoiseContext(result, 8192); err != nil {
		t.Fatal(err)
	}
	if err := NoiseContext(result, 4096); err == nil {
		t.Fatal("a different requested context matched")
	}
	other := *result.DeviceV2.Context.EffectiveTokens
	other--
	result.DeviceV2.Context.EffectiveTokens = &other
	if err := NoiseContext(result, 8192); err == nil {
		t.Fatal("a different effective context matched")
	}
	result.DeviceV2.Context.EffectiveTokens = nil
	if err := NoiseContext(result, 8192); err == nil {
		t.Fatal("an unverified effective context matched")
	}
	result = sealedSpreadRecord(t, 8192, []float64{19, 20, 21})
	if err := NoiseDevice(result.DeviceV2.Device, result); err != nil {
		t.Fatal(err)
	}
	live := result.DeviceV2.Device
	live.GPU = live.GPU + "-other"
	if err := NoiseDevice(live, result); err == nil {
		t.Fatal("a different device fingerprint matched")
	}
	// Comparability keys drop an empty config. The sealed struct does not:
	// a nil map and an empty map are different observations.
	nilConfig := result.DeviceV2.Device
	nilConfig.Config = nil
	emptyConfig := result.DeviceV2.Device
	emptyConfig.Config = map[string]string{}
	if err := NoiseDevice(nilConfig, &record.Record{DeviceV2: &device.FingerprintV2{Device: emptyConfig}}); err == nil {
		t.Fatal("nil and empty config matched")
	}
	if err := NoiseDevice(live, nil); err == nil || NoiseContext(nil, 8192) == nil {
		t.Fatal("missing noise source matched")
	}
}

func TestPredictedGapAgreesWithDominance(t *testing.T) {
	cv := stats.SampleCV([]float64{19, 20, 21})
	separation, err := stats.PredictMeanSeparation(cv, 1.08, 3)
	if err != nil {
		t.Fatal(err)
	}
	if establishedBetter(scaledBounds(100*1.08, cv, 3), scaledBounds(100, cv, 3), decision.Maximize) != separation.Separable {
		t.Fatalf("dominance disagreed with %+v", separation)
	}
}

func scaledBounds(mean, cv float64, repeats int) metricBounds {
	margin := stats.TCrit975(float64(repeats-1)) * (cv * mean) / math.Sqrt(float64(repeats))
	return metricBounds{low: math.Max(0, mean-margin), high: mean + margin}
}

func sealedSpreadRecord(t *testing.T, requested int, decode []float64) *record.Record {
	t.Helper()
	return sealedExperimentRecordForLevel(t, requested, nil, "2026-08-31T10:00:00Z", quantDigestA, "Q8_0",
		decode, "", "full")
}
