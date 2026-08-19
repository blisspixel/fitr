package eval

import (
	"sort"
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

// ItemStat is per-task discrimination between two paired batteries. An item
// that never flips on shared instances cannot separate a good quant from a
// damaged one - it is a candidate to drop, not dropped by this function.
type ItemStat struct {
	TaskID string
	Family string
	Need   string
	Shared int
	Flips  int
	APass  int
	BPass  int
}

func (s ItemStat) Discriminated() bool { return s.Flips > 0 }

// ItemStats rolls PairFlips down to each task id. Unshared instances are
// dropped, same as PairFlips: we do not invent a pair the other run never saw.
func ItemStats(a, b []CheckOutcome) []ItemStat {
	type key struct {
		id   string
		seed uint64
	}
	left := map[key]CheckOutcome{}
	for _, ck := range a {
		left[key{ck.TaskID, ck.Seed}] = ck
	}
	type acc struct {
		family, need          string
		shared, flips, aP, bP int
	}
	by := map[string]*acc{}
	for _, ck := range b {
		pa, ok := left[key{ck.TaskID, ck.Seed}]
		if !ok {
			continue
		}
		s := by[ck.TaskID]
		if s == nil {
			s = &acc{family: firstNonEmpty(ck.Family, pa.Family), need: firstNonEmpty(ck.Need, pa.Need)}
			by[ck.TaskID] = s
		}
		s.shared++
		if pa.Pass {
			s.aP++
		}
		if ck.Pass {
			s.bP++
		}
		if pa.Pass != ck.Pass {
			s.flips++
		}
	}
	out := make([]ItemStat, 0, len(by))
	for id, s := range by {
		out = append(out, ItemStat{
			TaskID: id, Family: s.family, Need: s.need,
			Shared: s.shared, Flips: s.flips, APass: s.aP, BPass: s.bP,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Flips != out[j].Flips {
			return out[i].Flips > out[j].Flips
		}
		return out[i].TaskID < out[j].TaskID
	})
	return out
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
