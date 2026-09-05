package artifact

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/blisspixel/fitr/internal/buildinfo"
	"github.com/blisspixel/fitr/internal/source"
)

type openedFile struct {
	file   *os.File
	before os.FileInfo
}

// Bind never executes code, contacts a model host or discovers companion files.
// Deadlines are cooperative between filesystem calls; they cannot interrupt a
// blocked OS read. Successful observations do not freeze writable local paths.
func Bind(ctx context.Context, resolution source.Resolution, spec Spec, options Options) (Binding, error) {
	result, err := newBinding(resolution, spec, options)
	if err != nil {
		return Binding{}, err
	}
	start, _ := time.Parse(time.RFC3339Nano, result.StartedAt)
	deadline := start.Add(time.Duration(result.Limits.TimeoutMillis) * time.Millisecond)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	opened := make([]openedFile, len(result.Files))
	defer closeFiles(opened)
	if err := preflight(ctx, &result, opened); err != nil {
		return Binding{}, err
	}
	if err := openFiles(ctx, &result, opened); err != nil {
		return Binding{}, err
	}
	hashFiles(ctx, &result, opened)
	finalRecheck(ctx, &result, opened)
	completed := time.Now().UTC()
	state := stopped(ctx)
	if state == "" && completed.After(deadline) {
		state = "timeout"
	}
	if state != "" {
		invalidateStopped(&result, state)
	}
	result.CompletedAt = completed.Format(time.RFC3339Nano)
	result.State, result.Gaps, result.UnmappedFiles = deriveResult(result)
	result.BindingSHA256, err = result.Digest()
	return result, err
}

func invalidateStopped(result *Binding, state string) {
	for index := range result.Files {
		file := &result.Files[index]
		file.State, file.ObservedSHA256, file.After, file.IdentityState = state, "", nil, "unobserved"
	}
}

func newBinding(resolution source.Resolution, spec Spec, options Options) (Binding, error) {
	if err := resolution.Validate(); err != nil {
		return Binding{}, err
	}
	if resolution.ResolvedCommit == "" || resolution.State == "unavailable" {
		return Binding{}, errors.New("artifact binding requires pinned source metadata")
	}
	if err := spec.Validate(); err != nil {
		return Binding{}, err
	}
	if spec.ResolutionSHA256 != resolution.ResolutionSHA256 {
		return Binding{}, errors.New("artifact mapping source digest mismatch")
	}
	limits, err := options.limits()
	if err != nil {
		return Binding{}, err
	}
	// A JSON copy owns every nested slice and size pointer before any file work.
	data, err := json.Marshal(struct {
		Source  source.Resolution
		Mapping Spec
	}{resolution, spec})
	if err != nil {
		return Binding{}, err
	}
	var owned struct {
		Source  source.Resolution
		Mapping Spec
	}
	if err := json.Unmarshal(data, &owned); err != nil {
		return Binding{}, err
	}
	result := Binding{Schema: BindingSchema, PolicyVersion: PolicyVersion, BinderVersion: buildinfo.Version(),
		ResolutionSHA256: resolution.ResolutionSHA256, Source: owned.Source, Mapping: owned.Mapping, Limits: limits,
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano), RuntimeState: "unbound", DependencyState: "unverified",
		CapacityState: "unmeasured", QualityState: "unmeasured", Files: make([]FileObservation, len(spec.Files))}
	return prepareMappings(result)
}

func prepareMappings(result Binding) (Binding, error) {
	slices.SortFunc(result.Mapping.Files, func(a, b Mapping) int { return comparePaths(a.SourcePath, b.SourcePath) })
	for index, mapping := range result.Mapping.Files {
		if !slices.Contains(result.Source.Request.Files, mapping.SourcePath) {
			return Binding{}, errors.New("artifact mapping includes an unselected source path")
		}
		if !filepath.IsAbs(mapping.LocalPath) {
			return Binding{}, errors.New("artifact local path must be absolute on this platform")
		}
		path, err := checkedPath(mapping.LocalPath)
		if err != nil {
			return Binding{}, err
		}
		result.Mapping.Files[index].LocalPath = path
		result.Files[index] = FileObservation{SourcePath: mapping.SourcePath, LocalPath: path, ComponentRole: mapping.ComponentRole,
			State: "not_read", IdentityState: "unobserved"}
	}
	return result, result.Mapping.Validate()
}

func closeFiles(opened []openedFile) {
	for _, item := range opened {
		if item.file != nil {
			_ = item.file.Close()
		}
	}
}

func preflight(ctx context.Context, result *Binding, opened []openedFile) error {
	var total int64
	for index := range result.Files {
		observation := &result.Files[index]
		if state := stopped(ctx); state != "" {
			observation.State = state
			continue
		}
		if err := rejectLinks(observation.LocalPath, true); err != nil {
			return err
		}
		info, err := os.Lstat(observation.LocalPath)
		if errors.Is(err, os.ErrNotExist) {
			observation.State = "missing"
			continue
		}
		if err != nil {
			observation.State = "read_error"
			continue
		}
		if !info.Mode().IsRegular() || info.Size() < 0 {
			return errors.New("artifact inputs must be regular files")
		}
		// Windows may lazily resolve an Lstat file ID on the first SameFile.
		// Materialize it now, before a later pathname replacement can change it.
		if !os.SameFile(info, info) {
			return errors.New("artifact input identity could not be captured")
		}
		observation.Before, opened[index].before = facts(info), info
		if err := distinctOpened(opened, index, info); err != nil {
			return err
		}
		total = cappedSum(total, info.Size())
		metadata := sourceFile(result.Source, observation.SourcePath)
		if metadata.SizeBytes != nil && *metadata.SizeBytes != info.Size() {
			observation.State = "size_mismatch"
		}
	}
	if total > result.Limits.MaxBytes {
		for index := range result.Files {
			if result.Files[index].State == "not_read" {
				result.Files[index].State = "budget_exceeded"
			}
		}
	}
	return nil
}

func openFiles(ctx context.Context, result *Binding, opened []openedFile) error {
	for index := range result.Files {
		observation := &result.Files[index]
		if observation.State != "not_read" {
			continue
		}
		if state := stopped(ctx); state != "" {
			observation.State = state
			continue
		}
		file, err := os.Open(observation.LocalPath)
		if err != nil {
			observation.State = "read_error"
			continue
		}
		opened[index].file = file
		info, err := file.Stat()
		if err != nil {
			observation.State = "read_error"
			continue
		}
		if !sameFacts(opened[index].before, info) {
			markChanged(observation, info)
			continue
		}
		if err := rejectLinks(observation.LocalPath, false); err != nil {
			return err
		}
		if err := distinctOpened(opened, index, info); err != nil {
			return err
		}
	}
	return nil
}

func distinctOpened(opened []openedFile, index int, info os.FileInfo) error {
	for previous := range index {
		if opened[previous].before != nil && os.SameFile(opened[previous].before, info) {
			return errors.New("artifact mappings cannot refer to the same local file through hard links")
		}
	}
	return nil
}

func stopped(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}
	if ctx.Err() != nil {
		return "cancelled"
	}
	return ""
}

func facts(info os.FileInfo) *FileFacts {
	return &FileFacts{SizeBytes: info.Size(), ModifiedAt: info.ModTime().UTC().Format(time.RFC3339Nano)}
}

func sourceFile(resolution source.Resolution, path string) source.FileMetadata {
	for _, file := range resolution.Files {
		if file.Path == path {
			return file
		}
	}
	return source.FileMetadata{}
}
