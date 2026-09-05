package artifact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestPinnedIncompleteMetadataKeepsSourceGaps(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "size_unknown", true: "source_file_missing"}[missing], func(t *testing.T) {
			r, s := artifactFixture(t, 1)
			r.Files[0].SizeBytes = nil
			r.State = "incomplete"
			gap := "selected_size_missing"
			if missing {
				r.Files[0].State = "missing"
				r.Files[0].GitBlobOID = ""
				r.Files[0].DeclaredSHA256 = ""
				r.InventoryPaths = []string{}
				gap = "selected_file_missing"
			}
			r.Gaps = append(r.Gaps, gap)
			slices.Sort(r.Gaps)
			resealSource(t, &r, &s)
			result, err := Bind(t.Context(), r, s, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(result.Gaps, "source:"+gap) || result.RuntimeState != "unbound" {
				t.Fatal("source uncertainty lost")
			}
			if missing && result.Files[0].State != "locally_hashed" {
				t.Fatal("missing provider file promoted to match")
			}
		})
	}
}

func TestUnavailableSourceRejected(t *testing.T) {
	r, s := artifactFixture(t, 1)
	r.State = "unavailable"
	r.ResolvedRepo = ""
	r.ResolvedCommit = ""
	r.Files = nil
	r.InventoryPaths = nil
	r.Dependencies = nil
	r.Gaps = []string{"transport_error"}
	r.Queries = r.Queries[:1]
	r.Queries[0].Outcome = "transport_error"
	r.Queries[0].HTTPStatus = 0
	r.Queries[0].ResponseSHA256 = ""
	resealSource(t, &r, &s)
	if _, err := Bind(t.Context(), r, s, Options{}); err == nil {
		t.Fatal("unresolved source established a binding")
	}
}

func TestEmptyFileAndMaximumMapping(t *testing.T) {
	r, s := artifactFixture(t, 1)
	if err := os.WriteFile(s.Files[0].LocalPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	*r.Files[0].SizeBytes = 0
	r.Files[0].DeclaredSHA256 = contentHash(nil)
	resealSource(t, &r, &s)
	result, err := Bind(t.Context(), r, s, Options{MaxBytes: 1})
	if err != nil || result.State != "matched" || result.BytesRead != 0 {
		t.Fatalf("empty file: %s %v", result.State, err)
	}
	r, s = artifactFixture(t, MaxFiles)
	result, err = Bind(t.Context(), r, s, Options{})
	if err != nil || len(result.Files) != MaxFiles {
		t.Fatal("bounded multi-file input failed", err)
	}
	s.Files = make([]Mapping, MaxFiles+1)
	if s.Validate() == nil {
		t.Fatal("mapping count bound missing")
	}
}

func TestRawSymlinkParentTraversalNeverReadsOrPublishes(t *testing.T) {
	root := artifactRoot(t)
	safe, outside := filepath.Join(root, "safe"), filepath.Join(root, "outside")
	for _, path := range []string{safe, filepath.Join(outside, "child")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(safe, "link")
	if err := os.Symlink(filepath.Join(outside, "child"), link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	result := bindFixture(t)
	raw := link + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "receipt.json"
	if ValidateOutputPath(raw) == nil || WriteBinding(raw, result) == nil {
		t.Fatal("unchecked raw path published")
	}
	if _, err := os.Lstat(filepath.Join(outside, "receipt.json")); !os.IsNotExist(err) {
		t.Fatal("outside file created")
	}
	data, err := result.JSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(safe, "receipt.json"), filepath.Join(outside, "receipt.json")} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := LoadBinding(raw); err == nil {
		t.Fatal("raw path read outside checked directory")
	}
	leaf := filepath.Join(root, "receipt-link.json")
	if err := os.Symlink(filepath.Join(safe, "receipt.json"), leaf); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBinding(leaf); err == nil {
		t.Fatal("linked receipt accepted")
	}
}

func TestRehashedKnownSizeMismatchCannotHideInHash(t *testing.T) {
	result := bindFixture(t)
	result.Files[0].Before.SizeBytes++
	result.Files[0].After.SizeBytes++
	result.Files[0].BytesRead++
	result.BytesRead++
	if _, err := result.Digest(); err == nil {
		t.Fatal("provider size disagreement hidden by a matching hash")
	}
	result = bindFixture(t)
	if err := os.Remove(result.Files[0].LocalPath); err != nil {
		t.Fatal(err)
	}
	if ValidateBindingOutputPath(result.Files[0].LocalPath, result.Mapping) == nil {
		t.Fatal("missing input output overlap accepted")
	}
}

func TestSpecAndReceiptPathFailure(t *testing.T) {
	root := artifactRoot(t)
	if _, err := LoadSpec(filepath.Join(root, "missing")); err == nil {
		t.Fatal("missing mapping accepted")
	}
	if _, err := LoadBinding(root); err == nil {
		t.Fatal("directory receipt accepted")
	}
	if _, err := LoadSpec(root); err == nil {
		t.Fatal("directory mapping accepted")
	}
	result := bindFixture(t)
	data, _ := json.Marshal(result.Mapping)
	path := filepath.Join(root, "mapping.json")
	if err := os.WriteFile(path, append(data, []byte(" {}")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSpec(path); err == nil {
		t.Fatal("trailing mapping data accepted")
	}
	result.BindingSHA256 = "bad"
	if WriteBinding(filepath.Join(root, "invalid.json"), result) == nil {
		t.Fatal("invalid receipt published")
	}
}
