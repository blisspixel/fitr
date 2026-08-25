package stats

import (
	"math"
	"testing"
)

// Degenerate inputs reach these functions from real runs: a single repeat, a
// battery where every trial passed, a metric that did not vary. None may
// produce NaN, Inf, or an interval that claims more than it knows.
func TestStatsSurviveDegenerateInputs(t *testing.T) {
	finite := func(t *testing.T, name string, vs ...float64) {
		t.Helper()
		for i, v := range vs {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Fatalf("%s produced a non-finite value at %d: %v (from %v)", name, i, v, vs)
			}
		}
	}

	for _, tc := range []struct{ passes, n int }{
		{0, 0}, {0, 1}, {1, 1}, {5, 5}, {0, 5}, {3, 7},
	} {
		iv := Wilson(tc.passes, tc.n)
		finite(t, "Wilson", iv.Lo, iv.Hi)
		if tc.n > 0 && (iv.Lo < 0 || iv.Hi > 1 || iv.Lo > iv.Hi) {
			t.Fatalf("Wilson(%d,%d) = [%v,%v], outside [0,1] or inverted", tc.passes, tc.n, iv.Lo, iv.Hi)
		}
	}

	for _, xs := range [][]float64{nil, {}, {1}, {2, 2}, {2, 2, 2}, {0, 0}} {
		s := MeanSD(xs)
		finite(t, "MeanSD", s.Mean, s.SD)
		if s.N != len(xs) {
			t.Fatalf("MeanSD(%v).N = %d, want %d", xs, s.N, len(xs))
		}
		finite(t, "Median", Median(xs))
		_ = ModifiedZOutliers(xs)
		_ = Flakiness(nil)
	}

	// A zero-variance or single-sample pair must refuse to produce a ratio
	// rather than divide its way to Inf.
	for _, pair := range [][2]Summary{
		{MeanSD([]float64{2, 2, 2}), MeanSD([]float64{2, 2, 2})},
		{MeanSD([]float64{1}), MeanSD([]float64{1})},
		{MeanSD(nil), MeanSD([]float64{1, 2, 3})},
		{MeanSD([]float64{0, 0, 0}), MeanSD([]float64{1, 2, 3})},
	} {
		if lo, hi, ratio, ok := FiellerRatio(pair[0], pair[1]); ok {
			finite(t, "FiellerRatio", lo, hi, ratio)
		}
		if ratio, sd, ok := RatioWithError(pair[0], pair[1]); ok {
			finite(t, "RatioWithError", ratio, sd)
		}
	}

	if lo, hi, ok := NewcombeDiff(0, 0, 0, 0); ok {
		finite(t, "NewcombeDiff", lo, hi)
	}
	for _, bc := range [][2]int{{0, 0}, {0, 1}, {1, 0}, {3, 4}} {
		p, mid, _ := McNemarExact(bc[0], bc[1])
		finite(t, "McNemarExact", p, mid)
	}
	if iv := ClusteredWilson(nil); math.IsNaN(iv.Lo) || math.IsNaN(iv.Hi) {
		t.Fatalf("ClusteredWilson(nil) = [%v,%v]", iv.Lo, iv.Hi)
	}
	if iv := ClusteredWilson([]Cluster{{}}); math.IsNaN(iv.Lo) || math.IsNaN(iv.Hi) {
		t.Fatalf("ClusteredWilson(empty cluster) = [%v,%v]", iv.Lo, iv.Hi)
	}
}
