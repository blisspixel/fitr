package boundedio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileEnforcesLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(path, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadFile(path, 5); err != nil || string(got) != "12345" {
		t.Fatalf("exact-limit read = %q, %v", got, err)
	}
	if _, err := ReadFile(path, 4); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized read error = %v", err)
	}
}

func TestReadTailReturnsOnlyLatestBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(path, []byte("old-latest"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadTail(path, 6)
	if err != nil || string(got) != "latest" {
		t.Fatalf("tail = %q, %v", got, err)
	}
}

func TestReadEdgesPreservesBeginningAndEndWithinLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(path, []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadEdges(path, 5)
	if err != nil || string(got) != "ab\nef" {
		t.Fatalf("edges = %q, %v", got, err)
	}
	if len(got) > 5 {
		t.Fatalf("edges returned %d bytes, limit is 5", len(got))
	}
	got, err = ReadEdges(path, 6)
	if err != nil || string(got) != "abcdef" {
		t.Fatalf("full edge read = %q, %v", got, err)
	}
}

func TestReadsRejectDirectoriesAndInvalidLimits(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadFile(dir, 1); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory read error = %v", err)
	}
	if _, err := ReadTail("", 1); err == nil {
		t.Fatal("empty tail path was accepted")
	}
	if _, err := ReadFile(filepath.Join(dir, "missing"), 0); err == nil {
		t.Fatal("zero limit was accepted")
	}
}
