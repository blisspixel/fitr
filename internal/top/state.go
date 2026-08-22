package top

import (
	"slices"
	"sort"
	"strings"
)

// Update applies one event and returns the next state plus boundary effects.
// It performs no I/O and does not mutate slices owned by the caller.
func Update(state State, event Event) (State, []Effect) {
	s := state
	var effects []Effect
	change := func() { s.Revision++ }

	switch e := event.(type) {
	case SnapshotEvent:
		next := cloneSnapshot(e.Snapshot)
		latestGeneration := s.Snapshot.Generation
		if s.PendingSnapshot != nil {
			latestGeneration = max(latestGeneration, s.PendingSnapshot.Generation)
		}
		if next.Generation > 0 && latestGeneration > next.Generation {
			return s, nil
		}
		completedID := completedRunID(s.Snapshot, next)
		if next.Live.RunID == "" && !next.Live.Active && !next.Live.Completed && s.Snapshot.Live.RunID != "" {
			next.Live = cloneLive(s.Snapshot.Live)
		}
		if s.Paused {
			if s.PendingLive != nil && s.PendingLive.RunID == next.Live.RunID && s.PendingLive.Sequence <= next.Live.Sequence {
				s.PendingLive = nil
			}
			s.PendingSnapshot = &next
		} else {
			s.Snapshot = next
			s.Error = ""
		}
		normalizeSelection(&s)
		if !s.Paused && completedID != "" {
			s.Selected[ViewHistory] = completedID
			s.Selected[ViewResult] = completedID
		}
		change()
	case LiveEvent:
		next := cloneLive(e.Live)
		latestLive := s.Snapshot.Live
		if s.PendingSnapshot != nil && s.PendingSnapshot.Live.RunID == next.RunID && s.PendingSnapshot.Live.Sequence > latestLive.Sequence {
			latestLive = s.PendingSnapshot.Live
		}
		if s.PendingLive != nil && s.PendingLive.RunID == next.RunID && s.PendingLive.Sequence > latestLive.Sequence {
			latestLive = *s.PendingLive
		}
		if next.RunID != "" && next.RunID == latestLive.RunID && next.Sequence > 0 && next.Sequence < latestLive.Sequence {
			return s, nil
		}
		if s.Paused {
			s.PendingLive = &next
		} else {
			s.Snapshot.Live = next
		}
		change()
	case TickEvent:
		if s.Snapshot.Live.Active && !s.Paused && !e.Now.IsZero() && !e.Now.Equal(s.Now) {
			s.Now = e.Now
			change()
		}
	case ResizeEvent:
		width, height := max(e.Width, 1), max(e.Height, 1)
		if width != s.Width || height != s.Height {
			s.Width, s.Height = width, height
			clampOffset(&s, s.View)
			change()
		}
	case ErrorEvent:
		latestGeneration := s.Snapshot.Generation
		if s.PendingSnapshot != nil {
			latestGeneration = max(latestGeneration, s.PendingSnapshot.Generation)
		}
		if e.Generation > 0 && latestGeneration > e.Generation {
			return s, nil
		}
		if e.Err != nil {
			s.Error = Sanitize(e.Err.Error())
			change()
		}
	case InputEvent:
		var changed bool
		s, effects, changed = applyInput(s, e)
		if changed {
			change()
		}
	}
	return s, effects
}

// completedRunID recognizes the saved result created by a live-to-complete
// transition. RunID is authoritative when present. A newly added history ID
// is the fallback for producers that assign the saved artifact ID only when
// persisting the final snapshot.
func completedRunID(previous, next Snapshot) string {
	transitioning := previous.Live.Active && !previous.Live.Completed
	terminalEventArrived := previous.Live.Completed && previous.Live.RunID != "" && previous.Live.RunID == next.Live.RunID
	if (!transitioning && !terminalEventArrived) || !next.Live.Completed {
		return ""
	}
	if next.Live.RunID != "" {
		for _, run := range next.History {
			if run.ID == next.Live.RunID {
				return run.ID
			}
		}
	}
	known := make(map[string]struct{}, len(previous.History))
	for _, run := range previous.History {
		known[run.ID] = struct{}{}
	}
	var newest *Run
	for i := range next.History {
		run := &next.History[i]
		if run.ID == "" {
			continue
		}
		if _, exists := known[run.ID]; exists {
			continue
		}
		if newest == nil || run.StartedAt.After(newest.StartedAt) ||
			(run.StartedAt.Equal(newest.StartedAt) && run.ID < newest.ID) {
			newest = run
		}
	}
	if newest != nil {
		return newest.ID
	}
	return ""
}

func applyInput(s State, event InputEvent) (State, []Effect, bool) { //nolint:gocyclo
	var effects []Effect
	if s.ConfirmQuit {
		switch event.Action {
		case ActionConfirm:
			s.ConfirmQuit = false
			if s.Snapshot.Live.Active && !s.Snapshot.Live.Completed {
				effects = append(effects, Effect{Kind: EffectCancelRun})
			}
			effects = append(effects, Effect{Kind: EffectQuit})
			return s, effects, true
		case ActionReject, ActionBack, ActionQuit:
			s.ConfirmQuit = false
			return s, effects, true
		default:
			return s, effects, false
		}
	}

	if s.EditingFilter {
		switch event.Action {
		case ActionText:
			text := Sanitize(event.Text)
			if text != "" {
				s.Filter += text
				normalizeSelection(&s)
				return s, nil, true
			}
		case ActionBackspace:
			r := []rune(s.Filter)
			if len(r) > 0 {
				s.Filter = string(r[:len(r)-1])
				normalizeSelection(&s)
				return s, nil, true
			}
		case ActionClearFilter:
			if s.Filter != "" {
				s.Filter = ""
				normalizeSelection(&s)
				return s, nil, true
			}
		case ActionOpen, ActionConfirm:
			s.EditingFilter = false
			s.FilterBeforeEdit = ""
			return s, nil, true
		case ActionBack, ActionReject:
			s.EditingFilter = false
			s.Filter = s.FilterBeforeEdit
			s.FilterBeforeEdit = ""
			normalizeSelection(&s)
			return s, nil, true
		}
		return s, nil, false
	}

	if s.Help {
		switch event.Action {
		case ActionHelp, ActionBack, ActionReject, ActionOpen:
			s.Help = false
			return s, nil, true
		case ActionQuit:
			s.Help = false
			return s, nil, true
		default:
			return s, nil, false
		}
	}
	if s.Comparison != nil {
		switch event.Action {
		case ActionBack, ActionOpen, ActionCompare, ActionReject:
			s.Comparison = nil
			return s, nil, true
		}
	}

	switch event.Action {
	case ActionViewLive:
		return switchView(s, ViewLive), nil, s.View != ViewLive
	case ActionViewResult:
		return switchView(s, ViewResult), nil, s.View != ViewResult
	case ActionViewBoard:
		return switchView(s, ViewBoard), nil, s.View != ViewBoard
	case ActionViewHistory:
		return switchView(s, ViewHistory), nil, s.View != ViewHistory
	case ActionViewInventory:
		return switchView(s, ViewInventory), nil, s.View != ViewInventory
	case ActionNextView:
		next := View((int(s.View) + 1) % int(viewCount))
		return switchView(s, next), nil, true
	case ActionPrevView:
		next := View((int(s.View) + int(viewCount) - 1) % int(viewCount))
		return switchView(s, next), nil, true
	case ActionUp:
		return moveSelection(s, -1), nil, movePossible(s, -1)
	case ActionDown:
		return moveSelection(s, 1), nil, movePossible(s, 1)
	case ActionPageUp:
		return moveSelection(s, -pageSize(s)), nil, len(orderedIDs(s, s.View)) > 0
	case ActionPageDown:
		return moveSelection(s, pageSize(s)), nil, len(orderedIDs(s, s.View)) > 0
	case ActionHome:
		return selectIndex(s, 0), nil, len(orderedIDs(s, s.View)) > 0
	case ActionEnd:
		ids := orderedIDs(s, s.View)
		return selectIndex(s, len(ids)-1), nil, len(ids) > 0
	case ActionOpen:
		if s.View == ViewLive && s.Snapshot.Live.Completed && s.Selected[ViewResult] != "" {
			s.View = ViewResult
			return s, nil, true
		}
		if s.View == ViewBoard || s.View == ViewHistory {
			id := s.Selected[s.View]
			if id != "" {
				s.Selected[ViewResult] = id
				s.View = ViewResult
				return s, nil, true
			}
		}
		if s.View == ViewInventory {
			id := s.Selected[ViewInventory]
			if run, ok := inventoryRun(s, id); ok {
				s.Selected[ViewResult] = run.ID
				s.View = ViewResult
				return s, nil, true
			}
		}
	case ActionBack:
		if s.Filter != "" {
			s.Filter = ""
			normalizeSelection(&s)
			return s, nil, true
		}
		if s.View == ViewResult {
			s.View = ViewHistory
			return s, nil, true
		}
	case ActionFilter:
		if s.View == ViewBoard || s.View == ViewHistory || s.View == ViewInventory {
			s.FilterBeforeEdit = s.Filter
			s.EditingFilter = true
			return s, nil, true
		}
	case ActionSort:
		if s.View == ViewBoard {
			s.BoardSort = Sort((int(s.BoardSort) + 1) % int(sortCount))
			normalizeSelection(&s)
			return s, nil, true
		}
		if s.View == ViewHistory {
			s.HistorySort = Sort((int(s.HistorySort) + 1) % int(sortCount))
			normalizeSelection(&s)
			return s, nil, true
		}
	case ActionPause:
		if s.View == ViewHistory {
			selected := s.Selected[ViewHistory]
			if selected == "" {
				return s, nil, false
			}
			if s.BaselineID == selected {
				s.BaselineID = ""
			} else {
				s.BaselineID = selected
			}
			s.Comparison = nil
			s.Error = ""
			return s, nil, true
		}
		if s.View != ViewLive {
			return s, nil, false
		}
		s.Paused = !s.Paused
		if !s.Paused {
			completedID := ""
			if s.PendingSnapshot != nil {
				completedID = completedRunID(s.Snapshot, *s.PendingSnapshot)
				s.Snapshot = cloneSnapshot(*s.PendingSnapshot)
				s.PendingSnapshot = nil
			}
			if s.PendingLive != nil {
				s.Snapshot.Live = cloneLive(*s.PendingLive)
				s.PendingLive = nil
			}
			normalizeSelection(&s)
			if completedID != "" {
				s.Selected[ViewHistory] = completedID
				s.Selected[ViewResult] = completedID
			}
		}
		return s, nil, true
	case ActionCompare:
		if s.View != ViewHistory {
			return s, nil, false
		}
		selected := s.Selected[ViewHistory]
		if s.BaselineID == "" {
			s.Error = "mark a History baseline with Space first"
			return s, nil, true
		}
		if selected == "" || selected == s.BaselineID {
			s.Error = "select a different History run to compare"
			return s, nil, true
		}
		baseline, baselineOK := FindRun(s.Snapshot, s.BaselineID)
		candidate, candidateOK := FindRun(s.Snapshot, selected)
		if !baselineOK || !candidateOK {
			s.Error = "one comparison run disappeared; reload and mark the baseline again"
			return s, nil, true
		}
		comparison := compareRuns(baseline, candidate)
		s.Comparison = &comparison
		s.Error = ""
		return s, nil, true
	case ActionReload:
		return s, []Effect{{Kind: EffectReload}}, false
	case ActionHelp:
		s.Help = true
		return s, nil, true
	case ActionQuit:
		if s.Snapshot.Live.Active && !s.Snapshot.Live.Completed {
			s.ConfirmQuit = true
			return s, nil, true
		}
		return s, []Effect{{Kind: EffectQuit}}, false
	case ActionConfirm:
		return s, nil, false
	case ActionReject:
		return s, nil, false
	case ActionInterrupt:
		s.Interrupted = true
		if s.Snapshot.Live.Active && !s.Snapshot.Live.Completed {
			effects = append(effects, Effect{Kind: EffectCancelRun})
		}
		effects = append(effects, Effect{Kind: EffectQuit})
		return s, effects, true
	case ActionRedraw:
		return s, []Effect{{Kind: EffectRedraw}}, false
	}
	return s, effects, false
}

func switchView(s State, view View) State {
	s.View = view
	normalizeSelection(&s)
	clampOffset(&s, view)
	return s
}

func movePossible(s State, delta int) bool {
	ids := orderedIDs(s, s.View)
	if len(ids) == 0 {
		return false
	}
	idx := slices.Index(ids, s.Selected[s.View])
	if idx < 0 {
		idx = 0
	}
	return min(max(idx+delta, 0), len(ids)-1) != idx
}

func moveSelection(s State, delta int) State {
	ids := orderedIDs(s, s.View)
	if len(ids) == 0 {
		return s
	}
	idx := slices.Index(ids, s.Selected[s.View])
	if idx < 0 {
		idx = 0
	}
	return selectIndex(s, min(max(idx+delta, 0), len(ids)-1))
}

func selectIndex(s State, idx int) State {
	ids := orderedIDs(s, s.View)
	if len(ids) == 0 {
		return s
	}
	idx = min(max(idx, 0), len(ids)-1)
	s.Selected[s.View] = ids[idx]
	page := pageSize(s)
	if idx < s.Offset[s.View] {
		s.Offset[s.View] = idx
	} else if idx >= s.Offset[s.View]+page {
		s.Offset[s.View] = idx - page + 1
	}
	return s
}

func pageSize(s State) int { return max(s.Height-6, 1) }

func clampOffset(s *State, view View) {
	ids := orderedIDs(*s, view)
	maxOffset := max(len(ids)-pageSize(*s), 0)
	s.Offset[view] = min(max(s.Offset[view], 0), maxOffset)
}

func normalizeSelection(s *State) {
	for view := ViewLive; view < viewCount; view++ {
		ids := orderedIDs(*s, view)
		if len(ids) == 0 {
			s.Selected[view] = ""
			s.Offset[view] = 0
			continue
		}
		if !slices.Contains(ids, s.Selected[view]) {
			s.Selected[view] = ids[0]
			s.Offset[view] = 0
		}
		clampOffset(s, view)
	}
	if s.Selected[ViewResult] == "" {
		if id := s.Selected[ViewHistory]; id != "" {
			s.Selected[ViewResult] = id
		} else {
			s.Selected[ViewResult] = s.Selected[ViewBoard]
		}
	}
	if s.BaselineID != "" {
		if _, ok := FindRun(s.Snapshot, s.BaselineID); !ok {
			s.BaselineID = ""
			s.Comparison = nil
		}
	}
}

func compareRuns(baseline, selected Run) Comparison {
	comparison := Comparison{
		BaselineID: baseline.ID, SelectedID: selected.ID,
		BaselineModel: baseline.Model, SelectedModel: selected.Model,
		DecodeA: baseline.DecodeMean, DecodeB: selected.DecodeMean,
		DecodeDiff: selected.DecodeMean - baseline.DecodeMean,
		PrefillA:   baseline.PrefillMean, PrefillB: selected.PrefillMean,
		MemoryA: baseline.MemoryGB, MemoryB: selected.MemoryGB,
		Compatible: baseline.DeviceID != "" && baseline.DeviceID == selected.DeviceID,
	}
	if runInconclusive(baseline) || runInconclusive(selected) {
		comparison.Compatible = false
		comparison.Reason = "INCONCLUSIVE: evidence integrity does not support a direct comparison"
		return comparison
	}
	if comparison.Compatible {
		comparison.Reason = "same hardware, runtime, request context, and config"
		return comparison
	}
	switch {
	case baseline.HardwareID != "" && baseline.HardwareID == selected.HardwareID && baseline.Context != selected.Context:
		comparison.Reason = "request context differs; throughput and quality move with KV size"
	default:
		comparison.Reason = "hardware, runtime, or server config differs; a direct comparison would be misleading"
	}
	return comparison
}

func runInconclusive(run Run) bool {
	for _, verdict := range run.Verdicts {
		if strings.EqualFold(strings.TrimSpace(verdict.State), "INCONCLUSIVE") {
			return true
		}
	}
	return false
}

func orderedIDs(s State, view View) []string {
	switch view {
	case ViewBoard:
		groups := VisibleBoard(s)
		var ids []string
		for _, group := range groups {
			for _, run := range group.Runs {
				ids = append(ids, run.ID)
			}
		}
		return ids
	case ViewHistory:
		runs := VisibleHistory(s)
		ids := make([]string, 0, len(runs))
		for _, run := range runs {
			ids = append(ids, run.ID)
		}
		return ids
	case ViewInventory:
		items := VisibleInventory(s)
		ids := make([]string, 0, len(items))
		for _, item := range items {
			ids = append(ids, item.ID)
		}
		return ids
	case ViewResult:
		runs := VisibleHistory(s)
		ids := make([]string, 0, len(runs))
		seen := make(map[string]struct{}, len(runs))
		for _, run := range runs {
			ids = append(ids, run.ID)
			seen[run.ID] = struct{}{}
		}
		for _, group := range VisibleBoard(s) {
			for _, run := range group.Runs {
				if _, ok := seen[run.ID]; !ok {
					ids = append(ids, run.ID)
					seen[run.ID] = struct{}{}
				}
			}
		}
		return ids
	default:
		return nil
	}
}

// VisibleBoard returns filtered groups with rows sorted only inside their
// original comparison boundary.
func VisibleBoard(s State) []BoardGroup {
	groups := make([]BoardGroup, 0, len(s.Snapshot.Board))
	for _, source := range s.Snapshot.Board {
		group := source
		group.Runs = filterRuns(source.Runs, s.Filter)
		sortRuns(group.Runs, s.BoardSort)
		if len(group.Runs) > 0 {
			groups = append(groups, group)
		}
	}
	return groups
}

// VisibleHistory returns the filtered, sorted history.
func VisibleHistory(s State) []Run {
	runs := filterRuns(s.Snapshot.History, s.Filter)
	sortRuns(runs, s.HistorySort)
	return runs
}

func filterRuns(source []Run, query string) []Run {
	query = strings.ToLower(strings.TrimSpace(query))
	out := make([]Run, 0, len(source))
	for _, run := range source {
		if query == "" || runMatches(run, query) {
			out = append(out, cloneRun(run))
		}
	}
	return out
}

func runMatches(run Run, query string) bool {
	fields := []string{run.Model, run.Family, run.ParamSize, run.Quant, run.Device,
		run.Driver, run.Runtime, run.Config, run.Level, run.UseFor, strings.Join(run.Serves, " ")}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), query) {
			return true
		}
	}
	return false
}

func sortRuns(runs []Run, order Sort) {
	sort.SliceStable(runs, func(i, j int) bool {
		a, b := runs[i], runs[j]
		switch order {
		case SortModel:
			if c := strings.Compare(strings.ToLower(a.Model), strings.ToLower(b.Model)); c != 0 {
				return c < 0
			}
		case SortDecode:
			if a.DecodeMean != b.DecodeMean {
				return a.DecodeMean > b.DecodeMean
			}
		case SortPrefill:
			if a.PrefillMean != b.PrefillMean {
				return a.PrefillMean > b.PrefillMean
			}
		case SortMemory:
			if a.MemoryGB != b.MemoryGB {
				return a.MemoryGB < b.MemoryGB
			}
		case SortRepeats:
			if a.Repeats != b.Repeats {
				return a.Repeats > b.Repeats
			}
		default:
			if !a.StartedAt.Equal(b.StartedAt) {
				return a.StartedAt.After(b.StartedAt)
			}
		}
		return a.ID < b.ID
	})
}

// FindRun returns a defensive copy of a run from history or the board.
func FindRun(snapshot Snapshot, id string) (Run, bool) {
	for _, run := range snapshot.History {
		if run.ID == id {
			return cloneRun(run), true
		}
	}
	for _, group := range snapshot.Board {
		for _, run := range group.Runs {
			if run.ID == id {
				return cloneRun(run), true
			}
		}
	}
	return Run{}, false
}

func VisibleInventory(s State) []InventoryItem {
	items := make([]InventoryItem, 0, len(s.Snapshot.Inventory))
	filter := strings.ToLower(strings.TrimSpace(s.Filter))
	for _, item := range s.Snapshot.Inventory {
		if filter != "" && !strings.Contains(strings.ToLower(item.Model+" "+item.State+" "+item.Fit+" "+item.Ctx+" "+item.Windows+" "+item.Next), filter) {
			continue
		}
		items = append(items, item)
	}
	return items
}

func inventoryRun(s State, model string) (Run, bool) {
	if model == "" {
		return Run{}, false
	}
	for _, run := range s.Snapshot.History {
		if run.Model == model || strings.Contains(run.Model, model) {
			return run, true
		}
	}
	return Run{}, false
}

func cloneSnapshot(in Snapshot) Snapshot {
	out := in
	out.Live = cloneLive(in.Live)
	out.Board = make([]BoardGroup, len(in.Board))
	for i, group := range in.Board {
		out.Board[i] = group
		out.Board[i].Runs = cloneRuns(group.Runs)
	}
	out.History = cloneRuns(in.History)
	out.Inventory = slices.Clone(in.Inventory)
	return out
}

func cloneRuns(in []Run) []Run {
	out := make([]Run, len(in))
	for i := range in {
		out[i] = cloneRun(in[i])
	}
	return out
}

func cloneRun(in Run) Run {
	out := in
	out.DecodeSeries = slices.Clone(in.DecodeSeries)
	out.Serves = slices.Clone(in.Serves)
	out.Warnings = slices.Clone(in.Warnings)
	out.Verdicts = slices.Clone(in.Verdicts)
	return out
}

func cloneLive(in Live) Live {
	out := in
	out.DecodeSeries = slices.Clone(in.DecodeSeries)
	out.PrefillSeries = slices.Clone(in.PrefillSeries)
	out.TTFTSeries = slices.Clone(in.TTFTSeries)
	out.Warnings = slices.Clone(in.Warnings)
	out.Phases = slices.Clone(in.Phases)
	return out
}
