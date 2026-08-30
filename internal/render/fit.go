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
	HaveGB float64
	Note   string
	Points []ContextFitPoint
}

type ContextFitPoint struct {
	Ctx        int
	Tier       string
	WeightsGB  float64
	KVGB       float64
	OtherGB    float64
	OtherKnown bool
	NeedGB     float64
	HeadroomGB float64
	DecodeTPS  float64
	PrefillTPS float64
	Requested  bool
	Suggested  bool
	Note       string
}

// WriteContextFit prints weights / KV / derived other resident / headroom at
// each ctx. State is in the tier column. Other stays "n/a" unless total
// allocation at that exact window was observed.
func WriteContextFit(w io.Writer, table ContextFit, mode string) {
	if len(table.Points) == 0 && table.Note == "" {
		return
	}
	resolved := Resolve(mode)
	if resolved == "none" || resolved == "json" {
		return
	}
	rich := resolved == "rich"
	p := palette{}
	g := glyphs{" | ", "-", "+/-", "..."}
	unicode := false
	if rich {
		p = pickPalette(!noColor())
		g = pickGlyphs()
		unicode = unicodeOK()
	}
	fmt.Fprintln(w)
	// The tier leads. Status belongs in the leftmost column so the eye scans one
	// vertical strip for "incompatible" instead of reading to the end of a row;
	// it was last, behind eight numeric columns, which also pushed the row to 90
	// characters against an 80-column terminal.
	fmt.Fprintf(w, "%s\n", p.wrap(p.Head, fmt.Sprintf(fitHeaderFmt,
		"FIT", "CTX", "WEIGHT", "KV", "OTHER", "NEED", "ROOM", "DECODE", "PREFILL", "OF HAVE")))
	maxNeed := 0.0
	for _, pt := range table.Points {
		if pt.NeedGB > maxNeed {
			maxNeed = pt.NeedGB
		}
	}
	if table.HaveGB > maxNeed {
		maxNeed = table.HaveGB
	}
	for _, pt := range table.Points {
		mark := " "
		if pt.Suggested {
			mark = "*"
		}
		if pt.Requested {
			mark = ">"
			if pt.Suggested {
				mark = "*"
			}
		}
		other := "n/a"
		if pt.OtherKnown {
			other = fmt.Sprintf("%.1f", pt.OtherGB)
		}
		dec, pre := "-", "-"
		if pt.DecodeTPS > 0 {
			dec = fmt.Sprintf("%.1f", pt.DecodeTPS)
		}
		if pt.PrefillTPS > 0 {
			pre = fmt.Sprintf("%.1f", pt.PrefillTPS)
		}
		bar := valueBar(math.Max(pt.NeedGB, 0), maxNeed, 6, unicode)
		fmt.Fprintf(w, fitRowFmt,
			p.wrap(fitTierColor(p, pt.Tier), pad(pt.Tier, 12, g.Ell)),
			fmt.Sprintf("%s%d", mark, pt.Ctx),
			pt.WeightsGB, pt.KVGB, other, pt.NeedGB, pt.HeadroomGB,
			dec, pre, p.wrap(p.Accent, bar))
		if pt.Note != "" && pt.OtherKnown {
			fmt.Fprintf(w, "          %s\n", p.wrap(p.Muted, SingleLine(pt.Note)))
		}
	}
	if table.Note != "" {
		fmt.Fprintf(w, "  %s\n", p.wrap(p.Muted, SingleLine(table.Note)))
	}
	fmt.Fprintf(w, "  %s\n", p.wrap(p.Muted, "* suggested   > requested   other n/a until allocation is observed"))
	fmt.Fprintf(w, "  %s\n", p.wrap(p.Muted, "decode/prefill only from a saved run at that exact window"+g.Dot+"never invented"))
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
