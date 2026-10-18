package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/blisspixel/fitr/internal/desktop"
)

// desktopLabelWidth clears the longest status label, "Evidence freshness".
// A width of 20 is that label plus the two-space gutter, so the value was
// concatenated onto the word.
const desktopLabelWidth = 22

// WriteDesktopStatus prints a status document. It does not interpret fit,
// freshness, or requirements beyond the strings the projection already chose.
func WriteDesktopStatus(w io.Writer, status desktop.Status, mode string) {
	p, _ := inventoryStyle(Resolve(mode) == "rich")
	width := Width()
	fmt.Fprintf(w, "  %s\n", p.wrap(p.Head, "fitr / desktop"))
	Field(w, "  state", desktopLabelWidth, status.State, width)
	if status.Role != "" {
		Field(w, "  role", desktopLabelWidth, status.Role, width)
	}
	for _, name := range status.Roles {
		Field(w, "  choose", desktopLabelWidth, name, width)
	}
	for _, row := range status.Rows {
		Field(w, "  "+row.Label, desktopLabelWidth, row.Value+" ("+row.State+")", width)
	}
	for _, item := range status.Unresolved {
		Field(w, "  unresolved", desktopLabelWidth, item.ID+": "+item.State, width)
	}
	if status.Next.Text != "" {
		Field(w, "  next", desktopLabelWidth, status.Next.Text, width)
	}
	if status.Next.Reason != "" {
		Field(w, "  because", desktopLabelWidth, status.Next.Reason, width)
	}
	fmt.Fprintln(w)
	for _, limit := range status.Limits {
		Field(w, "  ", 2, limit, width)
	}
}

// WriteDesktopBenchmark prints whether a measurement was eligible and whether
// it was started. The argv is shown as text.
func WriteDesktopBenchmark(w io.Writer, plan desktop.BenchmarkPlan, mode string) {
	p, _ := inventoryStyle(Resolve(mode) == "rich")
	width := Width()
	fmt.Fprintf(w, "  %s\n", p.wrap(p.Head, "fitr / desktop benchmark"))
	state := "not started"
	if plan.Started {
		state = "started"
	}
	Field(w, "  state", 12, state, width)
	Field(w, "  because", 12, plan.Reason, width)
	if len(plan.Argv) > 0 {
		Field(w, "  command", 12, SingleLine(strings.Join(plan.Argv, " ")), width)
	}
}
