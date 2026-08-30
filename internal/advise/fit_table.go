package advise

import (
	"sort"
	"strconv"
	"strings"
)

// FitTable is weights / KV / other allocation / headroom at several context
// points. Other is a derived remainder, not an independent compute-buffer
// measurement, and is n/a unless a runtime total is available at that exact
// ctx. Decode/prefill overlay only from saved runs at that ctx. Allocator
// projections are excluded until their effective context and placement are
// sealed.
type FitTable struct {
	HaveGB  float64    `json:"have_gb,omitempty"`
	HaveSrc string     `json:"have_source,omitempty"`
	Note    string     `json:"note,omitempty"`
	Points  []FitPoint `json:"points,omitempty"`
}

type FitPoint struct {
	Ctx        int     `json:"ctx"`
	Tier       string  `json:"tier"`
	WeightsGB  float64 `json:"weights_gb"`
	KVGB       float64 `json:"kv_gb"`
	OtherGB    float64 `json:"other_resident_gb,omitempty"`
	OtherKnown bool    `json:"other_resident_known"`
	NeedGB     float64 `json:"need_gb"`
	HeadroomGB float64 `json:"headroom_gb"`
	DecodeTPS  float64 `json:"decode_tps,omitempty"`
	PrefillTPS float64 `json:"prefill_tps,omitempty"`
	Requested  bool    `json:"requested,omitempty"`
	Suggested  bool    `json:"suggested,omitempty"`
	// AllocationEvidence identifies the semantics behind NeedGB and its
	// component breakdown. It is additive so existing fitr.advise.v1 readers
	// may ignore it.
	AllocationEvidence string `json:"allocation_evidence,omitempty"`
	Note               string `json:"note,omitempty"`
}

const (
	allocationObservedTotal          = "observed_total"
	allocationObservedTotalRemainder = "observed_total_derived_remainder"
	allocationLowerBound             = "derived_lower_bound"
)

var defaultFitCtx = []int{2048, 4096, 8192, 16384, 32768}

// ContextFit sizes the model at several windows. Hybrid recurrent models
// stay SKIP on the algebraic path; a measured resident at one ctx is a
// single-point table, not a projection.
func ContextFit(in Input) *FitTable {
	t := &FitTable{HaveSrc: in.HaveSrc}
	if in.HaveGB <= 0 {
		t.Note = "GPU memory was not measured; no context table"
		return t
	}
	t.HaveGB = round1(in.HaveGB)
	if in.WeightsB <= 0 {
		t.Note = "model weights were not measured; no context table"
		return t
	}
	if in.Arch.Hybrid {
		return hybridFit(in, t)
	}
	return conventionalFit(in, t)
}

func conventionalFit(in Input, t *FitTable) *FitTable {
	if !in.Arch.KVReady() {
		t.Note = "KV cache not sized (no architecture metadata); pass a GGUF or --load"
		return t
	}
	elem, _, _ := kvElemBytes(in)
	perTok := in.Arch.kvBytesPerToken(elem)
	if perTok <= 0 {
		t.Note = "KV bytes per token could not be sized"
		return t
	}
	weightsB := float64(in.WeightsB)
	haveB := in.HaveGB * GiB
	for _, ctx := range fitCtxPoints(in.Arch.MaxCtx, in.Ctx) {
		t.Points = append(t.Points, makeFitPoint(in, ctx, weightsB, perTok, haveB))
	}
	markSuggested(t)
	if automaticNVIDIAUnifiedCapacity(in) {
		t.Note = "automatic shared-memory capacity is not a safe budget; ? is unproven"
	} else if !anyOtherKnown(t.Points) {
		t.Note = "weights + KV only; other allocation has no matched total evidence"
	}
	return t
}

func makeFitPoint(in Input, ctx int, weightsB, perTok, haveB float64) FitPoint {
	kvB := perTok * float64(ctx)
	p := FitPoint{
		Ctx: ctx, WeightsGB: round1(weightsB / GiB), KVGB: round1(kvB / GiB),
		Requested: in.Ctx > 0 && ctx == in.Ctx, AllocationEvidence: allocationLowerBound,
	}
	otherB, known, evidence, note := otherResidentAt(in, ctx, weightsB, kvB)
	p.OtherKnown, p.Note = known, note
	if evidence != "" {
		p.AllocationEvidence = evidence
	}
	needB := weightsB + kvB
	if known {
		p.OtherGB = round1(otherB / GiB)
		needB += otherB
	} else if p.Note == "" {
		p.Note = "other allocation n/a (no matched total evidence at this ctx)"
	}
	p.NeedGB = round1(needB / GiB)
	p.HeadroomGB = round1((haveB - needB) / GiB)
	p.Tier = fitPointTier(in, p.AllocationEvidence, needB, haveB)
	attachTiming(&p, in.Timings)
	return p
}

func hybridFit(in Input, t *FitTable) *FitTable {
	haveB := in.HaveGB * GiB
	add := func(ctx int, allocB int64, note string) {
		if ctx <= 0 || allocB <= 0 {
			return
		}
		need := float64(allocB)
		p := FitPoint{
			Ctx:                ctx,
			WeightsGB:          round1(float64(in.WeightsB) / GiB),
			NeedGB:             round1(need / GiB),
			HeadroomGB:         round1((haveB - need) / GiB),
			Requested:          in.Ctx > 0 && ctx == in.Ctx,
			AllocationEvidence: allocationObservedTotal,
			Note:               note,
		}
		p.Tier = fitPointTier(in, allocationObservedTotal, need, haveB)
		attachTiming(&p, in.Timings)
		t.Points = append(t.Points, p)
	}
	if in.ResidentB > 0 && in.ResidentCtx > 0 {
		add(in.ResidentCtx, in.ResidentB,
			"hybrid: observed total allocation; component breakdown is unknown")
	}
	if len(t.Points) == 0 {
		if in.FitB > 0 {
			t.Note = "allocator projection omitted: final context and placement were not captured; use --load"
		} else {
			t.Note = "hybrid recurrent architecture cannot be projected from weights plus KV; use --load"
		}
		return t
	}
	if automaticNVIDIAUnifiedCapacity(in) {
		t.Note = "automatic shared-memory capacity is not a safe budget; only observed points fit"
	}
	markSuggested(t)
	return t
}

func fitPointTier(in Input, evidence string, needB, haveB float64) string {
	observed := evidence == allocationObservedTotal || evidence == allocationObservedTotalRemainder
	if automaticNVIDIAUnifiedCapacity(in) && !observed {
		if needB > haveB {
			return Incompatible
		}
		return Skip
	}
	if automaticNVIDIAUnifiedCapacity(in) && observed && needB > haveB {
		return Skip
	}
	if needB <= haveB {
		return Compatible
	}
	return Incompatible
}

func fitCtxPoints(maxCtx, requested int) []int {
	var out []int
	seen := map[int]bool{}
	add := func(n int) {
		if n <= 0 || seen[n] {
			return
		}
		if maxCtx > 0 && n > maxCtx {
			return
		}
		seen[n] = true
		out = append(out, n)
	}
	for _, n := range defaultFitCtx {
		add(n)
	}
	add(maxCtx)
	add(requested)
	sort.Ints(out)
	return out
}

func otherResidentAt(in Input, ctx int, weightsB, kvB float64) (float64, bool, string, string) {
	if in.ResidentB > 0 && in.ResidentCtx == ctx {
		buf := float64(in.ResidentB) - weightsB - kvB
		if buf < 0 {
			return 0, false, allocationObservedTotalRemainder, "resident smaller than weights+KV estimate"
		}
		return buf, true, allocationObservedTotalRemainder,
			"other allocation is observed runtime total minus modeled weights and KV"
	}
	return 0, false, "", ""
}

func attachTiming(p *FitPoint, timings []SavedTiming) {
	for _, t := range timings {
		if t.Ctx == p.Ctx {
			p.DecodeTPS = t.DecodeTPS
			p.PrefillTPS = t.PrefillTPS
			return
		}
	}
}

func markSuggested(t *FitTable) {
	requested, bestCompat := -1, -1
	for i, p := range t.Points {
		if p.Requested {
			requested = i
		}
		if p.Tier == Compatible {
			bestCompat = i
		}
	}
	if requested >= 0 && t.Points[requested].Tier == Compatible {
		t.Points[requested].Suggested = true
		return
	}
	if bestCompat >= 0 {
		t.Points[bestCompat].Suggested = true
	}
}

func anyOtherKnown(points []FitPoint) bool {
	for _, p := range points {
		if p.OtherKnown {
			return true
		}
	}
	return false
}

// CompactWindows is the one-line context-fit graph:
// "2k ok | 4k ok | *8k ok | >16k no". A question mark is an unproven
// lower-bound point. Empty when nothing was sized. * is the suggested window;
// > is the requested window when it does not fit or remains unproven.
func CompactWindows(t *FitTable) string {
	if t == nil || len(t.Points) == 0 {
		return ""
	}
	parts := make([]string, 0, len(t.Points))
	for _, p := range t.Points {
		label := compactCtx(p.Ctx)
		if label == "" {
			continue
		}
		mark := "?"
		switch p.Tier {
		case Compatible:
			mark = "ok"
		case Incompatible:
			mark = "no"
		}
		prefix := ""
		if p.Suggested {
			prefix = "*"
		} else if p.Requested {
			prefix = ">"
		}
		parts = append(parts, prefix+label+" "+mark)
	}
	return strings.Join(parts, " | ")
}

func compactCtx(n int) string {
	if n <= 0 {
		return ""
	}
	if n%1024 == 0 && n >= 1024 {
		return strconv.Itoa(n/1024) + "k"
	}
	return strconv.Itoa(n)
}

func compactCtxPair(measured, serving int, known bool) string {
	if measured <= 0 {
		return ""
	}
	m := compactCtx(measured)
	if !known || serving <= 0 || serving == measured {
		return m
	}
	return m + "/" + compactCtx(serving)
}
