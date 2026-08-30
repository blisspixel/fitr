package render

import (
	"fmt"
	"io"
	"math"
	"strings"
)

// The two formats are written together so the header can never drift from the
// rows it labels. Both compose to exactly DefaultWidth.
const (
	fitHeaderFmt = "  %-12s %7s %6s %5s %7s %5s %5s %6s %7s  %s"
	fitRowFmt    = "  %s %7s %6.1f %5.1f %7s %5.1f %5.1f %6s %7s  %s\n"
)

// ContextFit is the presentation model for advise's multi-window table.
type ContextFit struct {
	HaveGB       float64
	HaveSource   string
	CapacityOnly bool
	Note         string
	Points       []ContextFitPoint
}

type ContextFitPoint struct {
	Ctx        int
	Tier       string
	WeightsGB  float64
	KVGB       float64
	OtherGB    float64
	OtherKnown bool
	NeedGB     float64
	MarginGB   float64
	DecodeTPS  float64
	PrefillTPS float64
	Requested  bool
	Suggested  bool
	Note       string
}

// WriteContextFit prints weights / KV / derived other allocation / headroom at
// each ctx. State is in the tier column. Other stays "n/a" unless a runtime
// total or allocator projection exists at that exact window.
func WriteContextFit(w io.Writer, table ContextFit, mode string) {
	if len(table.Points) == 0 && table.Note == "" {
		return
	}
	resolved := Resolve(mode)
	if resolved == "none" || resolved == "json" {
		return
	}
	p, g, unicode := contextFitStyle(resolved == "rich")
	fmt.Fprintln(w)
	// The tier leads. Status belongs in the leftmost column so the eye scans one
	// vertical strip for "incompatible" instead of reading to the end of a row;
	// it was last, behind eight numeric columns, which also pushed the row to 90
	// characters against an 80-column terminal.
	marginLabel := "ROOM"
	if table.CapacityOnly {
		marginLabel = "DELTA"
	}
	fmt.Fprintf(w, "%s\n", p.wrap(p.Head, fmt.Sprintf(fitHeaderFmt,
		"FIT", "CTX", "WEIGHT", "KV", "OTHER", "NEED", marginLabel, "DECODE", "PREFILL", "OF HAVE")))
	maxNeed := contextFitMaxNeed(table)
	for _, pt := range table.Points {
		writeContextFitPoint(w, pt, maxNeed, p, g, unicode)
	}
	if table.HaveSource != "" {
		line := fmt.Sprintf("budget %.1f GB (%s); ROOM is derived against this budget",
			table.HaveGB, SingleLine(table.HaveSource))
		if table.CapacityOnly {
			line = fmt.Sprintf("addressable capacity %.1f GB (%s); DELTA is capacity minus projection, not usable room",
				table.HaveGB, SingleLine(table.HaveSource))
		}
		fmt.Fprintf(w, "  %s\n", p.wrap(p.Muted, line))
	}
	if table.Note != "" {
		fmt.Fprintf(w, "  %s\n", p.wrap(p.Muted, SingleLine(table.Note)))
	}
	fmt.Fprintf(w, "  %s\n", p.wrap(p.Muted, "* suggested   > requested   ? unproven   other n/a without matched total"))
	fmt.Fprintf(w, "  %s\n", p.wrap(p.Muted, "decode/prefill only from a saved run at that exact window"+g.Dot+"never invented"))
}

func contextFitStyle(rich bool) (palette, glyphs, bool) {
	p := palette{}
	g := glyphs{" | ", "-", "+/-", "..."}
	if !rich {
		return p, g, false
	}
	return pickPalette(!noColor()), pickGlyphs(), unicodeOK()
}

func contextFitMaxNeed(table ContextFit) float64 {
	maxNeed := 0.0
	for _, point := range table.Points {
		if point.NeedGB > maxNeed {
			maxNeed = point.NeedGB
		}
	}
	if table.HaveGB > maxNeed {
		maxNeed = table.HaveGB
	}
	return maxNeed
}

func writeContextFitPoint(w io.Writer, point ContextFitPoint, maxNeed float64,
	p palette, g glyphs, unicode bool) {
	mark := " "
	if point.Suggested {
		mark = "*"
	} else if point.Requested {
		mark = ">"
	}
	other := "n/a"
	if point.OtherKnown {
		other = fmt.Sprintf("%.1f", point.OtherGB)
	}
	decode, prefill := optionalRate(point.DecodeTPS), optionalRate(point.PrefillTPS)
	bar := valueBar(math.Max(point.NeedGB, 0), maxNeed, 6, unicode)
	fmt.Fprintf(w, fitRowFmt,
		p.wrap(fitTierColor(p, point.Tier), pad(point.Tier, 12, g.Ell)),
		fmt.Sprintf("%s%d", mark, point.Ctx),
		point.WeightsGB, point.KVGB, other, point.NeedGB, point.MarginGB,
		decode, prefill, p.wrap(p.Accent, bar))
	if point.Note != "" {
		fmt.Fprintf(w, "          %s\n", p.wrap(p.Muted, SingleLine(point.Note)))
	}
}

func optionalRate(value float64) string {
	if value <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f", value)
}

func fitTierColor(p palette, tier string) string {
	switch strings.ToLower(tier) {
	case "compatible":
		return p.Pass
	case "incompatible":
		return p.Fail
	default:
		return p.Muted
	}
}
