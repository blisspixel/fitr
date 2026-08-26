package render

import (
	"os"
	"strconv"
)

// DefaultWidth is the width every fitr report is composed for.
//
// It is a cap, not a target. On a 200-column terminal the report still renders
// at 80, which buys a property worth more than filling the screen: what you see
// on screen and what lands in `fitr run m > out.txt` are the same bytes, so a
// pasted result is the result. It is also the width WCAG 2.2 SC 1.4.8 asks for,
// for the ordinary reason that long lines make people lose their place.
const DefaultWidth = 80

// MinWidth is where fitting stops being possible. Below this the report drops
// its right-hand columns onto continuation lines rather than shredding.
const MinWidth = 40

// maxWidth bounds FITR_WIDTH. A width is used to allocate padding, so an
// unbounded value read from the environment is an allocation read from the
// environment.
const maxWidth = 400

// Width resolves the content width for terminal output.
//
// Order: FITR_WIDTH, then the terminal if stdout is one and it is narrower
// than the default, then the default. COLUMNS is deliberately absent: bash
// sets it as a shell variable without exporting it, so it is usually missing,
// and when present it is frequently stale after a resize.
func Width() int { return widthFor(os.Stdout) }

func widthFor(f *os.File) int {
	if v := os.Getenv("FITR_WIDTH"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= MinWidth {
			return min(n, maxWidth)
		}
	}
	// Only consult the terminal to narrow, never to widen. Widening would make
	// output depend on the window it was produced in.
	if isTTY(f) {
		if n, ok := terminalWidth(f); ok && n >= MinWidth && n < DefaultWidth {
			return n
		}
	}
	return DefaultWidth
}
