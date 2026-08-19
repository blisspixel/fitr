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
