package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/blisspixel/fitr/internal/analysis"
)

func WriteSourceScreen(w io.Writer, report analysis.SourceScreen) {
	width := Width()
	fmt.Fprintln(w)
	Field(w, "  screen", 15, strings.ToUpper(report.State)+" under the explicit screening policy", width)
	for _, gate := range report.Gates {
		Field(w, "  "+gate.Name, 15, gate.State+": "+gate.Reason, width)
	}
	Field(w, "  next", 15, report.Next, width)
	Field(w, "  limits", 15, "Screening does not establish runtime support, complete dependencies, license permission, resident memory or model quality.", width)
}
