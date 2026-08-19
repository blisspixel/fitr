package eval

import (
	"strings"
)

// FlipReport is the item-level agreement between two check batteries that
// faced the same instances. Accuracy deltas hide quant damage: a lower
// quant can fail three items the higher quant passed and pass three the
// higher failed, and the two rates match. Flips expose that; rates do not.
type FlipReport struct {
	Shared int
	Agree  int
	AOnly  int // A passed, B failed
	BOnly  int // B passed, A failed
	APass  int
	BPass  int
}

// PairFlips aligns outcomes by (task, seed). Instances only one side saw
// are dropped, not invented.
func PairFlips(a, b []CheckOutcome) FlipReport {
	type key struct {
		id   string
		seed uint64
	}
	left := map[key]bool{}
	for _, ck := range a {
		left[key{ck.TaskID, ck.Seed}] = ck.Pass
	}
	var r FlipReport
	for _, ck := range b {
		pa, ok := left[key{ck.TaskID, ck.Seed}]
		if !ok {
			continue
		}
		r.Shared++
		if pa {
			r.APass++
		}
		if ck.Pass {
			r.BPass++
		}
		switch {
		case pa == ck.Pass:
			r.Agree++
		case pa:
			r.AOnly++
		default:
			r.BOnly++
		}
	}
	return r
}

// HidesDisagreement is the quant-damage tell: the two rates are identical
// and the items are not. A scoreboard of 6/8 vs 6/8 can still be four
// different questions.
func (r FlipReport) HidesDisagreement() bool {
	return r.Shared > 0 && r.APass == r.BPass && r.AOnly+r.BOnly > 0
}

// QuantRank is a coarse total order over common GGUF dtypes, used only to
// pick a reference run for directional damage. Unknown or incomparable
// schemes (IQ*, mixed) return 0 so callers SKIP the directional claim
// rather than invent a ranking.
func QuantRank(q string) int {
	u := strings.ToUpper(strings.TrimSpace(q))
	if strings.HasPrefix(u, "IQ") {
		return 0
	}
	switch {
	case u == "F32" || u == "FP32":
		return 100
	case u == "F16" || u == "FP16":
		return 90
	case u == "BF16":
		return 88
	case strings.HasPrefix(u, "Q8"):
		return 80
	case strings.HasPrefix(u, "Q6"):
		return 70
	case strings.HasPrefix(u, "Q5"):
		return 60
	case strings.HasPrefix(u, "Q4"):
		return 50
	case strings.HasPrefix(u, "Q3"):
		return 40
	case strings.HasPrefix(u, "Q2"):
		return 30
	}
	return 0
}
