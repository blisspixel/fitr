package top

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResultSignatureFollowsTheSelectedRun(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Signatures = []Signature{{
		Word: "fitted", Body: "coding: alpha:8b, measured 2026-10-17, review by 2026-11-16", RunID: "a",
	}, {
		Word: "refit", Body: "writing: beta:7b, measured 2026-08-01, review by 2026-09-01", RunID: "b",
	}}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "fitted") || strings.Contains(string(raw), "refit") || strings.Contains(string(raw), "alpha:8b, measured") {
		t.Fatalf("presentation JSON gained the line: %s", raw)
	}

	state := NewState(snapshot)
	state.View, state.Width, state.Height = ViewResult, 80, 40
	state.Selected[ViewResult] = "a"
	canvas := Render(state, DefaultGlyphs(true))
	plain := canvas.Plain()
	if !strings.Contains(plain, "fitted") || !strings.Contains(plain, "coding: alpha:8b") || strings.Contains(plain, "refit") || strings.Contains(plain, "writing:") {
		t.Fatalf("selected strip:\n%s", plain)
	}
	if signatureSpanRole(t, canvas, "fitted") != RolePass || signatureSpanRole(t, canvas, "2026-10-17") != RoleMuted {
		t.Fatalf("fitted ink:\n%s", plain)
	}

	state.Width = 60
	narrow := Render(state, DefaultGlyphs(true)).Plain()
	if !strings.Contains(narrow, "2026-10-17") || !strings.Contains(narrow, "2026-11-16") {
		t.Fatalf("narrow strip clipped a date:\n%s", narrow)
	}

	state.Width = 80
	state.Selected[ViewResult] = "b"
	stale := Render(state, DefaultGlyphs(true))
	if strings.Contains(stale.Plain(), "fitted") || signatureSpanRole(t, stale, "refit") != RoleWarning {
		t.Fatalf("stale strip:\n%s", stale.Plain())
	}
}

func signatureSpanRole(t *testing.T, canvas Canvas, text string) Role {
	t.Helper()
	for _, row := range canvas.Rows {
		for _, span := range row {
			if strings.Contains(span.Text, text) {
				return span.Role
			}
		}
	}
	t.Fatalf("span %q missing", text)
	return RoleDefault
}
