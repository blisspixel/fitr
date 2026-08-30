package record

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/eval"
)

func testRecord(model, started string) *Record {
	return &Record{
		SchemaVersion: 4,
		Model:         model,
		StartedAt:     started,
		Level:         "full",
		Repeats:       3,
		NumCtx:        8192,
		DeviceKey:     "host|gpu|driver|cuda|runtime|||ctx=8192",
		Profile:       "default",
	}
}

func TestContextSizeCurrentLegacyAndDefault(t *testing.T) {
	if got := (*Record)(nil).ContextSize(); got != eval.NumCtx {
		t.Fatalf("nil context = %d, want %d", got, eval.NumCtx)
	}
	if got := (&Record{NumCtx: 4096}).ContextSize(); got != 4096 {
		t.Fatalf("recorded context = %d", got)
	}
	if got := (&Record{DeviceKey: "host|gpu|ctx=2048"}).ContextSize(); got != 2048 {
		t.Fatalf("legacy context = %d", got)
	}
	if got := (&Record{}).ContextSize(); got != eval.NumCtx {
		t.Fatalf("default context = %d, want %d", got, eval.NumCtx)
	}
}

func TestStableRunIDSupportsLegacyRecords(t *testing.T) {
	r := testRecord("model", "2026-08-20T12:00:00Z")
	a := r.StableRunID()
	if len(a) != 24 {
		t.Fatalf("derived ID %q has length %d", a, len(a))
	}
	if b := r.StableRunID(); b != a {
		t.Fatalf("ID changed: %q then %q", a, b)
	}
	r.Profile = "other"
	if b := r.StableRunID(); b == a {
		t.Fatal("different legacy content received the same ID")
	}
	r.RunID = "stable_run_1234"
	if got := r.StableRunID(); got != r.RunID {
		t.Fatalf("stored ID = %q, got %q", r.RunID, got)
	}
}

func TestSavePreservesCanonicalAndAppendsPrivateHistory(t *testing.T) {
	dir := t.TempDir()
	rootMode := testRootMode(t, dir)
	store := NewStore(dir)
	r := testRecord("org/model:Q4_K_M", "2026-08-20T12:00:00.123456789Z")
	saved, err := store.Save(r)
	if err != nil {
		t.Fatal(err)
	}
	assertSavedPathsAndRunID(t, store, r, saved, dir)
	assertSavedRecordFiles(t, r, saved)
	assertPrivateStoreDirectories(t, dir, rootMode)
}

func testRootMode(t *testing.T, dir string) os.FileMode {
	t.Helper()
	if runtime.GOOS == "windows" {
		return 0
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func assertSavedPathsAndRunID(t *testing.T, store Store, r *Record, saved SavedPaths, dir string) {
	t.Helper()
	if saved.CanonicalPath != store.CanonicalPath(r.Model) ||
		filepath.Base(saved.CanonicalPath) == "org_model_Q4_K_M.json" {
		t.Fatalf("canonical path = %q", saved.CanonicalPath)
	}
	if saved.RunID == "" || r.RunID != saved.RunID {
		t.Fatalf("run ID was not assigned: saved=%q record=%q", saved.RunID, r.RunID)
	}
	if filepath.Dir(saved.HistoryPath) != filepath.Join(dir, historyDirName) {
		t.Fatalf("history path = %q", saved.HistoryPath)
	}
}

func assertSavedRecordFiles(t *testing.T, r *Record, saved SavedPaths) {
	t.Helper()
	for _, path := range []string{saved.CanonicalPath, saved.HistoryPath} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var got Record
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("%s is not valid JSON: %v", path, err)
		}
		if got.RunID != saved.RunID || got.Model != r.Model {
			t.Fatalf("wrong saved record in %s: %+v", path, got)
		}
		if runtime.GOOS != "windows" {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0o600 {
				t.Fatalf("%s mode = %o, want 600", path, got)
			}
		}
	}
}

func assertPrivateStoreDirectories(t *testing.T, dir string, rootMode os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	for _, check := range []struct {
		path string
		want os.FileMode
	}{
		{dir, rootMode},
		{filepath.Join(dir, historyDirName), 0o700},
	} {
		info, err := os.Stat(check.path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != check.want {
			t.Fatalf("%s mode = %o, want %o", check.path, got, check.want)
		}
	}
}

func TestRepeatedSaveReusesHistoryAndLoadsOneRecord(t *testing.T) {
	store := NewStore(t.TempDir())
	r := testRecord("model", "2026-08-20T12:00:00Z")
	a, err := store.Save(r)
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.Save(r)
	if err != nil {
		t.Fatal(err)
	}
	if a.HistoryPath != b.HistoryPath {
		t.Fatalf("identical save created two history paths: %q and %q", a.HistoryPath, b.HistoryPath)
	}
	loaded, err := store.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Records) != 1 || len(loaded.Warnings) != 0 {
		t.Fatalf("loaded records=%d warnings=%v", len(loaded.Records), loaded.Warnings)
	}
}

func TestHistoryKeepsMultipleRunsAndSortsNewestFirst(t *testing.T) {
	store := NewStore(t.TempDir())
	old := testRecord("same", "2026-08-20T10:00:00Z")
	newest := testRecord("same", "2026-08-20T12:00:00Z")
	middle := testRecord("other", "2026-08-20T11:00:00Z")
	for _, r := range []*Record{old, newest, middle} {
		if _, err := store.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := store.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Records) != 3 {
		t.Fatalf("records = %d, want 3", len(loaded.Records))
	}
	want := []string{newest.StartedAt, middle.StartedAt, old.StartedAt}
	for i, r := range loaded.Records {
		if r.StartedAt != want[i] {
			t.Fatalf("record %d started %q, want %q", i, r.StartedAt, want[i])
		}
	}
	current, err := store.Read(store.CanonicalPath("same"))
	if err != nil {
		t.Fatal(err)
	}
	if current.StartedAt != newest.StartedAt {
		t.Fatalf("canonical same model is %q, want %q", current.StartedAt, newest.StartedAt)
	}
}

func TestReadRecognizesCanonicalDirectoryThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	r := completedEvidenceRecord(t, nil, []eval.CheckOutcome{{TaskID: "check", Outcome: eval.OutcomeSkipped}})
	saved, err := store.Save(r)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "results-alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	loaded, err := store.Read(filepath.Join(alias, filepath.Base(saved.CanonicalPath)))
	if err != nil {
		t.Fatal(err)
	}
	if issue := loaded.EvidenceIntegrityIssue(); issue != "" {
		t.Fatalf("equivalent canonical path lost evidence integrity: %s", issue)
	}
}

func TestLoadLegacyCurrentAndHistoryDeduplicatesStably(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	r := testRecord("legacy", "2026-08-20T12:00:00Z")
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	if err := os.WriteFile(filepath.Join(dir, "legacy.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	history := filepath.Join(dir, historyDirName)
	if err := os.Mkdir(history, 0o700); err != nil {
		t.Fatal(err)
	}
	compact, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(history, "legacy-copy.json"), compact, 0o600); err != nil {
		t.Fatal(err)
	}

	first, err := store.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Records) != 1 || len(second.Records) != 1 {
		t.Fatalf("dedup counts = %d and %d", len(first.Records), len(second.Records))
	}
	if len(first.Warnings) != 0 || len(second.Warnings) != 0 {
		t.Fatalf("formatting-only differences produced warnings: %v and %v", first.Warnings, second.Warnings)
	}
	if first.Records[0].RunID == "" || first.Records[0].RunID != second.Records[0].RunID {
		t.Fatalf("legacy ID is not stable: %q and %q", first.Records[0].RunID, second.Records[0].RunID)
	}
}

func TestReadRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	store := NewStore(t.TempDir())
	for name, raw := range map[string]string{
		"unknown.json":   `{"schema_version":4,"model":"m","unexpected":true}`,
		"duplicate.json": `{"schema_version":4,"model":"m","model":"second"}`,
		"trailing.json":  `{"schema_version":4,"model":"m"} {"model":"second"}`,
	} {
		path := filepath.Join(store.Dir, name)
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Read(path); err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
}

func TestReadRejectsOversizedRecord(t *testing.T) {
	store := NewStore(t.TempDir())
	path := filepath.Join(store.Dir, "oversized.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxRecordBytes + 1); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(path); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized record error = %v", err)
	}
}

func FuzzDecodeRecord(f *testing.F) {
	f.Add([]byte(`{"schema_version":4,"model":"legacy"}`))
	f.Add([]byte(`{"schema_version":5,"model":"current","unexpected":true}`))
	f.Add([]byte(`{"model":"a"} {"model":"b"}`))
	f.Add([]byte{0xff, 0x00, '{', '}'})
	f.Fuzz(func(t *testing.T, raw []byte) {
		record, err := decodeRecord(raw)
		if err != nil {
			return
		}
		if record == nil || strings.TrimSpace(record.Model) == "" {
			t.Fatal("decoder accepted an empty result")
		}
		if err := record.ValidateManifest(); err != nil {
			t.Fatalf("decoder returned an invalid manifest: %v", err)
		}
		if err := record.ValidateEvidenceContract(); err != nil {
			t.Fatalf("decoder returned an invalid evidence contract: %v", err)
		}
	})
}

func TestCanonicalNamesDoNotCollideAndLegacyCurrentStillLoads(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	a := testRecord("org/model", "2026-08-20T12:00:00Z")
	b := testRecord("org:model", "2026-08-20T13:00:00Z")
	if store.LegacyCanonicalPath(a.Model) != store.LegacyCanonicalPath(b.Model) {
		t.Fatal("test models no longer exercise the legacy collision")
	}
	if store.CanonicalPath(a.Model) == store.CanonicalPath(b.Model) {
		t.Fatal("collision-safe canonical paths collided")
	}
	for _, record := range []*Record{a, b} {
		if _, err := store.Save(record); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := store.LoadCurrent()
	if err != nil || len(loaded.Records) != 2 {
		t.Fatalf("current collision-safe records=%d err=%v", len(loaded.Records), err)
	}

	legacy := testRecord("legacy", "2026-08-19T12:00:00Z")
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.LegacyCanonicalPath(legacy.Model), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.LoadCurrent()
	if err != nil || len(loaded.Records) != 3 {
		t.Fatalf("legacy migration records=%d err=%v", len(loaded.Records), err)
	}
}

func TestLoadCurrentMigratesLegacyFilenameWithoutDuplicatingModel(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	legacy := testRecord("org/model", "2026-08-19T12:00:00Z")
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.LegacyCanonicalPath(legacy.Model), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	current := testRecord(legacy.Model, "2026-08-20T12:00:00Z")
	if _, err := store.Save(current); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadCurrent()
	if err != nil || len(loaded.Records) != 1 || loaded.Records[0].StartedAt != current.StartedAt {
		t.Fatalf("migrated current=%+v err=%v", loaded, err)
	}
	history, err := store.LoadAll()
	if err != nil || len(history.Records) != 2 {
		t.Fatalf("migration history records=%d err=%v", len(history.Records), err)
	}
}

func TestLoadReportsCorruptionButIgnoresOtherValidArtifacts(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pair.json"), []byte(`{"schema":"fitr.calibration.pair.v1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	history := filepath.Join(dir, historyDirName)
	if err := os.Mkdir(history, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(history, "not-a-record.json"), []byte(`{"valid":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	good := testRecord("good", "2026-08-20T12:00:00Z")
	if _, err := store.Save(good); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Records) != 1 || loaded.Records[0].Model != "good" {
		t.Fatalf("records = %+v", loaded.Records)
	}
	if len(loaded.Warnings) != 2 {
		t.Fatalf("warnings = %v, want broken root and malformed history", loaded.Warnings)
	}
	joined := loaded.Warnings[0].Error() + "\n" + loaded.Warnings[1].Error()
	if !strings.Contains(joined, "broken.json") || !strings.Contains(joined, "not-a-record.json") {
		t.Fatalf("wrong warnings: %s", joined)
	}
	if strings.Contains(joined, "pair.json") {
		t.Fatalf("valid non-record artifact was warned about: %s", joined)
	}
}

func TestInvalidTimestampSortsAfterDatedRecords(t *testing.T) {
	store := NewStore(t.TempDir())
	for _, r := range []*Record{
		testRecord("undated", "not-a-time"),
		testRecord("dated", time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)),
	} {
		if _, err := store.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := store.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Records) != 2 || loaded.Records[0].Model != "dated" {
		t.Fatalf("order = %+v", loaded.Records)
	}
}

func TestAtomicWritesLeaveNoTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	if _, err := store.Save(testRecord("model", "2026-08-20T12:00:00Z")); err != nil {
		t.Fatal(err)
	}
	for _, scan := range []string{dir, filepath.Join(dir, historyDirName)} {
		entries, err := os.ReadDir(scan)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".fitr-record-") {
				t.Fatalf("temporary file remained in %s: %s", scan, entry.Name())
			}
		}
	}
}

func TestMissingStoreLoadsEmpty(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "missing"))
	loaded, err := store.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Records) != 0 || len(loaded.Warnings) != 0 {
		t.Fatalf("missing store = %+v", loaded)
	}
}

func TestCanonicalFailureReturnsRecoverableHistoryPath(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	r := testRecord("blocked", "2026-08-20T12:00:00Z")
	if err := os.Mkdir(store.CanonicalPath(r.Model), 0o700); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Save(r)
	if err == nil || !strings.Contains(err.Error(), "update canonical result") {
		t.Fatalf("save error = %v", err)
	}
	if saved.CanonicalPath != "" || saved.HistoryPath == "" {
		t.Fatalf("partial save paths = %+v", saved)
	}
	if _, err := os.Stat(saved.HistoryPath); err != nil {
		t.Fatalf("recoverable history copy missing: %v", err)
	}
	loaded, err := store.LoadAll()
	if err != nil || len(loaded.Records) != 1 || loaded.Records[0].RunID != saved.RunID {
		t.Fatalf("history recovery = %+v, err=%v", loaded, err)
	}
}

func TestSaveRejectsIncompleteRecords(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.Save(nil); err == nil {
		t.Fatal("nil record was accepted")
	}
	if _, err := store.Save(&Record{}); err == nil {
		t.Fatal("record without model was accepted")
	}
}

func TestLoadCurrentDoesNotResurrectDeletedCanonicalResult(t *testing.T) {
	store := NewStore(t.TempDir())
	saved, err := store.Save(testRecord("model", "2026-08-20T12:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(saved.CanonicalPath); err != nil {
		t.Fatal(err)
	}
	current, err := store.LoadCurrent()
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Records) != 0 {
		t.Fatalf("current records = %d, want 0", len(current.Records))
	}
	history, err := store.LoadAll()
	if err != nil || len(history.Records) != 1 {
		t.Fatalf("history records=%d err=%v", len(history.Records), err)
	}
}

func TestSavePreservesExistingResultDirectoryPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(dir).Save(testRecord("model", "2026-08-20T12:00:00Z")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o750 {
		t.Fatalf("existing directory mode = %o, want 750", got)
	}
}

func TestSaveRejectsDivergentContentForOneRunID(t *testing.T) {
	store := NewStore(t.TempDir())
	r := testRecord("model", "2026-08-20T12:00:00Z")
	r.RunID = "stable_run_1234"
	if _, err := store.Save(r); err != nil {
		t.Fatal(err)
	}
	r.Repeats++
	if _, err := store.Save(r); err == nil || !strings.Contains(err.Error(), "different persisted content") {
		t.Fatalf("divergent save error = %v", err)
	}
}

func TestClearHistoryKeepsCanonicalLatestResults(t *testing.T) {
	store := NewStore(t.TempDir())
	saved, err := store.Save(testRecord("model", "2026-08-20T12:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	removed, err := store.ClearHistory()
	if err != nil || removed != 1 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	if _, err := os.Stat(saved.CanonicalPath); err != nil {
		t.Fatalf("canonical result removed: %v", err)
	}
	loaded, err := store.LoadAll()
	if err != nil || len(loaded.Records) != 1 {
		t.Fatalf("loaded=%d err=%v", len(loaded.Records), err)
	}
}
