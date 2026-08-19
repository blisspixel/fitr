package eval

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The canonical spec lives at the REPO ROOT (spec/), and a second copy is
// embedded here because go:embed cannot reach outside the package directory.
// Two copies that nothing compares would silently drift -- which is exactly
// the failure mode a generated spec exists to prevent. This test IS the
// comparison. `make spec-sync` repairs a mismatch.
func TestEmbeddedSpecMatchesCanonical(t *testing.T) {
	root := filepath.Join("..", "..", "spec")
	if _, err := os.Stat(root); err != nil {
		t.Skip("canonical spec/ not present (built outside the repo)")
	}

	// canonical -> embedded path mapping. version.json sits at spec/version.json
	// but is embedded under tasks/ so the binary carries it.
	compare := func(canonical, embedded string) {
		t.Helper()
		want, err := os.ReadFile(canonical)
		if err != nil {
			t.Fatalf("canonical %s: %v", canonical, err)
		}
		got, err := tasksFS.ReadFile(embedded)
		if err != nil {
			t.Fatalf("%s is canonical but not embedded: %v (run `make spec-sync`)", canonical, err)
		}
		if !bytes.Equal(normalize(want), normalize(got)) {
			t.Errorf("embedded %s differs from canonical %s (run `make spec-sync`)", embedded, canonical)
		}
	}

	seen := map[string]bool{}
	err := filepath.WalkDir(filepath.Join(root, "tasks"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".json") {
			return err
		}
		rel, _ := filepath.Rel(filepath.Join(root, "tasks"), p)
		emb := "tasks/" + filepath.ToSlash(rel)
		seen[emb] = true
		compare(p, emb)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	seen["tasks/version.json"] = true
	compare(filepath.Join(root, "version.json"), "tasks/version.json")

	// The reverse direction: an embedded file with no canonical counterpart is
	// drift too -- someone edited the copy the binary uses and not the contract.
	err = fs.WalkDir(tasksFS, "tasks", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".json") {
			return err
		}
		if !seen[p] {
			t.Errorf("embedded %s has no canonical file under spec/ (run `make spec-sync`)", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// normalize strips CR so a Windows checkout with autocrlf does not read as drift.
func normalize(b []byte) []byte { return bytes.ReplaceAll(b, []byte("\r"), nil) }
