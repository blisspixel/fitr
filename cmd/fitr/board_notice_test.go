package main

import (
	"strings"
	"testing"
)

func TestBoardExclusionWrapsAtSixtyColumns(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")
	var buf strings.Builder
	writeBoardExclusionLines(&buf, boardExclusions{contaminated: 1, unscored: 2}, 60, "plain")
	got := buf.String()
	for _, want := range []string{"INCONCLUSIVE", "excluded 1", "warning"} {
		if !strings.Contains(got, want) {
			t.Fatalf("board exclusion missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "!") || strings.ContainsRune(got, '\x1b') {
		t.Fatalf("board exclusion kept a bang or an escape:\n%s", got)
	}
	wrapped := 0
	for _, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if len([]rune(line)) > 60 {
			t.Errorf("board line is %d cols: %q", len([]rune(line)), line)
		}
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(strings.TrimLeft(line, " "), "warning") {
			wrapped++
		}
	}
	if wrapped == 0 {
		t.Fatalf("long board exclusions did not wrap:\n%s", got)
	}
}
