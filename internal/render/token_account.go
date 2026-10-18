package render

import (
	"fmt"
	"io"

	"github.com/blisspixel/fitr/internal/analysis"
)

// resultTokenAccount prints the derived token receipt. The words and the
// counts come from the analysis projection. This does not read a byte prefix
// as a token target and does not turn the counts into a rate.
func (d *textDisplay) resultTokenAccount(w io.Writer, report *analysis.Report, width int) {
	if report == nil || report.TokenAccount == nil {
		return
	}
	view := report.TokenAccount
	colour := tokenToneStyle(d, view.Tone)
	fmt.Fprintf(w, "\n%s\n", d.pal.wrap(d.pal.Head, "token account"))
	fmt.Fprintf(w, "  target    %s %s tokens\n", view.TargetLabel, view.Semantics)
	fmt.Fprintf(w, "  payload   %s\n", view.PayloadLabel)
	fmt.Fprintf(w, "  overhead  %s\n", view.OverheadLabel)
	fmt.Fprintf(w, "  reserve   %s\n", view.ReserveLabel)
	fmt.Fprintf(w, "  window    %s\n", view.WindowLabel)
	fmt.Fprintf(w, "  result    %s\n", d.pal.wrap(colour, view.State))
	fmt.Fprintf(w, "  cases     %d passed, %d failed, %d unresolved, %d seeds\n",
		view.Passed, view.Failed, view.Unresolved, view.Seeds)
	for _, item := range view.Cases {
		fmt.Fprintf(w, "  %-22s %s\n", item.Family+" "+item.Position, item.Outcome)
	}
	d.footer(w, view.Reason, width, 2, 4, colour)
	d.footer(w, view.FreeMemory, width, 2, 4, d.pal.Muted)
	d.footer(w, view.NextAction, width, 2, 4, d.pal.Muted)
	d.footer(w, view.Uncertainty, width, 2, 4, d.pal.Muted)
}

func htmlTokenAccountFrom(view *analysis.TokenAccount) *htmlTokenAccount {
	if view == nil {
		return nil
	}
	rows := []htmlKV{
		{K: "target", V: view.TargetLabel + " " + view.Semantics + " tokens"},
		{K: "payload", V: view.PayloadLabel},
		{K: "overhead", V: view.OverheadLabel},
		{K: "reserve", V: view.ReserveLabel},
		{K: "window", V: view.WindowLabel},
		{K: "result", V: view.State},
		{K: "cases", V: fmt.Sprintf("%d passed, %d failed, %d unresolved, %d seeds",
			view.Passed, view.Failed, view.Unresolved, view.Seeds)},
		{K: "uncertainty", V: view.Uncertainty},
	}
	for _, item := range view.Cases {
		rows = append(rows, htmlKV{K: item.Family + " " + item.Position, V: item.Outcome})
	}
	return &htmlTokenAccount{Rows: rows, Reason: view.Reason, Free: view.FreeMemory, Next: view.NextAction}
}

func tokenToneStyle(d *textDisplay, tone string) string {
	switch tone {
	case analysis.TokenToneAccepted:
		return d.pal.Pass
	case analysis.TokenToneGap:
		return d.pal.Warn
	default:
		return d.pal.Head
	}
}
