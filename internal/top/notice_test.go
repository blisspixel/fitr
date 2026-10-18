package top

import (
	"strings"
	"testing"
)

func TestTUIInventoryUsesTheSameNoticeWords(t *testing.T) {
	suggest := renderInventoryPlain(t, "fitr suggest names models for this machine before a download")
	if noticeWord(suggest, "fitr suggest names") != "tip" {
		t.Fatalf("suggest inventory:\n%s", suggest)
	}
	trusted := renderInventoryPlain(t, "1 saved result file could not be trusted")
	if noticeWord(trusted, "could not be trusted") != "warning" {
		t.Fatalf("trusted inventory:\n%s", trusted)
	}
	assertLinesFit(t, suggest, 60)
	assertLinesFit(t, trusted, 60)
}

func TestTUILiveUsesErrorAndCancelledWords(t *testing.T) {
	failed := renderLivePlain(t, Live{
		Active: true, Model: "model", Phase: "checks",
		Error: "the runtime closed the connection before the run finished",
	})
	if noticeWord(failed, "the runtime closed") != "error" {
		t.Fatalf("live error:\n%s", failed)
	}
	stopped := renderLivePlain(t, Live{
		Active: true, Cancelled: true, Model: "model", Phase: "checks",
		Error: "stopped by the operator",
	})
	if noticeWord(stopped, "stopped by the operator") != "cancelled" {
		t.Fatalf("cancelled run:\n%s", stopped)
	}
	if strings.Contains(stopped, "\nerror") || strings.HasPrefix(strings.TrimSpace(stopped), "error") {
		t.Fatalf("cancellation was relabeled as an error:\n%s", stopped)
	}
	assertLinesFit(t, failed, 60)
}

func TestTUIResultWarningKeepsTheWarningWord(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.History[0].Warnings = []string{"thin evidence"}
	state := NewState(snapshot)
	state.View = ViewResult
	state.Selected[ViewResult] = snapshot.History[0].ID
	state.Width, state.Height = 80, 40
	got := Render(state, DefaultGlyphs(true)).Plain()
	if noticeWord(got, "thin evidence") != "warning" {
		t.Fatalf("result warning:\n%s", got)
	}
}

func renderInventoryPlain(t *testing.T, warning string) string {
	t.Helper()
	state := NewState(testSnapshot())
	state.View = ViewInventory
	state.Width, state.Height = 60, 24
	state.Snapshot.InventoryWarning = warning
	return Render(state, DefaultGlyphs(true)).Plain()
}

func renderLivePlain(t *testing.T, live Live) string {
	t.Helper()
	snapshot := testSnapshot()
	snapshot.Live = live
	state := NewState(snapshot)
	state.View = ViewLive
	state.Width, state.Height = 60, 24
	return Render(state, DefaultGlyphs(true)).Plain()
}

func noticeWord(text, fragment string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, fragment) {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				return ""
			}
			return fields[0]
		}
	}
	return ""
}

func assertLinesFit(t *testing.T, text string, width int) {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if n := len([]rune(line)); n > width {
			t.Errorf("line is %d cols: %q", n, line)
		}
	}
}
