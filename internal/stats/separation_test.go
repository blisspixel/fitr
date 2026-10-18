package stats

import (
	"math"
	"testing"
)

func TestPredictMeanSeparationUsesTheIntervalRule(t *testing.T) {
	// 1.6 percent variation, three repeats: the half-width is about four
	// percent. An eight percent gap still overlaps under (1+half)/(1-half),
	// which is the bar establishedBetter applies. Twice the half-width is
	// about 7.9 percent and would accept this gap.
	tight, err := PredictMeanSeparation(0.016, 1.08, 3)
	if err != nil {
		t.Fatal(err)
	}
	if tight.Separable || tight.RequiredRepeats != 4 {
		t.Fatalf("three repeats at 1.6%% variation = %+v", tight)
	}
	if tight.HalfWidth < 0.039 || tight.HalfWidth > 0.041 {
		t.Fatalf("half-width = %v, want about 4 percent", tight.HalfWidth)
	}
	fourth, err := PredictMeanSeparation(0.016, 1.08, 4)
	if err != nil || !fourth.Separable || fourth.RequiredRepeats != 4 {
		t.Fatalf("four repeats = %+v, %v", fourth, err)
	}
	wider, err := PredictMeanSeparation(0.05, 1.01, 3)
	if err != nil {
		t.Fatal(err)
	}
	if wider.HalfWidth < 0.12 || wider.HalfWidth > 0.13 {
		t.Fatalf("five percent variation half-width = %v, want about 12 percent", wider.HalfWidth)
	}
}

func TestPredictMeanSeparationMatchesConstructedIntervals(t *testing.T) {
	cv, ratio, repeats := 0.016, 1.08, 3
	separation, err := PredictMeanSeparation(cv, ratio, repeats)
	if err != nil {
		t.Fatal(err)
	}
	low, _ := expectedBounds(100*ratio, cv, repeats)
	otherLow, otherHigh := expectedBounds(100, cv, repeats)
	separated := low > otherHigh
	if separated || separation.Separable || low <= 0 || otherHigh <= otherLow {
		t.Fatalf("constructed low %v high %v separable %v", low, otherHigh, separation.Separable)
	}
	again, err := PredictMeanSeparation(cv, ratio, 4)
	if err != nil {
		t.Fatal(err)
	}
	low, _ = expectedBounds(100*ratio, cv, 4)
	_, otherHigh = expectedBounds(100, cv, 4)
	if low <= otherHigh || !again.Separable {
		t.Fatalf("four-repeat bounds %v <= %v, separable %v", low, otherHigh, again.Separable)
	}
}

func expectedBounds(mean, cv float64, repeats int) (float64, float64) {
	margin := TCrit975(float64(repeats-1)) * (cv * mean) / math.Sqrt(float64(repeats))
	return math.Max(0, mean-margin), mean + margin
}

func TestPredictMeanSeparationRejectsUnmeasuredInputs(t *testing.T) {
	cases := []struct {
		name   string
		cv     float64
		ratio  float64
		repeat int
	}{
		{"zero variation", 0, 1.08, 3},
		{"negative variation", -0.01, 1.08, 3},
		{"non-finite variation", math.NaN(), 1.08, 3},
		{"no gap", 0.016, 1, 3},
		{"reversed gap", 0.016, 0.9, 3},
		{"too few repeats", 0.016, 1.08, 2},
		{"too many repeats", 0.016, 1.08, 21},
	}
	if half, ok := relativeHalfWidth(0.016, 1); ok || half != 0 {
		t.Fatalf("one repeat half-width = %v %v", half, ok)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := PredictMeanSeparation(tc.cv, tc.ratio, tc.repeat); err == nil {
				t.Fatal("accepted unmeasured input")
			}
		})
	}
}

func TestSampleCVPreservesRoundedSummary(t *testing.T) {
	samples := []float64{19, 20, 21}
	summary := MeanSD(samples)
	if got := SampleCV(samples); got != summary.SD/summary.Mean {
		t.Fatalf("cv = %v, summary = %+v", got, summary)
	}
	if SampleCV([]float64{20}) != 0 || SampleCV([]float64{20, 20}) != 0 || SampleCV(nil) != 0 {
		t.Fatal("absent spread became a coefficient of variation")
	}
}

func TestPredictMeanSeparationNamesNoRepeatWhenNoneWorks(t *testing.T) {
	separation, err := PredictMeanSeparation(0.5, 1.01, 3)
	if err != nil {
		t.Fatal(err)
	}
	if separation.Separable || separation.RequiredRepeats != 0 {
		t.Fatalf("impossible budget = %+v", separation)
	}
}
