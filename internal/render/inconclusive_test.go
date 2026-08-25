package render

import (
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/score"
)

// Every verdict occupies the same column. INCONCLUSIVE is 12 characters, so a
// row carrying it rendered 14 columns wide against every other row's 6 and
// pushed the labels out of line. A row that does not line up reads as an
// exception, and an exception reads as something wrong, which is the opposite
// of what an undecided row means.
func TestEveryVerdictTagIsTheSameWidth(t *testing.T) {
	width := -1
	for _, s := range []score.State{
		score.Pass, score.Fail, score.Skip, score.NA, score.Blocked, score.Inconclusive,
	} {
		got := len("[" + pad4(stateTag(s)) + "]")
		if width == -1 {
			width = got
		}
		if got != width {
			t.Fatalf("state %q renders %d columns, others render %d", s, got, width)
		}
	}
	if width != 6 {
		t.Fatalf("verdict column is %d columns wide, want 6", width)
	}
	// The stored state is unchanged: it is persisted in saved results and in
	// the JSON contract, so only the display form is short.
	if score.Inconclusive != "INCONCLUSIVE" {
		t.Fatalf("stored state changed to %q; saved results would stop reading back", score.Inconclusive)
	}
}

// An undecided row makes no claim, so it must not be coloured like one. Amber
// is measured to read as weak-green rather than "unknown", is the worst colour
// for the most common colour blindness, and already carries BLKD here.
func TestUndecidedIsNotColouredLikeAWarning(t *testing.T) {
	d := &textDisplay{pal: pickPalette(true), g: pickGlyphs()}
	if got := d.stateStyle(score.Inconclusive); got == d.pal.Warn {
		t.Fatal("undecided shares amber with BLKD; one channel cannot mean two things")
	}
	if got := d.stateStyle(score.Inconclusive); got != d.pal.Muted {
		t.Fatalf("undecided style = %q, want muted so it cannot shout", got)
	}
	// FAIL must stay distinct from undecided, or the two collapse.
	if d.stateStyle(score.Fail) == d.stateStyle(score.Inconclusive) {
		t.Fatal("FAIL and undecided render identically")
	}
}

// Colour is decoration. With colour off, every verdict must still be readable
// and distinct, which is WCAG 1.4.1 and Section 508 302.3 applied to a CLI.
func TestVerdictsAreDistinguishableWithoutColour(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range []score.State{
		score.Pass, score.Fail, score.Skip, score.NA, score.Blocked, score.Inconclusive,
	} {
		tag := strings.TrimSpace(stateTag(s))
		if tag == "" {
			t.Fatalf("state %q has no text form", s)
		}
		if seen[tag] {
			t.Fatalf("state %q reuses the tag %q", s, tag)
		}
		seen[tag] = true
	}
}

func pad4(s string) string {
	for len(s) < 4 {
		s += " "
	}
	return s
}
