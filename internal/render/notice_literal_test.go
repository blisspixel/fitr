package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The notice word is the NoticeWarning constant. Padding is applied when
// the line is built. A quoted warning followed by a space is a second label.
func TestNoPaddedWarningLiteral(t *testing.T) {
	root := moduleRoot(t)
	// Two spaces keeps an ordinary sentence that begins with the word,
	// such as a test failure, from matching the padded label.
	word := "warn" + "ing"
	space := " "
	forbidden := []string{
		"\"" + word + space + space,
		"\"" + space + space + word + "\"",
	}
	var hits []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".agents", ".tmp", "vendor", "dist", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(body)
		for _, needle := range forbidden {
			if strings.Contains(text, needle) {
				hits = append(hits, path)
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) > 0 {
		t.Fatalf("padded warning label outside the notice helper:\n%s", strings.Join(hits, "\n"))
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
