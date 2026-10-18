package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/blisspixel/fitr/internal/analysis"
)

const (
	// UseForPass is accepted work. UseForWarn is a gap headline.
	// UseForPlain is calibration evidence and any line with nothing to qualify.
	// Cyan stays a focus color and is not one of these tones.
	UseForPass  = "pass"
	UseForWarn  = "warn"
	UseForPlain = "plain"
)

// UseForTone chooses the ink for a use-for line from what the line says.
// A calibration card stays in the default ink. A gap headline is amber.
// A line that names accepted work is green.
func UseForTone(text string, calibration bool) string {
	if calibration || strings.Contains(text, "calibration evidence only") {
		return UseForPlain
	}
	switch {
	case strings.Contains(text, "no need passed"),
		strings.Contains(text, "INCONCLUSIVE"),
		strings.Contains(text, "not a verdict"),
		strings.Contains(text, "none of the measured"):
		return UseForWarn
	case strings.TrimSpace(text) == "":
		return UseForPlain
	default:
		return UseForPass
	}
}

func (d *textDisplay) useForStyle(text string, calibration bool) string {
	switch UseForTone(text, calibration) {
	case UseForPass:
		return d.pal.Pass
	case UseForWarn:
		return d.pal.Warn
	default:
		return ""
	}
}

// MarkRunCard collapses the evidence-limit list on the card printed at the
// end of a run. Other displays keep every limit.
func MarkRunCard(d Display) {
	text, ok := d.(*textDisplay)
	if ok {
		text.collapseLimits = true
	}
}

func (d *textDisplay) writeLimits(w io.Writer, report *analysis.Report, model string, width int) {
	if len(report.Gaps) == 0 {
		return
	}
	if !d.collapseLimits {
		for _, gap := range report.Gaps {
			d.headerField(w, "limit", analysis.GapLabel(gap.Code)+": "+gap.Message, width, d.pal.Muted)
		}
		return
	}
	d.headerField(w, "limits", fmt.Sprintf("%d evidence limits; fitr view %s lists them",
		len(report.Gaps), analysis.FormatArgv([]string{model}, "")), width, d.pal.Muted)
}
