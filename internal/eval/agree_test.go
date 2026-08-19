package eval

import "testing"

func ck(id string, seed uint64, pass bool) CheckOutcome {
	return CheckOutcome{TaskID: id, Seed: seed, Pass: pass}
}

func TestPairFlipsDropsUnsharedInstances(t *testing.T) {
	a := []CheckOutcome{ck("x", 1, true), ck("y", 2, true)}
	b := []CheckOutcome{ck("x", 1, true), ck("z", 3, false)}
	r := PairFlips(a, b)
	if r.Shared != 1 || r.Agree != 1 {
		t.Fatalf("%+v, want one shared agreement, not invented pairs", r)
	}
}

func TestEqualAccuracyCanHideFlips(t *testing.T) {
	// Both 2/4. A fails the items B passes and vice versa.
	a := []CheckOutcome{
		ck("json", 1, true), ck("csv", 2, true),
		ck("math", 3, false), ck("list", 4, false),
	}
	b := []CheckOutcome{
		ck("json", 1, false), ck("csv", 2, false),
		ck("math", 3, true), ck("list", 4, true),
	}
	r := PairFlips(a, b)
	if r.APass != 2 || r.BPass != 2 || r.Shared != 4 {
		t.Fatalf("rates %+v, want 2/4 vs 2/4", r)
	}
	if r.AOnly != 2 || r.BOnly != 2 {
		t.Fatalf("flips %+v, want 2 each way", r)
	}
	if !r.HidesDisagreement() {
		t.Fatal("identical rates with four flips is the damage signal accuracy hides")
	}
}

func TestDirectionalQuantDamageIsHiOnly(t *testing.T) {
	hi := []CheckOutcome{ck("a", 1, true), ck("b", 2, true), ck("c", 3, true), ck("d", 4, true)}
	lo := []CheckOutcome{ck("a", 1, true), ck("b", 2, false), ck("c", 3, false), ck("d", 4, true)}
	r := PairFlips(hi, lo)
	if r.AOnly != 2 || r.BOnly != 0 {
		t.Fatalf("damage is items the higher quant passed and the lower failed: %+v", r)
	}
	if r.HidesDisagreement() {
		t.Fatal("4/4 vs 2/4 is a rate change as well as flips; HidesDisagreement is the equal-rate case")
	}
}

func TestEmptySidesAreNotDamage(t *testing.T) {
	if r := PairFlips(nil, nil); r.Shared != 0 || r.HidesDisagreement() {
		t.Fatalf("empty: %+v", r)
	}
}

func TestQuantRankOrdersCommonDtypesAndSkipsIQ(t *testing.T) {
	if QuantRank("Q8_0") <= QuantRank("Q4_K_M") {
		t.Fatal("Q8_0 must outrank Q4_K_M")
	}
	if QuantRank("F16") <= QuantRank("Q8_0") {
		t.Fatal("F16 must outrank Q8_0")
	}
	if QuantRank("Q4_K_M") != QuantRank("Q4_0") {
		t.Fatal("same-ballpark Q4s are not a directional claim")
	}
	if QuantRank("IQ4_XS") != 0 || QuantRank("mystery") != 0 {
		t.Fatal("unknown or IQ schemes must not invent a rank")
	}
}
