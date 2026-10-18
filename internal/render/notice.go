package render

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// NoticeLevel is one of four words. The word carries the meaning. Color only
// repeats it. hint stays the label for a copyable command after an error and
// is not a fifth level. INCONCLUSIVE stays a verdict word. Cancelled stays
// its own status word.
type NoticeLevel string

const (
	NoticeTip     NoticeLevel = "tip"
	NoticeNote    NoticeLevel = "note"
	NoticeWarning NoticeLevel = "warning"
	NoticeError   NoticeLevel = "error"
)

// NoticeGutter is the fixed column under which a notice hangs. It matches
// the inventory header so a tip, a note, a warning and an error share one
// left edge with cpu, gpu and runtime.
const NoticeGutter = invHeaderLabel

// NoticeLine is one wrapped row. Label is the gutter, empty of a word on
// every continuation. Body is the sanitized sentence.
type NoticeLine struct {
	Label string
	Body  string
}

// Word returns the fixed notice word. An unknown level fails closed to
// warning so a typo cannot invent a fifth word or print a blank gutter.
func (level NoticeLevel) Word() string {
	switch level {
	case NoticeTip, NoticeNote, NoticeWarning, NoticeError:
		return string(level)
	default:
		return string(NoticeWarning)
	}
}

// ClassifyInventoryNotice labels one inventory sentence for display.
// The JSON warnings array keeps the sentence. An unrecognized sentence
// stays a warning so a new caveat is not presented as optional advice.
// A trust or identity caveat outranks a tip that shares the same line.
func ClassifyInventoryNotice(text string) NoticeLevel {
	switch {
	case inventoryWarningCue(text):
		return NoticeWarning
	case strings.Contains(text, "mixture-of-experts"):
		return NoticeNote
	case inventoryTipCue(text):
		return NoticeTip
	default:
		return NoticeWarning
	}
}

func inventoryWarningCue(text string) bool {
	for _, cue := range []string{
		"could not be trusted",
		"damaged evidence",
		"could not be verified",
		"runtime status unavailable",
		"inventory unavailable",
	} {
		if strings.Contains(text, cue) {
			return true
		}
	}
	return false
}

func inventoryTipCue(text string) bool {
	switch {
	case strings.Contains(text, "fitr suggest names models for this machine before a download"):
		return true
	case strings.Contains(text, "start the installed runtime:"):
		return true
	case strings.Contains(text, "this GPU's Ollama directory"):
		return true
	case strings.Contains(text, "isolated execution"),
		strings.Contains(text, "bubblewrap"),
		strings.Contains(text, "sandbox"):
		return true
	default:
		return false
	}
}

// noticeGutter pads the word inside the inventory margin. The label is
// fitr's own text, so it must not pass through SingleLine.
func noticeGutter(word string) string {
	lead := "  " + word
	if extra := NoticeGutter - len([]rune(lead)); extra > 0 {
		lead += strings.Repeat(" ", extra)
	}
	return lead
}

// NoticeLines wraps one notice to width. The first gutter carries the word.
// Continuations are blank to the same column.
func NoticeLines(level NoticeLevel, text string, width int) []NoticeLine {
	return LabeledLines(level.Word(), text, width)
}

// LabeledLines wraps text under a caller-supplied gutter word. Cancelled
// uses it because a cancellation is its own disposition, not an error and
// not a warning.
func LabeledLines(word, text string, width int) []NoticeLine {
	text = SingleLine(strings.TrimSpace(text))
	word = SingleLine(strings.TrimSpace(word))
	if text == "" || word == "" {
		return nil
	}
	if width < NoticeGutter+1 {
		width = NoticeGutter + 1
	}
	parts := wrap(text, width-NoticeGutter)
	if len(parts) == 0 {
		return nil
	}
	// pad() runs SingleLine, which drops the leading gutter. Field keeps
	// the label raw for the same reason: the margin is ours, not user text.
	lead := noticeGutter(word)
	hang := strings.Repeat(" ", NoticeGutter)
	lines := make([]NoticeLine, len(parts))
	for i, part := range parts {
		label := hang
		if i == 0 {
			label = lead
		}
		lines[i] = NoticeLine{Label: label, Body: part}
	}
	return lines
}

// WriteNotice writes one notice. mode rich follows the terminal palette.
// plain, json, none, NO_COLOR and TERM=dumb keep the word and drop escapes.
func WriteNotice(w io.Writer, level NoticeLevel, text string, width int, mode string) {
	p := palette{}
	if Resolve(mode) == "rich" {
		p = pickPalette(!noColor())
	}
	writeColoredNotice(w, p, level, text, width)
}

func writeColoredNotice(w io.Writer, p palette, level NoticeLevel, text string, width int) {
	if os.Getenv("TERM") == "dumb" {
		p = palette{}
	}
	style := noticeColor(p, level)
	for _, line := range NoticeLines(level, text, width) {
		fmt.Fprintf(w, "%s%s\n", colorNoticeWord(line.Label, level.Word(), style), line.Body)
	}
}

func noticeColor(p palette, level NoticeLevel) string {
	switch level.Word() {
	case string(NoticeTip):
		return p.Muted
	case string(NoticeNote):
		return ""
	case string(NoticeError):
		return p.Fail
	default:
		return p.Warn
	}
}

// colorNoticeWord colors the word and leaves the gutter spaces and the
// sentence unstyled. The sentence has to stay readable when the word is
// already amber, dim or red.
func colorNoticeWord(label, word, style string) string {
	if style == "" || word == "" {
		return label
	}
	at := strings.Index(label, word)
	if at < 0 {
		return label
	}
	return label[:at] + style + word + reset + label[at+len(word):]
}
