package render

import (
	"fmt"
	"io"
	"strings"
)

// wrap breaks already-sanitized text into lines of at most width runes,
// preferring spaces. A word longer than the width is hard-split rather than
// allowed to overhang, because an overhanging line is what the terminal wraps
// for us at an arbitrary column, which is the failure this exists to prevent.
//
// It counts runes, not display cells. fitr's own text is ASCII; the only
// untrusted text reaching here has been through SingleLine, which strips
// controls and format runes. A wide CJK rune would still under-count, so the
// budget is spent conservatively elsewhere rather than by measuring here.
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
		for len([]rune(word)) > width {
			flush()
			r := []rune(word)
			lines = append(lines, string(r[:width]))
			word = string(r[width:])
		}
		switch {
		case cur == "":
			cur = word
		case len([]rune(cur))+1+len([]rune(word)) <= width:
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
// Callers pass already-styled text at their own risk: the wrap counts runes,
// and ANSI escapes are runes. Style the label, not the value.
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
