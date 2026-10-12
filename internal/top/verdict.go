package top

import "fmt"

func renderResultVerdicts(w *lineWriter, run Run, width int) {
	if len(run.Verdicts) == 0 {
		return
	}
	w.line(Span{Text: "verdicts", Role: RoleHeader})
	for _, verdict := range run.Verdicts {
		label := verdict.Label
		if label == "" {
			label = verdict.Need
		}
		spans := []Span{
			{Text: fmt.Sprintf("[%-4s] ", verdict.State), Role: verdictRole(verdict.State)},
			{Text: label, Role: RoleDefault},
		}
		if width >= 100 {
			spans = append(spans, Span{Text: "  " + verdict.Why, Role: RoleMuted})
		}
		if !w.line(spans...) {
			return
		}
		// A compact view still has to explain a failed quality floor. Put the
		// shared deciding summary first on its own clipped line so the label
		// cannot consume all the room for the reason.
		if width < 100 && verdict.State == "FAIL" && verdict.Why != "" {
			if !w.line(Span{Text: "       " + verdict.Why, Role: RoleMuted}) {
				return
			}
		}
	}
}
