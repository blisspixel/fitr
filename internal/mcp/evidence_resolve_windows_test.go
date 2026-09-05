package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestSelectionStatusPreservesPhysicalWindowsShortRoot(t *testing.T) {
	longRoot := filepath.Join(selectionRoot(t), "Selected Role Long Directory")
	if err := os.Mkdir(longRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	input, err := windows.UTF16PtrFromString(longRoot)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]uint16, 32768)
	size, err := windows.GetShortPathName(input, &buffer[0], uint32(len(buffer)))
	if err != nil {
		t.Skipf("8.3 paths unavailable on test filesystem: %v", err)
	}
	if size == 0 || size >= uint32(len(buffer)) {
		t.Fatal("invalid short-path response")
	}
	shortRoot := windows.UTF16ToString(buffer[:size])
	if strings.EqualFold(shortRoot, longRoot) {
		t.Skip("test filesystem did not allocate a distinct 8.3 alias")
	}
	resolved, err := resolveLocalEvidence(shortRoot)
	if err != nil || !samePath(resolved, longRoot) {
		t.Fatal("local resolver lost short-root identity", resolved, err)
	}
	f := newSelectionFixture(t, shortRoot, false)
	value, err := f.source.statusAt(t.Context(), f.library.Name, f.now)
	if err != nil || value.(statusSummary).State != "qualified" {
		t.Fatal("short root lost original sealed path identity", value, err)
	}
}
