package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/blisspixel/fitr/internal/desktop"
)

// WriteDesktopStatus prints a status document. It does not interpret fit,
// freshness, or requirements beyond the strings the projection already chose.
func WriteDesktopStatus(w io.Writer, status desktop.Status, mode string) {
	p, _ := inventoryStyle(Resolve(mode) == "rich")
	width := Width()
	fmt.Fprintf(w, "  %s\n", p.wrap(p.Head, "fitr / desktop"))
	Field(w, "  state", 20, status.State, width)
	if status.Role != "" {
		Field(w, "  role", 20, status.Role, width)
	}
	for _, name := range status.Roles {
		Field(w, "  choose", 20, name, width)
	}
	for _, row := range status.Rows {
		Field(w, "  "+row.Label, 20, row.Value+" ("+row.State+")", width)
	}
	for _, item := range status.Unresolved {
		Field(w, "  unresolved", 20, item.ID+": "+item.State, width)
	}
	if status.Next.Text != "" {
		Field(w, "  next", 20, status.Next.Text, width)
	}
	if status.Next.Reason != "" {
		Field(w, "  because", 20, status.Next.Reason, width)
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
