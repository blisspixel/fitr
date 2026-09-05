package role

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/blisspixel/fitr/internal/atomicfile"
	"github.com/blisspixel/fitr/internal/lock"
)

const (
	maximumRoleRevisions  = 32
	maximumRoleCandidates = 64
	maximumRoles          = 64
)

type Library struct {
	Schema          string       `json:"schema"`
	Name            string       `json:"name"`
	CurrentRevision string       `json:"current_revision"`
	Revisions       []Revision   `json:"revisions"`
	Candidates      []Attachment `json:"candidates"`
}

type Revision struct {
	ID   string `json:"id"`
	Spec Spec   `json:"spec"`
}

type Attachment struct {
	Path           string `json:"path"`
	EvidenceSHA256 string `json:"evidence_sha256"`
	RunID          string `json:"run_id"`
}

type Store struct{ Dir string }

func (library Library) Validate() error {
	if library.Schema != LibrarySchema || !roleNamePattern.MatchString(library.Name) {
		return errors.New("invalid role library schema or name")
	}
	if len(library.Revisions) < 1 || len(library.Revisions) > maximumRoleRevisions ||
		len(library.Candidates) > maximumRoleCandidates {
		return errors.New("role library revision or candidate count exceeds its bounds")
	}
	seen := make(map[string]bool, len(library.Revisions))
	for _, revision := range library.Revisions {
		digest, err := revision.Spec.Digest()
		if err != nil {
			return fmt.Errorf("role revision: %w", err)
		}
		if revision.Spec.Name != library.Name || revision.ID != digest || seen[revision.ID] {
			return errors.New("role revision identity is invalid or repeated")
		}
		seen[revision.ID] = true
	}
	if !seen[library.CurrentRevision] {
		return errors.New("current role revision does not exist")
	}
	seen = make(map[string]bool, len(library.Candidates))
	for _, attachment := range library.Candidates {
		if err := validateAttachment(attachment); err != nil {
			return err
		}
		if seen[attachment.EvidenceSHA256] {
			return errors.New("role attachment digest is repeated")
		}
		seen[attachment.EvidenceSHA256] = true
	}
	return nil
}

func (library Library) CurrentSpec() (Spec, error) {
	if err := library.Validate(); err != nil {
		return Spec{}, err
	}
	for _, revision := range library.Revisions {
		if revision.ID == library.CurrentRevision {
			return revision.Spec, nil
		}
	}
	return Spec{}, errors.New("current role revision does not exist")
}

func validateAttachment(attachment Attachment) error {
	if !filepath.IsAbs(attachment.Path) || !roleTextValid(attachment.Path, 32768, false) ||
		!roleTextValid(attachment.RunID, 128, false) || !roleDigestValid(attachment.EvidenceSHA256) {
		return errors.New("invalid role attachment path, evidence digest, or run ID")
	}
	return nil
}

func roleDigestValid(value string) bool {
	encoded, ok := strings.CutPrefix(value, "sha256:")
	if !ok || len(encoded) != 64 || encoded != strings.ToLower(encoded) {
		return false
	}
	_, err := hex.DecodeString(encoded)
	return err == nil
}

func (store Store) Define(spec Spec) (Library, error) {
	digest, err := spec.Digest()
	if err != nil {
		return Library{}, err
	}
	return store.update(spec.Name, true, func(library *Library) error {
		for _, revision := range library.Revisions {
			if revision.ID == digest {
				library.CurrentRevision = digest
				return nil
			}
		}
		if len(library.Revisions) >= maximumRoleRevisions {
			return errors.New("role library reached its 32-revision limit")
		}
		library.Revisions = append(library.Revisions, Revision{ID: digest, Spec: spec})
		library.CurrentRevision = digest
		return nil
	})
}

func (store Store) Load(name string) (Library, error) {
	path, err := store.path(name)
	if err != nil {
		return Library{}, err
	}
	if err := store.checkDirectory(false); err != nil {
		return Library{}, err
	}
	var library Library
	if err := readRoleJSON(path, &library); err != nil {
		return Library{}, err
	}
	if library.Name != name {
		return Library{}, errors.New("role library does not match its filename")
	}
	return library, library.Validate()
}

func (store Store) List() ([]Library, error) {
	if err := store.checkDirectory(false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Library{}, nil
		}
		return nil, err
	}
	entries, err := os.ReadDir(store.Dir)
	if err != nil {
		return nil, err
	}
	libraries := []Library{}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, errors.New("role directory cannot contain symbolic links")
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if len(libraries) >= maximumRoles {
			return nil, errors.New("role directory exceeds its 64-role limit")
		}
		library, err := store.Load(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		libraries = append(libraries, library)
	}
	return libraries, nil
}

func (store Store) Attach(name string, attachment Attachment) (Library, error) {
	if err := validateAttachment(attachment); err != nil {
		return Library{}, err
	}
	return store.update(name, false, func(library *Library) error {
		for index, existing := range library.Candidates {
			if existing.EvidenceSHA256 == attachment.EvidenceSHA256 {
				if existing.RunID != attachment.RunID {
					return errors.New("attachment digest conflicts with its existing run ID")
				}
				// Explicit reattachment can relocate the same immutable evidence.
				// Review still verifies its canonical path and current identity.
				library.Candidates[index].Path = attachment.Path
				return nil
			}
		}
		if len(library.Candidates) >= maximumRoleCandidates {
			return errors.New("role library reached its 64-candidate limit")
		}
		library.Candidates = append(library.Candidates, attachment)
		return nil
	})
}

func (store Store) Detach(name, evidenceSHA256 string) (Library, error) {
	if !roleDigestValid(evidenceSHA256) {
		return Library{}, errors.New("invalid attachment digest")
	}
	return store.update(name, false, func(library *Library) error {
		for index, attachment := range library.Candidates {
			if attachment.EvidenceSHA256 == evidenceSHA256 {
				library.Candidates = append(library.Candidates[:index], library.Candidates[index+1:]...)
				break
			}
		}
		return nil
	})
}

func (store Store) update(name string, create bool, mutate func(*Library) error) (Library, error) {
	path, err := store.path(name)
	if err != nil {
		return Library{}, err
	}
	if err := store.checkDirectory(true); err != nil {
		return Library{}, err
	}
	guard, err := store.acquire()
	if err != nil {
		return Library{}, err
	}
	defer func() { _ = guard.Release() }()
	library, err := store.Load(name)
	if errors.Is(err, os.ErrNotExist) && create {
		library, err = store.emptyLibrary(name)
	}
	if err != nil {
		return Library{}, err
	}
	if err := mutate(&library); err != nil {
		return Library{}, err
	}
	if err := library.Validate(); err != nil {
		return Library{}, err
	}
	data, err := json.MarshalIndent(library, "", "  ")
	if err != nil {
		return Library{}, err
	}
	if len(data)+1 > maximumLibraryBytes {
		return Library{}, errors.New("role library exceeds one MiB")
	}
	if err := atomicfile.Write(path, append(data, '\n'), 0o600); err != nil {
		return Library{}, err
	}
	return library, nil
}

func (store Store) emptyLibrary(name string) (Library, error) {
	libraries, err := store.List()
	if err != nil {
		return Library{}, err
	}
	if len(libraries) >= maximumRoles {
		return Library{}, errors.New("role directory reached its 64-role limit")
	}
	return Library{Schema: LibrarySchema, Name: name, Revisions: []Revision{}, Candidates: []Attachment{}}, nil
}

func (store Store) path(name string) (string, error) {
	if !roleNamePattern.MatchString(name) || strings.TrimSpace(store.Dir) == "" {
		return "", errors.New("role name or store directory is invalid")
	}
	return filepath.Join(store.Dir, name+".json"), nil
}

func (store Store) checkDirectory(create bool) error {
	if strings.TrimSpace(store.Dir) == "" {
		return errors.New("role store directory is required")
	}
	if err := rejectRoleSymlink(store.Dir); err != nil {
		if !create || !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(store.Dir, 0o700); err != nil {
			return err
		}
	}
	info, err := os.Lstat(store.Dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("role store must be a directory without a symbolic link")
	}
	return nil
}

func rejectRoleSymlink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("role paths cannot be symbolic links")
	}
	return nil
}

func (store Store) acquire() (*lock.Lock, error) {
	path, err := filepath.Abs(store.Dir)
	if err != nil {
		return nil, err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	digest := sha256.Sum256([]byte(path))
	return lock.Acquire("roles-"+hex.EncodeToString(digest[:]), "update role library")
}
