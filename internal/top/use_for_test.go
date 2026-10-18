package top

import (
	"strings"
	"testing"
)

func TestResultUseForIsNotCyan(t *testing.T) {
	cases := []struct {
		useFor string
		level  string
		want   Role
	}{
		{useFor: "structured output", level: "full", want: RolePass},
		{useFor: "no need passed", level: "quick", want: RoleWarning},
		{useFor: "structured output", level: "checks", want: RoleDefault},
	}
	for _, tc := range cases {
		t.Run(tc.level+"/"+tc.useFor, func(t *testing.T) {
			role := renderedUseForRole(t, tc.useFor, tc.level)
			if role == RoleAccent || role != tc.want {
				t.Fatalf("use for role = %d, want %d", role, tc.want)
			}
		})
	}
}

func renderedUseForRole(t *testing.T, useFor, level string) Role {
	t.Helper()
	snapshot := testSnapshot()
	snapshot.History[0].UseFor = useFor
	snapshot.History[0].Level = level
	state := NewState(snapshot)
	state.View, state.Width, state.Height = ViewResult, 100, 30
	canvas := Render(state, DefaultGlyphs(true))
	for _, row := range canvas.Rows {
		for _, span := range row {
			if strings.Contains(span.Text, useFor) {
				return span.Role
			}
		}
	}
	t.Fatalf("result view omitted %q", useFor)
	return RoleAccent
}
