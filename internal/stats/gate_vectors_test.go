package stats

import (
	"math"
	"testing"
)

// Published reference values for the beta-mixture e-process at gate 0.75,
// alpha 0.05, K=64, Beta(2,2) on (gate,1) with 0.2 mass at p=1. If a change to
// the prior, the grid, or the accumulation drifts, these move.
func TestGateEvidenceMatchesReferenceVectors(t *testing.T) {
	g := NewGateEvidence(0.75, 0.05, 1)
	for _, tc := range []struct {
		passes, trials int
		logE           float64
	}{
		{11, 11, 2.3152174531},
		{14, 14, 3.0434595849},
		{20, 20, 4.5786540584},
		{18, 20, 0.9261552201},
		{45, 50, 2.8118669559},
		{90, 100, 6.1435203143},
		{270, 300, 20.1163495515},
		{50, 100, -5.6223830707},
	} {
		got := g.LogEvidence(tc.passes, tc.trials)
		if math.Abs(got-tc.logE) > 1e-6 {
			t.Errorf("LogEvidence(%d,%d) = %.10f, want %.10f", tc.passes, tc.trials, got, tc.logE)
		}
	}
}

func TestGateLowerBoundMatchesReferenceVectors(t *testing.T) {
	g := NewGateEvidence(0.75, 0.05, 1)
	for _, tc := range []struct {
		passes, trials int
		lower          float64
	}{
		{11, 11, 0.697769},
		{14, 14, 0.752938},
		{20, 20, 0.819197},
		{18, 20, 0.627546},
		{45, 50, 0.744990},
		{90, 100, 0.796209},
		{270, 300, 0.842069},
	} {
		got := g.LowerBound(tc.passes, tc.trials)
		if math.Abs(got-tc.lower) > 1e-5 {
			t.Errorf("LowerBound(%d,%d) = %.6f, want %.6f", tc.passes, tc.trials, got, tc.lower)
		}
	}
}
