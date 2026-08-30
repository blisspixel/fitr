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
	renderMainContent(&canvas, state, glyphs)
	renderFooter(&canvas, state)
	renderQuitConfirmation(&canvas, state)
	return canvas
}

func renderMainContent(canvas *Canvas, state State, glyphs Glyphs) {
	if state.Help {
		renderHelp(canvas, state, glyphs)
		return
	}
	if state.Comparison != nil {
		renderComparison(canvas, state)
		return
	}
	switch state.View {
	case ViewLive:
		renderLive(canvas, state, glyphs)
	case ViewResult:
		renderResult(canvas, state, glyphs)
	case ViewBoard:
		renderBoard(canvas, state, glyphs)
	case ViewHistory:
		renderHistory(canvas, state, glyphs)
	case ViewInventory:
		renderInventory(canvas, state, glyphs)
	}
}

func renderQuitConfirmation(canvas *Canvas, state State) {
	if !state.ConfirmQuit {
		return
	}
	y := min(max(state.Height/2, 2), state.Height-2)
	canvas.SetLine(y, Span{Text: " Quit and cancel the active run? [y] yes  [n] no ", Role: RoleWarning})
}

func renderTiny(canvas *Canvas, state State) {
	canvas.SetLine(0, Span{Text: "fitr top | " + strings.ToUpper(state.View.String()), Role: RoleHeader})
	if canvas.Height == 1 {
		return
	}
	if state.Help {
		canvas.SetLine(1, Span{Text: "1-5 views | j/k move | ? close | q quit", Role: RoleDefault})
	} else {
		renderTinyView(canvas, state)
	}
	if canvas.Height >= 3 && state.Error != "" {
		canvas.SetLine(2, Span{Text: "error: " + state.Error, Role: RoleFail})
	}
	canvas.SetLine(canvas.Height-1, Span{Text: "1-5 views | ? help | q quit", Role: RoleMuted})
}

func renderTinyView(canvas *Canvas, state State) {
	switch state.View {
	case ViewLive:
		live := state.Snapshot.Live
		canvas.SetLine(1, Span{Text: tinyLiveStatus(live) + " | " + live.Model, Role: RoleDefault})
	case ViewResult:
		renderTinyResult(canvas, state)
	case ViewBoard:
		canvas.SetLine(1, Span{Text: fmt.Sprintf("%d comparable group(s)", len(VisibleBoard(state))), Role: RoleDefault})
	case ViewHistory:
		canvas.SetLine(1, Span{Text: fmt.Sprintf("%d saved run(s)", len(VisibleHistory(state))), Role: RoleDefault})
	case ViewInventory:
		canvas.SetLine(1, Span{Text: fmt.Sprintf("%d installed", len(VisibleInventory(state))), Role: RoleDefault})
	}
}

func tinyLiveStatus(live Live) string {
	switch {
	case live.Active:
		return "running: " + live.Phase
	case live.Cancelled:
		return "cancelled"
	case live.Error != "":
		return "error: " + live.Error
	case live.Completed:
		return "complete; Enter opens Result"
	default:
		return "idle"
	}
}

func renderTinyResult(canvas *Canvas, state State) {
	run, ok := FindRun(state.Snapshot, state.Selected[ViewResult])
	if !ok {
		canvas.SetLine(1, Span{Text: "no saved result", Role: RoleMuted})
		return
	}
	canvas.SetLine(1, Span{Text: run.Model + fmt.Sprintf(" | %.2f tok/s", run.DecodeMean), Role: RoleDefault})
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
	renderLiveIdentity(w, live)
	renderLiveProgress(w, canvas, state, live, glyphs)
	renderLiveMessages(w, live)
	renderLivePhases(w, live, canvas.Height)
}

func renderLiveIdentity(w *lineWriter, live Live) {
	status, role := liveStatus(live)
	w.line(Span{Text: "LIVE  ", Role: RoleHeader}, Span{Text: status, Role: role})
	w.line(Span{Text: "model      ", Role: RoleMuted}, Span{Text: live.Model, Role: RoleDefault})
	w.line(Span{Text: "phase      ", Role: RoleMuted}, Span{Text: live.Phase, Role: RoleAccent},
		Span{Text: "  " + live.Detail, Role: RoleMuted})
}

func liveStatus(live Live) (string, Role) {
	status, role := "RUNNING", RoleAccent
	if live.Completed {
		status, role = "COMPLETE", RolePass
	}
	if live.Cancelled {
		status, role = "CANCELLED", RoleWarning
	} else if live.Error != "" {
		status, role = "ERROR", RoleFail
	}
	return status, role
}

func renderLiveProgress(w *lineWriter, canvas *Canvas, state State, live Live, glyphs Glyphs) {
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
}

func renderLiveMessages(w *lineWriter, live Live) {
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
}

func renderLivePhases(w *lineWriter, live Live, height int) {
	if len(live.Phases) == 0 || height < 18 {
		return
	}
	w.line(Span{Text: "phases", Role: RoleHeader})
	for _, phase := range live.Phases {
		stateToken, role := livePhaseStyle(phase.State)
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

func livePhaseStyle(state string) (string, Role) {
	switch state {
	case "running":
		return "RUN", RoleAccent
	case "completed":
		return "DONE", RolePass
	case "failed", "cancelled":
		return strings.ToUpper(state), RoleFail
	default:
		return strings.ToUpper(state), RoleMuted
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
	renderResultIdentity(w, run, glyphs)
	renderResultPerformance(w, run, glyphs)
	if !renderResultWarnings(w, run) {
		return
	}
	renderResultVerdicts(w, run, canvas.Width)
}

func renderResultIdentity(w *lineWriter, run Run, glyphs Glyphs) {
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
}

func renderResultPerformance(w *lineWriter, run Run, glyphs Glyphs) {
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
		w.line(Span{Text: "capacity", Role: RoleHeader})
		w.line(Span{Text: "resident   ", Role: RoleMuted}, Span{Text: fmt.Sprintf("%.2f GB after requested 32K load probe", run.MemoryGB), Role: RoleDefault})
	}
}

func renderResultWarnings(w *lineWriter, run Run) bool {
	for _, warning := range run.Warnings {
		if !w.line(Span{Text: "warning    ", Role: RoleWarning}, Span{Text: warning, Role: RoleDefault}) {
			return false
		}
	}
	if run.NextCommand != "" {
		w.line(Span{Text: "next       ", Role: RoleMuted}, Span{Text: run.NextCommand, Role: RoleAccent})
	}
	return true
}

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
		if !renderBoardGroup(w, group, state.Selected[ViewBoard], offset, &rowIndex, canvas.Width, glyphs) {
			return
		}
	}
}

func renderBoardGroup(w *lineWriter, group BoardGroup, selectedID string, offset int,
	rowIndex *int, width int, glyphs Glyphs) bool {
	headerWritten := false
	maxDecode := maxRunDecode(group.Runs)
	for _, run := range group.Runs {
		if *rowIndex < offset {
			(*rowIndex)++
			continue
		}
		var ok bool
		headerWritten, ok = renderBoardGroupHeader(w, group, headerWritten, width)
		if !ok {
			return false
		}
		if !w.line(boardRowSpans(run, run.ID == selectedID, maxDecode, width, glyphs)...) {
			return false
		}
		(*rowIndex)++
	}
	return true
}

func maxRunDecode(runs []Run) float64 {
	maxDecode := 0.0
	for _, run := range runs {
		maxDecode = max(maxDecode, run.DecodeMean)
	}
	return maxDecode
}

func renderBoardGroupHeader(w *lineWriter, group BoardGroup, written bool, width int) (bool, bool) {
	if written {
		return true, true
	}
	if !w.line(Span{Text: group.Title, Role: RoleHeader}) {
		return false, false
	}
	if group.Note == "" || width < 60 {
		return true, true
	}
	role := RoleMuted
	if !group.Comparable {
		role = RoleWarning
	}
	return true, w.line(Span{Text: "  " + group.Note, Role: role})
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

func renderInventory(canvas *Canvas, state State, glyphs Glyphs) {
	w := contentWriter(canvas)
	items := VisibleInventory(state)
	w.line(Span{Text: "INVENTORY  installed models, not a ranking", Role: RoleHeader})
	if len(items) == 0 {
		msg := "no models listed; start a runtime or pull a model"
		if strings.TrimSpace(state.Filter) != "" {
			msg = emptyFilterMessage(state)
		}
		w.line(Span{Text: msg, Role: RoleMuted})
		w.line(Span{Text: "unmeasured is a candidate, never a recommendation", Role: RoleMuted})
		return
	}
	w.line(Span{Text: "MODEL                    STATE        FIT        CTX       NEXT", Role: RoleMuted})
	offset := min(state.Offset[ViewInventory], max(len(items)-1, 0))
	for _, item := range items[offset:] {
		if !renderInventoryItem(w, item, item.ID == state.Selected[ViewInventory], canvas.Width, glyphs) {
			break
		}
	}
	w.line(Span{Text: "* loaded   CTX measured/serving   Enter opens a measured result   board still ranks only comparable runs", Role: RoleMuted})
}

func renderInventoryItem(w *lineWriter, item InventoryItem, selected bool, width int, glyphs Glyphs) bool {
	role, prefix := RoleDefault, "  "
	if selected {
		role, prefix = RoleSelected, glyphs.Selected+" "
	}
	name := item.Model
	if item.Loaded {
		name = "* " + name
	}
	line := prefix + padCells(clipCells(name, 22, glyphs.Ellipsis), 22)
	line += "  " + padCells(item.State, 12)
	line += padCells(inventoryFitLabel(item.Fit), 10)
	line += padCells(inventoryContextLabel(item.Ctx), 9)
	line += clipCells(item.Next, max(width-61, 10), glyphs.Ellipsis)
	if !w.line(Span{Text: line, Role: role}) {
		return false
	}
	if selected && item.Windows != "" &&
		!w.line(Span{Text: "    " + clipCells(item.Windows, max(width-6, 8), glyphs.Ellipsis), Role: RoleMuted}) {
		return false
	}
	if selected && item.Note != "" &&
		!w.line(Span{Text: "    " + clipCells(item.Note, max(width-6, 8), glyphs.Ellipsis), Role: RoleMuted}) {
		return false
	}
	return true
}

func inventoryFitLabel(label string) string {
	switch label {
	case "":
		return "-"
	case "low_memory":
		return "low mem"
	default:
		return label
	}
}

func inventoryContextLabel(label string) string {
	if label == "" {
		return "-"
	}
	return label
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
		"1-5 / Tab       switch view",
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
			spans = []Span{{Text: " 1-5 views  j/k move  Enter open  / filter  s sort  ? help  q quit", Role: RoleMuted}}
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

// flatFloor is the relative spread below which no shape is drawn.
//
// A sparkline is normalised to its own min and max, so without a floor a run
// that varied by 0.4% renders the identical dramatic zigzag to one that varied
// tenfold. The monitor states the mean and the spread on the same row, and
// those already say everything a picture of noise could.
//
// The CLI renderer holds the same constant. The two are deliberately not
// shared: this package is a renderer-neutral state reducer over the standard
// library, and importing the display layer to borrow one float would trade
// that for nothing.
const flatFloor = 0.05

// sparkline draws repeat shape, or says why it did not.
//
// "flat" is a claim about the data and is only returned when the data supports
// it. Being unable to draw -- one observation, or an ASCII stream with no
// readable ramp -- is a different statement and prints as an absence.
func sparkline(values []float64, limit int, glyphs Glyphs) string {
	// One cell cannot depict a series, and it is also the divide-by-zero in the
	// down-sampling step below.
	if limit < 2 {
		return "-"
	}
	clean := make([]float64, 0, len(values))
	for _, value := range values {
		if !math.IsNaN(value) && !math.IsInf(value, 0) {
			clean = append(clean, value)
		}
	}
	if len(clean) < 2 {
		return "-"
	}
	if flatSeries(clean) {
		return "flat"
	}
	if len(glyphs.Spark) == 0 {
		return "-"
	}
	if len(clean) > limit {
		sampled := make([]float64, limit)
		for i := range sampled {
			index := int(math.Round(float64(i) * float64(len(clean)-1) / float64(limit-1)))
			sampled[i] = clean[index]
		}
		clean = sampled
	}
	lo, hi := slices.Min(clean), slices.Max(clean)
	if hi <= lo {
		return "flat"
	}
	var out strings.Builder
	for _, value := range clean {
		out.WriteRune(glyphs.Spark[int(math.Round(float64(len(glyphs.Spark)-1)*(value-lo)/(hi-lo)))])
	}
	return out.String()
}

func flatSeries(clean []float64) bool {
	lo, hi := slices.Min(clean), slices.Max(clean)
	if hi <= lo {
		return true
	}
	sum := 0.0
	for _, v := range clean {
		sum += v
	}
	mean := sum / float64(len(clean))
	return mean == 0 || (hi-lo)/math.Abs(mean) < flatFloor
}
