package record

import (
	"errors"
	"regexp"
)

const (
	ManagedStoreSchema     = "fitr.evidence.store.v1"
	ManagedStoreRefSchema  = "fitr.evidence.store.ref.v1"
	ManagedStoreSpecSchema = "fitr.evidence.store.spec.v1"
	managedDirectory       = ".evidence-stores"
	managedMetadata        = ".fitr-managed-store.json"
	// Enumeration is bounded. Reaching this ceiling requires explicit cleanup
	// of unused stores; the library never silently discards lifecycle evidence.
	maximumManagedStores         = 4096
	maximumManagedRecords        = 16
	maximumManagedBytes    int64 = 2 << 30
	maximumManagedMetadata       = 64 << 10
)

var managedIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ManagedStoreSpec identifies one explicitly created session evidence group.
// IDs are namespace components, never filesystem paths.
type ManagedStoreSpec struct {
	Schema    string `json:"schema"`
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Purpose   string `json:"purpose"`
}

func (spec ManagedStoreSpec) Validate() error {
	if spec.Schema != ManagedStoreSpecSchema || !managedIDPattern.MatchString(spec.ID) || !managedIDPattern.MatchString(spec.SessionID) {
		return errors.New("invalid managed evidence store identity")
	}
	if spec.Purpose != "exploration" && spec.Purpose != "confirmation" {
		return errors.New("managed evidence purpose must be exploration or confirmation")
	}
	return nil
}

// ManagedStoreRef pins a closed store inside the caller's fixed results root.
// The seal protects pinned content integrity, not authentication against an
// authorized filesystem writer. It does not prove provider assertions.
type ManagedStoreRef struct {
	Schema     string `json:"schema"`
	ID         string `json:"id"`
	SealSHA256 string `json:"seal_sha256"`
}

func (ref ManagedStoreRef) Validate() error {
	if ref.Schema != ManagedStoreRefSchema || !managedIDPattern.MatchString(ref.ID) || !sha256Digest.MatchString(ref.SealSHA256) {
		return errors.New("invalid managed evidence store reference")
	}
	return nil
}

// ManagedStore has no caller-controlled directory after opening. Its contents
// become immutable once closed; a new evidence group needs a new ID.
type ManagedStore struct {
	root string
	id   string
}

type managedEntry struct {
	Model          string `json:"model"`
	RunID          string `json:"run_id"`
	EvidenceSHA256 string `json:"evidence_sha256"`
	ContentSHA256  string `json:"content_sha256"`
	SizeBytes      int64  `json:"size_bytes"`
	Canonical      string `json:"canonical"`
	History        string `json:"history"`
}

type managedManifest struct {
	Schema     string           `json:"schema"`
	Spec       ManagedStoreSpec `json:"spec"`
	State      string           `json:"state"`
	Entries    []managedEntry   `json:"entries"`
	SealSHA256 string           `json:"seal_sha256,omitempty"`
}
