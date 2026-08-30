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
//
// Wilson rather than Jeffreys: Brown, Cai & DasGupta (2001) co-recommend both
// at small n; Wilson needs one square root instead of an inverse incomplete
// beta, behaves at the x=0 and x=n boundaries unmodified, and the Newcombe
// difference interval below is BUILT from Wilson bounds - one interval family
// serves every display coherently.
func Wilson(passes, n int) Interval {
	lo, hi := wilsonRaw(passes, n)
	p := 0.0
	if n > 0 {
		p = float64(passes) / float64(n)
	}
	return Interval{Point: round(p, 4), Lo: round(lo, 4), Hi: round(hi, 4), N: n}
}

// wilsonRaw is the unrounded Wilson bound pair, used internally so derived
// intervals (Newcombe) do not accumulate display rounding.
func wilsonRaw(passes, n int) (lo, hi float64) {
	if n <= 0 {
		return 0, 1
	}
	return wilsonRate(float64(passes)/float64(n), float64(n))
}

func wilsonRate(p, n float64) (lo, hi float64) {
	if n <= 0 {
		return 0, 1
	}
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}
	denom := 1 + Z95*Z95/n
	centre := (p + Z95*Z95/(2*n)) / denom
	margin := (Z95 / denom) * math.Sqrt(p*(1-p)/n+Z95*Z95/(4*n*n))
	return math.Max(0, centre-margin), math.Min(1, centre+margin)
}

// Cluster is one family of binary trials. Repeats of the same generated
// family are not independent Bernoulli draws of the need.
type Cluster struct {
	Passes, N int
}

// ClusteredWilson is a Rao-Scott adjusted Wilson interval. Distinct check
// families are clusters; pooling them as iid overstates n and can mint a
// PASS that hides a dead family.
//
//	deff  = 1 + (((1 + CV_size^2)m̄) - 1)ρ̂
//	n_eff = n / deff
//
// ρ̂ is the unequal-cluster ANOVA intra-cluster correlation, floored at
// zero. The size-CV correction prevents a few large repeated families from
// impersonating many independent families. A single family has one effective
// unit because between-family variance is not identifiable. The interval is
// never narrower than ordinary Wilson.
func ClusteredWilson(clusters []Cluster) Interval {
	var passes, n, groups int
	equalSingletons := true
	sizeSquares := 0.0
	for _, c := range clusters {
		if c.N <= 0 {
			continue
		}
		groups++
		passes += c.Passes
		n += c.N
		sizeSquares += float64(c.N * c.N)
		if c.N != 1 {
			equalSingletons = false
		}
	}
	if n <= 0 {
		return Wilson(0, 0)
	}
	p := float64(passes) / float64(n)
	iid := Wilson(passes, n)
	if equalSingletons {
		return iid
	}
	if groups == 1 {
		lo, hi := wilsonRate(p, 1)
		return Interval{Point: round(p, 4), Lo: round(min(lo, iid.Lo), 4),
			Hi: round(max(hi, iid.Hi), 4), N: n}
	}

	meanSize := float64(n) / float64(groups)
	between, within := 0.0, 0.0
	for _, c := range clusters {
		if c.N <= 0 {
			continue
		}
		pi := float64(c.Passes) / float64(c.N)
		d := pi - p
		between += float64(c.N) * d * d
		within += float64(c.N) * pi * (1 - pi)
	}
	msb := between / float64(groups-1)
	msw := 0.0
	if n > groups {
		msw = within / float64(n-groups)
	}
	// n0 is the effective cluster size in the unequal-size one-way random
	// effects estimator. It equals meanSize when cluster sizes are equal.
	n0 := (float64(n) - sizeSquares/float64(n)) / float64(groups-1)
	rho := 1.0
	if denominator := msb + (n0-1)*msw; denominator > 0 {
		rho = (msb - msw) / denominator
	}
	if rho < 0 {
		rho = 0
	}
	if rho > 1 {
		rho = 1
	}
	sizeVariance := 0.0
	for _, c := range clusters {
		if c.N <= 0 {
			continue
		}
		d := float64(c.N) - meanSize
		sizeVariance += d * d
	}
	sizeVariance /= float64(groups)
	sizeCV2 := sizeVariance / (meanSize * meanSize)
	deff := 1 + ((1+sizeCV2)*meanSize-1)*rho
	if deff < 1 {
		deff = 1
	}
	nEff := float64(n) / deff
	if nEff > float64(n) {
		nEff = float64(n)
	}
	if nEff < 1 {
		nEff = 1
	}
	lo, hi := wilsonRate(p, nEff)
	if lo > iid.Lo {
		lo = iid.Lo
	}
	if hi < iid.Hi {
		hi = iid.Hi
	}
	return Interval{Point: round(p, 4), Lo: round(lo, 4), Hi: round(hi, 4), N: n}
}

// NewcombeDiff is the hybrid score ("square-and-add") 95% interval for the
// difference of two independent proportions p1-p2 (Newcombe 1998, method 10)
// - the method comparison studies actually recommend, built from the Wilson
// bounds of each arm:
//
//	L = d - sqrt((p1-l1)² + (u2-p2)²)
//	U = d + sqrt((u1-p1)² + (p2-l2)²)
//
// This interval is fitr's SOLE arbiter of "is A better than B" claims on pass
// rates. The tempting alternative - "the two intervals overlap, so no claim" -
// is not a 5% test: requiring non-overlap of two 95% intervals corresponds to
// an effective alpha near 0.006 (Schenker & Gentleman 2001), silently missing
// real differences. Per-arm intervals are for display; this one decides.
func NewcombeDiff(x1, n1, x2, n2 int) (lo, hi float64, ok bool) {
	if n1 <= 0 || n2 <= 0 {
		return 0, 0, false
	}
	p1 := float64(x1) / float64(n1)
	p2 := float64(x2) / float64(n2)
	l1, u1 := wilsonRaw(x1, n1)
	l2, u2 := wilsonRaw(x2, n2)
	d := p1 - p2
	lo = d - math.Sqrt((p1-l1)*(p1-l1)+(u2-p2)*(u2-p2))
	hi = d + math.Sqrt((u1-p1)*(u1-p1)+(p2-l2)*(p2-l2))
	return round(lo, 4), round(hi, 4), true
}

// McNemarExact is the exact conditional McNemar test for paired binary
// outcomes: b instances where A passed and B failed, c the reverse. The
// concordant pairs carry no information about the difference and never enter.
// Under H0 the discordant split is Binomial(b+c, 1/2):
//
//	pExact = min(1, 2 * P[X <= min(b,c)])   (two-sided, capped)
//	pMid   = pExact - P[X = min(b,c)]       (Fagerland et al. 2013 recommend
//	                                         mid-p; we report exact as the
//	                                         headline - never overclaim)
//
// separable is false when b+c < 6: the smallest achievable exact p is
// 2^(1-(b+c)), so below six discordant pairs NO split can reach p < 0.05 -
// and saying that beats printing a p-value that was never in play.
func McNemarExact(b, c int) (pExact, pMid float64, separable bool) {
	n := b + c
	if n == 0 {
		return 1, 1, false
	}
	k := min(b, c)
	tail, pmfK := 0.0, 0.0
	for i := 0; i <= k; i++ {
		pmf := binomHalfPMF(n, i)
		tail += pmf
		pmfK = pmf
	}
	pExact = math.Min(1, 2*tail)
	pMid = math.Max(0, pExact-pmfK)
	return round(pExact, 6), round(pMid, 6), n >= 6
}

// binomHalfPMF is C(n,i) * 0.5^n via lgamma - exact well past any b+c fitr sees.
func binomHalfPMF(n, i int) float64 {
	lgN, _ := math.Lgamma(float64(n + 1))
	lgI, _ := math.Lgamma(float64(i + 1))
	lgNI, _ := math.Lgamma(float64(n - i + 1))
	return math.Exp(lgN - lgI - lgNI + float64(n)*math.Log(0.5))
}

// FiellerRatio is Fieller's theorem (1954) 95% interval for the ratio of two
// independent means - the CORRECT interval for "how many times faster",
// unlike the naive ratio ± propagated-error which pretends the ratio is
// normal. Zero-covariance form with Welch-Satterthwaite df, floored into the
// t-table (fewer df -> wider interval -> conservative):
//
//	g    = t² · (s_b²/n_b) / mean_b²
//	half = (t/|mean_b|) · sqrt((s_a²/n_a)(1-g) + R²(s_b²/n_b))
//	CI   = [(R-half)/(1-g), (R+half)/(1-g)]
//
// ok is false when either side has n<2 or when g >= 1 - the denominator mean
// is not statistically separated from zero, Fieller's confidence set is not
// an interval, and printing numbers from it would be fiction. Callers fall
// back to the difference of means.
func FiellerRatio(a, b Summary) (lo, hi, ratio float64, ok bool) {
	if a.N < 2 || b.N < 2 || b.Mean == 0 {
		return 0, 0, 0, false
	}
	t := TCrit975(WelchDF(a.SD, a.N, b.SD, b.N))
	v11 := a.SD * a.SD / float64(a.N)
	v22 := b.SD * b.SD / float64(b.N)
	r := a.Mean / b.Mean
	g := t * t * v22 / (b.Mean * b.Mean)
	if g >= 1 {
		return 0, 0, round(r, 3), false
	}
	half := t / math.Abs(b.Mean) * math.Sqrt(v11*(1-g)+r*r*v22)
	return round((r-half)/(1-g), 3), round((r+half)/(1-g), 3), round(r, 3), true
}

// ---------------------------------------------------------------- outliers
// ModifiedZOutliers flags samples whose modified z-score exceeds 3.5
// (Iglewicz & Hoaglin 1993): 0.6745·(x-median)/MAD. When MAD is zero (at
// least half the samples tie the median - common with coarse timers) it falls
// back to the mean absolute deviation about the median scaled by 1.253314;
// when that is also zero every value is identical and nothing is an outlier.
// Below n=5 the median and MAD are too degenerate to trust, so nothing is
// flagged - a non-answer beats a fabricated one.
func ModifiedZOutliers(xs []float64) []bool {
	out := make([]bool, len(xs))
	if len(xs) < 5 {
		return out
	}
	med := Median(xs)
	devs := make([]float64, len(xs))
	for i, x := range xs {
		devs[i] = math.Abs(x - med)
	}
	scale := Median(devs) / 0.6745
	if scale == 0 {
		meanAD := 0.0
		for _, d := range devs {
			meanAD += d
		}
		meanAD /= float64(len(devs))
		scale = 1.253314 * meanAD
	}
	if scale == 0 {
		return out
	}
	for i, x := range xs {
		if math.Abs(x-med)/scale > 3.5 {
			out[i] = true
		}
	}
	return out
}

// Median is the sample median. An empty sample has no median and returns
// zero, the way MeanSD returns a zero Summary: callers already treat an
// unmeasured statistic as absent, and the even-length branch would otherwise
// index s[-1] and panic.
func Median(xs []float64) float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	n := len(s)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
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

// tCrit975 holds two-sided 95% Student-t critical values (t_{0.975,df}) for
// df 1..30. Above 30 the normal quantile is within ~1.5% and we use Z95.
// Hardcoded because fitr has zero dependencies and these constants have not
// changed since Gosset published them pseudonymously in 1908.
var tCrit975 = []float64{
	12.7062, 4.30265, 3.18245, 2.77645, 2.57058, 2.44691, 2.36462, 2.30600, 2.26216, 2.22814,
	2.20099, 2.17881, 2.16037, 2.14479, 2.13145, 2.11991, 2.10982, 2.10092, 2.09302, 2.08596,
	2.07961, 2.07387, 2.06866, 2.06390, 2.05954, 2.05553, 2.05183, 2.04841, 2.04523, 2.04227,
}

// TCrit975 returns the two-sided 95% Student-t critical value for df degrees
// of freedom. Fractional df (from Welch-Satterthwaite) rounds DOWN, and df
// beyond the table reuses the df=30 value rather than dropping to z - both
// choices err toward the wider interval.
func TCrit975(df float64) float64 {
	if df < 1 {
		return tCrit975[0]
	}
	i := int(df)
	if i > len(tCrit975) {
		return tCrit975[len(tCrit975)-1]
	}
	return tCrit975[i-1]
}

// WelchDF is the Welch-Satterthwaite effective degrees of freedom for the
// difference of two means with unequal variances:
//
//	df = (s1²/n1 + s2²/n2)² / [ (s1²/n1)²/(n1-1) + (s2²/n2)²/(n2-1) ]
//
// Used instead of pooling because two models' timing spreads on the same box
// are routinely unequal, and pooling would understate the uncertainty of the
// noisier one.
func WelchDF(sd1 float64, n1 int, sd2 float64, n2 int) float64 {
	if n1 < 2 || n2 < 2 {
		return 1
	}
	v1 := sd1 * sd1 / float64(n1)
	v2 := sd2 * sd2 / float64(n2)
	denom := v1*v1/float64(n1-1) + v2*v2/float64(n2-1)
	if denom == 0 {
		return float64(n1 + n2 - 2)
	}
	return (v1 + v2) * (v1 + v2) / denom
}

// CV is the coefficient of variation (SD/mean) - the one dimensionless
// stability number that compares across devices and units. Zero when it
// would be meaningless.
func (s Summary) CV() float64 {
	if !s.Valid() || s.Mean == 0 {
		return 0
	}
	return round(s.SD/s.Mean, 4)
}

// ZeroEventUpperBound is the exact one-sided 95% upper confidence bound on an
// event probability after observing ZERO events in n trials:
//
//	p_u = 1 - alpha^(1/n),  alpha = 0.05
//
// (Hanley & Lippman-Hand 1983; the familiar "rule of three" 3/n is its
// large-n approximation.) This is what "N identical runs" is actually worth:
// 5/5 identical bounds the per-run divergence probability below ~45%, not
// near zero - and saying so beats implying certainty from a handful of runs.
func ZeroEventUpperBound(n int) float64 {
	if n <= 0 {
		return 1
	}
	return round(1-math.Pow(0.05, 1/float64(n)), 4)
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}
