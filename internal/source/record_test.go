package source

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func sourceTempDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHFRequestValidation(t *testing.T) {
	cases := []HFRequest{
		{}, {RepoID: "model", Revision: "main", Files: []string{"file"}},
		{RepoID: "owner/../repo", Revision: "main", Files: []string{"file"}},
		{RepoID: "owner/mo--del", Revision: "main", Files: []string{"file"}},
		{RepoID: "owner/model.git", Revision: "main", Files: []string{"file"}},
		{RepoID: "owner/model", Revision: "", Files: []string{"file"}},
		{RepoID: "owner/model", Revision: "../main", Files: []string{"file"}},
		{RepoID: "owner/model", Revision: "main?x", Files: []string{"file"}},
		{RepoID: "owner/model", Revision: "main", Files: []string{"file", "file"}},
		{RepoID: "owner/model", Revision: "main", Files: []string{"/file"}},
		{RepoID: "owner/model", Revision: "main", Files: []string{`dir\file`}},
		{RepoID: "owner/model", Revision: "main", Files: []string{"dir//file"}},
		{RepoID: "owner/model", Revision: "main", Files: []string{"dir/./file"}},
		{RepoID: "owner/model", Revision: "main", Files: make([]string, MaxFiles+1)},
	}
	for _, request := range cases {
		if err := request.Validate(); err == nil {
			t.Fatalf("accepted request %+v", request)
		}
	}
	if err := sourceRequest().Validate(); err != nil {
		t.Fatal(err)
	}
}

func sourceClone(t *testing.T, result Resolution) Resolution {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var clone Resolution
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func TestResolutionRejectsAlteredClaims(t *testing.T) {
	fixture := sourceFixture(t)
	cases := map[string]func(*Resolution){
		"schema":              func(r *Resolution) { r.Schema = "fitr.source.v2" },
		"policy":              func(r *Resolution) { r.PolicyVersion = "other" },
		"scope":               func(r *Resolution) { r.Scope = "verified_artifact" },
		"provider":            func(r *Resolution) { r.Provider = "mirror" },
		"resolver":            func(r *Resolution) { r.ResolverVersion = "bad\nversion" },
		"digest":              func(r *Resolution) { r.ResolutionSHA256 = "sha256:" + strings.Repeat("0", 64) },
		"commit":              func(r *Resolution) { r.ResolvedCommit = strings.Repeat("d", 40) },
		"repository":          func(r *Resolution) { r.ResolvedRepo = "owner/other" },
		"state":               func(r *Resolution) { r.State = "incomplete" },
		"gaps":                func(r *Resolution) { r.Gaps = nil },
		"dependencies":        func(r *Resolution) { r.Dependencies = nil },
		"inventory":           func(r *Resolution) { r.InventoryPaths = nil },
		"inventory_duplicate": func(r *Resolution) { r.InventoryPaths = append(r.InventoryPaths, "model.gguf") },
		"inventory_path":      func(r *Resolution) { r.InventoryPaths = []string{"../bad"} },
		"file_path":           func(r *Resolution) { r.Files[0].Path = "different.gguf" },
		"file_missing":        func(r *Resolution) { r.Files[0].State = "missing" },
		"file_state":          func(r *Resolution) { r.Files[0].State = "verified" },
		"file_count":          func(r *Resolution) { r.Files = nil },
		"negative_size":       func(r *Resolution) { *r.Files[0].SizeBytes = -1 },
		"large_size":          func(r *Resolution) { *r.Files[0].SizeBytes = maxFileBytes + 1 },
		"oid":                 func(r *Resolution) { r.Files[0].GitBlobOID = "wrong" },
		"hash":                func(r *Resolution) { r.Files[0].DeclaredSHA256 = "wrong" },
		"observed":            func(r *Resolution) { r.ObservedAt = "2020-01-01T00:00:00Z" },
		"timestamp":           func(r *Resolution) { r.Queries[0].StartedAt = "yesterday" },
		"query_end":           func(r *Resolution) { r.Queries[0].CompletedAt = "bad" },
		"query_order":         func(r *Resolution) { r.Queries[1].StartedAt = r.Queries[0].StartedAt },
		"query_count":         func(r *Resolution) { r.Queries = nil },
		"query_revision":      func(r *Resolution) { r.Queries[0].Revision = "other" },
		"query_pin":           func(r *Resolution) { r.Queries[1].Revision = "main" },
		"query_state":         func(r *Resolution) { r.Queries[0].Outcome = "transport_error" },
		"query_status":        func(r *Resolution) { r.Queries[0].HTTPStatus = 700 },
		"query_digest":        func(r *Resolution) { r.Queries[0].ResponseSHA256 = "" },
	}
	for name, alter := range cases {
		t.Run(name, func(t *testing.T) {
			copyResult := sourceClone(t, fixture)
			alter(&copyResult)
			if err := copyResult.Validate(); err == nil {
				t.Fatal("altered receipt accepted")
			}
		})
	}
}

func TestResolutionSaveLoadAndExclusivePublish(t *testing.T) {
	result := sourceFixture(t)
	path := filepath.Join(sourceTempDir(t), "resolution.json")
	if err := ValidateOutputPath(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("preflight created output")
	}
	var successes atomic.Int32
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			if WriteResolution(path, result) == nil {
				successes.Add(1)
			}
		})
	}
	workers.Wait()
	if successes.Load() != 1 {
		t.Fatalf("exclusive publication succeeded %d times", successes.Load())
	}
	loaded, err := LoadResolution(path)
	if err != nil || loaded.ResolutionSHA256 != result.ResolutionSHA256 {
		t.Fatalf("load=%+v err=%v", loaded, err)
	}
	if err := WriteResolution(path, result); err == nil {
		t.Fatal("existing receipt overwritten")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v", info.Mode())
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary file leaked: %v %v", entries, err)
	}
}

func TestResolutionStrictLoading(t *testing.T) {
	result := sourceFixture(t)
	data, err := result.JSON()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"unknown":        strings.Replace(string(data), `"schema":`, `"artifact_bytes_verified":true,"schema":`, 1),
		"duplicate":      strings.Replace(string(data), `"schema":`, `"schema":"wrong","schema":`, 1),
		"case":           strings.Replace(string(data), `"schema":`, `"Schema":`, 1),
		"nested_unknown": strings.Replace(string(data), `"repo_id":`, `"unknown":1,"repo_id":`, 1),
		"trailing":       string(data) + "null", "null": "null", "primitive": "1",
		"nested_type": strings.Replace(string(data), `"request": {`, `"request": [`, 1),
		"oversize":    strings.Repeat(" ", MaxReceiptBytes+1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(sourceTempDir(t), "receipt.json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadResolution(path); err == nil {
				t.Fatal("invalid receipt loaded")
			}
		})
	}
}

func TestSourceOutputPathRejections(t *testing.T) {
	dir := sourceTempDir(t)
	file := filepath.Join(dir, "existing")
	if err := os.WriteFile(file, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "\n", dir, file, filepath.Join(dir, "missing", "receipt"), filepath.Join(file, "receipt")} {
		if err := ValidateOutputPath(path); err == nil {
			t.Fatalf("accepted output %q", path)
		}
	}
	if _, err := LoadResolution(dir); err == nil {
		t.Fatal("loaded directory")
	}
	if _, err := LoadResolution(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("loaded missing path")
	}
	if err := WriteResolution(filepath.Join(dir, "bad"), Resolution{}); err == nil {
		t.Fatal("saved invalid receipt")
	}
	if err := WriteResolution(filepath.Join(dir, "missing", "new"), sourceFixture(t)); err == nil {
		t.Fatal("created missing parent")
	}
}

func TestSourceRejectsSymlinks(t *testing.T) {
	dir := sourceTempDir(t)
	realPath := filepath.Join(dir, "real")
	if err := os.Mkdir(realPath, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(realPath, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := ValidateOutputPath(filepath.Join(link, "new.json")); err == nil {
		t.Fatal("symlink parent accepted")
	}
	path := filepath.Join(realPath, "receipt.json")
	if err := WriteResolution(path, sourceFixture(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadResolution(filepath.Join(link, "receipt.json")); err == nil {
		t.Fatal("symlink parent loaded")
	}
	leaf := filepath.Join(realPath, "leaf.json")
	if err := os.Symlink(path, leaf); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOutputPath(leaf); err == nil {
		t.Fatal("symlink output accepted")
	}
	if _, err := LoadResolution(leaf); err == nil {
		t.Fatal("symlink receipt loaded")
	}
}
