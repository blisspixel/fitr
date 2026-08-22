package record

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
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

// CanonicalPath returns the collision-safe latest-result path for a model.
// A readable prefix remains for humans while the digest distinguishes model
// names that sanitize to the same filesystem spelling.
func (s Store) CanonicalPath(model string) string {
	return filepath.Join(s.dir(), canonicalName(model))
}

// LegacyCanonicalPath identifies the pre-v0.4 filename. Loaders continue to
// read it, but new writes never target it because distinct model names could
// collide after sanitization.
func (s Store) LegacyCanonicalPath(model string) string {
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
	if err := r.ValidateManifest(); err != nil {
		return SavedPaths{}, fmt.Errorf("invalid run manifest: %w", err)
	}
	if err := r.ValidateEvidenceContract(); err != nil {
		return SavedPaths{}, fmt.Errorf("invalid evidence contract: %w", err)
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
	if sameDirectory(filepath.Dir(path), s.dir()) {
		s.reconcileWithHistory(r)
	} else if r.SchemaVersion >= EvidenceSchemaVersion {
		r.storageIntegrityIssue = "external or archived result is display-only without an exact canonical current twin"
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
	loaded := loadCandidateFiles(files, warnings)
	currentIndex := indexRecordFiles(current)
	for _, record := range loaded.Records {
		reconcileWithCurrentIndex(record, currentIndex)
	}
	return loaded, nil
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
	loaded := loadCandidateFiles(files, nil)
	history, historyErr := listJSON(s.HistoryDir(), true)
	historyIndex := recordIndex{}
	if historyErr == nil {
		historyIndex = indexRecordFiles(history)
	}
	for _, record := range loaded.Records {
		reconcileWithHistoryIndex(record, historyIndex)
	}
	seen := map[string]bool{}
	latest := make([]*Record, 0, len(loaded.Records))
	for _, record := range loaded.Records {
		if seen[record.Model] {
			continue
		}
		seen[record.Model] = true
		latest = append(latest, record)
	}
	loaded.Records = latest
	return loaded, nil
}

func sameDirectory(a, b string) bool {
	infoA, statErrA := os.Stat(a)
	infoB, statErrB := os.Stat(b)
	if statErrA == nil && statErrB == nil {
		return os.SameFile(infoA, infoB)
	}
	absA, errA := filepath.Abs(filepath.Clean(a))
	absB, errB := filepath.Abs(filepath.Clean(b))
	if errA != nil || errB != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(absA, absB)
	}
	return absA == absB
}

// reconcileWithHistory keeps a valid but altered canonical file visible while
// excluding it from ranking. Only an exact immutable history twin restores the
// evidence chain established by Save.
func (s Store) reconcileWithHistory(r *Record) {
	if r == nil || r.SchemaVersion < EvidenceSchemaVersion {
		return
	}
	files, err := listJSON(s.HistoryDir(), true)
	if err != nil {
		r.storageIntegrityIssue = "canonical result has no matching immutable history entry"
		return
	}
	reconcileWithHistoryIndex(r, indexRecordFiles(files))
}

func reconcileWithHistoryIndex(r *Record, index recordIndex) {
	if r == nil || r.SchemaVersion < EvidenceSchemaVersion {
		return
	}
	foundID, exact := index.match(r)
	if exact {
		r.storageIntegrityIssue = ""
	} else if foundID {
		r.storageIntegrityIssue = "canonical result differs from its immutable history entry"
	} else {
		r.storageIntegrityIssue = "canonical result has no matching immutable history entry"
	}
}

// reconcileWithCurrent makes full-history records presentation-only unless an
// exact canonical current twin proves they came through Store.Save. Older
// archives remain inspectable, but cannot enter a ranking surface. Without a
// separate local trust root, a JSON file copied into .history is an import.
func (s Store) reconcileWithCurrent(r *Record) {
	if r == nil || r.SchemaVersion < EvidenceSchemaVersion {
		return
	}
	files, err := listJSON(s.dir(), false)
	if err != nil {
		r.storageIntegrityIssue = "external or archived result is display-only without an exact canonical current twin"
		return
	}
	reconcileWithCurrentIndex(r, indexRecordFiles(files))
}

func reconcileWithCurrentIndex(r *Record, index recordIndex) {
	if r == nil || r.SchemaVersion < EvidenceSchemaVersion {
		return
	}
	foundID, exact := index.match(r)
	if exact {
		r.storageIntegrityIssue = ""
	} else if foundID {
		r.storageIntegrityIssue = "archived result differs from its canonical current twin"
	} else {
		r.storageIntegrityIssue = "external or archived result is display-only without an exact canonical current twin"
	}
}

type recordIndex map[string]map[string]bool

func indexRecordFiles(files []candidateFile) recordIndex {
	index := recordIndex{}
	for _, file := range files {
		b, err := os.ReadFile(file.path)
		if err != nil {
			continue
		}
		r, err := decodeRecord(b)
		if err != nil {
			continue
		}
		id := r.EnsureRunID()
		if index[id] == nil {
			index[id] = map[string]bool{}
		}
		index[id][recordContentHash(r)] = true
	}
	return index
}

func (index recordIndex) match(r *Record) (foundID, exact bool) {
	if r == nil {
		return false, false
	}
	contents, foundID := index[r.EnsureRunID()]
	return foundID, contents[recordContentHash(r)]
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
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("invalid result JSON: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("invalid result JSON: content after the result")
		}
		return nil, fmt.Errorf("invalid result JSON: %w", err)
	}
	if strings.TrimSpace(r.Model) == "" {
		return nil, errors.New("not a fitr result: missing model")
	}
	if err := r.ValidateManifest(); err != nil {
		return nil, fmt.Errorf("invalid run manifest: %w", err)
	}
	if err := r.ValidateEvidenceContract(); err != nil {
		return nil, fmt.Errorf("invalid evidence contract: %w", err)
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

func canonicalName(model string) string {
	return ArtifactStem(model) + ".json"
}

// ArtifactStem returns a readable, collision-resistant filename stem for an
// untrusted logical name. It contains only portable ASCII filename characters.
func ArtifactStem(name string) string {
	prefix := strings.Trim(safeName(name), " ._-")
	if prefix == "" {
		prefix = "model"
	}
	if len(prefix) > 80 {
		prefix = prefix[:80]
	}
	sum := sha256.Sum256([]byte(name))
	return prefix + "--" + hex.EncodeToString(sum[:16])
}
