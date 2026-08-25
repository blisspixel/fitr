package stats

import (
	"math/rand/v2"
	"testing"
)

// The defect this replaces was two instruments disagreeing on one dataset: the
// SPRT certified 11 straight passes while the Wilson interval put the same
// data across the gate. Here the decision IS the bound compared to the gate,
// so they cannot come apart. Exhaustive over the lattice, not sampled.
func TestCertifiedAgreesWithLowerBoundEverywhere(t *testing.T) {
	for _, gate := range []float64{0.70, 0.75, 0.80, 0.90} {
		g := NewGateEvidence(gate, 0.05, 1)
		// The lattice is walked exhaustively rather than sampled, so the
		// invariant is proved rather than spot-checked. Bounded at 80 trials
		// to keep the suite quick; it was verified to 300 once by hand.
		for trials := 1; trials <= 80; trials++ {
			for passes := 0; passes <= trials; passes++ {
				certified := g.Certified(passes, trials)
				above := g.LowerBound(passes, trials) > gate
				if certified != above {
					t.Fatalf("gate %.2f, %d/%d: certified=%v but lowerBound>gate=%v",
						gate, passes, trials, certified, above)
				}
			}
		}
	}
}

// Evidence must fall as the bar rises, or bisection is meaningless.
func TestEvidenceDecreasesInTheGate(t *testing.T) {
	for _, tc := range [][2]int{{11, 11}, {18, 20}, {45, 50}, {90, 100}} {
		prev := 0.0
		for i, gate := range []float64{0.50, 0.60, 0.70, 0.75, 0.80, 0.90} {
			got := NewGateEvidence(gate, 0.05, 1).LogEvidence(tc[0], tc[1])
			if i > 0 && got >= prev {
				t.Fatalf("%d/%d: evidence rose from %.4f to %.4f as the gate rose to %.2f",
					tc[0], tc[1], prev, got, gate)
			}
			prev = got
		}
	}
}

// The whole point: a model sitting at the gate must not be waved through. The
// previous gate certified it 44.6% of the time against a nominal 5%.
func TestErrorRateAtAndBelowTheGate(t *testing.T) {
	const gate, budget, runs = 0.75, 150, 20000
	for _, p := range []float64{0.60, 0.65, 0.70, 0.75} {
		rng := rand.New(rand.NewPCG(7, 11))
		g := NewGateEvidence(gate, 0.05, 1)
		certified := 0
		for range runs {
			passes := 0
			for i := 1; i <= budget; i++ {
				if rng.Float64() < p {
					passes++
				}
				if g.Certified(passes, i) {
					certified++
					break
				}
			}
		}
		rate := float64(certified) / runs
		t.Logf("true p=%.2f: certified %.2f%% of runs", p, 100*rate)
		if rate > 0.05 {
			t.Errorf("true p=%.2f certified %.3f of runs, above the nominal 0.05", p, rate)
		}
	}
}

// A genuinely good model must still get through, or the gate is useless.
func TestGoodModelsAreStillCertified(t *testing.T) {
	const gate, budget, runs = 0.75, 150, 4000
	for _, tc := range []struct {
		p       float64
		minRate float64
	}{
		{0.90, 0.95},
		{0.95, 0.99},
	} {
		rng := rand.New(rand.NewPCG(3, 5))
		g := NewGateEvidence(gate, 0.05, 1)
		certified, trials := 0, 0
		for range runs {
			passes := 0
			for i := 1; i <= budget; i++ {
				if rng.Float64() < tc.p {
					passes++
				}
				if g.Certified(passes, i) {
					certified++
					trials += i
					break
				}
			}
		}
		rate := float64(certified) / runs
		t.Logf("true p=%.2f: certified %.1f%% of runs, mean %.0f trials", tc.p, 100*rate, float64(trials)/float64(max(certified, 1)))
		if rate < tc.minRate {
			t.Errorf("true p=%.2f certified only %.3f of runs", tc.p, rate)
		}
	}
}
