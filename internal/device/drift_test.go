package device

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// spec/profiles is canonical; the embedded copy here exists only because
// go:embed cannot reach outside the package directory. This test keeps the
// two identical. `make spec-sync` repairs a mismatch.
func TestEmbeddedProfilesMatchCanonical(t *testing.T) {
	root := filepath.Join("..", "..", "spec", "profiles")
	if _, err := os.Stat(root); err != nil {
		t.Skip("canonical spec/profiles not present (built outside the repo)")
	}
	norm := func(b []byte) []byte { return bytes.ReplaceAll(b, []byte("\r"), nil) }

	seen := map[string]bool{}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		emb := "profiles/" + e.Name()
		seen[emb] = true
		want, err := os.ReadFile(filepath.Join(root, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		got, err := profilesFS.ReadFile(emb)
		if err != nil {
			t.Fatalf("%s is canonical but not embedded: %v (run `make spec-sync`)", e.Name(), err)
		}
		if !bytes.Equal(norm(want), norm(got)) {
			t.Errorf("embedded %s differs from canonical spec/profiles/%s (run `make spec-sync`)", emb, e.Name())
		}
	}
	err = fs.WalkDir(profilesFS, "profiles", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".json") {
			return err
		}
		if !seen[p] {
			t.Errorf("embedded %s has no canonical file under spec/profiles (run `make spec-sync`)", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
