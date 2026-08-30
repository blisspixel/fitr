package advise

import (
	"sort"
	"strconv"
	"strings"
)

// FitTable is weights / KV / other resident allocation / headroom at several
// context points. Other resident is a derived remainder, not an independent
// compute-buffer measurement, and is n/a unless total allocation was observed
// at that exact ctx. Decode/prefill overlay only from saved runs at that ctx.
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
	Note       string  `json:"note,omitempty"`
}

var defaultFitCtx = []int{2048, 4096, 8192, 16384, 32768}

// ContextFit sizes the model at several windows. Hybrid recurrent models
// stay SKIP on the algebraic path; a measured resident or dummy allocation
// at one ctx is a single-point table, not a projection.
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
		kvB := perTok * float64(ctx)
		p := FitPoint{
			Ctx:       ctx,
			WeightsGB: round1(weightsB / GiB),
			KVGB:      round1(kvB / GiB),
			Requested: in.Ctx > 0 && ctx == in.Ctx,
		}
		otherB, known, note := otherResidentAt(in, ctx, weightsB, kvB)
		p.OtherKnown = known
		p.Note = note
		needB := weightsB + kvB
		if known {
			p.OtherGB = round1(otherB / GiB)
			needB += otherB
		} else if p.Note == "" {
			p.Note = "other resident n/a (total allocation not observed at this ctx)"
		}
		p.NeedGB = round1(needB / GiB)
		p.HeadroomGB = round1((haveB - needB) / GiB)
		if needB <= haveB {
			p.Tier = Compatible
		} else {
			p.Tier = Incompatible
		}
		attachTiming(&p, in.Timings)
		t.Points = append(t.Points, p)
	}
	markSuggested(t)
	if !anyOtherKnown(t.Points) {
		t.Note = "weights + KV only; other runtime allocation not observed until --load or --fit"
	}
	return t
}

func hybridFit(in Input, t *FitTable) *FitTable {
	haveB := in.HaveGB * GiB
	add := func(ctx int, allocB int64, note string) {
		if ctx <= 0 || allocB <= 0 {
			return
		}
		need := float64(allocB)
		p := FitPoint{
			Ctx:        ctx,
			WeightsGB:  round1(float64(in.WeightsB) / GiB),
			OtherKnown: true,
			OtherGB:    round1(need / GiB),
			NeedGB:     round1(need / GiB),
			HeadroomGB: round1((haveB - need) / GiB),
			Requested:  in.Ctx > 0 && ctx == in.Ctx,
			Note:       note,
		}
		if need <= haveB {
			p.Tier = Compatible
		} else {
			p.Tier = Incompatible
		}
		attachTiming(&p, in.Timings)
		t.Points = append(t.Points, p)
	}
	if in.ResidentB > 0 && in.ResidentCtx > 0 {
		add(in.ResidentCtx, in.ResidentB, "hybrid: measured resident (includes recurrent state)")
	}
	if in.FitB > 0 && in.Ctx > 0 && (in.ResidentCtx != in.Ctx || in.ResidentB == 0) {
		add(in.Ctx, in.FitB, "hybrid: dummy allocation (includes recurrent state)")
	}
	if len(t.Points) == 0 {
		t.Note = "hybrid recurrent architecture cannot be projected from weights plus KV; use --load or --fit"
		return t
	}
	markSuggested(t)
	return t
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

func otherResidentAt(in Input, ctx int, weightsB, kvB float64) (float64, bool, string) {
	if in.ResidentB > 0 && in.ResidentCtx == ctx {
		buf := float64(in.ResidentB) - weightsB - kvB
		if buf < 0 {
			return 0, false, "resident smaller than weights+KV estimate"
		}
		return buf, true, "other resident is allocation minus modeled weights and KV"
	}
	if in.FitB > 0 && in.Ctx == ctx {
		buf := float64(in.FitB) - weightsB - kvB
		if buf < 0 {
			return 0, false, "dummy allocation smaller than weights+KV estimate"
		}
		return buf, true, "other resident is dummy allocation minus modeled weights and KV"
	}
	return 0, false, ""
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
// "2k ok | 4k ok | *8k ok | >16k no". Empty when nothing was sized.
// * is the suggested window; > is the requested window when it does not fit.
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
		mark := "ok"
		if p.Tier != Compatible {
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
