package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/clipperhouse/displaywidth"
)

// wrap breaks already-sanitized text into lines of at most width display cells,
// preferring spaces. A word longer than the width is hard-split rather than
// allowed to overhang, because an overhanging line is what the terminal wraps
// for us at an arbitrary column, which is the failure this exists to prevent.
//
// Grapheme boundaries preserve combining marks. A grapheme wider than the
// entire available column is represented by ? rather than overflowing it.
func wrap(s string, width int) []string {
	if width < 1 {
		return nil
	}
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return nil
	}
	var lines []string
	cur := ""
	flush := func() {
		if cur != "" {
			lines = append(lines, cur)
			cur = ""
		}
	}
	for _, word := range fields {
		for displaywidth.String(word) > width {
			flush()
			part := displaywidth.TruncateString(word, width, "")
			if part == "" {
				graphemes := displaywidth.StringGraphemes(word)
				graphemes.Next()
				word = word[len(graphemes.Value()):]
				lines = append(lines, "?")
			} else {
				lines = append(lines, part)
				word = word[len(part):]
			}
		}
		switch {
		case cur == "":
			cur = word
		case displaywidth.String(cur)+1+displaywidth.String(word) <= width:
			cur += " " + word
		default:
			flush()
			cur = word
		}
	}
	flush()
	return lines
}

// Field prints one label/value pair with the value wrapped under its own
// column, so a long value hangs rather than running past the rule.
//
// Values are sanitized before wrapping. Style the label, not the value.
func Field(w io.Writer, label string, labelWidth int, value string, width int) {
	// The label is not sanitized: it is fitr's own text, and SingleLine would
	// trim the leading gutter that puts the whole block in from the margin.
	// The value is untrusted and is sanitized.
	lead := label
	if d := labelWidth - len([]rune(label)); d > 0 {
		lead += strings.Repeat(" ", d)
	}
	for i, l := range wrap(SingleLine(value), max(width-labelWidth, MinWidth-labelWidth)) {
		if i > 0 {
			lead = strings.Repeat(" ", labelWidth)
		}
		fmt.Fprintf(w, "%s%s\n", lead, l)
	}
}

// pad left-aligns s in a field of n runes, truncating rather than overhanging.
//
// The distinction matters: %-34s pads a short value and silently lets a long
// one push its neighbour out of the column, so one 39-character label ran into
// the text beside it with no separator. A column that can be violated is not a
// column.
func pad(s string, n int, ell string) string {
	s = fit(s, n, ell)
	if d := n - len([]rune(s)); d > 0 {
		s += strings.Repeat(" ", d)
	}
	return s
}
