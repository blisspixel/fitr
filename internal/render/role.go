package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/blisspixel/fitr/internal/role"
)

func WriteRoleLibraries(w io.Writer, libraries []role.Library, mode string) {
	p, _ := inventoryStyle(Resolve(mode) == "rich")
	width := Width()
	fmt.Fprintf(w, "  %s\n", p.wrap(p.Head, "fitr / roles"))
	fmt.Fprintf(w, "  %s\n", p.wrap(p.Muted, strings.Repeat("-", width-4)))
	for _, library := range libraries {
		fmt.Fprintln(w)
		Field(w, "  ", 2, library.Name, width)
		Field(w, "  evidence", 13, fmt.Sprintf("%d attached candidates | %d role revisions", len(library.Candidates), len(library.Revisions)), width)
		Field(w, "  next", 13, "fitr role review "+library.Name, width)
	}
	if len(libraries) == 0 {
		Field(w, "  ", 2, "Define what good enough means for a job.", width)
		Field(w, "  next", 13, "fitr role init coding --quality user_tasks --memory-gb 22", width)
	}
	fmt.Fprintln(w)
	Field(w, "  ", 2, "Quality and resource floors come before preference weights.", width)
}

func WriteRoleReview(w io.Writer, report role.ReviewReport, mode string) {
	p, _ := inventoryStyle(Resolve(mode) == "rich")
	width := Width()
	fmt.Fprintf(w, "  %s\n", p.wrap(p.Head, "fitr / role review"))
	Field(w, "  ", 2, report.Role, width)
	style := p.Warn
	if report.State == "no-qualified-candidate" {
		style = p.Fail
	}
	fmt.Fprintf(w, "  %s\n", p.wrap(style, strings.ToUpper(SingleLine(report.State))))
	fmt.Fprintf(w, "  %s\n", p.wrap(p.Muted, strings.Repeat("-", width-4)))
	for index, candidate := range report.Candidates {
		fmt.Fprintln(w)
		Field(w, "  ", 2, fmt.Sprintf("%02d  %s", index+1, candidate.Model), width)
		stateStyle := p.Warn
		switch candidate.State {
		case "eligible":
			stateStyle = p.Pass
		case "ineligible":
			stateStyle = p.Fail
		}
		for _, line := range wrap(SingleLine(strings.ToUpper(candidate.State)), width-14) {
			fmt.Fprintf(w, "  state       %s\n", p.wrap(stateStyle, line))
		}
		if candidate.Preference != nil {
			Field(w, "  preference", 14, fmt.Sprintf("%.3f | bounds %.3f to %.3f", candidate.Preference.Estimate, candidate.Preference.Low, candidate.Preference.High), width)
		}
		for _, reason := range candidate.Reasons {
			Field(w, "  gap", 14, reason, width)
		}
		if candidate.Evaluation != nil {
			for _, requirement := range candidate.Evaluation.Requirements {
				Field(w, "  ", 14, requirement.ID+": "+string(requirement.State), width)
			}
		}
		if report.State == "exploration-lead" && report.Lead != "" && candidate.State == "eligible" && candidate.ID == report.Lead {
			Field(w, "  lead", 14, "Survives metric bounds and simultaneous +/-20% weight changes. Fresh confirmation still required.", width)
		}
	}
	for _, gap := range report.Gaps {
		Field(w, "  gap", 14, gap, width)
	}
	fmt.Fprintln(w)
	Field(w, "  next", 14, report.Next, width)
	fmt.Fprintln(w)
	Field(w, "  ", 2, "Battery screening only. Preference bounds are not a joint confidence interval. No automatic adoption.", width)
}
