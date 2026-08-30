# The statistics of fitr

fitr runs a finite battery on one machine and turns it into verdicts. That
regime - small family counts, repeated instances, and three to ten timing
repeats - is exactly where most benchmark statistics quietly stop being true. This
document states every method fitr uses, why it was chosen over the obvious
alternative, and what it refuses to compute. Each section follows the same
shape: the situation, the method, the rejected alternative, the reference.

<img src="assets/compare.svg?v=0.9.9" alt="fitr compare (mock data)" width="820">

Two design rules govern everything below:

1. Never fabricate precision. No error bar from one observation, no p-value
   that was mathematically incapable of reaching significance, no interval
   from a degenerate formula.
2. "Cannot separate" is a real answer. When the sample does not decide, fitr
   says so in those words rather than picking a winner.

## 1. One binary rate: the Wilson score interval

Every genuinely unclustered binary pool is summarized as `passes/n` with a
95% Wilson score interval. Generated need-level pools use the cluster-adjusted
extension in section 2 instead:

    center = (p + z^2/2n) / (1 + z^2/n)
    half   = (z / (1 + z^2/n)) * sqrt(p(1-p)/n + z^2/4n^2)

Why Wilson: the textbook Wald interval (`p +/- z*sqrt(p(1-p)/n)`) collapses
to zero width at p = 0 or 1, which is precisely where small batteries live
(a 3/3 result is common and means far less than it looks: Wilson reports
[0.29, 1.0]). Brown, Cai and DasGupta (2001) co-recommend Wilson and
Jeffreys at small n; Wilson wins here because it needs one square root
instead of an inverse incomplete beta function (fitr has zero dependencies),
it behaves at the boundaries unmodified and provides the conservative iid
baseline that clustered intervals are not allowed to narrow.

## 2. Uncertainty belongs to each need

The current battery contains 22 generated checks across 16 families. Those
checks do not measure one exchangeable property. Structured output, exact
instructions, reasoning, tool calling, and tool restraint are independent
product questions with different gates and different family structures.

fitr therefore does not add all PASS, FAIL, SKIP, and INCONCLUSIVE outcomes
into a global sample size or print a global minimum detectable effect. Such a
number would invent a denominator that no verdict uses. Each need prints its
own count, family breakdown, interval, and gate. If the gate lies inside that
need's interval, the verdict is `INCONCLUSIVE`.

The practical reading is local: `6/7 [0.49-0.97]` against a 0.75 structured
output gate does not establish either side, even if several unrelated needs
passed. More repeats add instances within declared families. They do not turn
different needs into one quality score or make one family prove a broader
need.

## 2a. Need-level pooling is clustered by family

The generated checks are 22 task specs across 16 families, not 22 iid draws
of one skill.
`json_object` and `csv_strict` both feed `structured_output`, but a model
that emits valid objects and broken CSV is not a 15/16 Bernoulli process.
Pooling them as iid Wilson overstates n and can mint a PASS that hides a
dead family: 60/70 with one family at 0/10 looks like `[0.76, 0.93]`
against a 0.75 gate.

Need-level intervals therefore use a conservative cluster-adjusted Wilson
approximation. Each family is a cluster. Intra-cluster correlation ρ̂ comes
from the unequal-size one-way ANOVA estimator. The design effect is:

    deff = 1 + (((1 + CV_size^2) * mean_size) - 1) * ρ̂
    n_eff = n / deff

The Wilson interval is evaluated at `n_eff` and is never allowed to become
narrower than iid Wilson. Singleton families reduce exactly to ordinary
Wilson. A single repeated family has one effective unit because between-family
variation is not identifiable; it can establish behavior inside that family,
not the broader need. Per-family counts remain on the explanation line so the
interval is not the only disclosure.

A family whose own Wilson interval lies entirely below the gate cannot
let the need PASS. Reasoning checks observed while executable coding is
SKIP are printed on the coding line as observations, not as a coding
verdict.

Rejected alternative: raising default `-k` until iid Wilson looks wide
enough. That spends wall time to paper over a dependence structure the
interval should have named.

This is a fixed-sample design-effect approximation, not an exact interval and
not a sequential confidence sequence. Its implementation is pinned by
boundary, single-family, singleton-family, dead-family, and unequal-size
tests.

## 3. Why unpaired behavior does not get a winner claim

Newcombe's hybrid score interval is an appropriate difference interval for
two independent binomial proportions. fitr retains a pinned implementation
for that statistical primitive. It does not apply it to the generated
behavior battery, because repeated task instances are clustered by family and
the two runs usually face different instances. Treating those observations as
independent binomial arms would overstate evidence and can conflict with the
paired item-level result.

For reference, the independent-arm interval is assembled from Wilson bounds:

    L = d - sqrt((p1-l1)^2 + (u2-p2)^2)
    U = d + sqrt((u1-p1)^2 + (p2-l2)^2)

The CLI instead prints no unpaired behavior winner and tells the operator how
to create a paired run. Descriptive per-need rates remain visible inside each
scorecard.

## 4. Comparing two models, paired: seedsets and a family-level exact sign test

Generated check tasks are drawn from a seeded RNG. Two runs that share a
`--seedset` face byte-identical instances. fitr prints item-level flips as a
descriptive view, but does not treat repeated instances from one generated
family as independent evidence.

For a claim, fitr first requires matching sealed plans and a scorable PASS or
FAIL on both sides for every planned instance. Missing, SKIP, or INCONCLUSIVE
evidence leaves the flips descriptive only. It then totals paired passes inside
each family and need. A family contributes one direction within that need:
model A had more paired passes, model B had more, or the family tied. Unrelated
needs never form a global winner. With b discordant family directions favoring
A and c favoring B inside one need, an exact two-sided sign test evaluates the
split against Binomial(b+c, 1/2):

    p = min(1, 2 * P[X <= min(b,c)])

Two honesty rules ride along. First, the smallest achievable p is
2^(1-(b+c)), so with fewer than six discordant family directions no split can reach
p < 0.05; fitr prints "too few to separate regardless of split" instead of
a p-value that was never in play. Second, the mid-p variant (Fagerland,
Lydersen and Laake 2013 recommend it as less conservative) is reported as a
secondary figure, never as the headline: when the two disagree about
significance, fitr sides with the conservative one.

Pinned seedsets trade contamination resistance for pairing power, so fresh
instances per run remain the default and the pinning is explicit.

Accuracy deltas hide item-level disagreement. One run can fail three items the
other passed and pass three the other failed, and both score 6/8.
`fitr compare` on a shared seedset therefore prints the flips even when the
rates match ("accuracy hid N item-level flips"). Those flips are descriptive;
only complete, need-stratified family directions feed an exact test. fitr does not attribute those
flips to quantization: matching family, parameter size, and a ranked GGUF dtype
do not prove both artifacts descend from the same exact base revision. Until a
sealed same-base lineage receipt binds both runtime-bound artifact digests,
directional quant attribution is INCONCLUSIVE. `fitr calibrate --lineage`
attaches that receipt; `fitr compare` still does not. IQ and unknown schemes also SKIP dtype ordering.

## 5. Ratios of means: Fieller's theorem

"Model A is 1.6x faster" needs an interval, and the ratio of two noisy
means is not normally distributed, so mean +/- propagated error is the
wrong shape. fitr uses Fieller's theorem (1954), the exact interval for a
ratio of independent normal means, with Welch-Satterthwaite degrees of
freedom floored into the t-table (fewer degrees of freedom widen the
interval - the conservative direction):

Prefill comparison adds an evidence rule before any ratio is allowed: every
sample on both sides must carry an explicit zero-cache receipt. Unknown cache
state or any positive cached-token count remains visible descriptively but
cannot support an uncached-prefill winner claim.

    g    = t^2 * (sb^2/nb) / mb^2
    half = (t/|mb|) * sqrt((sa^2/na)(1-g) + R^2(sb^2/nb))
    CI   = [(R-half)/(1-g), (R+half)/(1-g)]

The g >= 1 condition is not a technicality: it means the denominator mean is
not statistically separated from zero, Fieller's confidence set is no longer
an interval, and any numbers printed from it would be fiction. fitr reports
"no interval" and shows the bare ratio unclaimed. The old quadrature
propagation (hyperfine's method) survives only as that fallback display.

## 6. Fixed denominators and sealed schedules

Current runs execute every task spec in every requested round. Before
inference, the manifest seals the ordered task ID, family, need, origin, and
derived seed for every scheduled observation. A count alone would allow a
missing hard task to be replaced with a duplicate easy task.

PASS and FAIL are the only outcomes that enter a rate. SKIP, ERROR, and
INCONCLUSIVE remain terminal observations, are never anonymously padded, and
cannot establish a need. The raw observations, outcome counts, scorecard, and
profile are signed at completion. Readers recompute the scorecard from those
raw observations and the sealed profile before accepting it.

The former public adaptive mode is intentionally unavailable. Its sequential
Bernoulli process treated heterogeneous raw task specs as exchangeable while
the scorecard treated their families as clusters. Several specs share a
family, so completing every spec in a round was spec-balanced, not
equal-family-balanced. That mismatch could not support the advertised
anytime-valid pooled-rate claim.

A future sequential design must first declare its estimand and stratified
sampling plan. The current research direction is one fresh instance per
distinct family per complete round, with an anytime-valid bounded-mean process
over complete-round family averages. Until that design has replayable receipts
and calibration tests, fixed sampling is the evidence-bearing path.

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
  pool being judged, the verdict is `INCONCLUSIVE` because the interval does
  not establish either side of the gate;
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
- Robbins, H. (1970). Statistical methods related to the law of the iterated
  logarithm. Annals of Mathematical Statistics 41(5):1397-1409.
- Howard, S.R., Ramdas, A., McAuliffe, J., Sekhon, J. (2021). Time-uniform,
  nonparametric, nonasymptotic confidence sequences. Annals of Statistics
  49(2):1055-1080.
- Hanley, J.A., Lippman-Hand, A. (1983). If nothing goes wrong, is
  everything all right? Interpreting zero numerators. JAMA 249(13):1743-1745.
- Iglewicz, B., Hoaglin, D.C. (1993). How to Detect and Handle Outliers.
  ASQC Basic References in Quality Control, Vol. 16.
- Welch, B.L. (1947). The generalization of Student's problem when several
  different population variances are involved. Biometrika 34:28-35.
