package render

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/role"
)

// selectionLabelWidth matches the model, expires and next columns on role
// status. The fitted line hangs in that same gutter.
const selectionLabelWidth = 13

var signatureDatePattern = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)

// SelectionSignature projects the sentence role status can already prove.
// The status JSON has no operating window, so the line does not state one.
// measured is the selected run's start, then the selection's own evaluation.
// The status clock is when this review ran, and using it would move the
// measurement date every time someone asks.
func SelectionSignature(status role.SelectionStatus) (word, body string) {
	if status.Selection == nil {
		return "", ""
	}
	switch status.State {
	case "qualified":
		word = "fitted"
	case "stale":
		word = "refit"
	default:
		return "", ""
	}
	name := SingleLine(status.Role)
	model := SingleLine(status.Selection.Selected.Model.Resolved)
	if name == "" || model == "" {
		return "", ""
	}
	body = name + ": " + model
	measured := signatureDate(status.Selection.Selected.StartedAt)
	if measured == "" {
		measured = signatureDate(status.Selection.EvaluatedAt)
	}
	if measured != "" {
		body += ", measured " + measured
	}
	if review := signatureDate(status.Selection.ExpiresAt); review != "" {
		body += ", review by " + review
	}
	return word, body
}

// MatchingRunSignature reports the sentence only for the run the selection
// actually names. A model-name match is not that run.
func MatchingRunSignature(status role.SelectionStatus, runID string) (word, body string, ok bool) {
	word, body = SelectionSignature(status)
	if word == "" || runID == "" || status.Selection == nil || status.Selection.Selected.Attachment.RunID != runID {
		return "", "", false
	}
	return word, body, true
}

func writeSelectionSignature(w io.Writer, p palette, status role.SelectionStatus, width int) {
	word, body := SelectionSignature(status)
	if word == "" {
		return
	}
	style := p.Pass
	if word == "refit" {
		style = p.Warn
	}
	lines := wrap(SingleLine(body), max(width-selectionLabelWidth, MinWidth-selectionLabelWidth))
	gutter := "  " + word
	if padWidth := selectionLabelWidth - len([]rune(gutter)); padWidth > 0 {
		gutter += strings.Repeat(" ", padWidth)
	}
	for i, line := range lines {
		lead := strings.Repeat(" ", selectionLabelWidth)
		if i == 0 {
			lead = colorNoticeWord(gutter, word, style)
		}
		fmt.Fprintf(w, "%s%s\n", lead, colorSignatureDates(p, line))
	}
}

func signatureDate(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		if parsed, err = time.Parse(time.RFC3339, value); err != nil {
			return ""
		}
	}
	return parsed.UTC().Format("2006-01-02")
}

// SignaturePart is one piece of a fitted sentence. Date is true for a
// calendar day taken from a status timestamp.
type SignaturePart struct {
	Text string
	Date bool
}

// SignatureParts splits a wrapped signature line so a date can be dim
// without coloring the model name.
func SignatureParts(line string) []SignaturePart {
	var parts []SignaturePart
	rest := line
	for {
		loc := signatureDatePattern.FindStringIndex(rest)
		if loc == nil {
			if rest != "" {
				parts = append(parts, SignaturePart{Text: rest})
			}
			return parts
		}
		if loc[0] > 0 {
			parts = append(parts, SignaturePart{Text: rest[:loc[0]]})
		}
		parts = append(parts, SignaturePart{Text: rest[loc[0]:loc[1]], Date: true})
		rest = rest[loc[1]:]
	}
}

func colorSignatureDates(p palette, line string) string {
	var b strings.Builder
	for _, part := range SignatureParts(line) {
		if part.Date {
			b.WriteString(p.wrap(p.Muted, part.Text))
			continue
		}
		b.WriteString(part.Text)
	}
	return b.String()
}
