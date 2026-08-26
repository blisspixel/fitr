package top

import (
	"fmt"
	"strings"
	"testing"
)

func TestRenderResponsiveAndClipped(t *testing.T) {
	for _, size := range [][2]int{{1, 1}, {40, 12}, {60, 20}, {80, 24}, {120, 40}} {
		state := NewState(testSnapshot())
		state.View = ViewBoard
		state.Width, state.Height = size[0], size[1]
		canvas := Render(state, DefaultGlyphs(false))
		if len(canvas.Rows) != size[1] {
			t.Fatalf("%dx%d produced %d rows", size[0], size[1], len(canvas.Rows))
		}
		for y, row := range canvas.Rows {
			width := 0
			for _, span := range row {
				width += displayWidth(span.Text)
			}
			if width > size[0] {
				t.Fatalf("%dx%d row %d width %d: %q", size[0], size[1], y, width, canvas.Plain())
			}
		}
	}
}

func TestRenderInventoryListsStateInText(t *testing.T) {
	state := NewState(testSnapshot())
	state.View = ViewInventory
	state.Width, state.Height = 100, 24
	got := Render(state, DefaultGlyphs(false)).Plain()
	for _, want := range []string{"INVENTORY", "measured", "unproven", "not a ranking", "16k/8k", "2k ok", "fitr apply"} {
		if !strings.Contains(got, want) {
			t.Fatalf("inventory canvas missing %q:\n%s", want, got)
		}
	}
}

func TestASCIIHasNoUnicodeGraphGlyphs(t *testing.T) {
	state := NewState(testSnapshot())
	state.View = ViewBoard
	got := Render(state, DefaultGlyphs(true)).Plain()
	for _, forbidden := range []string{"█", "░", "─", "│", "›", "…"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("ASCII canvas contains %q:\n%s", forbidden, got)
		}
	}
	if !strings.Contains(got, "[#") {
		t.Fatalf("ASCII data bar missing:\n%s", got)
	}
}

func TestCanvasSanitizesTerminalControls(t *testing.T) {
	canvas := NewCanvas(80, 4)
	canvas.SetLine(0, Span{Text: "safe\x1b[31mRED\x1b[0m"})
	canvas.SetLine(1, Span{Text: "before\x1b]0;spoofed title\aafter"})
	canvas.SetLine(2, Span{Text: "line\nnext\tfield\x00"})
	canvas.SetLine(3, Span{Text: "left\u202eright before\x1bPforged payload\x1b\\after"})
	got := canvas.Plain()
	if strings.ContainsAny(got, "\x1b\x07\x00\u202e") || strings.Contains(got, "spoofed title") ||
		strings.Contains(got, "forged payload") {
		t.Fatalf("control sequence survived: %q", got)
	}
	for _, want := range []string{"safeRED", "beforeafter", "line next field", "leftright beforeafter"} {
		if !strings.Contains(got, want) {
			t.Fatalf("sanitized canvas missing %q: %q", want, got)
		}
	}
}

func TestSelectedRowHasSemanticRole(t *testing.T) {
	state := NewState(testSnapshot())
	state.View = ViewBoard
	canvas := Render(state, DefaultGlyphs(false))
	found := false
	for _, row := range canvas.Rows {
		for _, span := range row {
			if span.Role == RoleSelected {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("board has no semantic selected role")
	}
}

func TestBoardUsesSemanticColorForDataNotOnlyHeaders(t *testing.T) {
	state := NewState(testSnapshot())
	state.View, state.Width, state.Height = ViewBoard, 120, 24
	canvas := Render(state, DefaultGlyphs(false))
	foundAccent, foundPass := false, false
	for _, row := range canvas.Rows {
		for _, span := range row {
			foundAccent = foundAccent || span.Role == RoleAccent
			foundPass = foundPass || span.Role == RolePass
		}
	}
	if !foundAccent || !foundPass {
		t.Fatalf("board data roles accent=%v pass=%v", foundAccent, foundPass)
	}
}

func TestNoColorThemeRetainsNonColorState(t *testing.T) {
	theme := DefaultTheme(true)
	for role, style := range theme.Styles {
		if style.Foreground != ColorDefault {
			t.Fatalf("role %d retained color %d", role, style.Foreground)
		}
	}
	if !theme.Styles[RoleSelected].Reverse || !theme.Styles[RoleFail].Bold {
		t.Fatal("no-color theme lost redundant selection or failure emphasis")
	}
}

// The monitor had its own copy of the sparkline, with the two faults the CLI
// renderer had: it drew the unreadable ASCII ramp when Unicode was off, and it
// normalised to the series min and max with no floor, so a 0.4% wobble and a
// tenfold swing rendered identically.
func TestSparklineOnlyDrawsWhatItCanSupport(t *testing.T) {
	uni, ascii := DefaultGlyphs(false), DefaultGlyphs(true)
	cases := []struct {
		name   string
		values []float64
		limit  int
		glyphs Glyphs
		want   string
	}{
		{"no ASCII ramp is readable", []float64{0, 2, 4}, 7, ascii, "-"},
		{"one observation has no shape", []float64{4}, 7, uni, "-"},
		{"one cell cannot depict a series", []float64{1, 2, 3}, 1, uni, "-"},
		{"identical values are flat", []float64{4, 4, 4}, 7, uni, "flat"},
		{"spread below the floor is flat", []float64{100, 100.5, 100.2}, 7, uni, "flat"},
		{"real spread draws", []float64{0, 2, 4}, 7, uni, "▁▅█"},
	}
	for _, tc := range cases {
		if got := sparkline(tc.values, tc.limit, tc.glyphs); got != tc.want {
			t.Errorf("%s: sparkline = %q, want %q", tc.name, got, tc.want)
		}
	}
	// Down-sampling must not divide by zero or index out of range at any limit.
	series := []float64{1, 9, 2, 8, 3, 7, 4, 6, 5, 10, 0, 11}
	for limit := 1; limit <= 20; limit++ {
		got := sparkline(series, limit, uni)
		if n := len([]rune(got)); limit >= 2 && n > limit {
			t.Errorf("limit %d produced %d cells: %q", limit, n, got)
		}
	}
}

func FuzzRenderBounds(f *testing.F) {
	f.Add(80, 24, "alpha")
	f.Add(1, 1, "\x1b]0;title\a")
	f.Fuzz(func(t *testing.T, width, height int, model string) {
		if width < 1 || width > 300 || height < 1 || height > 100 {
			t.Skip()
		}
		snapshot := testSnapshot()
		snapshot.Board[0].Runs[0].Model = model
		state := NewState(snapshot)
		state.View, state.Width, state.Height = ViewBoard, width, height
		canvas := Render(state, DefaultGlyphs(width%2 == 0))
		if len(canvas.Rows) != height {
			t.Fatalf("rows=%d want %d", len(canvas.Rows), height)
		}
		for _, row := range canvas.Rows {
			lineWidth := 0
			for _, span := range row {
				if strings.ContainsAny(span.Text, "\x1b\x00\x07") {
					t.Fatalf("unsafe span %q", span.Text)
				}
				lineWidth += displayWidth(span.Text)
			}
			if lineWidth > width {
				t.Fatalf("line width %d exceeds %d", lineWidth, width)
			}
		}
	})
}

func BenchmarkRenderBoard120x40(b *testing.B) {
	state := NewState(testSnapshot())
	state.View, state.Width, state.Height = ViewBoard, 120, 40
	glyphs := DefaultGlyphs(false)
	b.ReportAllocs()
	for b.Loop() {
		_ = Render(state, glyphs)
	}
}

func TestTinyRendererKeepsStateAndExitAffordance(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Live = Live{Active: true, Model: "model", Phase: "checks"}
	state := NewState(snapshot)
	state.Width, state.Height = 40, 5
	plain := Render(state, DefaultGlyphs(true)).Plain()
	for _, want := range []string{"LIVE", "running: checks", "? help", "q quit"} {
		if !strings.Contains(plain, want) {
			t.Errorf("tiny render missing %q:\n%s", want, plain)
		}
	}
}

func TestLiveTrustWarningsRenderBeforePhaseHistory(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Live = Live{Active: true, Model: "model", Phase: "checks", Warnings: []string{"integrity warning"}}
	for i := range 20 {
		snapshot.Live.Phases = append(snapshot.Live.Phases, LivePhase{Name: fmt.Sprintf("phase-%d", i), State: "completed"})
	}
	state := NewState(snapshot)
	state.Width, state.Height = 80, 18
	plain := Render(state, DefaultGlyphs(true)).Plain()
	warningAt, phasesAt := strings.Index(plain, "integrity warning"), strings.Index(plain, "phases")
	if warningAt < 0 || phasesAt < 0 || warningAt > phasesAt {
		t.Fatalf("warning must precede phase history:\n%s", plain)
	}
}

func TestIncompatibleComparisonShowsNoDelta(t *testing.T) {
	comparison := &Comparison{BaselineModel: "a", SelectedModel: "b", Reason: "request context differs"}
	state := NewState(testSnapshot())
	state.View, state.Comparison, state.Width, state.Height = ViewHistory, comparison, 80, 24
	plain := Render(state, DefaultGlyphs(true)).Plain()
	if !strings.Contains(plain, "NOT COMPARABLE") || !strings.Contains(plain, comparison.Reason) || strings.Contains(plain, "delta") {
		t.Fatalf("incompatible comparison rendered incorrectly:\n%s", plain)
	}
}
