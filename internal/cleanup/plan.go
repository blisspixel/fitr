// Package cleanup inventories local storage for human review. It does not infer
// model quality or unused files, and it never removes, moves, or opens file data.
package cleanup

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

const Schema = "fitr.cleanup.plan.v1"

// IntegrityUnmeasured is the only file-integrity value this scan records.
// A sibling name, a manifest-shaped filename, and an aged partial are not
// a completed-file digest.
const IntegrityUnmeasured = "unmeasured"

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

// Component groups files that appear to belong to one artifact: a weights set
// and its shards, a vision projector, a speculative draft, an adapter, a
// tokenizer, or a runtime's libraries. Grouping is by filename only. It is a
// hint for review and never proof that the files are used together, complete,
// or safe to remove; the scanner does not open file data to confirm it.
type Component struct {
	Name          string   `json:"name"`
	Role          string   `json:"role"`
	Files         int      `json:"files"`
	ApparentBytes int64    `json:"apparent_bytes"`
	Paths         []string `json:"paths,omitempty"`
}

type Entry struct {
	Path          string    `json:"path"`
	Category      string    `json:"category"`
	ApparentBytes int64     `json:"apparent_bytes"`
	ModifiedAt    time.Time `json:"modified_at"`
	State         string    `json:"state"`
	Reason        string    `json:"reason"`
	// FileIntegrity is unmeasured. The scan does not read bytes or manifests,
	// so a partial beside a similarly named file is not a verified leftover.
	FileIntegrity string `json:"file_integrity"`
}

type Issue struct {
	Path string `json:"path"`
	Code string `json:"code"`
}

type Plan struct {
	Schema              string    `json:"schema"`
	Root                string    `json:"root"`
	AsOf                time.Time `json:"as_of"`
	Cutoff              time.Time `json:"cutoff"`
	MinAgeDays          int       `json:"min_age_days"`
	Limits              Limits    `json:"limits"`
	Complete            bool      `json:"complete"`
	StopReason          string    `json:"stop_reason,omitempty"`
	EntriesVisited      int       `json:"entries_visited"`
	Files               int       `json:"files"`
	Directories         int       `json:"directories"`
	LinksSkipped        int       `json:"links_skipped"`
	SpecialFilesSkipped int       `json:"special_files_skipped"`
	ApparentBytes       int64     `json:"apparent_bytes"`
	// AllocatedBytes sums stat's 512-byte block counts once per inode for the
	// regular files whose lengths this scan accepted. Nil means at least one
	// of those files did not report a block count or an inode, so the total
	// is unmeasured. Zero is a measured empty sum, including a walk that
	// accepted no regular files. The sum is not space a deletion would free:
	// reflinks, compression and deduplication can share blocks that stat
	// still counts, and an incomplete plan does not describe files the walk
	// did not accept.
	AllocatedBytes       *int64      `json:"allocated_bytes"`
	TopLevel             []Summary   `json:"top_level"`
	TopLevelOmitted      int         `json:"top_level_omitted"`
	Categories           []Summary   `json:"categories"`
	Components           []Component `json:"components"`
	LargestFiles         []Entry     `json:"largest_files"`
	ReviewCandidates     []Entry     `json:"review_candidates"`
	ReviewCandidateCount int         `json:"review_candidate_count"`
	ReviewApparentBytes  int64       `json:"review_apparent_bytes"`
	// CompletedFileIntegrity is unmeasured for the whole plan. Nothing in
	// this scan is a digest of a completed file, and the field is not a
	// deletion decision.
	CompletedFileIntegrity string   `json:"completed_file_integrity"`
	Issues                 []Issue  `json:"issues"`
	IssueCount             int      `json:"issue_count"`
	UnenteredDirectories   []string `json:"unentered_directories,omitempty"`
	UnenteredCount         int      `json:"unentered_directory_count,omitempty"`
	Notes                  []string `json:"notes"`
}

type scanner struct {
	ctx        context.Context
	plan       Plan
	groups     map[string]Summary
	categories map[string]Summary
	components map[string]*componentAcc
	// Hooks allow deterministic tests of filesystem failures and races.
	lstat      func(string) (os.FileInfo, error)
	openDir    func(string) (*os.File, error)
	entryInfo  func(string, os.DirEntry) (os.FileInfo, error)
	allocation func(os.FileInfo) (int64, uint64, uint64, bool)
	// allocatedKnown stays true only while every accepted file reported a
	// block count and an inode. The map records inodes already added so a
	// second name for the same file does not count its blocks again.
	allocatedKnown bool
	allocatedBytes int64
	inodes         map[inodeKey]struct{}
	queue          dirQueue
	seq            int
}

// inodeKey identifies one file across hard-linked names. It is not a
// content hash and does not see reflinks, which have distinct inodes.
type inodeKey struct {
	dev uint64
	ino uint64
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
		allocation: observedAllocation, allocatedKnown: true,
		groups: map[string]Summary{}, categories: map[string]Summary{},
		components: map[string]*componentAcc{},
		plan: Plan{Schema: Schema, Root: absolute, AsOf: o.AsOf.UTC(),
			Cutoff: o.AsOf.UTC().AddDate(0, 0, -o.MinAgeDays), MinAgeDays: o.MinAgeDays,
			Limits: o.Limits, Complete: true, CompletedFileIntegrity: IntegrityUnmeasured,
			TopLevel: []Summary{}, Categories: []Summary{},
			LargestFiles: []Entry{}, ReviewCandidates: []Entry{}, Issues: []Issue{},
			Notes: []string{
				"Read-only plan. REVIEW does not mean safe to delete; usage and dependencies remain unresolved.",
				"Apparent bytes sum regular file lengths, not recoverable disk space. Hard links, deduplication, compression and sparse files can change physical usage. Allocated bytes, when present, sum stat block counts once per inode among accepted files. Absent allocated bytes were not measured, and neither figure is space a deletion would free.",
				"Names and modification times are hints, not proof of ownership, inactivity, model quality or obsolescence. No model contents or application references were inspected.",
				"Observed symbolic links and special files are skipped. Concurrent changes or skipped entries can make this inventory incomplete.",
				"The deadline is checked between filesystem operations; a stalled filesystem operation can outlast it.",
				"Directories are entered with model stores (blobs, manifests, models) ahead of cache trees. An incomplete scan names directories that were seen and not entered.",
				"Component groups come from filenames only, not from manifests. A group does not resolve shared dependencies across models or prove that its files belong to one artifact.",
			}},
	}
}

// directoryRank spends a bounded scan on the directories this command exists
// to review. A depth-first walk gives the first cache tree the entire deadline
// and can report a few gigabytes of tool cache while never opening blobs.
func directoryRank(path string) int {
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "blobs", "manifests", "models":
		return 0
	}
	if strings.Contains(base, "cache") {
		return 2
	}
	return 1
}

type queuedDir struct {
	path  string
	depth int
	rank  int
	seq   int
}

type dirQueue []queuedDir

func (q dirQueue) Len() int { return len(q) }
func (q dirQueue) Less(i, j int) bool {
	if q[i].rank != q[j].rank {
		return q[i].rank < q[j].rank
	}
	return q[i].seq < q[j].seq
}
func (q dirQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }

// heap.Interface erases the element type. A mismatch is a programmer error,
// the same failure an unchecked assertion would raise.
func (q *dirQueue) Push(v any) {
	item, ok := v.(queuedDir)
	if !ok {
		panic(fmt.Sprintf("cleanup queue: pushed %T, want queuedDir", v))
	}
	*q = append(*q, item)
}
func (q *dirQueue) Pop() any {
	old := *q
	item := old[len(old)-1]
	*q = old[:len(old)-1]
	return item
}

func (s *scanner) enqueue(path string, depth int) {
	s.seq++
	heap.Push(&s.queue, queuedDir{path: path, depth: depth, rank: directoryRank(path), seq: s.seq})
}

// walk enters one directory, then the rest of the priority queue. Callers that
// still name a single path, including the tests for raced entries, use it.
func (s *scanner) walk(path string, depth int) {
	s.enqueue(path, depth)
	s.drain()
}

func (s *scanner) drain() {
	for s.queue.Len() > 0 {
		if s.stopped() {
			break
		}
		// Opening a directory only to have its first child trip the entry
		// limit does not review it. Leave it in the unentered set.
		if s.plan.EntriesVisited >= s.plan.Limits.MaxEntries {
			s.stop("entry_limit")
			break
		}
		popped := heap.Pop(&s.queue)
		item, ok := popped.(queuedDir)
		if !ok {
			panic(fmt.Sprintf("cleanup queue: popped %T, want queuedDir", popped))
		}
		s.enter(item.path, item.depth)
	}
	s.recordUnentered()
}

func (s *scanner) recordUnentered() {
	if s.plan.Complete && s.plan.StopReason == "" {
		return
	}
	pending := make([]string, 0, s.queue.Len())
	for _, item := range s.queue {
		if item.path == "." {
			continue
		}
		pending = append(pending, filepath.ToSlash(item.path))
	}
	sort.Strings(pending)
	s.plan.UnenteredCount = len(pending)
	if len(pending) > s.plan.Limits.MaxItems {
		pending = pending[:s.plan.Limits.MaxItems]
	}
	s.plan.UnenteredDirectories = pending
}

func (s *scanner) enter(path string, depth int) {
	if s.stopped() {
		s.enqueue(path, depth)
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
				s.issue(path, "listing_incomplete")
				return
			}
			if s.plan.EntriesVisited >= s.plan.Limits.MaxEntries {
				s.stop("entry_limit")
				s.issue(path, "listing_incomplete")
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
		if depth > s.plan.Limits.MaxDepth {
			s.issue(path, "depth_limit")
			return
		}
		s.enqueue(path, depth)
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
	s.addComponent(name, info.Size())
	entry := Entry{
		Path: name, Category: category, ApparentBytes: info.Size(), ModifiedAt: info.ModTime().UTC(),
		State: "USAGE_UNRESOLVED", Reason: "Preserve until model, shard, projector, runtime and fallback dependencies are resolved.",
		FileIntegrity: IntegrityUnmeasured,
	}
	if category == "incomplete_download" && !info.ModTime().After(s.plan.Cutoff) {
		entry.State, entry.Reason = "REVIEW", "Incomplete-download filename and modification time meet the age threshold. Verify no active transfer or resume dependency before any removal."
		s.plan.ReviewCandidateCount++
		s.plan.ReviewApparentBytes += info.Size()
		s.plan.ReviewCandidates = keepLargest(s.plan.ReviewCandidates, entry, s.plan.Limits.MaxItems)
	}
	s.plan.LargestFiles = keepLargest(s.plan.LargestFiles, entry, s.plan.Limits.MaxItems)
	s.noteAllocation(info)
}

// noteAllocation adds one file's stat blocks, once per inode. A missing
// observation drops the whole total: a sum of only the files that happened
// to report blocks would look like a measurement of the accepted set.
func (s *scanner) noteAllocation(info os.FileInfo) {
	if !s.allocatedKnown {
		return
	}
	blocks, dev, ino, ok := s.allocation(info)
	if !ok || blocks < 0 {
		s.forgetAllocation()
		return
	}
	key := inodeKey{dev: dev, ino: ino}
	if _, seen := s.inodes[key]; seen {
		return
	}
	if blocks > (1<<63-1)-s.allocatedBytes {
		s.forgetAllocation()
		return
	}
	if s.inodes == nil {
		s.inodes = map[inodeKey]struct{}{}
	}
	s.inodes[key] = struct{}{}
	s.allocatedBytes += blocks
}

func (s *scanner) forgetAllocation() {
	s.allocatedKnown = false
	s.allocatedBytes = 0
	s.inodes = nil
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
	s.plan.Components = sortedComponents(s.components, s.plan.Limits.MaxItems)
	if s.allocatedKnown {
		value := s.allocatedBytes
		s.plan.AllocatedBytes = &value
	}
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

// shardPattern matches the "<name>-<index>-of-<count>" suffix multi-file weight
// sets use, so a shard group is reviewed as one component rather than as
// unrelated large files.
var shardPattern = regexp.MustCompile(`-\d{2,5}-of-\d{2,5}`)

// componentAcc accumulates one component's files during the walk.
type componentAcc struct {
	role  string
	files int
	bytes int64
	paths []string
}

// componentKey is the filename with any shard suffix removed, so every shard of
// one set shares a key.
func componentKey(base string) string {
	if loc := shardPattern.FindStringIndex(base); loc != nil {
		return base[:loc[0]] + base[loc[1]:]
	}
	return base
}

// componentRole names the dependency role a filename suggests. It is a hint
// from the name, never a reading of the file's contents, and never proof that
// the file is used, complete, or safe to remove.
func componentRole(base string) string {
	lower := strings.ToLower(base)
	switch {
	case strings.Contains(lower, "mmproj"), strings.Contains(lower, "projector"), strings.Contains(lower, "clip"):
		return "vision_projector"
	case strings.Contains(lower, "draft"), strings.Contains(lower, "speculat"), strings.Contains(lower, "mtp"):
		return "speculative_draft"
	case strings.Contains(lower, "lora"), strings.Contains(lower, "adapter"):
		return "adapter"
	case strings.Contains(lower, "tokenizer"), strings.Contains(lower, "vocab"), strings.Contains(lower, "merges"):
		return "tokenizer"
	}
	switch filepath.Ext(lower) {
	case ".gguf", ".safetensors", ".pt", ".pth", ".onnx", ".tflite", ".ckpt", ".model":
		return "weights"
	case ".so", ".dll", ".dylib", ".exe":
		return "runtime_asset"
	default:
		return ""
	}
}

// addComponent folds one file into its inferred component group.
func (s *scanner) addComponent(name string, size int64) {
	base := filepath.Base(name)
	role := componentRole(base)
	if role == "" {
		return
	}
	key := componentKey(base)
	acc := s.components[key]
	if acc == nil {
		acc = &componentAcc{role: role}
		s.components[key] = acc
	}
	acc.files++
	acc.bytes += size
	if len(acc.paths) < s.plan.Limits.MaxItems {
		acc.paths = append(acc.paths, name)
	}
}

func sortedComponents(values map[string]*componentAcc, limit int) []Component {
	result := make([]Component, 0, len(values))
	for name, acc := range values {
		result = append(result, Component{Name: name, Role: acc.role, Files: acc.files,
			ApparentBytes: acc.bytes, Paths: acc.paths})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ApparentBytes != result[j].ApparentBytes {
			return result[i].ApparentBytes > result[j].ApparentBytes
		}
		return result[i].Name < result[j].Name
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result
}

func Bytes(value int64) string { return fmt.Sprintf("%.2f GiB", float64(value)/(1<<30)) }

// IntegrityLine is the text for file integrity. An empty value stays
// unmeasured, which is the same claim as IntegrityUnmeasured: no digest
// was read. Any other value is shown unchanged.
func IntegrityLine(value string) string {
	if value == "" || value == IntegrityUnmeasured {
		return "unmeasured; file bytes and manifests were not read"
	}
	return value
}

// AllocatedLine is the text for AllocatedBytes. Unmeasured stays a word so
// a reader cannot mistake a missing stat block count for zero disk usage.
func AllocatedLine(value *int64) string {
	if value == nil {
		return "unmeasured"
	}
	return Bytes(*value) + "; stat blocks once per inode, not space a deletion would free"
}
