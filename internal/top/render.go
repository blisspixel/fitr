package top

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/analysis"
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
		canvas.SetLine(0, Span{Text: "fitr", Role: RoleHeader})
		return
	}
	spans := []Span{{Text: " fitr ", Role: RoleHeader}}
	for view := ViewLive; view < viewCount; view++ {
		label := fmt.Sprintf(" %d %s ", int(view)+1, view.String())
		role := RoleMuted
		if state.View == view {
			label = fmt.Sprintf(" [%d %s] ", int(view)+1, view.String())
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
	if canvas.Height < 32 {
		renderCompactResult(w, run, glyphs)
		return
	}
	renderResultIdentity(w, run, glyphs)
	renderResultVerdicts(w, run, canvas.Width)
	renderResultNext(w, run)
	renderResultPerformance(w, run, glyphs, false)
	renderResultAnalysis(w, run)
	renderResultWarnings(w, run)
}

func renderCompactResult(w *lineWriter, run Run, glyphs Glyphs) {
	renderResultIdentity(w, run, glyphs)
	renderCompactVerdicts(w, run)
	renderResultNext(w, run)
	renderResultPerformance(w, run, glyphs, true)
	diagnoses, gaps := 0, 0
	if run.Analysis != nil {
		diagnoses = len(run.Analysis.Diagnoses)
		gaps = len(run.Analysis.Gaps)
	}
	w.line(Span{Text: "details    ", Role: RoleMuted}, Span{Text: fmt.Sprintf(
		"compact; %d gap, %d diagnosis, %d warning; full: fitr view %s",
		gaps, diagnoses, len(run.Warnings), run.Model), Role: RoleDefault})
}

func renderCompactVerdicts(w *lineWriter, run Run) {
	if len(run.Verdicts) == 0 {
		return
	}
	w.line(Span{Text: "verdicts", Role: RoleHeader})
	order := []string{"PASS", "FAIL", "INCONCLUSIVE", "BLKD", "SKIP", "N/A"}
	counts := make(map[string]int, len(order))
	var attention []string
	for _, verdict := range run.Verdicts {
		state := strings.ToUpper(strings.TrimSpace(verdict.State))
		counts[state]++
		if state == "FAIL" || state == "BLKD" || state == "INCONCLUSIVE" {
			label := verdict.Label
			if label == "" {
				label = verdict.Need
			}
			attention = append(attention, label+" ["+state+"]")
		}
	}
	parts := make([]string, 0, len(order))
	for _, state := range order {
		if counts[state] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[state], state))
		}
	}
	w.line(Span{Text: "summary    ", Role: RoleMuted}, Span{Text: strings.Join(parts, " | "), Role: RoleDefault})
	if len(attention) > 0 {
		w.line(Span{Text: "attention  ", Role: RoleWarning}, Span{Text: strings.Join(attention, "; "), Role: RoleDefault})
	}
}

func renderResultIdentity(w *lineWriter, run Run, glyphs Glyphs) {
	w.line(Span{Text: "RESULT  ", Role: RoleHeader}, Span{Text: run.Model, Role: RoleAccent})
	if run.UseFor != "" {
		w.line(Span{Text: "use for    ", Role: RoleMuted}, Span{Text: run.UseFor, Role: RoleAccent})
	}
	build := strings.TrimSpace(strings.Join([]string{run.ParamSize, run.Quant, run.Family}, " "))
	w.line(Span{Text: "build      ", Role: RoleMuted}, Span{Text: build + glyphs.Dot + fmt.Sprintf("ctx %d", run.Context), Role: RoleDefault})
	if identity := runArtifactLine(run, glyphs); identity != "" {
		w.line(Span{Text: "artifact   ", Role: RoleMuted}, Span{Text: identity, Role: RoleDefault})
	}
	identity := joinPresent(glyphs.Dot, run.Device, run.Driver, run.Runtime)
	if identity != "" {
		w.line(Span{Text: "device     ", Role: RoleMuted}, Span{Text: identity, Role: RoleDefault})
	}
	config := joinPresent(glyphs.Dot, run.Config, "profile "+run.Profile)
	w.line(Span{Text: "config     ", Role: RoleMuted}, Span{Text: config, Role: RoleDefault})
}

func joinPresent(separator string, values ...string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" && value != "profile" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, separator)
}

func renderResultPerformance(w *lineWriter, run Run, glyphs Glyphs, compact bool) {
	w.line(Span{Text: "performance", Role: RoleHeader})
	if run.DecodePresent {
		observation := analysis.PerformanceObservation{}
		if run.Analysis != nil {
			observation = run.Analysis.Performance.DecodeTPS
		}
		w.line(resultMetricSpans("decode", run.DecodeMean, run.DecodeSD, run.Repeats,
			"tok/s", run.DecodeSeries, observation, glyphs)...)
	}
	if run.PrefillPresent {
		observation := analysis.PerformanceObservation{}
		if run.Analysis != nil {
			observation = run.Analysis.Performance.PrefillTPS
		}
		w.line(resultMetricSpans("prefill", run.PrefillMean, 0, run.Repeats,
			"tok/s", nil, observation, glyphs)...)
	}
	if run.TTFTPresent {
		label := "TTFT"
		if run.Analysis != nil {
			label = analysis.TTFTLabel(run.Analysis.Performance.TTFTSeconds)
		}
		observation := analysis.PerformanceObservation{}
		if run.Analysis != nil {
			observation = run.Analysis.Performance.TTFTSeconds
		}
		w.line(resultMetricSpans(label, run.TTFTMean, 0, run.Repeats,
			"s", nil, observation, glyphs)...)
	}
	if run.Analysis != nil {
		renderAnalysisMetric(w, "loaded cache-hit TTFT", run.Analysis.Performance.LoadedCacheHitTTFTSeconds, glyphs)
		renderAnalysisMetric(w, "runtime-unloaded TTFT", run.Analysis.Performance.RuntimeUnloadedTTFTSeconds, glyphs)
		renderAnalysisMetric(w, "runtime load", run.Analysis.Performance.RuntimeLoadSeconds, glyphs)
	}
	renderResultCapacity(w, run, compact)
}

func renderResultCapacity(w *lineWriter, run Run, compact bool) {
	if !run.MemoryPresent && (run.Analysis == nil ||
		run.Analysis.Capacity.Policy == nil && run.Analysis.Capacity.Prediction == nil) {
		return
	}
	w.line(Span{Text: "capacity", Role: RoleHeader})
	var capacityReport analysis.Capacity
	if run.Analysis != nil {
		capacityReport = run.Analysis.Capacity
	}
	renderResultCapacityPolicy(w, capacityReport.Policy, compact)
	renderResultCapacityPrediction(w, capacityReport.Prediction, compact)
	if !run.MemoryPresent {
		return
	}
	residentLabel, residentQualifier := "resident   ", ""
	if capacityReport.Resident != nil && capacityReport.Resident.Status == analysis.StatusDescriptiveOnly {
		residentLabel = "resident [descriptive] "
		residentQualifier = " via " + analysis.AcquisitionLabel(capacityReport.Resident.Acquisition)
	}
	w.line(Span{Text: residentLabel, Role: RoleMuted}, Span{Text: fmt.Sprintf("%.2f GB after requested %s load probe", run.MemoryGB,
		residentContextLabel(run.ResidentContext)), Role: RoleDefault}, Span{Text: residentQualifier, Role: RoleMuted})
	renderResultCapacityBudget(w, capacityReport.Budget)
	if compact || capacityReport.Placement == nil {
		return
	}
	renderResultCapacityPlacement(w, capacityReport.Placement)
}

func renderResultCapacityPolicy(w *lineWriter, policy *analysis.CapacityPolicyObservation, compact bool) {
	if policy == nil {
		return
	}
	if policy.UsableBudgetBytes != nil {
		w.line(Span{Text: "safe budget ", Role: RoleMuted}, Span{Text: fmt.Sprintf("%.2f GiB  %s",
			float64(*policy.UsableBudgetBytes)/(1024*1024*1024), policy.Formula), Role: RoleDefault})
	} else {
		w.line(Span{Text: "safe budget ", Role: RoleMuted},
			Span{Text: "unresolved; no operator budget or reserve", Role: RoleWarning})
	}
	if compact || policy.CurrentAvailableBytes == nil {
		return
	}
	w.line(Span{Text: "available   ", Role: RoleMuted}, Span{Text: fmt.Sprintf("%.2f GiB  %s",
		float64(*policy.CurrentAvailableBytes)/(1024*1024*1024), policy.CurrentAvailableSource), Role: RoleDefault})
	if policy.CurrentAvailableAt != "" {
		w.line(Span{Text: "observed    " + policy.CurrentAvailableAt, Role: RoleMuted})
	}
}

func renderResultCapacityPrediction(w *lineWriter, prediction *analysis.CapacityPredictionObservation, compact bool) {
	if compact || prediction == nil || prediction.KnownComponentBytes == nil {
		return
	}
	w.line(Span{Text: "pre-load   ", Role: RoleMuted}, Span{Text: fmt.Sprintf("%.2f GiB components at %s; not a fit claim",
		float64(*prediction.KnownComponentBytes)/(1024*1024*1024),
		residentContextLabel(prediction.RequestedContext)), Role: RoleDefault})
}

func renderResultCapacityBudget(w *lineWriter, budget *analysis.CapacityBudgetObservation) {
	if budget == nil || budget.State == analysis.CapacityBudgetUnresolved {
		return
	}
	label, role := "FIT", RolePass
	if budget.State == analysis.CapacityBudgetExceeded {
		label, role = "EXCEEDED", RoleFail
	}
	headroom := float64(*budget.HeadroomBytes) / (1024 * 1024 * 1024)
	w.line(Span{Text: "budget     ", Role: RoleMuted},
		Span{Text: fmt.Sprintf("%s  headroom %+.2f GiB", label, headroom), Role: role})
}

func renderResultCapacityPlacement(w *lineWriter, placement *analysis.PlacementObservation) {
	const gib = 1024 * 1024 * 1024
	w.line(Span{Text: "runtime accel", Role: RoleMuted}, Span{Text: fmt.Sprintf("  %.2f GB (%.1f%% of runtime allocation)",
		float64(placement.AcceleratorBytes)/gib, placement.AcceleratorPercent), Role: RoleDefault})
	w.line(Span{Text: "non-accel  ", Role: RoleMuted}, Span{Text: fmt.Sprintf("  %.2f GB (derived remainder)",
		float64(placement.NonAcceleratorBytes)/gib), Role: RoleDefault})
	boundary := strings.SplitN(placement.Boundary, "; ", 2)
	w.line(Span{Text: "            " + boundary[0] + ";", Role: RoleMuted})
	if len(boundary) == 2 {
		w.line(Span{Text: "            " + boundary[1], Role: RoleMuted})
	}
}

func runArtifactLine(run Run, glyphs Glyphs) string {
	if run.Analysis == nil {
		return ""
	}
	artifact := run.Analysis.Artifact
	digest := analysis.ShortDigest(artifact.Digest)
	if digest == "" && run.ArtifactDigest != "" {
		digest = analysis.ShortDigest(run.ArtifactDigest)
	}
	parts := make([]string, 0, 3)
	if digest != "" {
		parts = append(parts, "sha256:"+digest)
	}
	size := artifact.SizeBytes
	if size == 0 {
		size = run.ArtifactBytes
	}
	if size > 0 {
		parts = append(parts, fmt.Sprintf("%.1f GB file", float64(size)/(1024*1024*1024)))
	}
	if run.Quant != "" && digest != "" {
		parts = append(parts, "quant "+run.Quant+" is a recipe label")
	}
	return strings.Join(parts, glyphs.Dot)
}

func renderResultAnalysis(w *lineWriter, run Run) {
	if run.Analysis == nil {
		return
	}
	for _, diagnosis := range run.Analysis.Diagnoses {
		presented := analysis.PresentDiagnosis(diagnosis)
		w.line(Span{Text: "explain    ", Role: RoleMuted},
			Span{Text: presented.Support + " · " + presented.Headline, Role: RoleDefault})
		if len(presented.NextArgv) > 0 {
			w.line(Span{Text: "           ", Role: RoleMuted},
				Span{Text: "next experiment " + analysis.FormatArgv(presented.NextArgv, run.Model), Role: RoleMuted})
		}
	}
	for _, gap := range run.Analysis.Gaps {
		w.line(Span{Text: "limit      ", Role: RoleMuted},
			Span{Text: analysis.GapLabel(gap.Code) + ": " + gap.Message, Role: RoleDefault})
	}
}

func renderAnalysisMetric(w *lineWriter, label string, observation analysis.PerformanceObservation, glyphs Glyphs) {
	if observation.Estimate == nil {
		return
	}
	w.line(resultMetricSpans(label, *observation.Estimate, 0, observation.SampleCount,
		"s", observation.Samples, observation, glyphs)...)
}

func resultMetricSpans(name string, fallback, fallbackSD float64, fallbackN int, unit string,
	series []float64, observation analysis.PerformanceObservation, glyphs Glyphs) []Span {
	value, sd, sampleCount := fallback, fallbackSD, fallbackN
	hasSD := fallbackN > 1
	if observation.Estimate != nil {
		value, sampleCount = *observation.Estimate, observation.SampleCount
		hasSD = observation.SD != nil
		if hasSD {
			sd = *observation.SD
		}
		if len(observation.Samples) > 0 {
			series = observation.Samples
		}
	}
	valueText := fmt.Sprintf("%8.2f %s", value, unit)
	if hasSD {
		valueText = fmt.Sprintf("%8.2f ± %.2f %s", value, sd, unit)
	}
	if sampleCount > 0 {
		valueText += fmt.Sprintf("  n=%d", sampleCount)
	}
	qualifier := ""
	if observation.Status == analysis.StatusDescriptiveOnly {
		name += " [descriptive]"
		qualifier = " via " + analysis.AcquisitionLabel(observation.Acquisition)
	}
	spans := []Span{{Text: fmt.Sprintf("%-24s", name), Role: RoleMuted}, {Text: valueText, Role: RoleDefault}}
	if qualifier != "" {
		spans = append(spans, Span{Text: qualifier, Role: RoleMuted})
	}
	if len(series) > 0 {
		spans = append(spans, Span{Text: "  " + sparkline(series, 12, glyphs), Role: RoleAccent})
	}
	return spans
}

func residentContextLabel(ctx int) string {
	if ctx <= 0 || ctx == 32768 {
		return "32K"
	}
	return fmt.Sprintf("%d-token", ctx)
}

func renderResultWarnings(w *lineWriter, run Run) bool {
	for _, warning := range run.Warnings {
		if !w.line(Span{Text: "warning    ", Role: RoleWarning}, Span{Text: warning, Role: RoleDefault}) {
			return false
		}
	}
	return true
}

func renderResultNext(w *lineWriter, run Run) {
	if run.NextCommand != "" {
		w.line(Span{Text: "next       ", Role: RoleMuted}, Span{Text: run.NextCommand, Role: RoleAccent})
	}
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
	groups := VisibleBoard(state)
	w := contentWriter(canvas)
	if len(groups) == 0 {
		w.line(Span{Text: "BOARD", Role: RoleHeader})
		w.line(Span{Text: emptyFilterMessage(state), Role: RoleMuted})
		return
	}
	if canvas.Width >= 120 && canvas.Height >= 22 {
		renderWideBoard(canvas, state, glyphs, groups)
		return
	}
	if !w.line(Span{Text: fmt.Sprintf("BOARD  %d comparable group(s)  sort %s%s", len(groups), state.BoardSort, filterSuffix(state.Filter)), Role: RoleHeader}) {
		return
	}
	if canvas.Width >= 80 {
		if !w.line(Span{Text: "columns  model / build | relative bar | decode tok/s | SD | N", Role: RoleMuted}) {
			return
		}
	}
	offset := state.Offset[ViewBoard]
	rowIndex := 0
	for _, group := range groups {
		if !renderBoardGroup(w, group, state.Selected[ViewBoard], offset, &rowIndex, canvas.Width, glyphs) {
			return
		}
	}
}

// renderWideBoard gives ordinary desktop terminals a stable master-detail
// surface. The left pane remains the comparable list; the right pane explains
// the selected receipt. Narrow terminals retain the single-column renderer
// above instead of squeezing a dashboard into unreadable columns.
func renderWideBoard(canvas *Canvas, state State, glyphs Glyphs, groups []BoardGroup) {
	w := contentWriter(canvas)
	run, selected := selectedBoardRun(groups, state.Selected[ViewBoard])
	configurations := 0
	for _, group := range groups {
		configurations += len(group.Runs)
	}
	w.line(
		Span{Text: fmt.Sprintf("board  %d %s", configurations, countLabel(configurations, "configuration", "configurations")), Role: RoleHeader},
		Span{Text: fmt.Sprintf("  %d comparable %s  sort ", len(groups), countLabel(len(groups), "group", "groups")), Role: RoleMuted},
		Span{Text: state.BoardSort.String() + " " + boardSortArrow(state.BoardSort), Role: RoleAccent},
		Span{Text: filterSuffix(state.Filter), Role: RoleMuted},
	)
	if selected {
		w.line(wideBoardMetricSpans(run, glyphs)...)
	} else {
		w.line(Span{Text: "No configuration selected.", Role: RoleMuted})
	}
	w.line(Span{Text: strings.Repeat(glyphs.Horizontal, canvas.Width), Role: RoleMuted})

	leftWidth := max(min(canvas.Width*58/100, 72), 56)
	left := wideBoardList(groups, state, leftWidth, glyphs)
	right := wideBoardDetail(run, selected, canvas.Width-leftWidth-3, glyphs)
	renderBoardPanes(canvas, w.y, w.end, leftWidth, left, right, glyphs)
}

func selectedBoardRun(groups []BoardGroup, selectedID string) (Run, bool) {
	for _, group := range groups {
		for _, run := range group.Runs {
			if run.ID == selectedID {
				return run, true
			}
		}
	}
	for _, group := range groups {
		if len(group.Runs) > 0 {
			return group.Runs[0], true
		}
	}
	return Run{}, false
}

func wideBoardMetricSpans(run Run, glyphs Glyphs) []Span {
	spans := []Span{{Text: "selected  ", Role: RoleMuted}, {Text: run.Model, Role: RoleAccent}}
	if run.DecodePresent {
		spans = append(spans, Span{Text: fmt.Sprintf("  decode %.2f tok/s", run.DecodeMean), Role: RoleDefault})
	}
	if run.TTFTPresent && run.TTFTMean > 0 {
		spans = append(spans, Span{Text: fmt.Sprintf("  TTFT %.2f s", run.TTFTMean), Role: RoleDefault})
	}
	if run.MemoryPresent {
		spans = append(spans, Span{Text: fmt.Sprintf("  resident %.2f GB", run.MemoryGB), Role: RoleDefault})
	}
	if run.Context > 0 {
		spans = append(spans, Span{Text: glyphs.Dot + "ctx " + shortContext(run.Context), Role: RoleMuted})
	}
	passes := 0
	for _, verdict := range run.Verdicts {
		if strings.EqualFold(strings.TrimSpace(verdict.State), "PASS") {
			passes++
		}
	}
	if passes > 0 {
		spans = append(spans, Span{Text: fmt.Sprintf("  %d pass", passes), Role: RolePass})
	}
	return spans
}

func wideBoardList(groups []BoardGroup, state State, width int, glyphs Glyphs) [][]Span {
	lines := [][]Span{{
		{Text: "configurations", Role: RoleHeader},
		{Text: "  comparable evidence only", Role: RoleMuted},
	}, {
		{Text: "  MODEL / BUILD", Role: RoleMuted},
		{Text: "                         RELATIVE   TOK/S", Role: RoleMuted},
	}}
	offset := state.Offset[ViewBoard]
	rowIndex := 0
	for _, group := range groups {
		groupWritten := false
		ceiling := maxRunDecode(group.Runs)
		for _, run := range group.Runs {
			if rowIndex < offset {
				rowIndex++
				continue
			}
			if !groupWritten {
				lines = append(lines, []Span{{Text: clipCells(group.Title, width, glyphs.Ellipsis), Role: RoleHeader}})
				if group.Note != "" {
					role := RoleMuted
					if !group.Comparable {
						role = RoleWarning
					}
					lines = append(lines, []Span{{Text: "  " + clipCells(group.Note, max(width-2, 1), glyphs.Ellipsis), Role: role}})
				}
				groupWritten = true
			}
			selected := run.ID == state.Selected[ViewBoard]
			row := boardRowSpans(run, selected, ceiling, width, glyphs)
			if len(run.Serves) > 0 {
				role := RolePass
				if selected {
					role = RoleSelected
				}
				row = append(row, Span{Text: fmt.Sprintf("  %d %s", len(run.Serves),
					countLabel(len(run.Serves), "need", "needs")), Role: role})
			}
			lines = append(lines, row)
			rowIndex++
		}
	}
	return lines
}

func wideBoardDetail(run Run, selected bool, width int, glyphs Glyphs) [][]Span {
	lines := [][]Span{{{Text: "selected evidence", Role: RoleHeader}}}
	if !selected {
		return append(lines, []Span{{Text: "No saved evidence selected.", Role: RoleMuted}})
	}
	lines = append(lines,
		[]Span{{Text: clipCells(run.Model, width, glyphs.Ellipsis), Role: RoleAccent}},
		[]Span{{Text: clipCells(strings.TrimSpace(run.ParamSize+" "+run.Quant+glyphs.Dot+"ctx "+shortContext(run.Context)), width, glyphs.Ellipsis), Role: RoleMuted}},
	)
	if identity := runArtifactLine(run, glyphs); identity != "" {
		lines = append(lines, []Span{{Text: clipCells(identity, width, glyphs.Ellipsis), Role: RoleMuted}})
	}
	if len(run.Serves) > 0 {
		lines = append(lines, []Span{{Text: "established  ", Role: RoleMuted}, {
			Text: clipCells(strings.Join(run.Serves, ", "), max(width-13, 1), glyphs.Ellipsis), Role: RolePass,
		}})
	}
	if why := boardWhyNot(run); why != "" {
		lines = append(lines, []Span{{Text: "why not     ", Role: RoleWarning}, {
			Text: clipCells(why, max(width-12, 1), glyphs.Ellipsis), Role: RoleDefault,
		}})
	}
	lines = append(lines, []Span{{Text: "requirements", Role: RoleHeader}})
	for _, verdict := range orderedBoardVerdicts(run.Verdicts) {
		label := verdict.Label
		if label == "" {
			label = verdict.Need
		}
		lines = append(lines, []Span{
			{Text: fmt.Sprintf("[%-4s] ", shortVerdictState(verdict.State)), Role: verdictRole(verdict.State)},
			{Text: clipCells(label, max(width-7, 1), glyphs.Ellipsis), Role: RoleDefault},
		})
		if len(lines) >= 10 {
			break
		}
	}
	lines = append(lines, []Span{{Text: "measurements", Role: RoleHeader}})
	if run.DecodePresent {
		lines = append(lines, []Span{{Text: "decode     ", Role: RoleMuted}, {Text: fmt.Sprintf("%.2f tok/s", run.DecodeMean), Role: RoleDefault}})
	}
	if run.TTFTPresent && run.TTFTMean > 0 {
		lines = append(lines, []Span{{Text: "TTFT       ", Role: RoleMuted}, {Text: fmt.Sprintf("%.2f s", run.TTFTMean), Role: RoleDefault}})
	}
	if run.MemoryPresent {
		lines = append(lines, []Span{{Text: "resident   ", Role: RoleMuted}, {Text: fmt.Sprintf("%.2f GB", run.MemoryGB), Role: RoleDefault}})
	}
	if run.NextCommand != "" {
		lines = append(lines,
			[]Span{{Text: "next", Role: RoleHeader}},
			[]Span{{Text: clipCells(run.NextCommand, width, glyphs.Ellipsis), Role: RoleAccent}},
		)
	}
	return lines
}

func boardWhyNot(run Run) string {
	for _, verdict := range orderedBoardVerdicts(run.Verdicts) {
		state := strings.ToUpper(strings.TrimSpace(verdict.State))
		if state != "FAIL" && state != "BLKD" {
			continue
		}
		label := verdict.Label
		if label == "" {
			label = verdict.Need
		}
		return label + " [" + shortVerdictState(verdict.State) + "]"
	}
	if run.Analysis != nil && len(run.Analysis.Diagnoses) > 0 {
		presented := analysis.PresentDiagnosis(run.Analysis.Diagnoses[0])
		return presented.Support + " · " + presented.Label
	}
	return ""
}

func orderedBoardVerdicts(verdicts []Verdict) []Verdict {
	ordered := slices.Clone(verdicts)
	slices.SortStableFunc(ordered, func(a, b Verdict) int {
		return verdictPriority(a.State) - verdictPriority(b.State)
	})
	return ordered
}

func verdictPriority(state string) int {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "FAIL":
		return 0
	case "BLKD":
		return 1
	case "INCONCLUSIVE":
		return 2
	default:
		return 3
	}
}

func shortVerdictState(state string) string {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "INCONCLUSIVE":
		return "INCL"
	case "ESTABLISHED":
		return "PASS"
	case "DISPROVEN":
		return "FAIL"
	default:
		return strings.ToUpper(strings.TrimSpace(state))
	}
}

func countLabel(count int, singular, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
}

func boardSortArrow(sort Sort) string {
	if sort == SortModel || sort == SortMemory {
		return "↑"
	}
	return "↓"
}

func shortContext(context int) string {
	if context <= 0 {
		return "unknown"
	}
	if context%1024 == 0 {
		return fmt.Sprintf("%dK", context/1024)
	}
	return strconv.Itoa(context)
}

func renderBoardPanes(canvas *Canvas, start, end, leftWidth int, left, right [][]Span, glyphs Glyphs) {
	rows := max(len(left), len(right))
	for index := 0; index < rows && start+index < end; index++ {
		leftLine := paneLine(left, index)
		rightLine := paneLine(right, index)
		leftLine = fitPaneLine(leftLine, leftWidth)
		paddingRole := RoleDefault
		for _, span := range leftLine {
			if span.Role == RoleSelected {
				paddingRole = RoleSelected
				break
			}
		}
		used := spansWidth(leftLine)
		if used < leftWidth {
			leftLine = append(leftLine, Span{Text: strings.Repeat(" ", leftWidth-used), Role: paddingRole})
		}
		line := slices.Clone(leftLine)
		line = append(line, Span{Text: " " + glyphs.Vertical + " ", Role: RoleMuted})
		line = append(line, fitPaneLine(rightLine, max(canvas.Width-leftWidth-3, 1))...)
		canvas.SetLine(start+index, line...)
	}
}

func paneLine(lines [][]Span, index int) []Span {
	if index < 0 || index >= len(lines) {
		return nil
	}
	return slices.Clone(lines[index])
}

func fitPaneLine(spans []Span, width int) []Span {
	remaining := max(width, 0)
	result := make([]Span, 0, len(spans))
	for _, span := range spans {
		if remaining == 0 {
			break
		}
		text := singleLine(span.Text)
		text = clipCells(text, remaining, "...")
		if text == "" {
			continue
		}
		result = append(result, Span{Text: text, Role: span.Role})
		remaining -= displayWidth(text)
	}
	return result
}

func spansWidth(spans []Span) int {
	width := 0
	for _, span := range spans {
		width += displayWidth(span.Text)
	}
	return width
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
		spans = append(spans, Span{Text: fmt.Sprintf("  SD %-5.2f  N %-2d", run.DecodeSD, run.Repeats), Role: RoleMuted})
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
	if canvas.Width >= 120 && canvas.Height >= 22 {
		renderWideHistory(canvas, state, glyphs, runs)
		return
	}
	w.line(Span{Text: fmt.Sprintf("HISTORY  %d run(s)  sort %s%s", len(runs), state.HistorySort, filterSuffix(state.Filter)), Role: RoleHeader})
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

func renderWideHistory(canvas *Canvas, state State, glyphs Glyphs, runs []Run) {
	w := contentWriter(canvas)
	run, selected := selectedHistoryRun(runs, state.Selected[ViewHistory])
	w.line(
		Span{Text: fmt.Sprintf("HISTORY  %d run(s)", len(runs)), Role: RoleHeader},
		Span{Text: fmt.Sprintf("  sort %s%s", state.HistorySort, filterSuffix(state.Filter)), Role: RoleMuted},
	)
	w.line(Span{Text: strings.Repeat(glyphs.Horizontal, canvas.Width), Role: RoleMuted})
	leftWidth := max(min(canvas.Width*58/100, 72), 56)
	left := wideHistoryList(runs, state, leftWidth, glyphs)
	right := wideBoardDetail(run, selected, canvas.Width-leftWidth-3, glyphs)
	renderBoardPanes(canvas, w.y, w.end, leftWidth, left, right, glyphs)
}

func selectedHistoryRun(runs []Run, selectedID string) (Run, bool) {
	for _, run := range runs {
		if run.ID == selectedID {
			return run, true
		}
	}
	if len(runs) > 0 {
		return runs[0], true
	}
	return Run{}, false
}

func wideHistoryList(runs []Run, state State, width int, glyphs Glyphs) [][]Span {
	lines := [][]Span{{{Text: "runs", Role: RoleHeader}, {Text: "  newest first unless sorted", Role: RoleMuted}}}
	offset := min(state.Offset[ViewHistory], max(len(runs)-1, 0))
	modelWidth := max(width-36, 8)
	for index, run := range runs {
		if index < offset {
			continue
		}
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
		digest := analysis.ShortDigest(run.ArtifactDigest)
		line := prefix + baseline + " " + padCells(clipCells(run.Model, modelWidth, glyphs.Ellipsis), modelWidth)
		line += "  " + when
		if digest != "" {
			line += "  " + digest
		}
		lines = append(lines, []Span{{Text: clipCells(line, width, glyphs.Ellipsis), Role: role}})
	}
	return lines
}

func renderInventory(canvas *Canvas, state State, glyphs Glyphs) {
	w := contentWriter(canvas)
	items := VisibleInventory(state)
	w.line(Span{Text: "INVENTORY  installed models, not a ranking" + filterSuffix(state.Filter), Role: RoleHeader})
	observed := "observed " + state.Snapshot.UpdatedAt.UTC().Format("15:04:05Z") + "; r reload"
	w.line(Span{Text: observed, Role: RoleMuted})
	if state.Snapshot.InventoryWarning != "" {
		w.line(Span{Text: "warning  ", Role: RoleWarning}, Span{Text: state.Snapshot.InventoryWarning, Role: RoleDefault})
	}
	if len(items) == 0 {
		msg := "no models listed; start a runtime or pull a model"
		if strings.TrimSpace(state.Filter) != "" {
			msg = emptyFilterMessage(state)
		}
		w.line(Span{Text: msg, Role: RoleMuted})
		w.line(Span{Text: "unmeasured is a candidate, never a recommendation", Role: RoleMuted})
		return
	}
	w.line(Span{Text: "MODEL                    STATE        FIT        EVID      NEXT", Role: RoleMuted})
	offset := min(state.Offset[ViewInventory], max(len(items)-1, 0))
	for _, item := range items[offset:] {
		if !renderInventoryItem(w, item, item.ID == state.Selected[ViewInventory], canvas.Width, glyphs) {
			break
		}
	}
	w.line(Span{Text: "[L] loaded   EVID measured/serving, not artifact max   Enter opens a measured result", Role: RoleMuted})
}

func renderInventoryItem(w *lineWriter, item InventoryItem, selected bool, width int, glyphs Glyphs) bool {
	role, prefix := RoleDefault, "  "
	if selected {
		role, prefix = RoleSelected, glyphs.Selected+" "
	}
	name := item.Model
	if item.Loaded {
		name = "[L] " + name
	}
	line := prefix + padCells(clipCells(name, 22, glyphs.Ellipsis), 22)
	line += "  " + padCells(item.State, 12)
	line += padCells(inventoryFitLabel(item.Fit), 10)
	line += padCells(inventoryContextLabel(item.Ctx), 9)
	line += clipCells(item.Next, max(width-61, 10), glyphs.Ellipsis)
	if !w.line(Span{Text: line, Role: role}) {
		return false
	}
	if selected && item.Shape != "" &&
		!w.line(Span{Text: "    " + clipCells(item.Shape, max(width-6, 8), glyphs.Ellipsis), Role: RoleMuted}) {
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

func filterSuffix(filter string) string {
	if filter = strings.TrimSpace(filter); filter != "" {
		return fmt.Sprintf("  filter %q", filter)
	}
	return ""
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

func renderHelp(canvas *Canvas, state State, _ Glyphs) {
	w := contentWriter(canvas)
	w.line(Span{Text: "CURRENT VIEW  " + strings.ToUpper(state.View.String()), Role: RoleHeader})
	for _, item := range helpForView(state.View) {
		if !w.line(Span{Text: item, Role: RoleDefault}) {
			return
		}
	}
	w.line(Span{Text: "COMMON", Role: RoleHeader})
	items := []string{
		"1-5 / Tab        switch view",
		"h / l            previous / next view",
		"r                reload saved results",
		"Ctrl+L           force complete redraw",
		"? / F1           close help",
		"q                quit",
	}
	for _, item := range items {
		if !w.line(Span{Text: item, Role: RoleDefault}) {
			break
		}
	}
}

func helpForView(view View) []string {
	switch view {
	case ViewLive:
		return []string{"Space            pause / resume display updates", "Enter            open completed Result"}
	case ViewResult:
		return []string{
			"j / k            previous / next saved result",
			"Esc              return to History",
			"TTFT             request, loaded, cache-hit, and runtime-unloaded are different claims",
			"explain          support class is not a score; direct cites a sealed receipt",
			"capacity         weights, KV, availability, and observed resident stay separate",
		}
	case ViewBoard:
		return []string{
			"j / k, PgUp/Dn   move selection",
			"Enter            open selected evidence",
			"/                edit filter",
			"Ctrl+U           clear filter",
			"s                cycle group-local sort",
			"groups           bars and sort never cross a device or request-config block",
			"quant            recipe label, not file identity; digest distinguishes artifacts",
		}
	case ViewHistory:
		return []string{
			"j / k, PgUp/Dn   move selection",
			"Enter            open selected evidence",
			"Space            mark comparison baseline",
			"c                compare with baseline",
			"/                edit filter",
			"Ctrl+U           clear filter",
			"s                cycle sort",
			"compare          exact observations only; no statistical winner here",
		}
	case ViewInventory:
		return []string{
			"j / k, PgUp/Dn   move selection",
			"Enter            open measured evidence",
			"/                edit filter",
			"Ctrl+U           clear filter",
			"EVID             measured or serving context, not the artifact maximum",
			"unmeasured       a candidate, never a recommendation",
		}
	default:
		return nil
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
		spans = []Span{{Text: " error: ", Role: RoleFail}, {Text: state.Error, Role: RoleDefault},
			{Text: "  r retry  ? help  q quit", Role: RoleMuted}}
	default:
		if state.Comparison != nil {
			spans = []Span{{Text: " Esc/Enter/c close comparison  ? help  q quit", Role: RoleMuted}}
		} else {
			spans = footerForView(state)
		}
	}
	canvas.SetLine(canvas.Height-1, spans...)
}

func footerForView(state State) []Span {
	var text string
	switch state.View {
	case ViewLive:
		text = " Space pause  ? help  q quit"
		if state.Snapshot.Live.Completed {
			text = " Enter result  ? help  q quit"
		}
	case ViewResult:
		text = " Esc history  j/k result  ? help  q quit"
	case ViewBoard:
		text = " j/k move  Enter open  / filter  s sort  ? help  q quit"
	case ViewHistory:
		text = " Space baseline  c compare  Enter open  / filter  s sort  ? help  q quit"
	case ViewInventory:
		text = " j/k move  Enter evidence  / filter  ? help  q quit"
	}
	return []Span{{Text: text, Role: RoleMuted}}
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
	case "BLKD", "WARN":
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
