package record

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/blisspixel/fitr/internal/strictjson"
)

func managedHash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func managedSnapshot(r *Record) ([]byte, managedEntry, error) {
	if r == nil || r.Manifest == nil || r.Completion == nil || r.EvidenceIntegrityIssue() != "" {
		return nil, managedEntry{}, errors.New("managed evidence requires a valid completed signed run")
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, managedEntry{}, err
	}
	data = append(data, '\n')
	if len(data) > maxRecordBytes {
		return nil, managedEntry{}, errors.New("managed evidence record exceeds its byte limit")
	}
	// Reparse the independent serialized snapshot before accepting its identity.
	snapshot, err := decodeRecord(data)
	if err != nil {
		return nil, managedEntry{}, err
	}
	return data, managedEntryFor(snapshot, data), nil
}

func managedSeal(manifest managedManifest) (string, error) {
	manifest.SealSHA256 = ""
	data, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	return managedHash(append([]byte(ManagedStoreSchema+"\x00"), data...)), nil
}

func managedReadFile(path string, limit int64) ([]byte, error) {
	if err := managedNoLinks(path); err != nil {
		return nil, err
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() > limit {
		return nil, errors.New("managed evidence file is not regular or exceeds its byte limit")
	}
	// Force lazy Windows file identity capture before opening the path again.
	_ = os.SameFile(before, before)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, errors.New("managed evidence file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	after, statErr := f.Stat()
	current, pathErr := os.Lstat(path)
	if statErr != nil || pathErr != nil || len(data) > int(limit) || !os.SameFile(opened, after) || !os.SameFile(opened, current) ||
		after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) || int64(len(data)) != before.Size() {
		return nil, errors.New("managed evidence file changed while reading")
	}
	if err := managedNoLinks(path); err != nil {
		return nil, err
	}
	return data, nil
}

func managedPublish(path string, data []byte) error {
	if err := managedNoLinks(filepath.Dir(path)); err != nil {
		return err
	}
	if existing, err := managedReadFile(path, maxRecordBytes); err == nil {
		if bytes.Equal(existing, data) {
			return nil
		}
		return errors.New("managed evidence publication would overwrite existing content")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".fitr-managed-publish-")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Link(f.Name(), path)
}

// Check every publication target before creating any record. An identical
// retry charges only missing twins, and conflicting content leaves no orphan.
func managedPublicationBytes(data []byte, paths ...string) (int64, error) {
	var additional int64
	for _, path := range paths {
		existing, err := managedReadFile(path, maxRecordBytes)
		if errors.Is(err, os.ErrNotExist) {
			additional += int64(len(data))
			continue
		}
		if err != nil {
			return 0, err
		}
		if !bytes.Equal(existing, data) {
			return 0, errors.New("managed evidence publication would overwrite existing content")
		}
	}
	return additional, nil
}

func (store ManagedStore) loadManifest() (managedManifest, error) {
	if err := store.checkPath(); err != nil {
		return managedManifest{}, err
	}
	data, err := managedReadFile(filepath.Join(store.directory(), managedMetadata), maximumManagedMetadata)
	if err != nil {
		return managedManifest{}, err
	}
	var manifest managedManifest
	if err := managedDecode(data, &manifest); err != nil {
		return managedManifest{}, err
	}
	if err := manifest.validate(store.id); err != nil {
		return managedManifest{}, err
	}
	return manifest, nil
}

// Canonical re-encoding comparison also refuses case aliases and omitted/null
// mandatory fields. Values are strings and bounded integers, without floats.
func managedDecode(data []byte, into any) error {
	if err := strictjson.Validate(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return err
	}
	canonical, err := json.Marshal(into)
	if err != nil {
		return err
	}
	var input, output any
	if err := json.Unmarshal(data, &input); err != nil {
		return err
	}
	if err := json.Unmarshal(canonical, &output); err != nil {
		return err
	}
	if !reflect.DeepEqual(input, output) {
		return errors.New("managed evidence metadata has noncanonical fields")
	}
	return nil
}

func (manifest managedManifest) validate(id string) error {
	if manifest.Schema != ManagedStoreSchema || manifest.Spec.ID != id {
		return errors.New("managed evidence manifest identity does not match its directory")
	}
	if err := manifest.Spec.Validate(); err != nil {
		return err
	}
	if len(manifest.Entries) > maximumManagedRecords || manifest.Entries == nil {
		return errors.New("managed evidence manifest has an invalid record count")
	}
	if manifest.State == "open" {
		if len(manifest.Entries) != 0 || manifest.SealSHA256 != "" {
			return errors.New("open managed evidence cannot claim a closed seal")
		}
		return nil
	}
	if manifest.State != "closed" || len(manifest.Entries) == 0 {
		return errors.New("managed evidence state is invalid")
	}
	if err := manifest.validateEntries(); err != nil {
		return err
	}
	digest, err := managedSeal(manifest)
	if err != nil || manifest.SealSHA256 != digest {
		return errors.New("managed evidence store seal does not match")
	}
	return nil
}

func (manifest managedManifest) validateEntries() error {
	previous := ""
	runs := map[string]bool{}
	for _, entry := range manifest.Entries {
		if strings.TrimSpace(entry.Model) == "" || len(entry.Model) > 4096 || !validRunID.MatchString(entry.RunID) || runs[entry.RunID] ||
			!sha256Digest.MatchString(entry.EvidenceSHA256) || !sha256Digest.MatchString(entry.ContentSHA256) ||
			entry.SizeBytes < 1 || entry.SizeBytes > maxRecordBytes || entry.Canonical != canonicalName(entry.Model) || entry.Canonical <= previous {
			return errors.New("closed managed evidence has invalid record identities or ordering")
		}
		suffix := "-" + entry.RunID + "-" + strings.TrimPrefix(entry.ContentSHA256, "sha256:")[:12] + ".json"
		if filepath.Base(entry.History) != entry.History || strings.ContainsAny(entry.History, "/\\") || !strings.HasSuffix(entry.History, suffix) {
			return errors.New("closed managed history name differs from its record identity")
		}
		runs[entry.RunID], previous = true, entry.Canonical
	}
	return nil
}

func managedEntryFor(record *Record, data []byte) managedEntry {
	return managedEntry{
		Model: record.Model, RunID: record.StableRunID(), EvidenceSHA256: record.Completion.EvidenceSHA256,
		ContentSHA256: managedHash(data), SizeBytes: int64(len(data)), Canonical: canonicalName(record.Model),
		History: fmt.Sprintf("%s-%s-%s.json", historyStamp(record.StartedAt), record.StableRunID(), strings.TrimPrefix(managedHash(data), "sha256:")[:12]),
	}
}

func (store ManagedStore) scan(skip *managedEntry) ([]managedEntry, error) {
	entries, err := managedEntries(store.directory(), maximumManagedRecords+2)
	if err != nil {
		return nil, err
	}
	out := []managedEntry{}
	history := map[string]bool{}
	runs := map[string]bool{}
	for _, entry := range entries {
		if entry.Name() == managedMetadata || entry.Name() == historyDirName || (skip != nil && entry.Name() == skip.Canonical) {
			continue
		}
		data, err := managedReadFile(filepath.Join(store.directory(), entry.Name()), maxRecordBytes)
		if err != nil {
			return nil, err
		}
		r, err := decodeRecord(data)
		if err != nil || r.Completion == nil || r.Manifest == nil || r.EvidenceIntegrityIssue() != "" {
			return nil, errors.New("managed store requires valid signed completed evidence")
		}
		item := managedEntryFor(r, data)
		if item.Canonical != entry.Name() || runs[item.RunID] {
			return nil, errors.New("managed evidence canonical name or run identity is invalid")
		}
		twin, err := managedReadFile(filepath.Join(store.directory(), historyDirName, item.History), maxRecordBytes)
		if err != nil || !bytes.Equal(data, twin) {
			return nil, errors.New("managed evidence lacks an exact immutable history twin")
		}
		runs[item.RunID], history[item.History] = true, true
		out = append(out, item)
	}
	archived, err := managedEntries(filepath.Join(store.directory(), historyDirName), maximumManagedRecords)
	if err != nil {
		return nil, err
	}
	for _, entry := range archived {
		if !history[entry.Name()] && (skip == nil || skip.History != entry.Name()) {
			return nil, errors.New("managed history contains an unregistered or copied record")
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Canonical < out[j].Canonical })
	return out, nil
}
