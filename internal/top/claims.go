package top

import "github.com/blisspixel/fitr/internal/render"

func renderWorkloadClaims(w *lineWriter, claims []string) {
	if len(claims) == 0 || w == nil || w.canvas == nil {
		return
	}
	for _, part := range render.WrapLines(claims, w.canvas.Width) {
		if !w.line(Span{Text: part, Role: RoleWarning}) {
			return
		}
	}
}
