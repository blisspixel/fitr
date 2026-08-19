# The statistics of fitr

fitr runs a handful of trials on one machine and turns them into verdicts.
That regime - three to fifty binary outcomes, three to ten timing repeats -
is exactly where most benchmark statistics quietly stop being true. This
document states every method fitr uses, why it was chosen over the obvious
alternative, and what it refuses to compute. Each section follows the same
shape: the situation, the method, the rejected alternative, the reference.

Two design rules govern everything below:

1. Never fabricate precision. No error bar from one observation, no p-value
   that was mathematically incapable of reaching significance, no interval
   from a degenerate formula.
2. "Cannot separate" is a real answer. When the sample does not decide, fitr
   says so in those words rather than picking a winner.

## 1. One pass rate: the Wilson score interval

Every binary pool (coding trials, structured-output checks, instruction
checks) is summarized as `passes/n` with a 95% Wilson score interval:

    center = (p + z^2/2n) / (1 + z^2/n)
    half   = (z / (1 + z^2/n)) * sqrt(p(1-p)/n + z^2/4n^2)

Why Wilson: the textbook Wald interval (`p +/- z*sqrt(p(1-p)/n)`) collapses
to zero width at p = 0 or 1, which is precisely where small batteries live
(a 3/3 result is common and means far less than it looks: Wilson reports
[0.29, 1.0]). Brown, Cai and DasGupta (2001) co-recommend Wilson and
Jeffreys at small n; Wilson wins here because it needs one square root
instead of an inverse incomplete beta function (fitr has zero dependencies),
it behaves at the boundaries unmodified, and the Newcombe interval of
section 3 is built from Wilson bounds, so a single interval family serves
every display coherently.

## 2. What a run cannot resolve: the minimum detectable effect

Every run prints its minimum detectable effect, computed at the worst case
(p = 0.5, 80% power, alpha = 0.05):

    MDE ~ 2.8 * sqrt(0.25 / n)

A default run's ~23 binary trials give an MDE near 29 percentage points:
fitr can tell broken from working, not good from slightly better, and the
scorecard says that sentence on every run. Tools that omit this number are
not more precise; they are less honest about the same sample.

## 3. Comparing two models, unpaired: the Newcombe difference interval

The intuitive rule - "the two intervals overlap, so no claim" - is not a 5%
test. Requiring non-overlap of two 95% intervals corresponds to an effective
alpha near 0.006 (Schenker and Gentleman 2001; Payton et al. 2003 show that
~83% intervals would give the intended 5%). The overlap rule silently misses
real differences, which is the opposite failure from the one it is guarding
against.

fitr therefore claims a difference if and only if the 95% interval for the
difference of the two rates excludes zero, using Newcombe's hybrid score
interval (1998, method 10), assembled from the per-arm Wilson bounds:

    L = d - sqrt((p1-l1)^2 + (u2-p2)^2)
    U = d + sqrt((u1-p1)^2 + (p2-l2)^2)

Worked example, from Newcombe's own paper: 56/70 vs 48/80 gives a difference
of +0.20 with interval [0.052, 0.334]; zero is excluded, so the difference
is claimed. The per-model intervals shown alongside are context, not the
test.

## 4. Comparing two models, paired: seedsets and McNemar's exact test

Generated check tasks are drawn from a seeded RNG. Two runs that share a
`--seedset` face byte-identical instances, which upgrades the comparison
from "two rates" to "item-level flips": instances both models passed or
both failed carry no information about the difference, and only the
discordant ones decide. With b flips won by model A and c by model B, the
exact conditional McNemar test evaluates the split against Binomial(b+c,
1/2):

    p = min(1, 2 * P[X <= min(b,c)])

Two honesty rules ride along. First, the smallest achievable p is
2^(1-(b+c)), so with fewer than six discordant instances no split can reach
p < 0.05; fitr prints "too few to separate regardless of split" instead of
a p-value that was never in play. Second, the mid-p variant (Fagerland,
Lydersen and Laake 2013 recommend it as less conservative) is reported as a
secondary figure, never as the headline: when the two disagree about
significance, fitr sides with the conservative one.

Pinned seedsets trade contamination resistance for pairing power, so fresh
instances per run remain the default and the pinning is explicit.

## 5. Ratios of means: Fieller's theorem

"Model A is 1.6x faster" needs an interval, and the ratio of two noisy
means is not normally distributed, so mean +/- propagated error is the
wrong shape. fitr uses Fieller's theorem (1954), the exact interval for a
ratio of independent normal means, with Welch-Satterthwaite degrees of
freedom floored into the t-table (fewer degrees of freedom widen the
interval - the conservative direction):

    g    = t^2 * (sb^2/nb) / mb^2
    half = (t/|mb|) * sqrt((sa^2/na)(1-g) + R^2(sb^2/nb))
    CI   = [(R-half)/(1-g), (R+half)/(1-g)]

The g >= 1 condition is not a technicality: it means the denominator mean is
not statistically separated from zero, Fieller's confidence set is no longer
an interval, and any numbers printed from it would be fiction. fitr reports
"no interval" and shows the bare ratio unclaimed. The old quadrature
propagation (hyperfine's method) survives only as that fallback display.

## 6. Deciding against a gate sequentially: Wald's SPRT

`fitr run --adaptive` replaces the fixed repeat count with the sequential
probability ratio test (Wald 1945). For a gate at rate g, it tests
p0 = g - 0.1 against p1 = g + 0.1 at alpha = beta = 0.05: each fresh
instance moves a running log-likelihood ratio by ln(p1/p0) on a pass or
ln((1-p1)/(1-p0)) on a fail, and crossing +/-ln(19) decides. Wald and
Wolfowitz (1948) proved no test with the same error rates needs fewer
expected trials - for a gate at 0.75 the expected sample is roughly 22-26
trials, worst case near 38.

Two deliberate choices. The indifference half-width of 0.10 is a budget
statement: expected sample size grows as 1/delta^2, so halving the width
would quadruple the run time for a distinction the battery cannot support
anyway. And at the truncation cap the outcome is reported as "undecided:
the sample cannot separate the rate from the gate" - the three-way honesty
that Wald's original truncation rule (decide by the sign of the final ratio)
deliberately gives up. Precedent for the whole construction: Stockfish's
Fishtest has gated every engine patch by truncated SPRT for over a decade.

## 7. Zero failures observed: the exact bound behind "deterministic"

`fitr doctor` runs the same request N times and byte-compares. When all N
agree, the honest claim is not "deterministic" but an upper bound: zero
events in n trials bounds the event probability below

    p_upper = 1 - 0.05^(1/n)

at 95% confidence (Hanley and Lippman-Hand 1983; the folk "rule of three",
3/n, is its large-n approximation and is badly wrong at fitr's n - at n = 3
the exact bound is 0.63, not 1.0). Five identical runs still admit a 45%
per-run divergence rate, and the doctor's PASS line says so, with the
sample size that would tighten it.

## 8. Outliers in timing repeats: modified z-scores, annotate only

With five or more repeats, decode samples are screened by the modified
z-score (Iglewicz and Hoaglin 1993): 0.6745 * (x - median) / MAD against
the 3.5 threshold. When MAD is zero (at least half the samples tie the
median, common with coarse timers) the mean absolute deviation about the
median scaled by 1.253314 substitutes; when that is also zero, all values
are identical and nothing is flagged. Below five repeats the estimator is
degenerate and stays silent.

Flagged runs are annotated, never deleted: the summary keeps every sample
and the warning names the likely cause and the fix, following hyperfine's
practice (whose own threshold, at roughly ten robust standard deviations,
is deliberately laxer because its warning gates advice rather than data).

## 9. Small honesty devices

- Flakiness: a task that neither always passes nor always fails within one
  run is flagged; the mean was hiding the interesting fact.
- First-run-slow: a first repeat 1.25x slower than the rest is called out
  (cold caches), following hyperfine, rather than vanishing into a standard
  deviation.
- CV: the coefficient of variation rides along with every mean +/- sd line;
  it is the one dimensionless stability number comparable across devices.
- Borderline gates: when a gate value sits inside the Wilson interval of the
  pool being judged, the verdict carries "borderline: gate inside the CI";
  the point estimate picked a side, the sample did not.
- No error bar is ever printed from a single observation, and a summary at
  n = 1 is labeled "(abs, n=1)" instead of "+/- 0.00".

## References

- Brown, L.D., Cai, T.T., DasGupta, A. (2001). Interval estimation for a
  binomial proportion. Statistical Science 16(2):101-133.
- Newcombe, R.G. (1998). Interval estimation for the difference between
  independent proportions: comparison of eleven methods. Statistics in
  Medicine 17(8):873-890.
- Schenker, N., Gentleman, J.F. (2001). On judging the significance of
  differences by examining the overlap between confidence intervals. The
  American Statistician 55(3):182-186.
- Payton, M.E., Greenstone, M.H., Schenker, N. (2003). Overlapping
  confidence intervals or standard error intervals: what do they mean in
  terms of statistical significance? Journal of Insect Science 3:34.
- McNemar, Q. (1947). Note on the sampling error of the difference between
  correlated proportions or percentages. Psychometrika 12:153-157.
- Fagerland, M.W., Lydersen, S., Laake, P. (2013). The McNemar test for
  binary matched-pairs data: mid-p and asymptotic are better than exact
  conditional. BMC Medical Research Methodology 13:91.
- Fieller, E.C. (1954). Some problems in interval estimation. Journal of
  the Royal Statistical Society B 16(2):175-185.
- Wald, A. (1945). Sequential tests of statistical hypotheses. Annals of
  Mathematical Statistics 16(2):117-186.
- Wald, A., Wolfowitz, J. (1948). Optimum character of the sequential
  probability ratio test. Annals of Mathematical Statistics 19(3):326-339.
- Hanley, J.A., Lippman-Hand, A. (1983). If nothing goes wrong, is
  everything all right? Interpreting zero numerators. JAMA 249(13):1743-1745.
- Iglewicz, B., Hoaglin, D.C. (1993). How to Detect and Handle Outliers.
  ASQC Basic References in Quality Control, Vol. 16.
- Welch, B.L. (1947). The generalization of Student's problem when several
  different population variances are involved. Biometrika 34:28-35.
