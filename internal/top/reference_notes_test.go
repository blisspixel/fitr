package top

import (
	"strings"
	"testing"
)

func TestHistoryShowsAnExternalReferenceNote(t *testing.T) {
	state := NewState(Snapshot{
		History:        []Run{{ID: "run-a", Model: "qwen"}},
		ReferenceNotes: []string{"external reference aaaaaaaa vs bbbbbbbb: 1 discordant (1 left-only, 0 right-only), 1 excluded; not a fit or performance claim"},
	})
	state.View = ViewHistory
	// Wide history starts at width 120. The pair sentence is longer than that
	// row, and every history line clips to the canvas, so the fixture is wide
	// enough for the whole claim to stay visible.
	state.Width = 180
	state.Height = 24
	plain := Render(state, DefaultGlyphs(false)).Plain()
	if !strings.Contains(plain, "1 discordant") || !strings.Contains(plain, "not a fit or performance claim") {
		t.Fatalf("history hid the reference pair:\n%s", plain)
	}
}

func TestHistoryWithoutReferenceNotesStaysUnchanged(t *testing.T) {
	state := NewState(Snapshot{History: []Run{{ID: "run-a", Model: "qwen"}}})
	state.View = ViewHistory
	plain := Render(state, DefaultGlyphs(false)).Plain()
	if strings.Contains(plain, "external reference") {
		t.Fatalf("empty reference notes were drawn:\n%s", plain)
	}
}
