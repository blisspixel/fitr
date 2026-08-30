package top

import (
	"fmt"
	"strings"
	"testing"
	"time"
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

func TestRenderCoversEveryFullSizeSurface(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.History[0].Context = 16384
	snapshot.History[0].Driver = "driver"
	snapshot.History[0].Runtime = "runtime"
	snapshot.History[0].Config = "f16"
	snapshot.History[0].Profile = "default"
	snapshot.History[0].UseFor = "structured output"
	snapshot.History[0].PrefillMean = 120
	snapshot.History[0].TTFTMean = 0.5
	snapshot.History[0].MemoryGB = 6
	markFullMetricsPresent(&snapshot.History[0])
	snapshot.History[0].Warnings = []string{"thin evidence"}
	snapshot.History[0].NextCommand = "fitr apply alpha:8b"
	snapshot.History[0].Verdicts = []Verdict{
		{Need: "a", Label: "passes", State: "PASS", Why: "established"},
		{Need: "b", State: "FAIL", Why: "missed"},
		{Need: "c", State: "INCONCLUSIVE", Why: "thin"},
		{Need: "d", State: "SKIP", Why: "not measured"},
		{Need: "e", State: "N/A", Why: "unsupported"},
		{Need: "f", State: "BLKD", Why: "blocked"},
	}
	snapshot.Board[0].Runs[0] = snapshot.History[0]
	snapshot.Live = Live{
		Completed: true, Saved: true, Model: "alpha:8b", Phase: "done", Detail: "complete",
		StartedAt: snapshot.UpdatedAt.Add(-time.Minute), UpdatedAt: snapshot.UpdatedAt,
		CompletedSteps: 4, TotalSteps: 4, Placement: "GPU 100%",
		Decode: 12, Prefill: 120, TTFT: 0.5, MemoryGB: 6,
		DecodeSeries: []float64{10, 12, 14}, PrefillSeries: []float64{100, 120, 140},
		TTFTSeries: []float64{0.4, 0.5, 0.6}, Warnings: []string{"watch thermals"},
		Phases: []LivePhase{{Name: "speed", State: "completed", Completed: 3, Total: 3}},
	}

	for _, tc := range []struct {
		name string
		view View
		want string
	}{
		{"live", ViewLive, "COMPLETE"},
		{"result", ViewResult, "verdicts"},
		{"board", ViewBoard, "comparable group"},
		{"history", ViewHistory, "HISTORY"},
		{"inventory", ViewInventory, "installed models"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := NewState(snapshot)
			state.View, state.Width, state.Height = tc.view, 140, 40
			state.Selected[ViewResult] = "a"
			plain := Render(state, DefaultGlyphs(false)).Plain()
			if !strings.Contains(plain, tc.want) {
				t.Fatalf("%s render missing %q:\n%s", tc.name, tc.want, plain)
			}
		})
	}

	state := NewState(snapshot)
	state.Help, state.Width, state.Height = true, 100, 30
	if plain := Render(state, DefaultGlyphs(true)).Plain(); !strings.Contains(plain, "Ctrl+L") {
		t.Fatalf("help render missing keys:\n%s", plain)
	}
}

func markFullMetricsPresent(run *Run) {
	run.DecodePresent, run.PrefillPresent = true, true
	run.TTFTPresent, run.MemoryPresent = true, true
	run.ResidentContext = 32768
}

func TestResultRendersObservedZeroTTFTAndActualResidentContext(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.History[0].TTFTMean = 0
	snapshot.History[0].TTFTPresent = true
	snapshot.History[0].MemoryGB = 6
	snapshot.History[0].MemoryPresent = true
	snapshot.History[0].ResidentContext = 8192
	state := NewState(snapshot)
	state.View, state.Width, state.Height = ViewResult, 100, 30
	state.Selected[ViewResult] = snapshot.History[0].ID
	plain := Render(state, DefaultGlyphs(false)).Plain()
	if !strings.Contains(plain, "TTFT") || !strings.Contains(plain, "0.00 s") {
		t.Fatalf("observed zero TTFT was hidden:\n%s", plain)
	}
	if !strings.Contains(plain, "8192-token load probe") || strings.Contains(plain, "32K load probe") {
		t.Fatalf("resident context was mislabeled:\n%s", plain)
	}
}

func TestRenderCompatibleComparisonAndFooterModes(t *testing.T) {
	state := NewState(testSnapshot())
	state.View, state.Width, state.Height = ViewHistory, 100, 24
	state.Comparison = &Comparison{
		Compatible: true, Reason: "same conditions", BaselineModel: "a", SelectedModel: "b",
		DecodeA: 10, DecodeB: 12, DecodeDiff: 2,
		PrefillA: 100, PrefillB: 120, MemoryA: 5, MemoryB: 6,
	}
	plain := Render(state, DefaultGlyphs(true)).Plain()
	for _, want := range []string{"COMPARABLE", "prefill", "memory", "+2.00"} {
		if !strings.Contains(plain, want) {
			t.Errorf("comparison missing %q:\n%s", want, plain)
		}
	}

	state.Comparison = nil
	state.EditingFilter, state.Filter = true, "alpha"
	if plain = Render(state, DefaultGlyphs(true)).Plain(); !strings.Contains(plain, "Enter apply") {
		t.Fatalf("filter footer missing:\n%s", plain)
	}
	state.EditingFilter, state.Error = false, "reload failed"
	if plain = Render(state, DefaultGlyphs(true)).Plain(); !strings.Contains(plain, "error:reload failed") {
		t.Fatalf("error footer missing:\n%s", plain)
	}
}

func TestTinyRenderStatesAndEmptyFilters(t *testing.T) {
	for _, view := range []View{ViewResult, ViewBoard, ViewHistory, ViewInventory} {
		state := NewState(testSnapshot())
		state.View, state.Width, state.Height = view, 40, 5
		_ = Render(state, DefaultGlyphs(true)).Plain()
	}
	state := NewState(testSnapshot())
	state.Width, state.Height, state.Help = 40, 5, true
	if plain := Render(state, DefaultGlyphs(true)).Plain(); !strings.Contains(plain, "? close") {
		t.Fatalf("tiny help missing:\n%s", plain)
	}
	state = NewState(Snapshot{})
	state.View, state.Width, state.Height, state.Filter = ViewHistory, 80, 20, "missing"
	if plain := Render(state, DefaultGlyphs(true)).Plain(); !strings.Contains(plain, "match \"missing\"") {
		t.Fatalf("empty filter message missing:\n%s", plain)
	}
}
