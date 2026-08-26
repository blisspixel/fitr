package render

import (
	"os"
	"strings"
	"testing"
)

func TestWidthPolicy(t *testing.T) {
	// Not a TTY under test, so the terminal probe is never consulted and the
	// default is the answer unless FITR_WIDTH says otherwise.
	f, err := os.CreateTemp(t.TempDir(), "w")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	cases := []struct {
		env  string
		want int
	}{
		{"", DefaultWidth},
		{"100", 100},
		{"40", 40},
		// Below the floor the report cannot lay out at all, so the override is
		// refused rather than honoured into an unreadable column plan.
		{"12", DefaultWidth},
		{"-1", DefaultWidth},
		{"not a number", DefaultWidth},
		// A width is used to allocate padding, so it is bounded.
		{"100000000", maxWidth},
	}
	for _, tc := range cases {
		t.Setenv("FITR_WIDTH", tc.env)
		if got := widthFor(f); got != tc.want {
			t.Errorf("FITR_WIDTH=%q: width = %d, want %d", tc.env, got, tc.want)
		}
	}
}

// A terminal wider than the default does not widen the report. Output that
// depends on the window it was produced in cannot be pasted or diffed, and the
// piped copy would stop matching what was on screen.
func TestWidthNeverExceedsTheDefaultFromTheTerminal(t *testing.T) {
	t.Setenv("FITR_WIDTH", "")
	if got := widthFor(os.Stdout); got > DefaultWidth {
		t.Fatalf("width = %d, want at most %d", got, DefaultWidth)
	}
}

func TestWrapNeverOverflows(t *testing.T) {
	long := "supercalifragilisticexpialidocious-and-then-some-more-characters-still"
	for _, width := range []int{1, 7, 20, 40, 80} {
		for _, text := range []string{
			"short",
			"the quick brown fox jumps over the lazy dog and keeps going for a while",
			long,
			long + " then ordinary words follow",
			"",
		} {
			for _, line := range wrap(text, width) {
				if n := len([]rune(line)); n > width {
					t.Errorf("width %d: line is %d runes: %q", width, n, line)
				}
			}
		}
	}
}

func TestWrapKeepsEveryWord(t *testing.T) {
	text := "ranges are what this run pins the true rate to, not the spread of scores"
	joined := strings.Join(wrap(text, 21), " ")
	if joined != text {
		t.Fatalf("wrap lost or reordered text:\n got %q\nwant %q", joined, text)
	}
}

// pad is the fix for the column that could be violated: %-34s pads a short
// value and silently lets a long one run into its neighbour.
func TestPadNeverOverflowsItsColumn(t *testing.T) {
	for _, s := range []string{"", "short", "leaves tools alone when they don't apply"} {
		got := pad(s, 12, "...")
		if n := len([]rune(got)); n != 12 {
			t.Errorf("pad(%q) = %q (%d runes), want exactly 12", s, got, n)
		}
	}
	if got := pad("x", 0, "..."); got != "" {
		t.Errorf("pad to zero = %q, want empty", got)
	}
}
