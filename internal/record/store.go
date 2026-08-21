package record

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const historyDirName = ".history"

var saveMu sync.Mutex

// Store manages canonical latest results and their immutable local history.
type Store struct {
	Dir string
}

// SavedPaths identifies the two local representations written by Save.
type SavedPaths struct {
	RunID         string
	CanonicalPath string
	HistoryPath   string
}

// FileWarning describes a record file that could not be trusted. Loading
// continues so one damaged local result cannot hide every healthy result.
type FileWarning struct {
	Path string
	Err  error
}

func (w FileWarning) Error() string {
	return fmt.Sprintf("%s: %v", w.Path, w.Err)
}

// LoadResult is a newest-first, deduplicated record set plus non-fatal file
// warnings encountered while reading it.
type LoadResult struct {
	Records  []*Record
	Warnings []FileWarning
}

// DefaultDir returns fitr's configured result directory.
func DefaultDir() string {
	if d := os.Getenv("FITR_RESULTS"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "results"
	}
	return filepath.Join(home, ".fitr", "results")
}

// NewStore constructs a store rooted at dir. An empty dir uses DefaultDir.
func NewStore(dir string) Store {
	if dir == "" {
		dir = DefaultDir()
	}
	return Store{Dir: dir}
}

// CanonicalPath returns the backwards-compatible latest-result path for a
// model. Existing commands and user scripts can continue to use this file.
func (s Store) CanonicalPath(model string) string {
	return filepath.Join(s.dir(), safeName(model)+".json")
}

// HistoryDir returns the fitr-owned archive directory.
func (s Store) HistoryDir() string { return filepath.Join(s.dir(), historyDirName) }

// ClearHistory removes only immutable history entries and recreates the
// private archive directory. Canonical latest results are untouched.
func (s Store) ClearHistory() (int, error) {
	root := filepath.Clean(s.dir())
	target := filepath.Clean(s.HistoryDir())
	rel, err := filepath.Rel(root, target)
	if err != nil || rel != historyDirName {
		return 0, errors.New("refusing to clear history outside the result directory")
	}
	entries, err := os.ReadDir(target)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			count++
		}
	}
	if err := os.RemoveAll(target); err != nil {
		return 0, err
	}
	if err := privateDir(target); err != nil {
		return 0, err
	}
	return count, nil
}

func (s Store) dir() string {
	if s.Dir != "" {
		return s.Dir
	}
	return DefaultDir()
}

// Save atomically updates <model>.json and atomically appends an immutable
// history entry. Repeating Save with identical content reuses the same history
// path rather than manufacturing a duplicate run. History is written first;
// if the canonical update then fails, the returned HistoryPath identifies the
// recoverable copy even though Save still returns an error.
func (s Store) Save(r *Record) (SavedPaths, error) {
	saveMu.Lock()
	defer saveMu.Unlock()
	if r == nil {
		return SavedPaths{}, errors.New("cannot save a nil record")
	}
	if strings.TrimSpace(r.Model) == "" {
		return SavedPaths{}, errors.New("cannot save a record without a model")
	}
	runID := r.EnsureRunID()
	if runID == "" {
		return SavedPaths{}, errors.New("could not derive a run ID")
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return SavedPaths{}, err
	}
	b = append(b, '\n')

	dir := s.dir()
	historyDir := filepath.Join(dir, historyDirName)
	if err := storeDir(dir); err != nil {
		return SavedPaths{}, err
	}
	if err := privateDir(historyDir); err != nil {
		return SavedPaths{}, err
	}

	contentSum := sha256.Sum256(b)
	stamp := historyStamp(r.StartedAt)
	historyName := fmt.Sprintf("%s-%s-%s.json", stamp, runID, hex.EncodeToString(contentSum[:6]))
	historyPath := filepath.Join(historyDir, historyName)
	if err := rejectDivergentRunID(historyDir, historyName, runID); err != nil {
		return SavedPaths{}, err
	}
	if err := writeImmutable(historyPath, b); err != nil {
		return SavedPaths{}, fmt.Errorf("append history: %w", err)
	}
	saved := SavedPaths{RunID: runID, HistoryPath: historyPath}

	canonicalPath := s.CanonicalPath(r.Model)
	if err := writeAtomic(canonicalPath, b); err != nil {
		return saved, fmt.Errorf("update canonical result: %w", err)
	}
	saved.CanonicalPath = canonicalPath
	return saved, nil
}

func rejectDivergentRunID(historyDir, targetName, runID string) error {
	entries, err := os.ReadDir(historyDir)
	if err != nil {
		return err
	}
	marker := "-" + runID + "-"
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == targetName {
			continue
		}
		if strings.Contains(entry.Name(), marker) && strings.HasSuffix(entry.Name(), ".json") {
			return fmt.Errorf("run ID %s already has different persisted content", runID)
		}
	}
	return nil
}

// Read loads one result file. Unlike directory loading, a valid JSON document
// without a model is an error because the caller explicitly selected it.
func (s Store) Read(path string) (*Record, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	r, err := decodeRecord(b)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// LoadAll reads legacy/current root files and immutable history, deduplicates
// the two representations, and returns records newest first.
func (s Store) LoadAll() (LoadResult, error) {
	var files []candidateFile
	warnings := []FileWarning{}

	current, err := listJSON(s.dir(), false)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return LoadResult{}, err
	}
	files = append(files, current...)

	history, err := listJSON(filepath.Join(s.dir(), historyDirName), true)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return LoadResult{}, err
	}
	files = append(files, history...)
	return loadCandidateFiles(files, warnings), nil
}

// LoadCurrent reads only the backwards-compatible canonical result files.
// Ordinary commands use this path so deleting a canonical file keeps its
// established meaning. Full history remains available only through LoadAll.
func (s Store) LoadCurrent() (LoadResult, error) {
	files, err := listJSON(s.dir(), false)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return LoadResult{}, nil
		}
		return LoadResult{}, err
	}
	return loadCandidateFiles(files, nil), nil
}

func loadCandidateFiles(files []candidateFile, warnings []FileWarning) LoadResult {
	loaded := make([]loadedRecord, 0, len(files))
	for _, file := range files {
		b, err := os.ReadFile(file.path)
		if err != nil {
			warnings = append(warnings, FileWarning{Path: file.path, Err: err})
			continue
		}
		r, err := decodeRecord(b)
		if err != nil {
			if file.history || !validNonRecordJSON(b) {
				warnings = append(warnings, FileWarning{Path: file.path, Err: err})
			}
			continue
		}
		id := r.EnsureRunID()
		loaded = append(loaded, loadedRecord{
			record: r, path: file.path, history: file.history,
			id: id, content: recordContentHash(r), started: parseStartedAt(r.StartedAt),
		})
	}

	sort.SliceStable(loaded, func(i, j int) bool {
		return loadedLess(loaded[i], loaded[j])
	})

	seen := map[string]loadedRecord{}
	records := make([]*Record, 0, len(loaded))
	for _, item := range loaded {
		if prior, ok := seen[item.id]; ok {
			if prior.content != item.content {
				warnings = append(warnings, FileWarning{Path: item.path, Err: fmt.Errorf(
					"run ID %s has different content in %s", item.id, prior.path)})
			}
			continue
		}
		seen[item.id] = item
		records = append(records, item.record)
	}
	sort.Slice(warnings, func(i, j int) bool { return warnings[i].Path < warnings[j].Path })
	return LoadResult{Records: records, Warnings: warnings}
}

type candidateFile struct {
	path    string
	history bool
}

type loadedRecord struct {
	record  *Record
	path    string
	history bool
	id      string
	content string
	started time.Time
}

func listJSON(dir string, history bool) ([]candidateFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]candidateFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		out = append(out, candidateFile{path: filepath.Join(dir, entry.Name()), history: history})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, nil
}

func decodeRecord(b []byte) (*Record, error) {
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("invalid result JSON: %w", err)
	}
	if strings.TrimSpace(r.Model) == "" {
		return nil, errors.New("not a fitr result: missing model")
	}
	return &r, nil
}

// Other fitr artifacts, such as calibration and retonr JSON, may legitimately
// share the results directory. Valid JSON without a top-level model is ignored
// in the root rather than mislabeled as corruption.
func validNonRecordJSON(b []byte) bool {
	var v any
	return json.Unmarshal(b, &v) == nil
}

func recordContentHash(r *Record) string {
	b, err := json.Marshal(r)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func loadedLess(a, b loadedRecord) bool {
	if !a.started.Equal(b.started) {
		if a.started.IsZero() {
			return false
		}
		if b.started.IsZero() {
			return true
		}
		return a.started.After(b.started)
	}
	if a.history != b.history {
		return a.history
	}
	if a.record.Model != b.record.Model {
		return a.record.Model < b.record.Model
	}
	if a.id != b.id {
		return a.id < b.id
	}
	return a.path < b.path
}

func parseStartedAt(value string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return t
}

func historyStamp(value string) string {
	if t := parseStartedAt(value); !t.IsZero() {
		return t.UTC().Format("20060102T150405.000000000Z")
	}
	return "undated"
}

func privateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func storeDir(path string) error {
	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("result path is not a directory: %s", path)
		}
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func writeImmutable(path string, b []byte) error {
	if existing, err := os.ReadFile(path); err == nil {
		if bytes.Equal(existing, b) {
			return nil
		}
		return errors.New("history path already contains different content")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return writeAtomic(path, b)
}

func writeAtomic(path string, b []byte) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".fitr-record-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()
	if err = tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err = tmp.Write(b); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmpPath, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
