package top

import (
	"fmt"
)

// renderResultTokenConditions projects the derived matched-condition receipt.
// Compact and full views share the target, the result and the analysis
// sentences. Case rows stay in the full view. Nothing here recomputes the claim.
func renderResultTokenConditions(w *lineWriter, run Run, compact bool) {
	if run.Analysis == nil || run.Analysis.TokenConditions == nil {
		return
	}
	view := run.Analysis.TokenConditions
	role := tokenAccountRole(view.Tone)
	w.line(Span{Text: "token conditions", Role: RoleHeader})
	w.line(Span{Text: "target     ", Role: RoleMuted}, Span{
		Text: fmt.Sprintf("%s %s tokens", view.TargetLabel, view.Semantics), Role: RoleDefault,
	})
	w.line(Span{Text: "result     ", Role: RoleMuted}, Span{Text: view.State, Role: role})
	if !compact {
		for _, item := range view.Cases {
			w.line(Span{Text: item.Condition + " ", Role: RoleMuted}, Span{Text: item.Outcome, Role: RoleDefault})
		}
	}
	w.line(Span{Text: "note       ", Role: RoleMuted}, Span{Text: view.Reason, Role: role})
	w.line(Span{Text: "next       ", Role: RoleMuted}, Span{Text: view.NextAction, Role: RoleDefault})
}
