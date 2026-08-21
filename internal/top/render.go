package top

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// Render converts state into a clipped, renderer-neutral canvas.
func Render(state State, glyphs Glyphs) Canvas {
	canvas := NewCanvas(state.Width, state.Height)
	if state.Width < 56 || state.Height < 14 {
		renderTiny(&canvas, state)
		return canvas
	}
	renderHeader(&canvas, state, glyphs)
	if state.Help {
		renderHelp(&canvas, state, glyphs)
	} else if state.Comparison != nil {
		renderComparison(&canvas, state)
	} else {
		switch state.View {
		case ViewLive:
			renderLive(&canvas, state, glyphs)
		case ViewResult:
			renderResult(&canvas, state, glyphs)
		case ViewBoard:
			renderBoard(&canvas, state, glyphs)
		case ViewHistory:
			renderHistory(&canvas, state, glyphs)
		}
	}
	renderFooter(&canvas, state)
	if state.ConfirmQuit {
		y := min(max(state.Height/2, 2), state.Height-2)
		canvas.SetLine(y, Span{Text: " Quit and cancel the active run? [y] yes  [n] no ", Role: RoleWarning})
	}
	return canvas
}

func renderTiny(canvas *Canvas, state State) {
	canvas.SetLine(0, Span{Text: "fitr top | " + strings.ToUpper(state.View.String()), Role: RoleHeader})
	if canvas.Height == 1 {
		return
	}
	if state.Help {
		canvas.SetLine(1, Span{Text: "1-4 views | j/k move | ? close | q quit", Role: RoleDefault})
	} else {
		switch state.View {
		case ViewLive:
			live := state.Snapshot.Live
			status := "idle"
			if live.Active {
				status = "running: " + live.Phase
			} else if live.Cancelled {
				status = "cancelled"
			} else if live.Error != "" {
				status = "error: " + live.Error
			} else if live.Completed {
				status = "complete; Enter opens Result"
			}
			canvas.SetLine(1, Span{Text: status + " | " + live.Model, Role: RoleDefault})
		case ViewResult:
			if run, ok := FindRun(state.Snapshot, state.Selected[ViewResult]); ok {
				canvas.SetLine(1, Span{Text: run.Model + fmt.Sprintf(" | %.2f tok/s", run.DecodeMean), Role: RoleDefault})
			} else {
				canvas.SetLine(1, Span{Text: "no saved result", Role: RoleMuted})
			}
		case ViewBoard:
			canvas.SetLine(1, Span{Text: fmt.Sprintf("%d comparable group(s)", len(VisibleBoard(state))), Role: RoleDefault})
		case ViewHistory:
			canvas.SetLine(1, Span{Text: fmt.Sprintf("%d saved run(s)", len(VisibleHistory(state))), Role: RoleDefault})
		}
	}
	if canvas.Height >= 3 && state.Error != "" {
		canvas.SetLine(2, Span{Text: "error: " + state.Error, Role: RoleFail})
	}
	canvas.SetLine(canvas.Height-1, Span{Text: "1-4 views | ? help | q quit", Role: RoleMuted})
}

func renderHeader(canvas *Canvas, state State, glyphs Glyphs) {
	if canvas.Height < 2 {
		canvas.SetLine(0, Span{Text: "fitr top", Role: RoleHeader})
		return
	}
	spans := []Span{{Text: " fitr top ", Role: RoleHeader}}
	for view := ViewLive; view < viewCount; view++ {
		label := fmt.Sprintf(" %d:%s ", int(view)+1, view.String())
		role := RoleMuted
		if state.View == view {
			role = RoleSelected
		}
		spans = append(spans, Span{Text: label, Role: role})
	}
	if state.Paused {
		spans = append(spans, Span{Text: " VIEW PAUSED, MEASUREMENT CONTINUES ", Role: RoleWarning})
	}
	canvas.SetLine(0, spans...)
	canvas.SetLine(1, Span{Text: strings.Repeat(glyphs.Horizontal, canvas.Width), Role: RoleMuted})
}

type lineWriter struct {
	canvas *Canvas
	y, end int
}

func contentWriter(canvas *Canvas) *lineWriter {
	return &lineWriter{canvas: canvas, y: min(2, canvas.Height), end: max(canvas.Height-1, 0)}
}

func (w *lineWriter) line(spans ...Span) bool {
	if w.y >= w.end {
		return false
	}
	w.canvas.SetLine(w.y, spans...)
	w.y++
	return true
}

func renderLive(canvas *Canvas, state State, glyphs Glyphs) {
	w := contentWriter(canvas)
	live := state.Snapshot.Live
	if !live.Active && !live.Completed {
		w.line(Span{Text: "LIVE", Role: RoleHeader})
		w.line(Span{Text: "No active run.", Role: RoleMuted})
		w.line(Span{Text: "Start one with: fitr top run <model>", Role: RoleAccent})
		return
	}
	status, role := "RUNNING", RoleAccent
	if live.Completed {
		status, role = "COMPLETE", RolePass
	}
	if live.Cancelled {
		status, role = "CANCELLED", RoleWarning
	} else if live.Error != "" {
		status, role = "ERROR", RoleFail
	}
	w.line(Span{Text: "LIVE  ", Role: RoleHeader}, Span{Text: status, Role: role})
	w.line(Span{Text: "model      ", Role: RoleMuted}, Span{Text: live.Model, Role: RoleDefault})
	w.line(Span{Text: "phase      ", Role: RoleMuted}, Span{Text: live.Phase, Role: RoleAccent},
		Span{Text: "  " + live.Detail, Role: RoleMuted})
	if !live.StartedAt.IsZero() {
		end := state.Now
		if end.Before(live.StartedAt) {
			end = live.UpdatedAt
		}
		w.line(Span{Text: "elapsed    ", Role: RoleMuted}, Span{Text: formatDuration(end.Sub(live.StartedAt)), Role: RoleDefault})
	}
	if live.TotalSteps > 0 {
		width := max(min(canvas.Width-28, 30), 6)
		progress := valueBar(float64(live.CompletedSteps), float64(live.TotalSteps), width, glyphs)
		w.line(Span{Text: "progress   ", Role: RoleMuted}, Span{Text: progress, Role: RoleAccent},
			Span{Text: fmt.Sprintf(" %d/%d", live.CompletedSteps, live.TotalSteps), Role: RoleDefault})
	}
	if live.Placement != "" {
		w.line(Span{Text: "placement  ", Role: RoleMuted}, Span{Text: live.Placement, Role: RoleDefault})
	}
	if live.Decode > 0 {
		w.line(metricSpans("decode", live.Decode, "tok/s", live.DecodeSeries, glyphs)...)
	}
	if live.Prefill > 0 {
		w.line(metricSpans("prefill", live.Prefill, "tok/s", live.PrefillSeries, glyphs)...)
	}
	if live.TTFT > 0 {
		w.line(metricSpans("TTFT", live.TTFT, "s", live.TTFTSeries, glyphs)...)
	}
	if live.MemoryGB > 0 {
		w.line(Span{Text: "memory     ", Role: RoleMuted}, Span{Text: fmt.Sprintf("%.2f GB", live.MemoryGB), Role: RoleDefault})
	}
	for _, warning := range live.Warnings {
		if !w.line(Span{Text: "warning    ", Role: RoleWarning}, Span{Text: warning, Role: RoleDefault}) {
			break
		}
	}
	if live.Error != "" {
		label, role := "error      ", RoleFail
		if live.Cancelled {
			label, role = "cancelled  ", RoleWarning
		}
		w.line(Span{Text: label, Role: role}, Span{Text: live.Error, Role: RoleDefault})
	}
	if live.Completed && !live.Cancelled && live.Error == "" {
		if !live.Saved {
			w.line(Span{Text: "UNSAVED", Role: RoleFail}, Span{Text: "  check result directory permissions and free space", Role: RoleWarning})
		} else {
			w.line(Span{Text: "Enter opens the saved Result.", Role: RoleAccent})
		}
	}
	if len(live.Phases) > 0 && canvas.Height >= 18 {
		w.line(Span{Text: "phases", Role: RoleHeader})
		for _, phase := range live.Phases {
			stateToken, role := strings.ToUpper(phase.State), RoleMuted
			switch phase.State {
			case "running":
				stateToken, role = "RUN", RoleAccent
			case "completed":
				stateToken, role = "DONE", RolePass
			case "failed", "cancelled":
				role = RoleFail
			}
			progress := ""
			if phase.Total > 0 {
				progress = fmt.Sprintf(" %d/%d", phase.Completed, phase.Total)
			}
			if !w.line(Span{Text: fmt.Sprintf("  [%-4s] ", stateToken), Role: role},
				Span{Text: phase.Name + progress, Role: RoleDefault}) {
				break
			}
		}
	}
}

func renderResult(canvas *Canvas, state State, glyphs Glyphs) {
	w := contentWriter(canvas)
	id := state.Selected[ViewResult]
	run, ok := FindRun(state.Snapshot, id)
	if !ok {
		w.line(Span{Text: "RESULT", Role: RoleHeader})
		w.line(Span{Text: "No saved result selected.", Role: RoleMuted})
		return
	}
	w.line(Span{Text: "RESULT  ", Role: RoleHeader}, Span{Text: run.Model, Role: RoleAccent})
	if run.UseFor != "" {
		w.line(Span{Text: "use for    ", Role: RoleMuted}, Span{Text: run.UseFor, Role: RoleAccent})
	}
	build := strings.TrimSpace(strings.Join([]string{run.ParamSize, run.Quant, run.Family}, " "))
	w.line(Span{Text: "build      ", Role: RoleMuted}, Span{Text: build + glyphs.Dot + fmt.Sprintf("ctx %d", run.Context), Role: RoleDefault})
	identity := strings.TrimSpace(strings.Join([]string{run.Device, run.Driver, run.Runtime}, glyphs.Dot))
	if identity != "" {
		w.line(Span{Text: "device     ", Role: RoleMuted}, Span{Text: identity, Role: RoleDefault})
	}
	w.line(Span{Text: "config     ", Role: RoleMuted}, Span{Text: run.Config + glyphs.Dot + "profile " + run.Profile, Role: RoleDefault})
	w.line(Span{Text: "performance", Role: RoleHeader})
	if run.DecodeMean > 0 {
		w.line(Span{Text: "decode     ", Role: RoleMuted}, Span{Text: fmt.Sprintf("%.2f ± %.2f tok/s  k=%d", run.DecodeMean, run.DecodeSD, run.Repeats), Role: RoleDefault},
			Span{Text: "  " + sparkline(run.DecodeSeries, 12, glyphs), Role: RoleAccent})
	}
	if run.PrefillMean > 0 {
		w.line(metricSpans("prefill", run.PrefillMean, "tok/s", nil, glyphs)...)
	}
	if run.TTFTMean > 0 {
		w.line(metricSpans("TTFT", run.TTFTMean, "s", nil, glyphs)...)
	}
	if run.MemoryGB > 0 {
		w.line(Span{Text: "memory     ", Role: RoleMuted}, Span{Text: fmt.Sprintf("%.2f GB", run.MemoryGB), Role: RoleDefault})
	}
	if run.Trials > 0 {
		w.line(Span{Text: "resolution ", Role: RoleMuted}, Span{Text: fmt.Sprintf("%d trials; minimum detectable effect about %.0f pp", run.Trials, run.MDEpp), Role: RoleWarning})
	}
	for _, warning := range run.Warnings {
		if !w.line(Span{Text: "warning    ", Role: RoleWarning}, Span{Text: warning, Role: RoleDefault}) {
			return
		}
	}
	if run.NextCommand != "" {
		w.line(Span{Text: "next       ", Role: RoleMuted}, Span{Text: run.NextCommand, Role: RoleAccent})
	}
	if len(run.Verdicts) > 0 {
		w.line(Span{Text: "verdicts", Role: RoleHeader})
		for _, verdict := range run.Verdicts {
			role := verdictRole(verdict.State)
			label := verdict.Label
			if label == "" {
				label = verdict.Need
			}
			spans := []Span{{Text: fmt.Sprintf("[%-4s] ", verdict.State), Role: role}, {Text: label, Role: RoleDefault}}
			if canvas.Width >= 100 {
				spans = append(spans, Span{Text: "  " + verdict.Why, Role: RoleMuted})
			}
			if !w.line(spans...) {
				return
			}
		}
	}
}

func renderBoard(canvas *Canvas, state State, glyphs Glyphs) {
	w := contentWriter(canvas)
	groups := VisibleBoard(state)
	if len(groups) == 0 {
		w.line(Span{Text: "BOARD", Role: RoleHeader})
		w.line(Span{Text: emptyFilterMessage(state), Role: RoleMuted})
		return
	}
	if !w.line(Span{Text: fmt.Sprintf("BOARD  %d comparable group(s)  sort %s", len(groups), state.BoardSort), Role: RoleHeader}) {
		return
	}
	offset := state.Offset[ViewBoard]
	rowIndex := 0
	for _, group := range groups {
		headerWritten := false
		maxDecode := 0.0
		for _, run := range group.Runs {
			maxDecode = max(maxDecode, run.DecodeMean)
		}
		for _, run := range group.Runs {
			if rowIndex < offset {
				rowIndex++
				continue
			}
			if !headerWritten {
				if !w.line(Span{Text: group.Title, Role: RoleHeader}) {
					return
				}
				if group.Note != "" && canvas.Width >= 60 {
					role := RoleMuted
					if !group.Comparable {
						role = RoleWarning
					}
					if !w.line(Span{Text: "  " + group.Note, Role: role}) {
						return
					}
				}
				headerWritten = true
			}
			if !w.line(boardRowSpans(run, run.ID == state.Selected[ViewBoard], maxDecode, canvas.Width, glyphs)...) {
				return
			}
			rowIndex++
		}
	}
}

func boardRowSpans(run Run, selected bool, ceiling float64, width int, glyphs Glyphs) []Span {
	prefix := "  "
	if selected {
		prefix = glyphs.Selected + " "
	}
	if width < 60 {
		spans := []Span{{Text: prefix + clipCells(run.Model, max(width-18, 8), glyphs.Ellipsis), Role: RoleDefault},
			{Text: fmt.Sprintf(" %6.2f tok/s", run.DecodeMean), Role: RoleAccent}}
		return selectedSpans(spans, selected)
	}
	modelWidth := 24
	if width < 100 {
		modelWidth = 18
	}
	build := strings.TrimSpace(run.ParamSize + " " + run.Quant)
	barWidth := 8
	if width >= 100 {
		barWidth = 12
	}
	spans := []Span{
		{Text: prefix + padCells(clipCells(run.Model, modelWidth, glyphs.Ellipsis), modelWidth) + " " +
			padCells(clipCells(build, 14, glyphs.Ellipsis), 14) + " ", Role: RoleDefault},
		{Text: valueBar(run.DecodeMean, ceiling, barWidth, glyphs), Role: RoleAccent},
		{Text: fmt.Sprintf(" %6.2f", run.DecodeMean), Role: RoleDefault},
	}
	if width >= 80 {
		spans = append(spans, Span{Text: fmt.Sprintf("  sd %-5.2f  k %-2d", run.DecodeSD, run.Repeats), Role: RoleMuted})
	}
	if width >= 110 {
		spans = append(spans,
			Span{Text: "  " + sparkline(run.DecodeSeries, 10, glyphs), Role: RoleAccent},
			Span{Text: "  " + strings.Join(run.Serves, ","), Role: RolePass},
		)
	}
	return selectedSpans(spans, selected)
}

func selectedSpans(spans []Span, selected bool) []Span {
	if selected {
		for index := range spans {
			spans[index].Role = RoleSelected
		}
	}
	return spans
}

func renderHistory(canvas *Canvas, state State, glyphs Glyphs) {
	w := contentWriter(canvas)
	runs := VisibleHistory(state)
	if len(runs) == 0 {
		w.line(Span{Text: "HISTORY", Role: RoleHeader})
		w.line(Span{Text: emptyFilterMessage(state), Role: RoleMuted})
		return
	}
	w.line(Span{Text: fmt.Sprintf("HISTORY  %d run(s)  sort %s", len(runs), state.HistorySort), Role: RoleHeader})
	offset := min(state.Offset[ViewHistory], max(len(runs)-1, 0))
	for _, run := range runs[offset:] {
		selected := run.ID == state.Selected[ViewHistory]
		role, prefix := RoleDefault, "  "
		if selected {
			role, prefix = RoleSelected, glyphs.Selected+" "
		}
		when := "unknown"
		if !run.StartedAt.IsZero() {
			when = run.StartedAt.Format("2006-01-02 15:04")
		}
		baseline := "   "
		if run.ID == state.BaselineID {
			baseline = "[B]"
		}
		line := prefix + baseline + " " + padCells(clipCells(run.Model, max(min(canvas.Width/3, 28), 10), glyphs.Ellipsis), max(min(canvas.Width/3, 28), 10))
		if canvas.Width >= 55 {
			line += "  " + when
		}
		if canvas.Width >= 75 {
			line += fmt.Sprintf("  ctx=%-5d %-7s %6.2f tok/s", run.Context, run.Level, run.DecodeMean)
		}
		if canvas.Width >= 100 {
			line += "  " + padCells(clipCells(strings.TrimSpace(run.ParamSize+" "+run.Quant), 14, glyphs.Ellipsis), 14)
		}
		if canvas.Width >= 115 {
			line += "  " + clipCells(strings.Join(run.Serves, ","), 18, glyphs.Ellipsis)
		}
		if canvas.Width >= 135 {
			line += "  group " + clipCells(run.DeviceID, 8, "")
		}
		if !w.line(Span{Text: line, Role: role}) {
			break
		}
	}
}

func renderComparison(canvas *Canvas, state State) {
	w := contentWriter(canvas)
	comparison := state.Comparison
	if comparison == nil {
		return
	}
	w.line(Span{Text: "HISTORY COMPARISON", Role: RoleHeader})
	w.line(Span{Text: "baseline   ", Role: RoleMuted}, Span{Text: comparison.BaselineModel, Role: RoleDefault})
	w.line(Span{Text: "selected   ", Role: RoleMuted}, Span{Text: comparison.SelectedModel, Role: RoleAccent})
	if !comparison.Compatible {
		w.line(Span{Text: "NOT COMPARABLE", Role: RoleFail})
		w.line(Span{Text: comparison.Reason, Role: RoleWarning})
		w.line(Span{Text: "No relative bars, ranks, or winner are shown.", Role: RoleMuted})
		return
	}
	w.line(Span{Text: "COMPARABLE", Role: RolePass}, Span{Text: "  " + comparison.Reason, Role: RoleMuted})
	w.line(Span{Text: "", Role: RoleDefault})
	w.line(Span{Text: "metric       baseline      selected        delta", Role: RoleHeader})
	w.line(Span{Text: fmt.Sprintf("decode      %8.2f      %8.2f      %+8.2f tok/s",
		comparison.DecodeA, comparison.DecodeB, comparison.DecodeDiff), Role: RoleDefault})
	if comparison.PrefillA > 0 || comparison.PrefillB > 0 {
		w.line(Span{Text: fmt.Sprintf("prefill     %8.2f      %8.2f      %+8.2f tok/s",
			comparison.PrefillA, comparison.PrefillB, comparison.PrefillB-comparison.PrefillA), Role: RoleDefault})
	}
	if comparison.MemoryA > 0 || comparison.MemoryB > 0 {
		w.line(Span{Text: fmt.Sprintf("memory      %8.2f      %8.2f      %+8.2f GB",
			comparison.MemoryA, comparison.MemoryB, comparison.MemoryB-comparison.MemoryA), Role: RoleDefault})
	}
	w.line(Span{Text: "Exact observations only. No statistical winner is claimed here.", Role: RoleWarning})
	w.line(Span{Text: "Use fitr compare for confidence intervals and paired tests.", Role: RoleMuted})
}

func renderHelp(canvas *Canvas, _ State, _ Glyphs) {
	w := contentWriter(canvas)
	w.line(Span{Text: "KEYS", Role: RoleHeader})
	items := []string{
		"1-4 / Tab       switch view",
		"arrows / j k    move selection",
		"h l             previous or next view",
		"PgUp PgDn       move one page",
		"Home End / g G  first or last",
		"Enter Esc       open or go back",
		"/ / Ctrl+U      filter or clear filter",
		"s               cycle sort",
		"Space           pause Live display updates",
		"Space / c       mark or compare History runs",
		"r               reload saved results",
		"Ctrl+L          force complete redraw",
		"? / F1          close this help",
		"q               quit",
	}
	for _, item := range items {
		if !w.line(Span{Text: item, Role: RoleDefault}) {
			break
		}
	}
}

func renderFooter(canvas *Canvas, state State) {
	if canvas.Height < 2 {
		return
	}
	var spans []Span
	switch {
	case state.EditingFilter:
		spans = []Span{{Text: " / " + state.Filter, Role: RoleAccent}, {Text: "  Enter apply  Esc close", Role: RoleMuted}}
	case state.Error != "":
		spans = []Span{{Text: " error: ", Role: RoleFail}, {Text: state.Error, Role: RoleDefault}}
	default:
		if state.Comparison != nil {
			spans = []Span{{Text: " Esc/Enter/c close comparison  ? help  q quit", Role: RoleMuted}}
		} else if state.View == ViewHistory {
			spans = []Span{{Text: " Space baseline  c compare  Enter open  / filter  s sort  ? help  q quit", Role: RoleMuted}}
		} else {
			spans = []Span{{Text: " 1-4 views  j/k move  Enter open  / filter  s sort  ? help  q quit", Role: RoleMuted}}
		}
	}
	canvas.SetLine(canvas.Height-1, spans...)
}

func metricSpans(name string, value float64, unit string, series []float64, glyphs Glyphs) []Span {
	spans := []Span{{Text: fmt.Sprintf("%-11s", name), Role: RoleMuted},
		{Text: fmt.Sprintf("%8.2f %s", value, unit), Role: RoleDefault}}
	if len(series) > 0 {
		spans = append(spans, Span{Text: "  " + sparkline(series, 16, glyphs), Role: RoleAccent})
	}
	return spans
}

func verdictRole(state string) Role {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "PASS":
		return RolePass
	case "FAIL":
		return RoleFail
	case "BLKD", "WARN", "INCONCLUSIVE":
		return RoleWarning
	default:
		return RoleMuted
	}
}

func emptyFilterMessage(state State) string {
	if state.Filter != "" {
		return fmt.Sprintf("No runs match %q. Press Esc to clear the filter.", state.Filter)
	}
	return "No saved runs yet. Run a model first."
}

func formatDuration(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	duration = duration.Round(time.Second)
	return duration.String()
}

func valueBar(value, ceiling float64, width int, glyphs Glyphs) string {
	width = max(width, 1)
	filled := 0
	if value > 0 && ceiling > 0 {
		filled = min(max(int(math.Round(float64(width)*value/ceiling)), 1), width)
	}
	return "[" + strings.Repeat(glyphs.Full, filled) + strings.Repeat(glyphs.Empty, width-filled) + "]"
}

func sparkline(values []float64, limit int, glyphs Glyphs) string {
	if len(values) == 0 || limit < 1 || len(glyphs.Spark) == 0 {
		return "-"
	}
	clean := make([]float64, 0, len(values))
	for _, value := range values {
		if !math.IsNaN(value) && !math.IsInf(value, 0) {
			clean = append(clean, value)
		}
	}
	if len(clean) == 0 {
		return "-"
	}
	if len(clean) > limit {
		if limit == 1 {
			clean = clean[len(clean)-1:]
		} else {
			sampled := make([]float64, limit)
			for i := range sampled {
				index := int(math.Round(float64(i) * float64(len(clean)-1) / float64(limit-1)))
				sampled[i] = clean[index]
			}
			clean = sampled
		}
	}
	lo, hi := slices.Min(clean), slices.Max(clean)
	var out strings.Builder
	for _, value := range clean {
		index := (len(glyphs.Spark) - 1) / 2
		if hi > lo {
			index = int(math.Round(float64(len(glyphs.Spark)-1) * (value - lo) / (hi - lo)))
		}
		out.WriteRune(glyphs.Spark[index])
	}
	return out.String()
}
