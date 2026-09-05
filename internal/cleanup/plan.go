// Package cleanup inventories local storage for human review. It does not infer
// model quality or unused files, and it never removes, moves, or opens file data.
package cleanup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const Schema = "fitr.cleanup.plan.v1"

type Limits struct {
	MaxEntries int `json:"max_entries"`
	MaxDepth   int `json:"max_depth"`
	MaxSeconds int `json:"max_seconds"`
	MaxItems   int `json:"max_items_per_list"`
}

func DefaultLimits() Limits { return Limits{250000, 64, 30, 20} }

type Options struct {
	AsOf       time.Time
	MinAgeDays int
	Limits     Limits
}

type Summary struct {
	Name          string `json:"name"`
	Files         int    `json:"files"`
	ApparentBytes int64  `json:"apparent_bytes"`
}

type Entry struct {
	Path          string    `json:"path"`
	Category      string    `json:"category"`
	ApparentBytes int64     `json:"apparent_bytes"`
	ModifiedAt    time.Time `json:"modified_at"`
	State         string    `json:"state"`
	Reason        string    `json:"reason"`
}

type Issue struct {
	Path string `json:"path"`
	Code string `json:"code"`
}

type Plan struct {
	Schema               string    `json:"schema"`
	Root                 string    `json:"root"`
	AsOf                 time.Time `json:"as_of"`
	Cutoff               time.Time `json:"cutoff"`
	MinAgeDays           int       `json:"min_age_days"`
	Limits               Limits    `json:"limits"`
	Complete             bool      `json:"complete"`
	StopReason           string    `json:"stop_reason,omitempty"`
	EntriesVisited       int       `json:"entries_visited"`
	Files                int       `json:"files"`
	Directories          int       `json:"directories"`
	LinksSkipped         int       `json:"links_skipped"`
	SpecialFilesSkipped  int       `json:"special_files_skipped"`
	ApparentBytes        int64     `json:"apparent_bytes"`
	TopLevel             []Summary `json:"top_level"`
	TopLevelOmitted      int       `json:"top_level_omitted"`
	Categories           []Summary `json:"categories"`
	LargestFiles         []Entry   `json:"largest_files"`
	ReviewCandidates     []Entry   `json:"review_candidates"`
	ReviewCandidateCount int       `json:"review_candidate_count"`
	ReviewApparentBytes  int64     `json:"review_apparent_bytes"`
	Issues               []Issue   `json:"issues"`
	IssueCount           int       `json:"issue_count"`
	Notes                []string  `json:"notes"`
}

type scanner struct {
	ctx        context.Context
	plan       Plan
	groups     map[string]Summary
	categories map[string]Summary
	// Hooks allow deterministic tests of filesystem failures and races.
	lstat     func(string) (os.FileInfo, error)
	openDir   func(string) (*os.File, error)
	entryInfo func(string, os.DirEntry) (os.FileInfo, error)
}

func Scan(ctx context.Context, directory string, options Options) (Plan, error) {
	if options.AsOf.IsZero() {
		options.AsOf = time.Now()
	}
	if options.Limits == (Limits{}) {
		options.Limits = DefaultLimits()
	}
	if err := validateOptions(options); err != nil {
		return Plan{}, err
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return Plan{}, errors.New("cannot resolve cleanup root")
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return Plan{}, errors.New("cannot inspect cleanup root")
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Plan{}, errors.New("cleanup root must be a directory, not a symbolic link")
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return Plan{}, errors.New("cannot open cleanup root")
	}
	defer func() { _ = root.Close() }()
	opened, err := root.Lstat(".")
	if err != nil || !os.SameFile(info, opened) {
		return Plan{}, errors.New("cleanup root changed while opening")
	}
	bounded, cancel := context.WithTimeout(ctx, time.Duration(options.Limits.MaxSeconds)*time.Second)
	defer cancel()
	s := newScanner(bounded, root, absolute, options)
	s.walk(".", 0)
	s.finish()
	return s.plan, nil
}

func validateOptions(o Options) error {
	if o.MinAgeDays < 0 || o.MinAgeDays > 36500 {
		return errors.New("min-age-days must be between 0 and 36500")
	}
	l := o.Limits
	if l.MaxEntries < 1 || l.MaxEntries > 250000 || l.MaxDepth < 1 || l.MaxDepth > 64 ||
		l.MaxSeconds < 1 || l.MaxSeconds > 30 || l.MaxItems < 1 || l.MaxItems > 100 {
		return errors.New("cleanup limits exceed the supported scan bounds")
	}
	return nil
}

func newScanner(ctx context.Context, root *os.Root, absolute string, o Options) *scanner {
	openDirectory := func(path string) (*os.File, error) {
		directory, err := root.OpenRoot(path)
		if err != nil {
			return nil, err
		}
		defer func() { _ = directory.Close() }()
		return directory.Open(".")
	}
	entryInfo := func(path string, entry os.DirEntry) (os.FileInfo, error) {
		// Windows directory enumeration already includes lstat-style metadata.
		// Unix DirEntry.Info can resolve through a renamed parent by pathname,
		// so use the confined root there instead.
		if runtime.GOOS == "windows" {
			return entry.Info()
		}
		return root.Lstat(path)
	}
	return &scanner{ctx: ctx, lstat: root.Lstat, openDir: openDirectory, entryInfo: entryInfo,
		groups: map[string]Summary{}, categories: map[string]Summary{},
		plan: Plan{Schema: Schema, Root: absolute, AsOf: o.AsOf.UTC(),
			Cutoff: o.AsOf.UTC().AddDate(0, 0, -o.MinAgeDays), MinAgeDays: o.MinAgeDays,
			Limits: o.Limits, Complete: true, TopLevel: []Summary{}, Categories: []Summary{},
			LargestFiles: []Entry{}, ReviewCandidates: []Entry{}, Issues: []Issue{},
			Notes: []string{
				"Read-only plan. REVIEW does not mean safe to delete; usage and dependencies remain unresolved.",
				"Apparent bytes sum regular file lengths, not recoverable disk space. Hard links, deduplication, compression and sparse files can change physical usage.",
				"Names and modification times are hints, not proof of ownership, inactivity, model quality or obsolescence. No model contents or application references were inspected.",
				"Observed symbolic links and special files are skipped. Concurrent changes or skipped entries can make this inventory incomplete.",
				"The deadline is checked between filesystem operations; a stalled filesystem operation can outlast it.",
			}},
	}
}

func (s *scanner) walk(path string, depth int) {
	if s.stopped() {
		return
	}
	if depth > s.plan.Limits.MaxDepth {
		s.issue(path, "depth_limit")
		return
	}
	before, err := s.lstat(path)
	if err != nil {
		s.issue(path, "stat_failed")
		return
	}
	if before.Mode()&os.ModeSymlink != 0 {
		s.plan.LinksSkipped++
		s.issue(path, "link_skipped")
		return
	}
	if !before.IsDir() {
		s.issue(path, "directory_changed")
		return
	}
	directory, err := s.openDir(path)
	if err != nil {
		s.issue(path, "directory_unreadable")
		return
	}
	defer directory.Close()
	opened, err := directory.Stat()
	if err != nil || !os.SameFile(before, opened) {
		s.issue(path, "directory_changed")
		return
	}
	after, err := s.lstat(path)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, after) {
		s.issue(path, "directory_changed")
		return
	}
	s.readDirectory(directory, path, depth)
}

func (s *scanner) readDirectory(directory *os.File, path string, depth int) {
	for !s.stopped() {
		entries, err := directory.ReadDir(128)
		for _, entry := range entries {
			if s.stopped() {
				return
			}
			if s.plan.EntriesVisited >= s.plan.Limits.MaxEntries {
				s.stop("entry_limit")
				return
			}
			s.plan.EntriesVisited++
			s.visit(filepath.Join(path, entry.Name()), depth+1, entry)
		}
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			s.issue(path, "directory_read_failed")
			return
		}
	}
}

func (s *scanner) visit(path string, depth int, entry os.DirEntry) {
	info, err := s.entryInfo(path, entry)
	if err != nil {
		s.issue(path, "stat_failed")
		return
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		s.plan.LinksSkipped++
		s.issue(path, "link_skipped")
	case info.IsDir():
		s.plan.Directories++
		s.walk(path, depth)
	case info.Mode().IsRegular():
		s.addFile(path, info)
	default:
		s.plan.SpecialFilesSkipped++
		s.issue(path, "special_file_skipped")
	}
}

func (s *scanner) stopped() bool {
	if s.plan.StopReason != "" {
		return true
	}
	if err := s.ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			s.stop("deadline")
		} else {
			s.stop("cancelled")
		}
		return true
	}
	return false
}

func (s *scanner) stop(reason string) { s.plan.Complete, s.plan.StopReason = false, reason }

func (s *scanner) issue(path, code string) {
	s.plan.Complete = false
	s.plan.IssueCount++
	if len(s.plan.Issues) < s.plan.Limits.MaxItems {
		s.plan.Issues = append(s.plan.Issues, Issue{filepath.ToSlash(path), code})
	}
}

func (s *scanner) addFile(path string, info os.FileInfo) {
	if info.Size() < 0 || info.Size() > (1<<63-1)-s.plan.ApparentBytes {
		s.issue(path, "size_invalid")
		return
	}
	s.plan.Files++
	s.plan.ApparentBytes += info.Size()
	name := filepath.ToSlash(path)
	category := classify(name)
	group, _, nested := strings.Cut(name, "/")
	if !nested {
		group = ". (root files)"
	}
	addSummary(s.groups, group, info.Size())
	addSummary(s.categories, category, info.Size())
	entry := Entry{name, category, info.Size(), info.ModTime().UTC(), "USAGE_UNRESOLVED", "Preserve until model, shard, projector, runtime and fallback dependencies are resolved."}
	if category == "incomplete_download" && !info.ModTime().After(s.plan.Cutoff) {
		entry.State, entry.Reason = "REVIEW", "Incomplete-download filename and modification time meet the age threshold. Verify no active transfer or resume dependency before any removal."
		s.plan.ReviewCandidateCount++
		s.plan.ReviewApparentBytes += info.Size()
		s.plan.ReviewCandidates = keepLargest(s.plan.ReviewCandidates, entry, s.plan.Limits.MaxItems)
	}
	s.plan.LargestFiles = keepLargest(s.plan.LargestFiles, entry, s.plan.Limits.MaxItems)
}

func addSummary(summaries map[string]Summary, name string, size int64) {
	value := summaries[name]
	value.Name, value.Files, value.ApparentBytes = name, value.Files+1, value.ApparentBytes+size
	summaries[name] = value
}

func keepLargest(entries []Entry, entry Entry, limit int) []Entry {
	entries = append(entries, entry)
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].ApparentBytes != entries[j].ApparentBytes {
			return entries[i].ApparentBytes > entries[j].ApparentBytes
		}
		return entries[i].Path < entries[j].Path
	})
	return entries[:min(len(entries), limit)]
}

func (s *scanner) finish() {
	s.stopped()
	s.plan.TopLevel = sortedSummaries(s.groups)
	if len(s.plan.TopLevel) > s.plan.Limits.MaxItems {
		s.plan.TopLevelOmitted = len(s.plan.TopLevel) - s.plan.Limits.MaxItems
		s.plan.TopLevel = s.plan.TopLevel[:s.plan.Limits.MaxItems]
	}
	s.plan.Categories = sortedSummaries(s.categories)
}

func sortedSummaries(values map[string]Summary) []Summary {
	result := make([]Summary, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ApparentBytes != result[j].ApparentBytes {
			return result[i].ApparentBytes > result[j].ApparentBytes
		}
		return result[i].Name < result[j].Name
	})
	return result
}

func classify(path string) string {
	lower := strings.ToLower(path)
	switch filepath.Ext(lower) {
	case ".incomplete", ".part", ".partial", ".crdownload", ".download":
		return "incomplete_download"
	}
	for _, segment := range strings.Split(lower, "/") {
		switch segment {
		case ".cache", "cache", "caches", "build", "builds", ".venv", "venv", "node_modules", "__pycache__", ".git":
			return "build_or_runtime_cache"
		}
		if strings.HasPrefix(segment, "cmake-build-") {
			return "build_or_runtime_cache"
		}
	}
	switch filepath.Ext(lower) {
	case ".gguf", ".safetensors", ".pt", ".pth", ".bin", ".onnx", ".tflite", ".ckpt", ".model":
		return "possible_model_asset"
	case ".exe", ".dll", ".so", ".dylib":
		return "runtime_asset"
	default:
		return "other"
	}
}

func Bytes(value int64) string { return fmt.Sprintf("%.2f GiB", float64(value)/(1<<30)) }
