package stats

import (
	"errors"
	"math"
)

const (
	// MinimumComparisonRepeats and MaximumComparisonRepeats are the repeat
	// window a configuration confirmation will seal. The preflight refuses
	// outside it rather than naming a repeat count the plan cannot carry.
	MinimumComparisonRepeats = 3
	MaximumComparisonRepeats = 20
)

// MeanSeparation is the pre-data form of the non-overlapping 95% t-interval
// rule used for a timed confirmation objective. It does not say the declared
// gap was observed.
type MeanSeparation struct {
	Separable       bool
	Repeats         int
	RequiredRepeats int
	HalfWidth       float64
	Ratio           float64
}

// SampleCV is the coefficient of variation of a repeated measurement.
// It uses the same rounded mean and sample standard deviation that a
// performance observation stores, so this preflight and a later t interval
// describe one summary. Zero means unmeasured: fewer than two finite samples,
// a non-positive mean, or no spread. Zero is not perfect precision.
func SampleCV(samples []float64) float64 {
	summary := MeanSD(samples)
	if !summary.Valid() || summary.Mean <= 0 {
		return 0
	}
	return summary.SD / summary.Mean
}

// PredictMeanSeparation reports whether a planned repeat count can separate
// two means that truly differ by ratio, when both have the measured
// coefficient of variation cv. ratio is the higher mean divided by the lower
// mean, so 1.08 is an eight percent gap.
//
// The relative half-width is
//
//	t_{n-1, 0.975} * cv / sqrt(n)
//
// which is the margin in metricFromPerformance divided by the mean when the
// standard deviation is cv times that mean. Expected intervals separate when
// the half-width is below 1 and ratio exceeds (1+half)/(1-half). That is
// establishedBetter before either confirmation sample exists. The sum of the
// two half-widths is a slightly easier bar, and it is not this rule.
//
// A non-positive cv is unmeasured. A ratio at or below 1 names no difference.
// The 1.6 percent figure in the roadmap is one device's observation and is
// not a default here.
func PredictMeanSeparation(cv, ratio float64, repeats int) (MeanSeparation, error) {
	if !positiveFinite(cv) {
		return MeanSeparation{}, errors.New("coefficient of variation is unmeasured")
	}
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio <= 1 {
		return MeanSeparation{}, errors.New("declared mean ratio must be greater than 1")
	}
	if repeats < MinimumComparisonRepeats || repeats > MaximumComparisonRepeats {
		return MeanSeparation{}, errors.New("repeat count is outside the supported comparison budget")
	}
	half, _ := relativeHalfWidth(cv, repeats)
	return MeanSeparation{
		Separable:       intervalsSeparate(cv, ratio, repeats),
		Repeats:         repeats,
		RequiredRepeats: requiredRepeats(cv, ratio),
		HalfWidth:       half,
		Ratio:           ratio,
	}, nil
}

func requiredRepeats(cv, ratio float64) int {
	for n := MinimumComparisonRepeats; n <= MaximumComparisonRepeats; n++ {
		if intervalsSeparate(cv, ratio, n) {
			return n
		}
	}
	return 0
}

func intervalsSeparate(cv, ratio float64, repeats int) bool {
	half, ok := relativeHalfWidth(cv, repeats)
	if !ok {
		return false
	}
	// Additive 2*half would accept a gap the clamped intervals still overlap.
	return ratio > (1+half)/(1-half)
}

func relativeHalfWidth(cv float64, repeats int) (float64, bool) {
	if repeats < 2 || !positiveFinite(cv) {
		return 0, false
	}
	half := TCrit975(float64(repeats-1)) * cv / math.Sqrt(float64(repeats))
	if math.IsNaN(half) || math.IsInf(half, 0) || half <= 0 || half >= 1 {
		return half, false
	}
	return half, true
}

func positiveFinite(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
