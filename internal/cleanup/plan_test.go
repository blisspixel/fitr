package cleanup

import (
	"container/heap"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

func fixtureFile(t *testing.T, root, path string, size int, modified time.Time) string {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(full, modified, modified); err != nil {
		t.Fatal(err)
	}
	return full
}

func testOptions() Options { return Options{AsOf: testNow, MinAgeDays: 7, Limits: DefaultLimits()} }

// Files that name one artifact are grouped so a human reviews a shard set and
// its projector or adapter together. The role is a filename hint, never a
// reading of the contents or proof that the group is safe to remove.
func TestComponentsGroupShardsAndRoles(t *testing.T) {
	root := t.TempDir()
	fixtureFile(t, root, "models/model-00001-of-00002.safetensors", 100, testNow)
	fixtureFile(t, root, "models/model-00002-of-00002.safetensors", 100, testNow)
	fixtureFile(t, root, "models/mmproj-model.gguf", 64, testNow)
	fixtureFile(t, root, "models/model.lora.safetensors", 32, testNow)
	plan, err := Scan(context.Background(), root, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	components := map[string]Component{}
	for _, component := range plan.Components {
		components[component.Name] = component
	}
	if weights, ok := components["models/model.safetensors"]; !ok || weights.Role != "weights" || weights.Files != 2 || weights.ApparentBytes != 200 || weights.Ownership != OwnershipUnmeasured {
		t.Fatalf("weights component = %+v ok=%v", weights, ok)
	}
	if projector, ok := components["models/mmproj-model.gguf"]; !ok || projector.Role != "vision_projector" || projector.Ownership != OwnershipUnmeasured {
		t.Fatalf("projector component = %+v ok=%v", projector, ok)
	}
	if adapter, ok := components["models/model.lora.safetensors"]; !ok || adapter.Role != "adapter" || adapter.Ownership != OwnershipUnmeasured {
		t.Fatalf("adapter component = %+v ok=%v", adapter, ok)
	}
}

func TestSameShardNameInTwoDirectoriesIsNotOneOwner(t *testing.T) {
	root := t.TempDir()
	fixtureFile(t, root, "video/model-00001-of-00002.safetensors", 100, testNow)
	fixtureFile(t, root, "audio/model-00001-of-00002.safetensors", 80, testNow)
	plan, err := Scan(t.Context(), root, testOptions())
	if err != nil || !plan.Complete {
		t.Fatal(err)
	}
	components := map[string]Component{}
	for _, component := range plan.Components {
		components[component.Name] = component
	}
	video, audio := components["video/model.safetensors"], components["audio/model.safetensors"]
	if video.Files != 1 || audio.Files != 1 || video.Ownership != OwnershipUnmeasured || audio.Ownership != OwnershipUnmeasured {
		t.Fatalf("same shard name became one owner: video=%+v audio=%+v all=%+v", video, audio, plan.Components)
	}
}

func TestScanInventoryPreservesDependenciesAndReviewsOnlyAgedDownloads(t *testing.T) {
	root := t.TempDir()
	old, recent := testNow.AddDate(0, 0, -8), testNow.Add(-time.Hour)
	fixtureFile(t, root, "video/model-00001-of-00002.safetensors", 40, old)
	fixtureFile(t, root, "video/mmproj.gguf", 30, old)
	fixtureFile(t, root, "runtime/build/library.dll", 20, old)
	fixtureFile(t, root, "video/.cache/aged.incomplete", 60, old)
	fixtureFile(t, root, "video/.cache/current.incomplete", 50, recent)
	fixtureFile(t, root, "exact.part", 10, testNow.AddDate(0, 0, -7))
	fixtureFile(t, root, "important.tmp", 5, old)
	plan, err := Scan(t.Context(), root, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Complete || plan.ApparentBytes != 215 || plan.Files != 7 || plan.ReviewCandidateCount != 2 || plan.ReviewApparentBytes != 70 {
		t.Fatalf("incorrect inventory: %+v", plan)
	}
	if plan.TopLevel[0].Name != "video" || plan.TopLevel[0].ApparentBytes != 180 {
		t.Fatalf("bad grouping: %+v", plan.TopLevel)
	}
	if plan.ReviewCandidates[0].Path != "video/.cache/aged.incomplete" || plan.LargestFiles[0].State != "REVIEW" {
		t.Fatal("missing aged download")
	}
	for _, entry := range plan.LargestFiles {
		if entry.Category != "incomplete_download" && entry.State != "USAGE_UNRESOLVED" {
			t.Fatalf("dependency classified disposable: %+v", entry)
		}
		if filepath.IsAbs(entry.Path) {
			t.Fatalf("entry path is absolute: %s", entry.Path)
		}
	}
	joined := strings.Join(plan.Notes, " ")
	if plan.AsOf != testNow || plan.Cutoff != testNow.AddDate(0, 0, -7) || !strings.Contains(joined, "Hard links") || !strings.Contains(joined, "Allocated bytes") {
		t.Fatal("lost provenance or accounting limits")
	}
	if _, err := os.Stat(filepath.Join(root, "exact.part")); err != nil {
		t.Fatalf("read-only scan changed storage: %v", err)
	}
}

func TestScanBoundedAndCancelled(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.part", "b.part", "c.part"} {
		fixtureFile(t, root, name, 1, testNow.AddDate(0, 0, -8))
	}
	o := testOptions()
	o.Limits.MaxEntries, o.Limits.MaxItems = 2, 1
	plan, err := Scan(t.Context(), root, o)
	if err != nil || plan.Complete || plan.StopReason != "entry_limit" || plan.EntriesVisited != 2 || len(plan.LargestFiles) != 1 || plan.ReviewCandidateCount != 2 {
		t.Fatalf("entry/list bounds failed: %+v, %v", plan, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	plan, err = Scan(ctx, root, testOptions())
	if err != nil || plan.Complete || plan.StopReason != "cancelled" || plan.Files != 0 {
		t.Fatalf("cancellation failed: %+v, %v", plan, err)
	}
	ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	plan, err = Scan(ctx, root, testOptions())
	if err != nil || plan.StopReason != "deadline" {
		t.Fatalf("deadline failed: %+v, %v", plan, err)
	}
}

func TestScanDepthGroupsAndIssueBounds(t *testing.T) {
	root := t.TempDir()
	fixtureFile(t, root, "a/one/file.gguf", 10, testNow)
	fixtureFile(t, root, "b/two/file.gguf", 10, testNow)
	fixtureFile(t, root, "a/shallow.gguf", 20, testNow)
	fixtureFile(t, root, "b/shallow.gguf", 20, testNow)
	o := testOptions()
	o.Limits.MaxDepth, o.Limits.MaxItems = 1, 1
	plan, err := Scan(t.Context(), root, o)
	if err != nil || plan.Complete || plan.IssueCount != 2 || len(plan.Issues) != 1 || plan.Issues[0].Code != "depth_limit" || plan.TopLevelOmitted != 1 || plan.ApparentBytes != 40 {
		t.Fatalf("depth/group bounds failed: %+v, %v", plan, err)
	}
}

func TestScanSkipsSymlinkEscapeAndRejectsLinkedRoot(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	fixtureFile(t, outside, "secret.gguf", 100, testNow)
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	plan, err := Scan(t.Context(), root, testOptions())
	if err != nil || plan.Complete || plan.ApparentBytes != 0 || plan.LinksSkipped != 1 || plan.Issues[0].Code != "link_skipped" {
		t.Fatalf("followed link: %+v, %v", plan, err)
	}
	if _, err := Scan(t.Context(), filepath.Join(root, "linked"), testOptions()); err == nil {
		t.Fatal("accepted symbolic root")
	}
}

func TestScanHardLinksAreApparentBytesNotRecoveryPromises(t *testing.T) {
	root := t.TempDir()
	source := fixtureFile(t, root, "first.gguf", 100, testNow)
	if err := os.Link(source, filepath.Join(root, "second.gguf")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	plan, err := Scan(t.Context(), root, testOptions())
	if err != nil || plan.ApparentBytes != 200 || plan.Files != 2 || plan.ReviewCandidateCount != 0 {
		t.Fatalf("hard-link accounting changed or implied cleanup: %+v, %v", plan, err)
	}
	if !strings.Contains(strings.Join(plan.Notes, " "), "not recoverable disk space") {
		t.Fatal("missing physical allocation uncertainty")
	}
	info, err := os.Lstat(source)
	if err != nil {
		t.Fatal(err)
	}
	blocks, _, _, ok := observedAllocation(info)
	if !ok {
		if plan.AllocatedBytes != nil {
			t.Fatalf("allocated bytes were invented without a stat block count: %d", *plan.AllocatedBytes)
		}
		return
	}
	if blocks <= 0 {
		t.Fatalf("stat reported no blocks for a %d-byte file", info.Size())
	}
	if plan.AllocatedBytes == nil || *plan.AllocatedBytes != blocks {
		got := int64(-1)
		if plan.AllocatedBytes != nil {
			got = *plan.AllocatedBytes
		}
		t.Fatalf("hard-linked names changed the stat block total: got %d want %d apparent %d", got, blocks, plan.ApparentBytes)
	}
}

func TestWindowsEnumerationReusesMetadataWithoutResolvingFilePaths(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows directory metadata optimization")
	}
	s, root := scannerFixture(t)
	info, err := os.Lstat(filepath.Join(root, "a", "file.part"))
	if err != nil {
		t.Fatal(err)
	}
	// The enumerated observation remains usable after its path disappears;
	// resolving this nonexistent name would cause a redundant metadata error.
	s.visit("already-enumerated.part", 1, fs.FileInfoToDirEntry(info))
	s.finish()
	if !s.plan.Complete || s.plan.Files != 1 || s.plan.ApparentBytes != 1 || s.plan.AllocatedBytes != nil {
		t.Fatalf("enumerated metadata was resolved again or copied into allocated bytes: %+v", s.plan)
	}
}

func TestScanRejectsBadRootAndOptions(t *testing.T) {
	root := t.TempDir()
	file := fixtureFile(t, root, "file", 1, testNow)
	for _, path := range []string{file, filepath.Join(root, "missing")} {
		if _, err := Scan(t.Context(), path, testOptions()); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
	for _, age := range []int{-1, 36501} {
		o := testOptions()
		o.MinAgeDays = age
		if _, err := Scan(t.Context(), root, o); err == nil {
			t.Fatal("accepted invalid age")
		}
	}
	for _, limit := range []Limits{{-1, 1, 1, 1}, {250001, 1, 1, 1}, {1, 65, 1, 1}, {1, 1, 31, 1}, {1, 1, 1, 101}} {
		o := testOptions()
		o.Limits = limit
		if _, err := Scan(t.Context(), root, o); err == nil {
			t.Fatal("accepted invalid limit")
		}
	}
	plan, err := Scan(t.Context(), root, Options{})
	if err != nil || plan.AsOf.IsZero() || plan.Limits != DefaultLimits() {
		t.Fatalf("defaults failed: %+v, %v", plan, err)
	}
}

func TestScanEntersModelStoresBeforeCacheTrees(t *testing.T) {
	root := t.TempDir()
	// Create the cache first. A depth-first walk that follows creation order
	// spends the entry budget inside it and never stats the blob.
	for i := range 20 {
		fixtureFile(t, root, fmt.Sprintf(".tool-cache/n%d/f.bin", i), 1, testNow)
	}
	fixtureFile(t, root, "blobs/weights.gguf", 5000, testNow)
	o := testOptions()
	o.Limits.MaxEntries = 3
	plan, err := Scan(t.Context(), root, o)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Complete || plan.StopReason != "entry_limit" {
		t.Fatalf("budget was not exhausted: %+v", plan)
	}
	if plan.ApparentBytes < 5000 {
		t.Fatalf("model blob was not counted: %+v", plan)
	}
	if len(plan.LargestFiles) == 0 || plan.LargestFiles[0].Path != "blobs/weights.gguf" {
		t.Fatalf("largest file = %+v, want the blob", plan.LargestFiles)
	}
	if plan.UnenteredCount < 1 {
		t.Fatalf("incomplete scan did not name a directory it skipped: %+v", plan)
	}
	joined := strings.Join(plan.UnenteredDirectories, ",")
	if !strings.Contains(joined, ".tool-cache") {
		t.Fatalf("unentered = %v, want the cache tree", plan.UnenteredDirectories)
	}
}

func TestDirectoryRankPrefersModelStoresOverCaches(t *testing.T) {
	if directoryRank("blobs") >= directoryRank(".tool-cache") || directoryRank("manifests") >= directoryRank("notes") {
		t.Fatal("model stores do not outrank cache and ordinary directories")
	}
	// Insertion order is the cache first. Equal ranks would review that cache
	// before the blob directory that was listed second.
	s := &scanner{}
	s.enqueue(".tool-cache/nested", 1)
	s.enqueue("blobs", 1)
	first := heap.Pop(&s.queue).(queuedDir)
	if first.path != "blobs" {
		t.Fatalf("next directory = %s, want blobs ahead of the earlier cache", first.path)
	}
}

func TestClassificationIsConservativeAndDeterministic(t *testing.T) {
	for path, want := range map[string]string{
		"MODEL.GGUF": "possible_model_asset", "one/model.safetensors": "possible_model_asset",
		"CACHE/weights.bin": "build_or_runtime_cache", "runtime/cmake-build-debug/lib.so": "build_or_runtime_cache",
		"runtime/llama.exe": "runtime_asset", "data/notes.txt": "other", "x.partial": "incomplete_download",
	} {
		if got := classify(path); got != want {
			t.Fatalf("%s: got %s, want %s", path, got, want)
		}
	}
	entries := keepLargest([]Entry{{Path: "z", ApparentBytes: 1}}, Entry{Path: "a", ApparentBytes: 1}, 1)
	if entries[0].Path != "a" {
		t.Fatal("size tie is not deterministic")
	}
	got := sortedSummaries(map[string]Summary{"b": {Name: "b", ApparentBytes: 1}, "a": {Name: "a", ApparentBytes: 1}})
	if !reflect.DeepEqual(got, []Summary{{Name: "a", ApparentBytes: 1}, {Name: "b", ApparentBytes: 1}}) {
		t.Fatal("summary ties not deterministic")
	}
	if Bytes(1<<30) != "1.00 GiB" {
		t.Fatal("wrong units")
	}
	if AllocatedLine(nil) != "unmeasured" {
		t.Fatal("unmeasured allocation was given a number")
	}
	zero := int64(0)
	if !strings.Contains(AllocatedLine(&zero), "0.00 GiB") || strings.Contains(AllocatedLine(&zero), "unmeasured") {
		t.Fatal("measured zero lost its number")
	}
}

type allocationInfo struct {
	size int64
}

func (allocationInfo) Name() string       { return "file.gguf" }
func (info allocationInfo) Size() int64   { return info.size }
func (allocationInfo) Mode() os.FileMode  { return 0o600 }
func (allocationInfo) ModTime() time.Time { return testNow }
func (allocationInfo) IsDir() bool        { return false }
func (allocationInfo) Sys() any           { return nil }

func withAllocation(t *testing.T, s *scanner, fn func(os.FileInfo) (int64, uint64, uint64, bool)) {
	t.Helper()
	s.allocation = fn
}

func TestHardLinkedNamesCountStatBlocksOnce(t *testing.T) {
	s, _ := scannerFixture(t)
	withAllocation(t, s, func(os.FileInfo) (int64, uint64, uint64, bool) { return 4096, 3, 9, true })
	s.addFile("first.gguf", allocationInfo{size: 100})
	s.addFile("second.gguf", allocationInfo{size: 100})
	s.finish()
	if s.plan.ApparentBytes != 200 || s.plan.AllocatedBytes == nil || *s.plan.AllocatedBytes != 4096 {
		t.Fatalf("hard-linked names were summed or copied from apparent size: apparent=%d allocated=%s", s.plan.ApparentBytes, allocatedText(s.plan.AllocatedBytes))
	}
}

func TestMissingBlockCountStaysUnmeasured(t *testing.T) {
	s, _ := scannerFixture(t)
	withAllocation(t, s, func(os.FileInfo) (int64, uint64, uint64, bool) { return 0, 0, 0, false })
	s.addFile("weights.gguf", allocationInfo{size: 100})
	s.finish()
	if s.plan.ApparentBytes != 100 || s.plan.AllocatedBytes != nil {
		t.Fatalf("apparent size became allocated bytes: apparent=%d allocated=%s", s.plan.ApparentBytes, allocatedText(s.plan.AllocatedBytes))
	}
	raw, err := json.Marshal(s.plan)
	if err != nil || !strings.Contains(string(raw), `"allocated_bytes":null`) {
		t.Fatalf("unmeasured allocation was not null: %s %v", raw, err)
	}
}

func TestMeasuredZeroAllocationStaysZero(t *testing.T) {
	s, _ := scannerFixture(t)
	withAllocation(t, s, func(os.FileInfo) (int64, uint64, uint64, bool) { return 0, 4, 8, true })
	s.addFile("empty.gguf", allocationInfo{size: 0})
	s.finish()
	if s.plan.AllocatedBytes == nil || *s.plan.AllocatedBytes != 0 {
		t.Fatalf("measured zero became unmeasured: %v", s.plan.AllocatedBytes)
	}
	raw, err := json.Marshal(s.plan)
	if err != nil || !strings.Contains(string(raw), `"allocated_bytes":0`) {
		t.Fatalf("measured zero was omitted: %s %v", raw, err)
	}
}

func TestAllocationOverflowStaysUnmeasured(t *testing.T) {
	s, _ := scannerFixture(t)
	next := uint64(1)
	withAllocation(t, s, func(os.FileInfo) (int64, uint64, uint64, bool) {
		next++
		return 1 << 62, 1, next, true
	})
	s.addFile("one.gguf", allocationInfo{size: 1})
	s.addFile("two.gguf", allocationInfo{size: 1})
	s.finish()
	if s.plan.ApparentBytes != 2 || s.plan.AllocatedBytes != nil {
		t.Fatalf("overflowing block counts were published: apparent=%d allocated=%s", s.plan.ApparentBytes, allocatedText(s.plan.AllocatedBytes))
	}
}

func allocatedText(value *int64) string {
	if value == nil {
		return "unmeasured"
	}
	return strconv.FormatInt(*value, 10)
}

func TestSiblingFileDoesNotMakeAnAgedPartialRedundant(t *testing.T) {
	root := t.TempDir()
	old := testNow.AddDate(0, 0, -8)
	fixtureFile(t, root, "models/model.gguf", 100, old)
	fixtureFile(t, root, "models/model.gguf.partial", 40, old)
	plan, err := Scan(t.Context(), root, testOptions())
	if err != nil || !plan.Complete || plan.ApparentBytes != 140 || plan.ReviewCandidateCount != 1 {
		t.Fatalf("sibling inventory: %+v %v", plan, err)
	}
	if plan.CompletedFileIntegrity != IntegrityUnmeasured {
		t.Fatalf("completed-file integrity = %q, want unmeasured", plan.CompletedFileIntegrity)
	}
	partial := plan.ReviewCandidates[0]
	if partial.Path != "models/model.gguf.partial" || partial.State != "REVIEW" || partial.FileIntegrity != IntegrityUnmeasured {
		t.Fatalf("aged partial became a completed copy: %+v", partial)
	}
	for _, entry := range plan.LargestFiles {
		if entry.FileIntegrity != IntegrityUnmeasured || entry.State == "REDUNDANT" {
			t.Fatalf("file integrity was inferred: %+v", entry)
		}
	}
	raw, err := json.Marshal(plan)
	if err != nil || strings.Contains(string(raw), `"redundant"`) || !strings.Contains(string(raw), `"file_integrity":"unmeasured"`) {
		t.Fatalf("plan claimed redundancy: %s %v", raw, err)
	}
}

func TestEmptyDirectoryMeasuresZeroAllocatedBytes(t *testing.T) {
	plan, err := Scan(t.Context(), t.TempDir(), testOptions())
	if err != nil || !plan.Complete || plan.Files != 0 || plan.AllocatedBytes == nil || *plan.AllocatedBytes != 0 {
		t.Fatalf("empty observation was not a measured zero: allocated=%v err=%v", plan.AllocatedBytes, err)
	}
}

func scannerFixture(t *testing.T) (*scanner, string) {
	t.Helper()
	path := t.TempDir()
	fixtureFile(t, path, "a/file.part", 1, testNow)
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return newScanner(t.Context(), root, path, testOptions()), path
}

func TestScanReportsUnreadableAndRacedEntries(t *testing.T) {
	for _, target := range []string{".", "a/file.part"} {
		s, _ := scannerFixture(t)
		original := s.lstat
		originalEntry := s.entryInfo
		s.lstat = func(path string) (os.FileInfo, error) {
			if filepath.ToSlash(path) == target {
				return nil, os.ErrPermission
			}
			return original(path)
		}
		s.entryInfo = func(path string, entry os.DirEntry) (os.FileInfo, error) {
			if filepath.ToSlash(path) == target {
				return nil, os.ErrPermission
			}
			return originalEntry(path, entry)
		}
		s.walk(".", 0)
		if s.plan.Complete || s.plan.Issues[0].Code != "stat_failed" {
			t.Fatalf("lost stat error: %+v", s.plan)
		}
	}
	s, _ := scannerFixture(t)
	s.openDir = func(string) (*os.File, error) { return nil, os.ErrPermission }
	s.walk(".", 0)
	if s.plan.Issues[0].Code != "directory_unreadable" {
		t.Fatalf("lost read error: %+v", s.plan)
	}
	s, root := scannerFixture(t)
	original := s.openDir
	s.openDir = func(path string) (*os.File, error) {
		if path == "." {
			return os.Open(filepath.Join(root, "a"))
		}
		return original(path)
	}
	s.walk(".", 0)
	if s.plan.Issues[0].Code != "directory_changed" {
		t.Fatalf("lost race: %+v", s.plan)
	}
}

func TestScanHandlesDirectoryReplacementAndReadFailures(t *testing.T) {
	s, root := scannerFixture(t)
	s.walk("a/file.part", 0)
	if s.plan.Issues[0].Code != "directory_changed" {
		t.Fatal("file traversed as directory")
	}
	s, _ = scannerFixture(t)
	file, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	s.readDirectory(file, ".", 0)
	if s.plan.Issues[0].Code != "directory_read_failed" {
		t.Fatal("lost closed directory read error")
	}
	s, _ = scannerFixture(t)
	original := s.lstat
	count := 0
	s.lstat = func(path string) (os.FileInfo, error) {
		count++
		if count == 2 {
			return nil, errors.New("entry disappeared")
		}
		return original(path)
	}
	s.walk(".", 0)
	if s.plan.Issues[0].Code != "directory_changed" {
		t.Fatal("lost replacement after open")
	}
}
