package render

import (
	"strings"
	"testing"
)

func TestInventoryNoticesUseFourWordsAtSixtyColumns(t *testing.T) {
	t.Setenv("FITR_WIDTH", "60")
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")
	var out strings.Builder
	WriteInventory(&out, inventoryNoticeFixture(), "rich")
	assertInventoryNoticeLabels(t, out.String())
}

func inventoryNoticeFixture() Inventory {
	return Inventory{
		Fitr: "0.12.0", RuntimeKind: "ollama", RuntimeURL: "http://127.0.0.1:11434",
		Warnings: []string{
			"fitr suggest names models for this machine before a download",
			"bubblewrap can run generated verifiers without the user's home directory or network",
			"isolated execution is unavailable: bubblewrap (bwrap) is not installed",
			"a sandbox can run generated verifiers without the home directory",
			"start the installed runtime: systemctl --user start ollama.service",
			"this GPU's Ollama directory has only CPU builds; install vendor-package",
			"on a CPU or APU, catalog mixture-of-experts names come first only among weights that fit the budget",
			"1 saved result file could not be trusted",
			"damaged evidence",
		},
		Rows: []InventoryRow{{Model: "qwen3:8b", State: "unproven", Next: "fitr advise qwen3:8b"}},
	}
}

func assertInventoryNoticeLabels(t *testing.T, got string) {
	t.Helper()
	checks := []struct{ fragment, label string }{
		{"fitr suggest names models", "tip"},
		{"bubblewrap can run", "tip"},
		{"isolated execution is unavailable", "tip"},
		{"a sandbox can run", "tip"},
		{"start the installed runtime", "tip"},
		{"this GPU's Ollama directory", "tip"},
		{"mixture-of-experts", "note"},
		{"could not be trusted", "warning"},
		{"damaged evidence", "warning"},
	}
	for _, check := range checks {
		if gotLabel := noticeLabel(got, check.fragment); gotLabel != check.label {
			t.Errorf("%q labeled %q, want %q\n%s", check.fragment, gotLabel, check.label, got)
		}
	}
	if strings.ContainsRune(got, '\x1b') {
		t.Fatalf("NO_COLOR or TERM=dumb leaked escapes:\n%s", got)
	}
	assertNoticeLinesFit(t, got, 60)
}

func assertNoticeLinesFit(t *testing.T, got string, width int) {
	t.Helper()
	hang := strings.Repeat(" ", NoticeGutter)
	for _, line := range strings.Split(got, "\n") {
		fields := strings.Fields(line)
		labeled := len(fields) > 0 && (fields[0] == "tip" || fields[0] == "note" || fields[0] == "warning" || fields[0] == "error")
		continuation := strings.HasPrefix(line, hang) && strings.TrimSpace(line) != ""
		if (labeled || continuation) && len([]rune(line)) > width {
			t.Errorf("notice line is %d cols: %q", len([]rune(line)), line)
		}
	}
}

func TestSuggestNoticeHangsUnderTheWord(t *testing.T) {
	lines := NoticeLines(NoticeTip, "fitr suggest names models for this machine before a download", 60)
	if len(lines) < 2 {
		t.Fatalf("suggest notice did not wrap: %+v", lines)
	}
	if lines[0].Label != "  tip       " || strings.TrimSpace(lines[1].Label) != "" {
		t.Fatalf("gutter = %q / %q", lines[0].Label, lines[1].Label)
	}
	if len(lines[0].Label) != NoticeGutter || len(lines[1].Label) != NoticeGutter {
		t.Fatalf("gutter width = %d / %d", len(lines[0].Label), len(lines[1].Label))
	}
	if n := len([]rune(lines[0].Label + lines[0].Body)); n > 60 {
		t.Fatalf("first line is %d cols", n)
	}
}

func TestNoticeColorReinforcesTheWordOnly(t *testing.T) {
	t.Setenv("TERM", "")
	var warning, note, tip, errLine strings.Builder
	writeColoredNotice(&warning, pickPalette(true), NoticeWarning, "could not be trusted", 80)
	writeColoredNotice(&note, pickPalette(true), NoticeNote, "the sort is fixed", 80)
	writeColoredNotice(&tip, pickPalette(true), NoticeTip, "optional next step", 80)
	writeColoredNotice(&errLine, pickPalette(true), NoticeError, "the command stopped", 80)
	if !strings.Contains(warning.String(), "\x1b[33mwarning") {
		t.Fatalf("warning word was not amber: %q", warning.String())
	}
	if strings.Contains(warning.String(), "\x1b[33mcould") {
		t.Fatal("amber reached the sentence")
	}
	if strings.ContainsRune(note.String(), '\x1b') || !strings.Contains(note.String(), "note") {
		t.Fatalf("note must keep the default color: %q", note.String())
	}
	if !strings.Contains(tip.String(), "\x1b[90mtip") || !strings.Contains(errLine.String(), "\x1b[31merror") {
		t.Fatalf("tip/error color:\n%s\n%s", tip.String(), errLine.String())
	}
}

func TestDumbTerminalKeepsNoticeWords(t *testing.T) {
	t.Setenv("TERM", "dumb")
	t.Setenv("NO_COLOR", "1")
	var out strings.Builder
	writeColoredNotice(&out, pickPalette(true), NoticeError, "the command stopped", 60)
	got := out.String()
	if strings.ContainsRune(got, '\x1b') {
		t.Fatalf("dumb terminal kept escapes: %q", got)
	}
	for _, word := range []string{"error", "the command stopped"} {
		if !strings.Contains(got, word) {
			t.Fatalf("missing %q in %q", word, got)
		}
	}
}

func TestProgressNoteUsesTheNoticeWord(t *testing.T) {
	var buf strings.Builder
	d := &textDisplay{out: &buf, err: &buf, pal: palette{}, g: glyphs{" | ", "-", "+/-", "..."}}
	d.Note("watch this", "warn")
	d.Note("the seed set is pinned", "")
	got := buf.String()
	if noticeLabel(got, "watch this") != "warning" || noticeLabel(got, "the seed set is pinned") != "note" {
		t.Fatalf("progress notes:\n%s", got)
	}
	if strings.Contains(got, "!") {
		t.Fatalf("progress still uses a bang:\n%s", got)
	}
}

func TestUnknownNoticeLevelFailsClosedToWarning(t *testing.T) {
	if got := NoticeLevel("urgent").Word(); got != "warning" {
		t.Fatalf("unknown level word = %q", got)
	}
	lines := NoticeLines("urgent", "something changed", 80)
	if len(lines) != 1 || strings.TrimSpace(lines[0].Label) != "warning" {
		t.Fatalf("unknown level lines = %+v", lines)
	}
}

func noticeLabel(text, fragment string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, fragment) {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				return ""
			}
			return fields[0]
		}
	}
	return ""
}

func TestNoticeWordsSurviveAPlainPalette(t *testing.T) {
	var out strings.Builder
	WriteNotice(&out, NoticeWarning, "could not be trusted", 60, "plain")
	if strings.ContainsRune(out.String(), '\x1b') || noticeLabel(out.String(), "could not be trusted") != "warning" {
		t.Fatalf("plain notice: %q", out.String())
	}
}
