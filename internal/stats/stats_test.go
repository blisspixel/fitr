package stats

import "testing"

func TestWilsonSingleSampleIsBarelyInformative(t *testing.T) {
	// One pass must NOT read as certainty. This is the whole argument for -k 3.
	got := Wilson(1, 1)
	if got.Point != 1.0 {
		t.Fatalf("point = %v, want 1.0", got.Point)
	}
	if got.Lo >= 0.25 {
		t.Fatalf("lo = %v, want a very wide interval (<0.25)", got.Lo)
	}
}

func TestWilsonKnownValue(t *testing.T) {
	got := Wilson(8, 10)
	if got.Point != 0.8 {
		t.Fatalf("point = %v, want 0.8", got.Point)
	}
	if got.Lo < 0.48 || got.Lo > 0.50 {
		t.Fatalf("lo = %v, want ~0.49", got.Lo)
	}
	if got.Hi < 0.94 || got.Hi > 0.95 {
		t.Fatalf("hi = %v, want ~0.943", got.Hi)
	}
}

func TestWilsonEdges(t *testing.T) {
	if lo := Wilson(0, 5).Lo; lo != 0 {
		t.Fatalf("lo = %v, want 0", lo)
	}
	if hi := Wilson(5, 5).Hi; hi != 1 {
		t.Fatalf("hi = %v, want 1", hi)
	}
	if got := Wilson(0, 0); got.N != 0 {
		t.Fatalf("empty should be inert, got %+v", got)
	}
}

func TestOverlappingIntervalsAreIndistinguishable(t *testing.T) {
	a, b := Wilson(3, 3), Wilson(2, 3)
	if !Overlap(a, b) {
		t.Fatal("3/3 and 2/3 must overlap at this sample size")
	}
	if got := Compare("A", a, "B", b); got == "" ||
		!contains(got, "INDISTINGUISHABLE") {
		t.Fatalf("got %q, want an INDISTINGUISHABLE verdict", got)
	}
}

func TestSeparatedIntervalsAreRanked(t *testing.T) {
	a, b := Wilson(20, 20), Wilson(2, 20)
	if Overlap(a, b) {
		t.Fatal("20/20 and 2/20 must not overlap")
	}
	if got := Compare("A", a, "B", b); !contains(got, "A > B") {
		t.Fatalf("got %q, want A > B", got)
	}
}

func TestFlakinessDetectsFlipping(t *testing.T) {
	if !Flakiness([]bool{true, false, true}).Flaky {
		t.Fatal("a flipping task must be flagged flaky")
	}
	if Flakiness([]bool{true, true, true}).Flaky {
		t.Fatal("always-pass is not flaky")
	}
	if Flakiness([]bool{false, false}).Flaky {
		t.Fatal("always-fail is not flaky")
	}
}

func TestSummaryValidRejectsSingleObservation(t *testing.T) {
	// "+/- 0.00" is a lie; Valid() is what stops us printing one.
	if MeanSD([]float64{5}).Valid() {
		t.Fatal("a single observation must not be treated as an estimate")
	}
	if !MeanSD([]float64{5, 6, 7}).Valid() {
		t.Fatal("three observations should be valid")
	}
}

func TestRatioWithErrorPropagatesInQuadrature(t *testing.T) {
	a := MeanSD([]float64{20, 21, 22}) // mean 21
	b := MeanSD([]float64{10, 10.5, 11})
	ratio, sd, ok := RatioWithError(a, b)
	if !ok {
		t.Fatal("both inputs have n>=2 and sd>0, expected ok")
	}
	if ratio < 1.9 || ratio > 2.1 {
		t.Fatalf("ratio = %v, want ~2", ratio)
	}
	if sd <= 0 {
		t.Fatalf("sd = %v, want a positive propagated error", sd)
	}
}

func TestRatioRefusesErrorBarOnSingleObservation(t *testing.T) {
	a, b := MeanSD([]float64{20}), MeanSD([]float64{10})
	ratio, sd, ok := RatioWithError(a, b)
	if ok {
		t.Fatal("must not claim an error bar from single observations")
	}
	if sd != 0 {
		t.Fatalf("sd = %v, want 0 with ok=false", sd)
	}
	if ratio != 2 {
		t.Fatalf("ratio = %v, want 2 (still reported, just without +/-)", ratio)
	}
}

func TestMinDetectableEffectShrinksWithRepeats(t *testing.T) {
	k1, k3 := MinDetectableEffect(6, 1), MinDetectableEffect(6, 3)
	if k3 >= k1 {
		t.Fatalf("repeats should shrink MDE: k1=%v k3=%v", k1, k3)
	}
	if k1 < 0.4 {
		t.Fatalf("a 6-task battery cannot resolve small effects; got %v", k1)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func TestFirstRunSlowDetectsColdStart(t *testing.T) {
	// Real measurement: a model reported these three decode figures in one run.
	slow, ratio := FirstRunSlow([]float64{6.35, 14.76, 14.63})
	if !slow {
		t.Fatal("a first run 2.3x slower than the rest must be flagged")
	}
	if ratio < 2.0 {
		t.Fatalf("ratio = %v, want ~2.3", ratio)
	}
}

func TestFirstRunSlowIgnoresSettledRuns(t *testing.T) {
	if slow, _ := FirstRunSlow([]float64{26.45, 25.48, 25.73}); slow {
		t.Fatal("a settled model (CV ~2%) must not be flagged")
	}
	if slow, _ := FirstRunSlow([]float64{10, 11}); slow {
		t.Fatal("fewer than 3 observations cannot support the claim")
	}
	// A first run that is FASTER than the rest is not this phenomenon.
	if slow, _ := FirstRunSlow([]float64{30, 20, 20}); slow {
		t.Fatal("only a slow first run counts")
	}
}

func TestZeroEventUpperBound(t *testing.T) {
	// Exact: 1 - 0.05^(1/n). The rule of three (3/n) is its large-n shadow.
	for n, want := range map[int]float64{3: 0.6316, 5: 0.4507, 10: 0.2589, 20: 0.1391} {
		if got := ZeroEventUpperBound(n); got < want-0.001 || got > want+0.001 {
			t.Errorf("ZeroEventUpperBound(%d) = %v, want ~%v", n, got, want)
		}
	}
	// Large n converges on 3/n - the mnemonic must agree with the math.
	if got, approx := ZeroEventUpperBound(300), 3.0/300; got < approx*0.95 || got > approx*1.05 {
		t.Errorf("rule-of-three sanity: got %v, want ~%v", got, approx)
	}
	if ZeroEventUpperBound(0) != 1 {
		t.Error("zero trials bound nothing")
	}
}

func TestTCrit975KnownValues(t *testing.T) {
	// NIST/SEMATECH e-Handbook values; 5 d.p. equal to R's qt(0.975, df).
	for df, want := range map[float64]float64{1: 12.7062, 2: 4.30265, 4: 2.77645, 8: 2.30600, 30: 2.04227} {
		if got := TCrit975(df); got != want {
			t.Errorf("TCrit975(%v) = %v, want %v", df, got, want)
		}
	}
	// Fractional df rounds DOWN - conservative, wider interval.
	if got := TCrit975(4.9); got != 2.77645 {
		t.Errorf("TCrit975(4.9) = %v, want the df=4 value", got)
	}
	// Beyond the table stays at df=30 rather than dropping to z: conservative.
	if got := TCrit975(200); got != 2.04227 {
		t.Errorf("TCrit975(200) = %v, want the df=30 value", got)
	}
}

func TestWelchDF(t *testing.T) {
	// Equal variances and sizes collapse toward n1+n2-2.
	if df := WelchDF(2, 10, 2, 10); df < 17.5 || df > 18.0 {
		t.Errorf("symmetric WelchDF = %v, want ~18", df)
	}
	// A much noisier small sample drags df toward ITS n-1: the noisy side
	// dominates the uncertainty, and the df must say so.
	if df := WelchDF(10, 3, 1, 30); df > 3 {
		t.Errorf("asymmetric WelchDF = %v, want <=3 (dominated by the noisy n=3 sample)", df)
	}
}

func TestCV(t *testing.T) {
	s := Summary{Mean: 20, SD: 1, N: 3}
	if got := s.CV(); got != 0.05 {
		t.Errorf("CV = %v, want 0.05", got)
	}
	if (Summary{Mean: 20, N: 1}).CV() != 0 {
		t.Error("single observation has no CV")
	}
}

func near(got, want, tol float64) bool { return got >= want-tol && got <= want+tol }

func TestNewcombeDiffPublishedPins(t *testing.T) {
	// Newcombe 1998, Table II (56/70 vs 48/80) and derived pins verified
	// against R DescTools::BinomDiffCI method "score".
	cases := []struct {
		x1, n1, x2, n2 int
		lo, hi         float64
	}{
		{56, 70, 48, 80, 0.052431, 0.333873},
		{10, 10, 0, 10, 0.607509, 1.0},
		{6, 7, 2, 7, 0.058228, 0.806250},
		{5, 56, 0, 29, -0.038137, 0.192560},
	}
	for _, c := range cases {
		lo, hi, ok := NewcombeDiff(c.x1, c.n1, c.x2, c.n2)
		if !ok {
			t.Fatalf("NewcombeDiff(%d/%d, %d/%d) not ok", c.x1, c.n1, c.x2, c.n2)
		}
		if !near(lo, c.lo, 5e-4) || !near(hi, c.hi, 5e-4) {
			t.Errorf("NewcombeDiff(%d/%d, %d/%d) = [%v, %v], want [%v, %v]",
				c.x1, c.n1, c.x2, c.n2, lo, hi, c.lo, c.hi)
		}
	}
	if _, _, ok := NewcombeDiff(1, 0, 1, 2); ok {
		t.Fatal("zero-trial arm must refuse")
	}
	// The interval always brackets the observed difference.
	lo, hi, _ := NewcombeDiff(3, 5, 2, 5)
	if d := 0.6 - 0.4; lo > d || hi < d {
		t.Fatalf("interval [%v, %v] does not bracket d=%v", lo, hi, d)
	}
}

func TestMcNemarExactPins(t *testing.T) {
	// Pins verify against R binom.test(min(b,c), b+c, 0.5).
	cases := []struct {
		b, c         int
		pExact, pMid float64
		separable    bool
	}{
		{1, 5, 0.218750, 0.125000, true},
		{2, 8, 0.109375, 0.065430, true},
		{0, 4, 0.125000, 0.062500, false},
		{3, 3, 1.0, 0.687500, true},
		{5, 15, 0.041389, 0.026604, true},
	}
	for _, c := range cases {
		pe, pm, sep := McNemarExact(c.b, c.c)
		if !near(pe, c.pExact, 1e-4) || !near(pm, c.pMid, 1e-4) || sep != c.separable {
			t.Errorf("McNemarExact(%d,%d) = %v, %v, %v; want %v, %v, %v",
				c.b, c.c, pe, pm, sep, c.pExact, c.pMid, c.separable)
		}
	}
	// No discordant pairs: nothing was tested, and the flag must say so.
	if _, _, sep := McNemarExact(0, 0); sep {
		t.Fatal("b+c=0 must be non-separable")
	}
	// Below six discordant pairs the exact test CANNOT reach 0.05 for any
	// split - the floor is 2^(1-n).
	if pe, _, sep := McNemarExact(0, 5); sep || pe < 0.0625-1e-9 {
		t.Fatalf("(0,5): p=%v sep=%v; floor is 0.0625 and separable must be false", pe, sep)
	}
}

func TestFiellerRatioPins(t *testing.T) {
	// Formula-derived pins cross-checkable against R mratios::t.test.ratio
	// with var.equal=FALSE and the floored-Welch-df t convention.
	lo, hi, r, ok := FiellerRatio(Summary{Mean: 15, SD: 2, N: 5}, Summary{Mean: 10, SD: 1.5, N: 5})
	if !ok || !near(lo, 1.214, 2e-3) || !near(hi, 1.863, 2e-3) || r != 1.5 {
		t.Fatalf("Fieller (15,2,5)/(10,1.5,5) = [%v, %v] r=%v ok=%v; want [1.214, 1.863] 1.5", lo, hi, r, ok)
	}
	lo, hi, _, ok = FiellerRatio(Summary{Mean: 42.3, SD: 1.8, N: 8}, Summary{Mean: 35.1, SD: 2.4, N: 8})
	if !ok || !near(lo, 1.134, 2e-3) || !near(hi, 1.283, 2e-3) {
		t.Fatalf("Fieller pin 2 = [%v, %v] ok=%v; want [1.134, 1.283]", lo, hi, ok)
	}
	// Denominator not separated from zero (g >= 1): the confidence set is not
	// an interval, and the only honest number is no number.
	if _, _, _, ok := FiellerRatio(Summary{Mean: 5, SD: 1, N: 5}, Summary{Mean: 1, SD: 2, N: 5}); ok {
		t.Fatal("g >= 1 must refuse to report an interval")
	}
	if _, _, _, ok := FiellerRatio(Summary{Mean: 5, SD: 1, N: 1}, Summary{Mean: 2, SD: 1, N: 5}); ok {
		t.Fatal("single observation must refuse")
	}
}

func TestSPRTBoundariesAndIncrements(t *testing.T) {
	s, err := NewSPRT(0.65, 0.85)
	if err != nil {
		t.Fatal(err)
	}
	if !near(s.incS, 0.268264, 1e-5) || !near(s.incF, -0.847298, 1e-5) || !near(s.logA, 2.944439, 1e-5) {
		t.Fatalf("increments/boundaries: %+v", s)
	}
	// A clean streak accepts H1 at exactly trial 11 (Wald pin), not before.
	for i := 1; i <= 10; i++ {
		if d := s.Add(true); d != SPRTContinue {
			t.Fatalf("decided at %d passes, want continue through 10", i)
		}
	}
	if d := s.Add(true); d != SPRTAcceptH1 {
		t.Fatalf("11 straight passes must accept H1, got %v", d)
	}
	// A losing streak accepts H0 at exactly trial 4.
	s2, _ := NewSPRT(0.65, 0.85)
	for i := 1; i <= 3; i++ {
		if d := s2.Add(false); d != SPRTContinue {
			t.Fatalf("decided at %d failures, want continue through 3", i)
		}
	}
	if d := s2.Add(false); d != SPRTAcceptH0 {
		t.Fatalf("4 straight failures must accept H0, got %v", d)
	}
	if _, err := NewSPRT(0.8, 0.7); err == nil {
		t.Fatal("p1 <= p0 must be rejected")
	}
	if g, err := GateSPRT(0.75); err != nil || g == nil {
		t.Fatalf("GateSPRT(0.75): %v", err)
	}
}

func TestModifiedZOutliers(t *testing.T) {
	// Iglewicz-Hoaglin style example: only the 8.2 is an outlier.
	xs := []float64{2.1, 2.6, 2.4, 2.5, 2.3, 2.1, 2.3, 2.6, 8.2, 2.5}
	flags := ModifiedZOutliers(xs)
	for i, f := range flags {
		want := xs[i] == 8.2
		if f != want {
			t.Errorf("x=%v flagged=%v, want %v", xs[i], f, want)
		}
	}
	// MAD=0 (majority ties the median): the MeanAD fallback still catches it.
	flags = ModifiedZOutliers([]float64{5, 5, 5, 5, 9})
	if !flags[4] || flags[0] {
		t.Fatalf("MeanAD fallback: %v", flags)
	}
	// All identical: no outliers, no NaN panic.
	for _, f := range ModifiedZOutliers([]float64{7, 7, 7, 7, 7}) {
		if f {
			t.Fatal("identical values cannot contain outliers")
		}
	}
	// Below n=5 the estimator is degenerate; refuse to flag.
	for _, f := range ModifiedZOutliers([]float64{1, 1, 99}) {
		if f {
			t.Fatal("n<5 must not flag")
		}
	}
}
