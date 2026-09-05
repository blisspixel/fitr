package record

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/blisspixel/fitr/internal/atomicfile"
	"github.com/blisspixel/fitr/internal/lock"
)

// CreateManagedStore creates an explicit evidence group without modifying the
// ordinary canonical results. Repeating identical creation is idempotent.
func CreateManagedStore(results Store, spec ManagedStoreSpec) (created ManagedStore, err error) {
	if err := spec.Validate(); err != nil {
		return ManagedStore{}, err
	}
	root, err := managedRoot(results, true)
	if err != nil {
		return ManagedStore{}, err
	}
	store := ManagedStore{root: root, id: spec.ID}
	guard, err := store.acquire()
	if err != nil {
		return ManagedStore{}, err
	}
	defer func() { err = errors.Join(err, guard.Release()) }()
	namespace := filepath.Join(root, managedDirectory)
	if err := managedCheckedDirectory(namespace, true); err != nil {
		return ManagedStore{}, err
	}
	count, total, err := store.usage()
	if err != nil {
		return ManagedStore{}, err
	}
	if _, err := os.Lstat(store.directory()); err == nil {
		return store, store.validateExistingSpec(spec)
	} else if !errors.Is(err, os.ErrNotExist) {
		return ManagedStore{}, err
	}
	if count >= maximumManagedStores || total > maximumManagedBytes-maximumManagedMetadata {
		return ManagedStore{}, errors.New("managed evidence namespace reached its storage limit")
	}
	if err := managedCheckedDirectory(store.directory(), true); err != nil {
		return ManagedStore{}, err
	}
	if err := managedCheckedDirectory(filepath.Join(store.directory(), historyDirName), true); err != nil {
		return ManagedStore{}, err
	}
	manifest := managedManifest{Schema: ManagedStoreSchema, Spec: spec, State: "open", Entries: []managedEntry{}}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return ManagedStore{}, err
	}
	if err := managedPublish(filepath.Join(store.directory(), managedMetadata), append(data, '\n')); err != nil {
		return ManagedStore{}, err
	}
	return store, nil
}

func (store ManagedStore) validateExistingSpec(spec ManagedStoreSpec) error {
	manifest, err := store.loadManifest()
	if err != nil || manifest.Spec != spec {
		return errors.New("managed store ID already belongs to another or invalid group")
	}
	if manifest.State == "closed" {
		_, err = store.Ref()
	}
	return err
}

func OpenManagedStore(results Store, id string) (ManagedStore, error) {
	if !managedIDPattern.MatchString(id) {
		return ManagedStore{}, errors.New("invalid managed store ID")
	}
	root, err := managedRoot(results, false)
	if err != nil {
		return ManagedStore{}, err
	}
	store := ManagedStore{root: root, id: id}
	manifest, err := store.loadManifest()
	if err != nil {
		return ManagedStore{}, err
	}
	if manifest.State == "closed" {
		if _, err := store.Ref(); err != nil {
			return ManagedStore{}, err
		}
	}
	return store, nil
}

// ResolveManagedStore validates the entire closed group against its pinned
// reference. Neither arbitrary paths nor open stores can resolve as evidence.
func ResolveManagedStore(results Store, ref ManagedStoreRef) (ManagedStore, error) {
	if err := ref.Validate(); err != nil {
		return ManagedStore{}, err
	}
	store, err := OpenManagedStore(results, ref.ID)
	if err != nil {
		return ManagedStore{}, err
	}
	actual, err := store.Ref()
	if err != nil || actual != ref {
		return ManagedStore{}, errors.New("managed evidence store does not match the pinned closed seal")
	}
	return store, nil
}

func (store ManagedStore) acquire() (*lock.Lock, error) {
	return lock.Acquire("managed-evidence-"+strings.TrimPrefix(managedHash([]byte(store.root)), "sha256:")[:32], "managed session evidence")
}

func (store ManagedStore) CanonicalPath(model string) string {
	return filepath.Join(store.directory(), canonicalName(model))
}

func (store ManagedStore) Spec() (ManagedStoreSpec, error) {
	manifest, err := store.loadManifest()
	return manifest.Spec, err
}

func (store ManagedStore) Save(r *Record) (saved SavedPaths, err error) {
	data, entry, err := managedSnapshot(r)
	if err != nil {
		return SavedPaths{}, err
	}
	guard, err := store.acquire()
	if err != nil {
		return SavedPaths{}, err
	}
	defer func() { err = errors.Join(err, guard.Release()) }()
	manifest, err := store.loadManifest()
	if err != nil {
		return SavedPaths{}, err
	}
	if manifest.State != "open" {
		return SavedPaths{}, errors.New("managed evidence store is closed and immutable")
	}
	paths := SavedPaths{RunID: entry.RunID, CanonicalPath: store.CanonicalPath(entry.Model), HistoryPath: filepath.Join(store.directory(), historyDirName, entry.History)}
	additional, err := managedPublicationBytes(data, paths.CanonicalPath, paths.HistoryPath)
	if err != nil {
		return SavedPaths{}, err
	}
	entries, err := store.scan(&entry)
	if err != nil {
		return SavedPaths{}, err
	}
	for _, existing := range entries {
		if existing.RunID == entry.RunID {
			return SavedPaths{}, errors.New("managed run identity was already used by another model")
		}
	}
	if len(entries) >= maximumManagedRecords {
		return SavedPaths{}, errors.New("managed evidence store reached its record limit")
	}
	_, used, err := store.usage()
	if err != nil {
		return SavedPaths{}, err
	}
	if additional > maximumManagedBytes-used-maximumManagedMetadata {
		return SavedPaths{}, errors.New("managed evidence namespace exceeds its aggregate byte limit")
	}
	if err := managedPublish(paths.HistoryPath, data); err != nil {
		return SavedPaths{}, err
	}
	if err := managedPublish(paths.CanonicalPath, data); err != nil {
		return SavedPaths{}, err
	}
	return paths, nil
}

func (store ManagedStore) Close() (ref ManagedStoreRef, err error) {
	guard, err := store.acquire()
	if err != nil {
		return ManagedStoreRef{}, err
	}
	defer func() { err = errors.Join(err, guard.Release()) }()
	manifest, err := store.loadManifest()
	if err != nil {
		return ManagedStoreRef{}, err
	}
	entries, err := store.scan(nil)
	if err != nil || len(entries) == 0 {
		return ManagedStoreRef{}, errors.New("managed store cannot close without complete canonical/history twins")
	}
	if manifest.State == "closed" {
		return store.refFor(manifest, entries)
	}
	manifest.State, manifest.Entries = "closed", entries
	manifest.SealSHA256, err = managedSeal(manifest)
	if err != nil {
		return ManagedStoreRef{}, err
	}
	if err := store.writeClosedManifest(manifest); err != nil {
		return ManagedStoreRef{}, err
	}
	return store.refFor(manifest, entries)
}

func (store ManagedStore) writeClosedManifest(manifest managedManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil || len(data)+1 > maximumManagedMetadata {
		return errors.New("managed store manifest exceeds its byte limit")
	}
	path := filepath.Join(store.directory(), managedMetadata)
	old, err := managedReadFile(path, maximumManagedMetadata)
	if err != nil {
		return err
	}
	_, used, err := store.usage()
	if err != nil {
		return err
	}
	if int64(len(data)+1-len(old)) > maximumManagedBytes-used {
		return errors.New("closing managed evidence exceeds the aggregate byte limit")
	}
	return atomicfile.Write(path, append(data, '\n'), 0o600)
}

func (store ManagedStore) Ref() (ManagedStoreRef, error) {
	manifest, err := store.loadManifest()
	if err != nil {
		return ManagedStoreRef{}, err
	}
	entries, err := store.scan(nil)
	if err != nil {
		return ManagedStoreRef{}, err
	}
	return store.refFor(manifest, entries)
}

func (store ManagedStore) refFor(manifest managedManifest, entries []managedEntry) (ManagedStoreRef, error) {
	if manifest.State != "closed" || !reflect.DeepEqual(manifest.Entries, entries) {
		return ManagedStoreRef{}, errors.New("managed evidence is open or differs from its closed manifest")
	}
	return ManagedStoreRef{Schema: ManagedStoreRefSchema, ID: store.id, SealSHA256: manifest.SealSHA256}, nil
}

func (store ManagedStore) Read(model string) (*Record, error) {
	if _, err := store.Ref(); err != nil {
		return nil, err
	}
	manifest, err := store.loadManifest()
	if err != nil {
		return nil, err
	}
	return store.readVerified(model, manifest.Entries)
}

// ReadSaved reconciles a completed point whose journal append was interrupted.
// It requires an open store with exact signed canonical/history twins. This
// does not provide a closed-store reference or authorize role qualification;
// role review and lifecycle APIs still require ResolveManagedStore.
func (store ManagedStore) ReadSaved(model string) (*Record, error) {
	manifest, err := store.loadManifest()
	if err != nil {
		return nil, err
	}
	if manifest.State != "open" {
		return nil, errors.New("saved-point reconciliation requires an open managed store")
	}
	entries, err := store.scan(nil)
	if err != nil {
		return nil, err
	}
	return store.readVerified(model, entries)
}

func (store ManagedStore) readVerified(model string, entries []managedEntry) (*Record, error) {
	data, err := managedReadFile(store.CanonicalPath(model), maxRecordBytes)
	if err != nil {
		return nil, err
	}
	r, err := decodeRecord(data)
	if err != nil || r.Model != model {
		return nil, errors.New("managed evidence model does not match its selected canonical file")
	}
	entry := managedEntryFor(r, data)
	for _, sealed := range entries {
		if sealed == entry {
			twin, err := managedReadFile(filepath.Join(store.directory(), historyDirName, entry.History), maxRecordBytes)
			if err == nil && bytes.Equal(data, twin) {
				return r, nil
			}
		}
	}
	return nil, errors.New("managed record changed after closed-store validation")
}

func (store ManagedStore) usage() (int, int64, error) {
	root := filepath.Join(store.root, managedDirectory)
	if err := managedCheckedDirectory(root, false); err != nil {
		return 0, 0, err
	}
	groups, err := managedEntries(root, maximumManagedStores)
	if err != nil {
		return 0, 0, err
	}
	var total int64
	for _, group := range groups {
		if !managedIDPattern.MatchString(group.Name()) {
			return 0, 0, errors.New("unexpected entry in managed evidence namespace")
		}
		path := filepath.Join(root, group.Name())
		if err := managedCheckedDirectory(path, false); err != nil {
			return 0, 0, err
		}
		for _, directory := range []string{path, filepath.Join(path, historyDirName)} {
			if err := managedCheckedDirectory(directory, false); err != nil {
				return 0, 0, err
			}
			entries, err := managedEntries(directory, maximumManagedRecords+2)
			if err != nil {
				return 0, 0, err
			}
			for _, entry := range entries {
				if directory == path && entry.Name() == historyDirName {
					continue
				}
				info, err := os.Lstat(filepath.Join(directory, entry.Name()))
				if err != nil || !info.Mode().IsRegular() || info.Size() > maxRecordBytes || info.Size() > maximumManagedBytes-total {
					return 0, 0, errors.New("managed evidence exceeds its regular-file storage bounds")
				}
				total += info.Size()
			}
		}
	}
	return len(groups), total, nil
}
