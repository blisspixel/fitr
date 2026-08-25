package stats

import "math"

// Anytime-valid gating for a pass rate against a threshold.
//
// What this replaces and why. The previous gate built a Wald SPRT of
// "0.85 against 0.65" for a 0.75 threshold: it tested whether the rate looked
// more like the upper hypothesis than the lower one, and the threshold itself
// never entered the test statistic. Evidence for 0.85 over 0.65 does not mean
// the rate clears 0.75, so a model sitting exactly on the gate was certified
// 44% of the time against a nominal 5%. It was, however, near-perfectly
// calibrated at 0.65, which is the tell: it was answering a question nobody
// asked. A separate fixed-sample Wilson interval was then computed on the
// stopped sample and could disagree with it on the same data.
//
// This is one object instead of two. The evidence value is evaluated AT the
// gate and is strictly decreasing in the gate, so
//
//	certified  <=>  E_t(gate) >= threshold  <=>  LowerBound(t) > gate
//
// The stopping rule and the reported bound are the same arithmetic and cannot
// contradict each other.
//
// The construction is Robbins' beta-mixture e-process (1970), equivalently the
// sub-Bernoulli beta-binomial mixture of Howard, Ramdas, McAuliffe & Sekhon
// (Ann. Statist. 2021). For a prior pi supported strictly above the gate,
//
//	E_t(m) = sum_k w_k (p_k/m)^S ((1-p_k)/(1-m))^(t-S)
//
// Each term is the exact Bernoulli likelihood ratio of p_k against m. Under
// any true rate q <= m the expectation of each term is at most 1, so the sum
// is a non-negative supermartingale and Ville's inequality gives
// P(ever crossing 1/alpha) <= alpha, at every stopping time including a
// budget cap. Supporting the prior ABOVE the gate is what makes this valid for
// the composite null p <= gate rather than only at the point p = gate.
type GateEvidence struct {
	gate      float64
	logWeight []float64 // prior mass, log scale
	logPassLR []float64 // log(p_k / gate)
	logFailLR []float64 // log((1-p_k) / (1-gate))
	logThresh float64
	alpha     float64
	pools     int
}

const (
	// gateGridK is the mixture grid size. K=32 and K=64 agree to four
	// significant figures; 64 is cheap and removes the question.
	gateGridK = 64
	// gateAtomWeight is prior mass placed exactly at p=1. It shortens the
	// all-pass path, which is the common case for a healthy pool, from 17
	// trials to 14, at a cost of a few trials in the middle of the range.
	gateAtomWeight = 0.2
)

// NewGateEvidence builds the evidence process for one gated pool.
//
// pools is the number of gated pools in the run. Thresholds are raised to
// pools/alpha, which is e-Bonferroni: valid under arbitrary dependence between
// pools, which matters here because one model drives all of them. Three pools
// at 0.05 each otherwise give a family-wise error near 0.12.
func NewGateEvidence(gate, alpha float64, pools int) *GateEvidence {
	if pools < 1 {
		pools = 1
	}
	g := &GateEvidence{gate: gate, logThresh: math.Log(float64(pools) / alpha), alpha: alpha, pools: pools}

	// Beta(2,2) shaped mass on (gate, 1), by Riemann sum on uniform nodes.
	// Uniform nodes with density weights are numerically identical to
	// quantile-spacing the prior and need no inverse incomplete beta, which
	// keeps this dependency-free.
	total := 0.0
	raw := make([]float64, gateGridK)
	for k := range gateGridK {
		u := (float64(k) + 0.5) / gateGridK
		raw[k] = 6 * u * (1 - u)
		total += raw[k]
	}
	for k := range gateGridK {
		u := (float64(k) + 0.5) / gateGridK
		p := gate + u*(1-gate)
		w := (1 - gateAtomWeight) * raw[k] / total
		g.logWeight = append(g.logWeight, math.Log(w))
		g.logPassLR = append(g.logPassLR, math.Log(p/gate))
		g.logFailLR = append(g.logFailLR, math.Log((1-p)/(1-gate)))
	}
	// The atom at p=1 never survives a failure, which is correct: one failure
	// is decisive evidence against a perfect rate.
	g.logWeight = append(g.logWeight, math.Log(gateAtomWeight))
	g.logPassLR = append(g.logPassLR, math.Log(1/gate))
	g.logFailLR = append(g.logFailLR, math.Inf(-1))
	return g
}

// LogEvidence is the log of the mixture evidence value after t trials with S
// passes. Accumulated in log space because the raw value exceeds 1e8 by a few
// hundred trials while individual terms underflow.
func (g *GateEvidence) LogEvidence(passes, trials int) float64 {
	if trials <= 0 || passes < 0 || passes > trials {
		return 0
	}
	fails := trials - passes
	terms := make([]float64, 0, len(g.logWeight))
	best := math.Inf(-1)
	for k := range g.logWeight {
		t := g.logWeight[k] + float64(passes)*g.logPassLR[k]
		if fails > 0 {
			t += float64(fails) * g.logFailLR[k]
		}
		if math.IsNaN(t) {
			continue
		}
		terms = append(terms, t)
		if t > best {
			best = t
		}
	}
	if math.IsInf(best, -1) {
		return math.Inf(-1)
	}
	sum := 0.0
	for _, t := range terms {
		sum += math.Exp(t - best)
	}
	return best + math.Log(sum)
}

// Certified reports whether the evidence clears the threshold. This is the
// stopping rule, and it agrees with LowerBound by construction.
func (g *GateEvidence) Certified(passes, trials int) bool {
	return g.LogEvidence(passes, trials) >= g.logThresh
}

// LowerBound is the anytime-valid lower confidence bound on the rate: the
// largest gate this evidence would still certify. Because LogEvidence is
// strictly decreasing in the gate, bisection finds it exactly, and the bound
// agrees with Certified by construction rather than by coincidence.
//
// Valid at every stopping time, including a budget cap. A fixed-sample
// interval is not, which is why stopping early used to invalidate the number
// that got printed.
func (g *GateEvidence) LowerBound(passes, trials int) float64 {
	if trials <= 0 || passes <= 0 {
		return 0
	}
	lo, hi := 0.0, math.Min(float64(passes)/float64(trials), 1-1e-12)
	// 60 halvings resolve the bound far past display precision, and each one
	// rebuilds the mixture, so this is the loop worth keeping short.
	for range 60 {
		mid := (lo + hi) / 2
		if mid <= 0 || mid >= 1 {
			break
		}
		if NewGateEvidence(mid, g.alpha, g.pools).LogEvidence(passes, trials) >= g.logThresh {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo
}
