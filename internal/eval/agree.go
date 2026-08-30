package eval

import (
	"sort"
	"strings"
)

// FlipReport is the item-level agreement between two check batteries that
// faced the same instances. Accuracy deltas hide disagreement: one run can
// fail three items the other passed and pass three the other failed, while
// the two rates match. Flips expose that; rates do not. Attributing those
// flips to quantization requires separately verified same-base lineage.
type FlipReport struct {
	Shared int
	Agree  int
	AOnly  int // A passed, B failed
	BOnly  int // B passed, A failed
	APass  int
	BPass  int
}

// PairFlips aligns scorable outcomes by (task, seed). Instances only one side
// saw, and pairs where either side was not measured as PASS or FAIL, are
// dropped rather than invented or silently counted as failures.
func PairFlips(a, b []CheckOutcome) FlipReport {
	type key struct {
		id   string
		seed uint64
	}
	left := map[key]bool{}
	for _, ck := range a {
		pass, measured := MeasuredOutcome(ck.Outcome, ck.Pass)
		if measured {
			left[key{ck.TaskID, ck.Seed}] = pass
		}
	}
	var r FlipReport
	for _, ck := range b {
		pa, ok := left[key{ck.TaskID, ck.Seed}]
		if !ok {
			continue
		}
		pb, measured := MeasuredOutcome(ck.Outcome, ck.Pass)
		if !measured {
			continue
		}
		r.Shared++
		if pa {
			r.APass++
		}
		if pb {
			r.BPass++
		}
		switch {
		case pa == pb:
			r.Agree++
		case pa:
			r.AOnly++
		default:
			r.BOnly++
		}
	}
	return r
}

// FamilyDirectionReport is the direction of paired differences after each
// generated family contributes at most one sign. Item-level flips remain
// descriptive; this is the unit used within one need for a paired claim.
type FamilyDirectionReport struct {
	Shared int
	Agree  int
	AOnly  int // more paired passes for A within the family
	BOnly  int // more paired passes for B within the family
}

// NeedDirectionStat keeps independent product questions out of one global
// model winner. Its family directions are interpreted only within Need.
type NeedDirectionStat struct {
	Need string
	FamilyDirectionReport
}

type directionKey struct {
	id   string
	seed uint64
}

type directionObserved struct {
	check CheckOutcome
	pass  bool
}

type directionStratum struct{ need, family string }
type directionCounts struct{ a, b int }

// NeedDirections aligns scorable instances and aggregates paired pass counts
// by family within each need. It is the claimable paired estimand: independent
// product questions remain separate, and each family contributes at most one
// sign inside that need.
func NeedDirections(a, b []CheckOutcome) []NeedDirectionStat {
	left := indexMeasuredDirections(a)
	byStratum := countDirectionStrata(left, b)
	byNeed := summarizeNeedDirections(byStratum)
	out := make([]NeedDirectionStat, 0, len(byNeed))
	for need, report := range byNeed {
		out = append(out, NeedDirectionStat{Need: need, FamilyDirectionReport: report})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Need < out[j].Need })
	return out
}

func indexMeasuredDirections(checks []CheckOutcome) map[directionKey]directionObserved {
	left := map[directionKey]directionObserved{}
	for _, ck := range checks {
		pass, measured := MeasuredOutcome(ck.Outcome, ck.Pass)
		if measured {
			left[directionKey{ck.TaskID, ck.Seed}] = directionObserved{check: ck, pass: pass}
		}
	}
	return left
}

func countDirectionStrata(left map[directionKey]directionObserved, checks []CheckOutcome) map[directionStratum]directionCounts {
	byStratum := map[directionStratum]directionCounts{}
	for _, ck := range checks {
		pa, ok := left[directionKey{ck.TaskID, ck.Seed}]
		if !ok {
			continue
		}
		pb, measured := MeasuredOutcome(ck.Outcome, ck.Pass)
		if !measured {
			continue
		}
		need := firstNonEmpty(ck.Need, pa.check.Need)
		if need == "" {
			need = "unspecified"
		}
		family := firstNonEmpty(ck.Family, pa.check.Family)
		if family == "" {
			family = ck.TaskID
		}
		k := directionStratum{need: need, family: family}
		c := byStratum[k]
		if pa.pass {
			c.a++
		}
		if pb {
			c.b++
		}
		byStratum[k] = c
	}
	return byStratum
}

func summarizeNeedDirections(byStratum map[directionStratum]directionCounts) map[string]FamilyDirectionReport {
	byNeed := map[string]FamilyDirectionReport{}
	for k, c := range byStratum {
		r := byNeed[k.need]
		r.Shared++
		switch {
		case c.a > c.b:
			r.AOnly++
		case c.b > c.a:
			r.BOnly++
		default:
			r.Agree++
		}
		byNeed[k.need] = r
	}
	return byNeed
}

// HidesDisagreement reports that the rates are identical and the item-level
// outcomes are not. A scoreboard of 6/8 vs 6/8 can still be four different
// questions.
func (r FlipReport) HidesDisagreement() bool {
	return r.Shared > 0 && r.APass == r.BPass && r.AOnly+r.BOnly > 0
}

// QuantRank is a coarse total order over common GGUF dtypes. It can order
// display inputs, but does not establish that artifacts share a base-model
// revision. Unknown or incomparable schemes (IQ*, mixed) return 0.
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
		if _, measured := MeasuredOutcome(ck.Outcome, ck.Pass); measured {
			left[key{ck.TaskID, ck.Seed}] = ck
		}
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
		paPass, paMeasured := MeasuredOutcome(pa.Outcome, pa.Pass)
		pbPass, pbMeasured := MeasuredOutcome(ck.Outcome, ck.Pass)
		if !paMeasured || !pbMeasured {
			continue
		}
		s := by[ck.TaskID]
		if s == nil {
			s = &acc{family: firstNonEmpty(ck.Family, pa.Family), need: firstNonEmpty(ck.Need, pa.Need)}
			by[ck.TaskID] = s
		}
		s.shared++
		if paPass {
			s.aP++
		}
		if pbPass {
			s.bP++
		}
		if paPass != pbPass {
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
