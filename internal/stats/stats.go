// Package stats provides small-N statistics for local model evals.
//
// Why this exists: a single run is not a measurement. Published reruns of the
// SAME model with the SAME config show 10-20 percentage-point swings. We
// observed the same coding task flip pass/fail across three models, and an
// agentic task fail twice then pass on the third identical attempt.
//
// Below N~300 the CLT is the wrong tool. Wilson score intervals are the
// standard recommendation for binary pass/fail and behave correctly near p=0
// and p=1 where the normal approximation falls apart.
package stats

import (
	"fmt"
	"math"
	"sort"
)

// Z95 is the two-sided 95% normal quantile.
const Z95 = 1.959963984540054

// Interval is a point estimate with a confidence interval.
type Interval struct {
	Point float64 `json:"point"`
	Lo    float64 `json:"lo"`
	Hi    float64 `json:"hi"`
	N     int     `json:"n"`
}

// Wilson returns the Wilson score interval for a binomial proportion.
//
// At n=1 with a single pass this returns roughly [0.21, 1.0] -- which is the
// honest width of a one-shot result, and the entire argument for -k 3.
func Wilson(passes, n int) Interval {
	if n <= 0 {
		return Interval{0, 0, 1, 0}
	}
	p := float64(passes) / float64(n)
	fn := float64(n)
	denom := 1 + Z95*Z95/fn
	centre := (p + Z95*Z95/(2*fn)) / denom
	margin := (Z95 / denom) * math.Sqrt(p*(1-p)/fn+Z95*Z95/(4*fn*fn))
	return Interval{
		Point: round(p, 4),
		Lo:    round(math.Max(0, centre-margin), 4),
		Hi:    round(math.Min(1, centre+margin), 4),
		N:     n,
	}
}

// Summary describes the spread of a repeated numeric measurement.
type Summary struct {
	Mean   float64 `json:"mean"`
	SD     float64 `json:"sd"`
	N      int     `json:"n"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
	Spread float64 `json:"spread"`
}

// Valid reports whether the summary rests on more than one observation.
// A single observation has no meaningful standard deviation and must never be
// rendered as "+/- 0.00" -- that is a fabricated claim of precision.
func (s Summary) Valid() bool { return s.N >= 2 && s.SD > 0 }

// MeanSD summarises a slice, ignoring NaN and non-positive placeholders.
func MeanSD(xs []float64) Summary {
	var v []float64
	for _, x := range xs {
		if !math.IsNaN(x) && !math.IsInf(x, 0) {
			v = append(v, x)
		}
	}
	if len(v) == 0 {
		return Summary{}
	}
	sum := 0.0
	for _, x := range v {
		sum += x
	}
	n := float64(len(v))
	mean := sum / n
	sd := 0.0
	if len(v) > 1 {
		acc := 0.0
		for _, x := range v {
			acc += (x - mean) * (x - mean)
		}
		sd = math.Sqrt(acc / (n - 1))
	}
	sorted := append([]float64(nil), v...)
	sort.Float64s(sorted)
	return Summary{
		Mean: round(mean, 3), SD: round(sd, 3), N: len(v),
		Min: round(sorted[0], 3), Max: round(sorted[len(sorted)-1], 3),
		Spread: round(sorted[len(sorted)-1]-sorted[0], 3),
	}
}

// Flakiness reports whether a task neither always passes nor always fails.
// A flipping task is telling you something the mean hides.
type Flake struct {
	N      int     `json:"n"`
	Passes int     `json:"passes"`
	Flaky  bool    `json:"flaky"`
	Rate   float64 `json:"rate"`
}

func Flakiness(results []bool) Flake {
	if len(results) == 0 {
		return Flake{}
	}
	p := 0
	for _, r := range results {
		if r {
			p++
		}
	}
	return Flake{
		N: len(results), Passes: p,
		Flaky: p > 0 && p < len(results),
		Rate:  round(float64(p)/float64(len(results)), 3),
	}
}

// Overlap reports whether two intervals overlap. If they do, you must NOT
// claim one is better than the other.
func Overlap(a, b Interval) bool {
	return !(a.Hi < b.Lo || b.Hi < a.Lo)
}

// Compare gives an honest pairwise verdict for two binary rates.
func Compare(aName string, a Interval, bName string, b Interval) string {
	if Overlap(a, b) {
		return fmt.Sprintf("%s and %s are INDISTINGUISHABLE on this sample size", aName, bName)
	}
	hi, lo := aName, bName
	if b.Point > a.Point {
		hi, lo = bName, aName
	}
	return fmt.Sprintf("%s > %s (intervals do not overlap)", hi, lo)
}

// RatioWithError propagates relative error in quadrature, as hyperfine does:
//
//	sigma_ratio = ratio * sqrt((sd_a/mean_a)^2 + (sd_b/mean_b)^2)
//
// ok is false when either input is a single observation. A ratio without a
// +/- is not a claim, so callers must not print one.
func RatioWithError(a, b Summary) (ratio, sd float64, ok bool) {
	if a.Mean == 0 || b.Mean == 0 {
		return 0, 0, false
	}
	ratio = a.Mean / b.Mean
	if !a.Valid() || !b.Valid() {
		return round(ratio, 3), 0, false
	}
	ra, rb := a.SD/a.Mean, b.SD/b.Mean
	return round(ratio, 3), round(ratio*math.Hypot(ra, rb), 3), true
}

// FirstRunSlow reports whether the first observation is an outlier well below
// the rest -- the classic cold-cache / not-yet-settled signature.
//
// hyperfine warns about this explicitly rather than letting it vanish into a
// standard deviation, and it matters more here: one model measured
// [6.35, 14.76, 14.63] tok/s, so a single-sample run had a 1-in-3 chance of
// reporting less than half the real throughput.
//
// ratio is how many times faster the rest were than the first run.
func FirstRunSlow(xs []float64) (slow bool, ratio float64) {
	if len(xs) < 3 {
		return false, 0
	}
	first := xs[0]
	if first <= 0 {
		return false, 0
	}
	rest := MeanSD(xs[1:])
	if rest.Mean <= 0 {
		return false, 0
	}
	ratio = round(rest.Mean/first, 2)
	// 1.25x is well outside the ~2% run-to-run spread a settled model shows.
	return ratio >= 1.25, ratio
}

// MinDetectableEffect is a rough MDE for a binary eval (worst case p=0.5,
// 80% power, alpha=0.05). Exposed so the tool can state what it CANNOT
// resolve rather than implying precision it does not have.
func MinDetectableEffect(items, repeats int) float64 {
	n := max(items*max(1, repeats), 1)
	return round(2.8*math.Sqrt(0.25/float64(n)), 3)
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}
