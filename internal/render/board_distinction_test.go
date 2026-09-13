package render

import (
	"strings"
	"testing"
)

func sameHeaderGroups() []BoardGroup {
	base := BoardGroup{
		GPU: "NVIDIA GeForce RTX 4090", Driver: "32.0.16.1047",
		NumCtx: 8192, EffectiveCtx: 8192, Note: "this machine",
		Rows: []BoardRow{{Model: "m", DecodeMean: 100, Repeats: 3}},
	}
	older, newer := base, base
	older.Runtime, older.ModelStore = "0.24.0", `C:\Users\x\.ollama\models`
	newer.Runtime, newer.ModelStore = "0.34.0", `E:\models`
	return []BoardGroup{older, newer}
}

// Refusing to rank across configurations only helps if the reader can tell
// which configuration each block is. These two render the same header and
// belong to different blocks, so the board has to say what separated them.
func TestIdenticalHeadersDiscloseWhatSeparatesThem(t *testing.T) {
	var out strings.Builder
	WriteBoard(&out, Board{Groups: sameHeaderGroups(), Results: 2}, "plain")
	text := out.String()
	for _, want := range []string{"runtime 0.24.0", "runtime 0.34.0", `E:\models`} {
		if !strings.Contains(text, want) {
			t.Fatalf("board did not disclose %q:\n%s", want, text)
		}
	}
	if strings.Count(text, "separated by") != 2 {
		t.Fatalf("both colliding blocks must be annotated:\n%s", text)
	}
}

// An ordinary board is unchanged. A note on every block would be noise, and
// noise is how a warning stops being read.
func TestDistinctHeadersGetNoExtraLine(t *testing.T) {
	groups := sameHeaderGroups()
	groups[1].GPU = "AMD Radeon RX 7900 XTX"
	var out strings.Builder
	WriteBoard(&out, Board{Groups: groups, Results: 2}, "plain")
	if strings.Contains(out.String(), "separated by") {
		t.Fatalf("distinct headers were annotated:\n%s", out.String())
	}
}

// When the sealed configuration differs in a way the group does not carry, the
// board still says the blocks are separate rather than looking duplicated.
func TestCollisionWithoutCarriedFieldsStillSaysSomething(t *testing.T) {
	groups := sameHeaderGroups()
	for i := range groups {
		groups[i].Runtime, groups[i].ModelStore = "", ""
	}
	var out strings.Builder
	WriteBoard(&out, Board{Groups: groups, Results: 2}, "plain")
	if !strings.Contains(out.String(), "sealed configuration this header does not show") {
		t.Fatalf("an unexplained collision said nothing:\n%s", out.String())
	}
}
