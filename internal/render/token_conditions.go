package render

import (
	"fmt"
	"io"

	"github.com/blisspixel/fitr/internal/analysis"
)

// resultTokenConditions prints the derived matched-condition receipt. The
// words and the counts come from the analysis projection. This does not read
// a v1 family, position and seed as this cell, and it does not turn the
// counts into a rate.
func (d *textDisplay) resultTokenConditions(w io.Writer, report *analysis.Report, width int) {
	if report == nil || report.TokenConditions == nil {
		return
	}
	view := report.TokenConditions
	colour := tokenToneStyle(d, view.Tone)
	fmt.Fprintf(w, "\n%s\n", d.pal.wrap(d.pal.Head, "token conditions"))
	fmt.Fprintf(w, "  target    %s %s tokens\n", view.TargetLabel, view.Semantics)
	fmt.Fprintf(w, "  result    %s\n", d.pal.wrap(colour, view.State))
	fmt.Fprintf(w, "  cases     %d passed, %d failed, %d unresolved\n", view.Passed, view.Failed, view.Unresolved)
	for _, item := range view.Cases {
		fmt.Fprintf(w, "  %-22s %s\n", item.Condition, item.Outcome)
	}
	d.footer(w, view.Reason, width, 2, 4, colour)
	d.footer(w, view.NextAction, width, 2, 4, d.pal.Muted)
	d.footer(w, view.Uncertainty, width, 2, 4, d.pal.Muted)
}

func setHTMLTokenReceipts(d *htmlData, report *analysis.Report) {
	d.TokenAccount = htmlTokenAccountFrom(report.TokenAccount)
	d.TokenConditions = htmlTokenConditionsFrom(report.TokenConditions)
}

func htmlTokenConditionsFrom(view *analysis.TokenConditions) *htmlTokenAccount {
	if view == nil {
		return nil
	}
	rows := []htmlKV{
		{K: "target", V: view.TargetLabel + " " + view.Semantics + " tokens"},
		{K: "result", V: view.State},
		{K: "cases", V: fmt.Sprintf("%d passed, %d failed, %d unresolved", view.Passed, view.Failed, view.Unresolved)},
		{K: "uncertainty", V: view.Uncertainty},
	}
	for _, item := range view.Cases {
		rows = append(rows, htmlKV{K: item.Condition, V: item.Outcome})
	}
	return &htmlTokenAccount{Rows: rows, Reason: view.Reason, Next: view.NextAction}
}
