package discovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/boundedio"
	"github.com/blisspixel/fitr/internal/lock"
	"github.com/blisspixel/fitr/internal/source"
)

// SourceStore.Directory is the existing discovery inbox, normally .discovery.
// Its sibling <inbox-name>-sources holds only private managed association copies.
// A configured inbox alias is resolved once; managed descendants reject links.
type SourceStore struct{ Directory string }

type sourceStorePaths struct{ inbox, root string }

func (store SourceStore) Attach(ideaID string, receipt source.Resolution, now time.Time) (attachment SourceAttachment, err error) {
	if err := receipt.Validate(); err != nil {
		return SourceAttachment{}, err
	}
	paths, guard, err := store.acquire(ideaID)
	if err != nil {
		return SourceAttachment{}, err
	}
	defer func() { err = errors.Join(err, guard.Release()) }()
	all, total, err := paths.readAll()
	if err != nil {
		return SourceAttachment{}, err
	}
	for _, existing := range all[ideaID] {
		if existing.ResolutionSHA256 == receipt.ResolutionSHA256 {
			return existing, nil
		}
	}
	if len(all[ideaID]) >= MaxSourcesPerIdea {
		return SourceAttachment{}, errors.New("idea reached its four-source limit")
	}
	if _, exists := all[ideaID]; !exists && len(all) >= maximumIdeas {
		return SourceAttachment{}, errors.New("discovery source idea limit exceeded")
	}
	attachment = SourceAttachment{Schema: SourceAttachmentSchema, IdeaID: ideaID, ResolutionSHA256: receipt.ResolutionSHA256,
		Relation: "operator_association", AttachedAt: now.UTC().Format(time.RFC3339Nano), Resolution: receipt}
	attachment.AttachmentSHA256, err = attachment.Digest()
	if err != nil {
		return SourceAttachment{}, err
	}
	data, err := json.MarshalIndent(attachment, "", "  ")
	if err != nil {
		return SourceAttachment{}, err
	}
	data = append(data, '\n')
	if len(data) > maxSourceAttachmentBytes || total+int64(len(data)) > MaxSourceStoreBytes {
		return SourceAttachment{}, errors.New("discovery source storage limit exceeded")
	}
	if err := paths.publish(ideaID, receipt.ResolutionSHA256, data); err != nil {
		return SourceAttachment{}, err
	}
	// Return an independent value, including the nested receipt's slices/pointers.
	return decodeSourceAttachment(data)
}

func (store SourceStore) List(ideaID string) (summaries []SourceSummary, err error) {
	paths, guard, err := store.acquire(ideaID)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, guard.Release()) }()
	all, _, err := paths.readAll()
	if err != nil {
		return nil, err
	}
	return summarizeSources(all[ideaID]), nil
}

func summarizeSources(attachments []SourceAttachment) []SourceSummary {
	summaries := make([]SourceSummary, 0, len(attachments))
	for _, attachment := range attachments {
		summaries = append(summaries, attachment.summary())
	}
	return summaries
}

// Detach removes exactly one managed copy. It never touches the original
// receipt, the Idea file, model files, or any external path.
func (store SourceStore) Detach(ideaID, resolutionSHA256 string) (err error) {
	filename, err := sourceDigestFilename(resolutionSHA256)
	if err != nil {
		return err
	}
	paths, guard, err := store.acquire(ideaID)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, guard.Release()) }()
	all, _, err := paths.readAll()
	if err != nil {
		return err
	}
	for _, attachment := range all[ideaID] {
		if attachment.ResolutionSHA256 == resolutionSHA256 {
			return os.Remove(filepath.Join(paths.root, ideaID, filename))
		}
	}
	return errors.New("source association not found")
}

func (store SourceStore) acquire(ideaID string) (sourceStorePaths, *lock.Lock, error) {
	var paths sourceStorePaths
	if !sourceIDPattern.MatchString(ideaID) {
		return paths, nil, errors.New("a full discovery idea ID is required")
	}
	if err := sourceRawPath(store.Directory); err != nil {
		return paths, nil, err
	}
	absolute, err := filepath.Abs(store.Directory)
	if err != nil {
		return paths, nil, err
	}
	paths.inbox, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return paths, nil, err
	}
	paths.root = paths.inbox + "-sources"
	if _, err := paths.idea(ideaID); err != nil {
		return paths, nil, err
	}
	identity := paths.root
	if runtime.GOOS == "windows" {
		identity = strings.ToLower(identity)
	}
	guard, err := lock.Acquire("discovery-sources-"+strings.TrimPrefix(sourceHash([]byte(identity)), "sha256:"), "discovery source associations")
	return paths, guard, discoveryLockError(err)
}

func (paths sourceStorePaths) idea(id string) (Idea, error) {
	path := filepath.Join(paths.inbox, id+".json")
	if err := sourcePhysicalPath(path); err != nil {
		return Idea{}, err
	}
	idea, err := Load(path)
	if err != nil {
		return Idea{}, err
	}
	if idea.ID != id {
		return Idea{}, errors.New("discovery filename does not match idea identity")
	}
	return idea, nil
}

func (paths sourceStorePaths) readAll() (map[string][]SourceAttachment, int64, error) {
	all := make(map[string][]SourceAttachment)
	if err := sourcePhysicalPath(paths.root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return all, 0, nil
		}
		return nil, 0, err
	}
	entries, err := sourceEntries(paths.root, maximumIdeas)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	count := 0
	for _, entry := range entries {
		if !sourceIDPattern.MatchString(entry.Name()) || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil, 0, errors.New("invalid source association directory")
		}
		if _, err := paths.idea(entry.Name()); err != nil {
			return nil, 0, err
		}
		attachments, size, err := paths.readIdea(entry.Name(), MaxSourceStoreBytes-total)
		if err != nil {
			return nil, 0, err
		}
		total += size
		count += len(attachments)
		if total > MaxSourceStoreBytes || count > maxSourceAttachments {
			return nil, 0, errors.New("discovery source store exceeds aggregate bounds")
		}
		all[entry.Name()] = attachments
	}
	return all, total, nil
}

func (paths sourceStorePaths) readIdea(ideaID string, remainingBytes int64) ([]SourceAttachment, int64, error) {
	directory := filepath.Join(paths.root, ideaID)
	entries, err := sourceEntries(directory, MaxSourcesPerIdea)
	if err != nil {
		return nil, 0, err
	}
	attachments := make([]SourceAttachment, 0, len(entries))
	var size int64
	for _, entry := range entries {
		path := filepath.Join(directory, entry.Name())
		if err := sourcePhysicalPath(path); err != nil {
			return nil, 0, err
		}
		info, err := entry.Info()
		if err != nil {
			return nil, 0, err
		}
		if !info.Mode().IsRegular() || info.Size() > maxSourceAttachmentBytes {
			return nil, 0, errors.New("invalid source association file")
		}
		size += info.Size()
		if size > remainingBytes {
			return nil, 0, errors.New("discovery source store exceeds aggregate byte bound")
		}
		data, err := boundedio.ReadFile(path, maxSourceAttachmentBytes)
		if err != nil {
			return nil, 0, err
		}
		attachment, err := decodeSourceAttachment(data)
		if err != nil {
			return nil, 0, err
		}
		filename, err := sourceDigestFilename(attachment.ResolutionSHA256)
		if err != nil || filename != entry.Name() || attachment.IdeaID != ideaID {
			return nil, 0, errors.New("source association filename or idea mismatch")
		}
		attachments = append(attachments, attachment)
	}
	return attachments, size, nil
}

func sourceEntries(directory string, maximum int) ([]os.DirEntry, error) {
	file, err := os.Open(directory)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	entries, err := file.ReadDir(maximum + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > maximum {
		return nil, errors.New("discovery source directory exceeds entry bound")
	}
	// ReadDir(n) does not sort. Stable summaries and selection must not depend on it.
	slices.SortFunc(entries, func(left, right os.DirEntry) int { return strings.Compare(left.Name(), right.Name()) })
	return entries, nil
}

func (paths sourceStorePaths) publish(ideaID, digest string, data []byte) error {
	directory := filepath.Join(paths.root, ideaID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := sourcePhysicalPath(directory); err != nil {
		return err
	}
	filename, err := sourceDigestFilename(digest)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".fitr-source-*")
	if err != nil {
		return err
	}
	defer func() { _ = temporary.Close(); _ = os.Remove(temporary.Name()) }()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Link(temporary.Name(), filepath.Join(directory, filename))
}

func sourceRawPath(path string) error {
	if strings.TrimSpace(path) == "" || strings.ContainsAny(path, "\x00\r\n") {
		return errors.New("invalid discovery source path")
	}
	for _, part := range strings.FieldsFunc(strings.TrimPrefix(path, filepath.VolumeName(path)), func(char rune) bool { return char == '/' || char == '\\' }) {
		if part == ".." {
			return errors.New("discovery source paths cannot contain parent traversal")
		}
	}
	return nil
}

func sourcePhysicalPath(path string) error {
	if err := sourceRawPath(path); err != nil {
		return err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for {
		info, err := os.Lstat(absolute)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("source managed path cannot contain symlinks: %s", filepath.Base(absolute))
		}
		parent := filepath.Dir(absolute)
		if parent == absolute {
			return nil
		}
		absolute = parent
	}
}
