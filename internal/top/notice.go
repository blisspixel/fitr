package top

import (
	"strings"

	"github.com/blisspixel/fitr/internal/render"
)

// Notices share the CLI words. Live and result warnings stay warnings:
// they are caveats about the run, not inventory advice. Cancellation keeps
// its own word so a stopped run is not relabeled as an error.
func renderLiveMessages(w *lineWriter, live Live) {
	for _, warning := range live.Warnings {
		if !writeNotice(w, render.NoticeWarning, warning) {
			return
		}
	}
	if live.Error != "" {
		if live.Cancelled {
			if !writeStatus(w, "cancelled", RoleWarning, live.Error) {
				return
			}
		} else if !writeNotice(w, render.NoticeError, live.Error) {
			return
		}
	}
	if live.Completed && !live.Cancelled && live.Error == "" {
		writeLiveSaved(w, live.Saved)
	}
}

func writeLiveSaved(w *lineWriter, saved bool) {
	if !saved {
		w.line(Span{Text: "UNSAVED", Role: RoleFail}, Span{Text: "  check result directory permissions and free space", Role: RoleWarning})
		return
	}
	w.line(Span{Text: "Enter opens the saved Result.", Role: RoleAccent})
}

func renderResultWarnings(w *lineWriter, run Run) bool {
	for _, warning := range run.Warnings {
		if !writeNotice(w, render.NoticeWarning, warning) {
			return false
		}
	}
	return true
}

func renderInventoryNotice(w *lineWriter, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	writeNotice(w, render.ClassifyInventoryNotice(text), text)
}

func writeNotice(w *lineWriter, level render.NoticeLevel, text string) bool {
	return writeLabeled(w, noticeRole(level), render.NoticeLines(level, text, w.canvas.Width))
}

func writeStatus(w *lineWriter, word string, role Role, text string) bool {
	return writeLabeled(w, role, render.LabeledLines(word, text, w.canvas.Width))
}

func writeLabeled(w *lineWriter, role Role, lines []render.NoticeLine) bool {
	for _, line := range lines {
		labelRole := role
		if strings.TrimSpace(line.Label) == "" {
			labelRole = RoleDefault
		}
		if !w.line(Span{Text: line.Label, Role: labelRole}, Span{Text: line.Body, Role: RoleDefault}) {
			return false
		}
	}
	return true
}

func noticeRole(level render.NoticeLevel) Role {
	switch level {
	case render.NoticeTip:
		return RoleMuted
	case render.NoticeNote:
		return RoleDefault
	case render.NoticeError:
		return RoleFail
	default:
		return RoleWarning
	}
}
