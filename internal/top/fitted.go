package top

import (
	"strings"

	"github.com/blisspixel/fitr/internal/render"
)

const signatureGutter = 11

func renderRunSignature(w *lineWriter, snapshot Snapshot, run Run) {
	if w == nil || w.canvas == nil {
		return
	}
	width := max(w.canvas.Width-signatureGutter, 1)
	for _, sig := range snapshot.Signatures {
		wordRole, ok := signatureWordRole(sig.Word)
		if !ok || sig.RunID != run.ID {
			continue
		}
		lines := render.WrapLines([]string{sig.Body}, width)
		for i, line := range lines {
			lead, leadRole := strings.Repeat(" ", signatureGutter), RoleDefault
			if i == 0 {
				lead, leadRole = signatureLead(sig.Word), wordRole
			}
			spans := append([]Span{{Text: lead, Role: leadRole}}, signatureBodySpans(line)...)
			if !w.line(spans...) {
				return
			}
		}
	}
}

func signatureWordRole(word string) (Role, bool) {
	switch word {
	case "fitted":
		return RolePass, true
	case "refit":
		return RoleWarning, true
	default:
		return RoleDefault, false
	}
}

func signatureLead(word string) string {
	if len([]rune(word)) >= signatureGutter {
		return word
	}
	return word + strings.Repeat(" ", signatureGutter-len([]rune(word)))
}

func signatureBodySpans(line string) []Span {
	parts := render.SignatureParts(line)
	spans := make([]Span, 0, len(parts))
	for _, part := range parts {
		role := RoleDefault
		if part.Date {
			role = RoleMuted
		}
		spans = append(spans, Span{Text: part.Text, Role: role})
	}
	return spans
}
