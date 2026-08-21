package top

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func testSnapshot() Snapshot {
	base := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	a := Run{ID: "a", Model: "alpha:8b", Family: "alpha", ParamSize: "8B", Quant: "Q4_K_M",
		DeviceID: "device-1", Device: "GPU one", DecodeMean: 12, DecodeSD: .4, Repeats: 3,
		StartedAt: base.Add(-time.Hour), DecodeSeries: []float64{11, 12, 13}, Serves: []string{"fast"}}
	b := Run{ID: "b", Model: "beta:7b", Family: "beta", ParamSize: "7B", Quant: "Q8_0",
		DeviceID: "device-2", Device: "GPU two", DecodeMean: 40, Repeats: 3,
		StartedAt: base.Add(-2 * time.Hour)}
	c := Run{ID: "c", Model: "charlie:9b", Family: "alpha", ParamSize: "9B", Quant: "Q6_K",
		DeviceID: "device-1", Device: "GPU one", DecodeMean: 20, Repeats: 5,
		StartedAt: base.Add(-3 * time.Hour)}
	return Snapshot{UpdatedAt: base,
		Board: []BoardGroup{
			{ID: "g1", Title: "GPU one | current config", Comparable: true, Runs: []Run{a, c}},
			{ID: "g2", Title: "GPU two | another config", Note: "not comparable", Runs: []Run{b}},
		},
		History: []Run{a, b, c},
	}
}

func apply(t *testing.T, state State, event Event) (State, []Effect) {
	t.Helper()
	next, effects := Update(state, event)
	return next, effects
}

func TestBoardSortStaysInsideGroups(t *testing.T) {
	state := NewState(testSnapshot())
	state.View = ViewBoard
	state.BoardSort = SortDecode
	groups := VisibleBoard(state)
	if len(groups) != 2 {
		t.Fatalf("got %d groups", len(groups))
	}
	if got := []string{groups[0].Runs[0].ID, groups[0].Runs[1].ID}; !slices.Equal(got, []string{"c", "a"}) {
		t.Fatalf("first group order = %v", got)
	}
	if groups[1].Runs[0].ID != "b" {
		t.Fatalf("second group changed boundary: %+v", groups[1].Runs)
	}
}

func TestSelectionUsesStableIDAcrossReload(t *testing.T) {
	state := NewState(testSnapshot())
	state.View = ViewHistory
	state = selectIndex(state, 1)
	want := state.Selected[ViewHistory]
	nextSnapshot := testSnapshot()
	slices.Reverse(nextSnapshot.History)
	next, _ := apply(t, state, SnapshotEvent{Snapshot: nextSnapshot})
	if next.Selected[ViewHistory] != want {
		t.Fatalf("selection moved from %q to %q", want, next.Selected[ViewHistory])
	}
}

func TestFilterEditingAndClearing(t *testing.T) {
	state := NewState(testSnapshot())
	state.View = ViewHistory
	state, _ = apply(t, state, InputEvent{Action: ActionFilter})
	state, _ = apply(t, state, InputEvent{Action: ActionText, Text: "beta"})
	if !state.EditingFilter || state.Filter != "beta" {
		t.Fatalf("filter state = %+v", state)
	}
	visible := VisibleHistory(state)
	if len(visible) != 1 || visible[0].ID != "b" || state.Selected[ViewHistory] != "b" {
		t.Fatalf("filtered history = %+v, selected %q", visible, state.Selected[ViewHistory])
	}
	state, _ = apply(t, state, InputEvent{Action: ActionOpen})
	state, _ = apply(t, state, InputEvent{Action: ActionBack})
	if state.Filter != "" || len(VisibleHistory(state)) != 3 {
		t.Fatalf("filter was not cleared: %q", state.Filter)
	}
}

func TestPauseBuffersLatestData(t *testing.T) {
	state := NewState(testSnapshot())
	state, _ = apply(t, state, InputEvent{Action: ActionPause})
	nextSnapshot := testSnapshot()
	nextSnapshot.History[0].Model = "updated"
	state, _ = apply(t, state, SnapshotEvent{Snapshot: nextSnapshot})
	state, _ = apply(t, state, LiveEvent{Live: Live{Active: true, Model: "live-model"}})
	if state.Snapshot.History[0].Model == "updated" || state.Snapshot.Live.Model == "live-model" {
		t.Fatal("paused state applied buffered data")
	}
	state, _ = apply(t, state, InputEvent{Action: ActionPause})
	if state.Snapshot.History[0].Model != "updated" || state.Snapshot.Live.Model != "live-model" {
		t.Fatal("unpause did not apply latest buffered data")
	}
	if state.PendingSnapshot != nil || state.PendingLive != nil {
		t.Fatal("pending data remained after unpause")
	}
}

func TestQuitConfirmationAndInterruptEffects(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Live = Live{Active: true, Model: "running"}
	state := NewState(snapshot)
	state, effects := apply(t, state, InputEvent{Action: ActionQuit})
	if !state.ConfirmQuit || len(effects) != 0 {
		t.Fatalf("quit should ask first: state=%+v effects=%+v", state, effects)
	}
	state, effects = apply(t, state, InputEvent{Action: ActionConfirm})
	if state.ConfirmQuit || !slices.Equal(effects, []Effect{{Kind: EffectCancelRun}, {Kind: EffectQuit}}) {
		t.Fatalf("confirm effects = %+v", effects)
	}

	state = NewState(snapshot)
	state, effects = apply(t, state, InputEvent{Action: ActionInterrupt})
	if !state.Interrupted || !slices.Equal(effects, []Effect{{Kind: EffectCancelRun}, {Kind: EffectQuit}}) {
		t.Fatalf("interrupt state=%+v effects=%+v", state, effects)
	}
}

func TestNavigationClampsAndKeepsSelectionVisible(t *testing.T) {
	state := NewState(testSnapshot())
	state.View = ViewHistory
	state.Height = 7
	for range 20 {
		state, _ = apply(t, state, InputEvent{Action: ActionDown})
	}
	if state.Selected[ViewHistory] == "" || state.Offset[ViewHistory] < 0 {
		t.Fatalf("invalid navigation state: %+v", state)
	}
	state, _ = apply(t, state, ResizeEvent{Width: 2, Height: 1})
	if state.Width != 2 || state.Height != 1 || state.Offset[ViewHistory] < 0 {
		t.Fatalf("invalid tiny resize: %+v", state)
	}
}

func TestSnapshotInputIsDefensivelyCopied(t *testing.T) {
	snapshot := testSnapshot()
	state := NewState(snapshot)
	snapshot.History[0].Model = "mutated"
	snapshot.Board[0].Runs[0].DecodeSeries[0] = 999
	if state.Snapshot.History[0].Model == "mutated" || state.Snapshot.Board[0].Runs[0].DecodeSeries[0] == 999 {
		t.Fatal("state aliases caller-owned snapshot")
	}
}

func TestCompletedSnapshotFocusesJustFinishedResultWithoutLeavingLive(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Live = Live{Active: true, RunID: "finished", Model: "new-model"}
	state := NewState(snapshot)

	completed := testSnapshot()
	completed.Live = Live{Completed: true, RunID: "finished", Model: "new-model"}
	completed.History = append(completed.History, Run{
		ID: "finished", Model: "new-model", StartedAt: completed.UpdatedAt,
	})
	state, _ = apply(t, state, SnapshotEvent{Snapshot: completed})

	if state.View != ViewLive {
		t.Fatalf("view = %s, want live", state.View)
	}
	if state.Selected[ViewHistory] != "finished" || state.Selected[ViewResult] != "finished" {
		t.Fatalf("history/result selection = %q/%q", state.Selected[ViewHistory], state.Selected[ViewResult])
	}
	state, _ = apply(t, state, InputEvent{Action: ActionOpen})
	if state.View != ViewResult {
		t.Fatalf("Enter view = %s, want result", state.View)
	}
}

func TestCompletedSnapshotFindsNewArtifactID(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Live = Live{Active: true, RunID: "session-id", Model: "new-model"}
	state := NewState(snapshot)

	completed := testSnapshot()
	completed.Live = Live{Completed: true, RunID: "session-id", Model: "new-model"}
	completed.History = append(completed.History, Run{
		ID: "artifact-id", Model: "new-model", StartedAt: completed.UpdatedAt,
	})
	state, _ = apply(t, state, SnapshotEvent{Snapshot: completed})

	if state.Selected[ViewResult] != "artifact-id" {
		t.Fatalf("result selection = %q, want artifact-id", state.Selected[ViewResult])
	}
}

func TestPausedCompletionFocusesResultWhenResumed(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Live = Live{Active: true, RunID: "finished"}
	state := NewState(snapshot)
	state, _ = apply(t, state, InputEvent{Action: ActionPause})

	completed := testSnapshot()
	completed.Live = Live{Completed: true, RunID: "finished"}
	completed.History = append(completed.History, Run{ID: "finished", StartedAt: completed.UpdatedAt})
	state, _ = apply(t, state, SnapshotEvent{Snapshot: completed})
	if state.View != ViewLive {
		t.Fatalf("paused snapshot changed view to %s", state.View)
	}
	state, _ = apply(t, state, InputEvent{Action: ActionPause})
	if state.View != ViewLive || state.Selected[ViewResult] != "finished" {
		t.Fatalf("resumed state view=%s result=%q", state.View, state.Selected[ViewResult])
	}
}

func TestPausedCompletionCannotBeOverwrittenByLateLiveUpdate(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Live = Live{Active: true, RunID: "finished", Sequence: 10}
	state := NewState(snapshot)
	state, _ = apply(t, state, InputEvent{Action: ActionPause})

	completed := testSnapshot()
	completed.Live = Live{Completed: true, Saved: true, RunID: "finished", Sequence: 12}
	completed.History = append(completed.History, Run{ID: "finished", StartedAt: completed.UpdatedAt})
	state, _ = apply(t, state, SnapshotEvent{Snapshot: completed})
	state, _ = apply(t, state, LiveEvent{Live: Live{Active: true, RunID: "finished", Sequence: 11}})
	state, _ = apply(t, state, InputEvent{Action: ActionPause})

	if !state.Snapshot.Live.Completed || state.Snapshot.Live.Active || !state.Snapshot.Live.Saved {
		t.Fatalf("late live update replaced completion: %+v", state.Snapshot.Live)
	}
}

func TestPresentationTypesHaveExplicitSnakeCaseJSONTags(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeFor[Snapshot](),
		reflect.TypeFor[Run](),
		reflect.TypeFor[BoardGroup](),
		reflect.TypeFor[Live](),
		reflect.TypeFor[LivePhase](),
		reflect.TypeFor[Verdict](),
	}
	for _, typ := range types {
		for i := range typ.NumField() {
			field := typ.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "" || name == "-" || name != strings.ToLower(name) || strings.ContainsAny(name, " -") {
				t.Errorf("%s.%s has invalid json tag %q", typ.Name(), field.Name, name)
			}
		}
	}
}

func TestBoardAndHistoryKeepIndependentSorts(t *testing.T) {
	state := NewState(testSnapshot())
	if state.BoardSort != SortDecode || state.HistorySort != SortRecent {
		t.Fatalf("defaults board=%s history=%s", state.BoardSort, state.HistorySort)
	}
	state.View = ViewHistory
	state, _ = apply(t, state, InputEvent{Action: ActionSort})
	if state.HistorySort != SortModel || state.BoardSort != SortDecode {
		t.Fatalf("after History sort board=%s history=%s", state.BoardSort, state.HistorySort)
	}
	state.View = ViewBoard
	state, _ = apply(t, state, InputEvent{Action: ActionSort})
	if state.BoardSort != SortPrefill || state.HistorySort != SortModel {
		t.Fatalf("after Board sort board=%s history=%s", state.BoardSort, state.HistorySort)
	}
}

func TestHistoryBaselineComparisonAndExactMismatch(t *testing.T) {
	snapshot := Snapshot{History: []Run{
		{ID: "a", Model: "a", DeviceID: "device", HardwareID: "hardware", Context: 8192, DecodeMean: 10},
		{ID: "b", Model: "b", DeviceID: "device", HardwareID: "hardware", Context: 8192, DecodeMean: 12},
	}}
	state := NewState(snapshot)
	state.View = ViewHistory
	state.Selected[ViewHistory] = "a"
	state, _ = apply(t, state, InputEvent{Action: ActionPause})
	state, _ = apply(t, state, InputEvent{Action: ActionDown})
	state, _ = apply(t, state, InputEvent{Action: ActionCompare})
	if state.Comparison == nil || !state.Comparison.Compatible || state.Comparison.DecodeDiff != 2 {
		t.Fatalf("comparison = %+v", state.Comparison)
	}
	state, _ = apply(t, state, InputEvent{Action: ActionBack})
	state.Snapshot.History[1].DeviceID = "other-context"
	state.Snapshot.History[1].Context = 4096
	state, _ = apply(t, state, InputEvent{Action: ActionCompare})
	if state.Comparison == nil || state.Comparison.Compatible || !strings.Contains(state.Comparison.Reason, "request context differs") {
		t.Fatalf("mismatch comparison = %+v", state.Comparison)
	}
}

func TestFilterEscapeRestoresPreviousValue(t *testing.T) {
	state := NewState(testSnapshot())
	state.View = ViewHistory
	state.Filter = "old"
	state, _ = apply(t, state, InputEvent{Action: ActionFilter})
	state, _ = apply(t, state, InputEvent{Action: ActionText, Text: "new"})
	state, _ = apply(t, state, InputEvent{Action: ActionBack})
	if state.Filter != "old" || state.EditingFilter {
		t.Fatalf("filter=%q editing=%v", state.Filter, state.EditingFilter)
	}
}

func TestStaleSnapshotAndErrorGenerationsAreIgnoredWhilePaused(t *testing.T) {
	state := NewState(Snapshot{Generation: 2})
	state.View = ViewLive
	state.Snapshot.Live = Live{Active: true, RunID: "run", Sequence: 2}
	state, _ = apply(t, state, InputEvent{Action: ActionPause})
	state, _ = apply(t, state, SnapshotEvent{Snapshot: Snapshot{Generation: 4}})
	state, _ = apply(t, state, SnapshotEvent{Snapshot: Snapshot{Generation: 3}})
	if state.PendingSnapshot == nil || state.PendingSnapshot.Generation != 4 {
		t.Fatalf("pending generation = %+v", state.PendingSnapshot)
	}
	state, _ = apply(t, state, ErrorEvent{Err: errors.New("stale"), Generation: 3})
	if state.Error != "" {
		t.Fatalf("stale error applied: %q", state.Error)
	}
}
