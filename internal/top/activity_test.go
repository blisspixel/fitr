package top

import (
	"testing"
	"time"
)

func TestLiveActivityTracksWorkAndFreezesWithViewport(t *testing.T) {
	start := time.Unix(100, 0)
	state := NewState(Snapshot{UpdatedAt: start, Live: Live{Active: true, StartedAt: start}})
	for i, want := range []string{" |", " /", " -", " \\", " |"} {
		state, _ = Update(state, TickEvent{Now: start.Add(time.Duration(i) * 250 * time.Millisecond)})
		if got := liveActivity(state); got != want {
			t.Fatalf("frame %d = %q, want %q", i, got, want)
		}
	}
	state.Paused = true
	before := state
	state, _ = Update(state, TickEvent{Now: start.Add(1250 * time.Millisecond)})
	if state.Revision != before.Revision || liveActivity(state) != liveActivity(before) {
		t.Fatal("paused viewport advanced")
	}
	state.Paused = false
	state, _ = Update(state, TickEvent{Now: start.Add(-time.Second)})
	if !state.Now.Equal(before.Now) {
		t.Fatal("clock moved backwards")
	}
}

func TestLiveActivityStopsForTerminalStatesAndReducedMotion(t *testing.T) {
	for _, live := range []Live{{}, {Active: true, Completed: true}, {Active: true, Cancelled: true}, {Active: true, Error: "failed"}} {
		state := NewState(Snapshot{Live: live})
		if got := liveActivity(state); got != "" {
			t.Fatalf("terminal state animated: %q", got)
		}
		next, _ := Update(state, TickEvent{Now: state.Now.Add(time.Second)})
		if next.Revision != state.Revision {
			t.Fatal("terminal state advanced on tick")
		}
	}
	state := NewState(Snapshot{Live: Live{Active: true}})
	state.ReducedMotion = true
	if liveActivity(state) != "" {
		t.Fatal("reduced motion animated")
	}
	initial := (App{Initial: State{ReducedMotion: true}}).initialState(newFakeScreen(80, 24))
	if !initial.ReducedMotion {
		t.Fatal("initialization discarded reduced motion")
	}
	if (App{Initial: state}).tickInterval() != time.Second || (App{}).tickInterval() != 250*time.Millisecond {
		t.Fatal("unexpected motion cadence")
	}
	next, _ := Update(state, TickEvent{Now: state.Now.Add(time.Second)})
	if !next.Now.After(state.Now) {
		t.Fatal("reduced motion must retain elapsed time updates")
	}
}
