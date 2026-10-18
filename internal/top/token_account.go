package top

import (
	"fmt"

	"github.com/blisspixel/fitr/internal/analysis"
)

// renderResultTokenAccount projects the derived token receipt. Compact and
// full views share the target, the result and the analysis sentences. Case
// rows stay in the full view. Nothing here recomputes the claim.
func renderResultTokenAccount(w *lineWriter, run Run, compact bool) {
	if run.Analysis == nil || run.Analysis.TokenAccount == nil {
		return
	}
	view := run.Analysis.TokenAccount
	role := tokenAccountRole(view.Tone)
	w.line(Span{Text: "token account", Role: RoleHeader})
	w.line(Span{Text: "target     ", Role: RoleMuted}, Span{
		Text: fmt.Sprintf("%s %s tokens", view.TargetLabel, view.Semantics), Role: RoleDefault,
	})
	w.line(Span{Text: "result     ", Role: RoleMuted}, Span{Text: view.State, Role: role})
	if !compact {
		w.line(Span{Text: "payload    ", Role: RoleMuted}, Span{Text: view.PayloadLabel, Role: RoleDefault})
		w.line(Span{Text: "window     ", Role: RoleMuted}, Span{Text: view.WindowLabel, Role: RoleDefault})
		for _, item := range view.Cases {
			w.line(Span{Text: item.Family + " " + item.Position + " ", Role: RoleMuted}, Span{Text: item.Outcome, Role: RoleDefault})
		}
	}
	w.line(Span{Text: "note       ", Role: RoleMuted}, Span{Text: view.Reason, Role: role})
	w.line(Span{Text: "next       ", Role: RoleMuted}, Span{Text: view.NextAction, Role: RoleDefault})
}

func tokenAccountRole(tone string) Role {
	switch tone {
	case analysis.TokenToneAccepted:
		return RolePass
	case analysis.TokenToneGap:
		return RoleWarning
	default:
		return RoleDefault
	}
}
