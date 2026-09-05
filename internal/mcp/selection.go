package mcp

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/role"
)

type selectedSummary struct {
	ReceiptSHA256  string `json:"receipt_sha256"`
	Revision       string `json:"revision"`
	EvidenceSHA256 string `json:"evidence_sha256"`
	ExpiresAt      string `json:"expires_at"`
}

type statusSummary struct {
	Schema             string           `json:"schema"`
	Role               string           `json:"role"`
	Revision           string           `json:"revision"`
	Scope              string           `json:"scope"`
	State              string           `json:"state"`
	EvaluatedAt        string           `json:"evaluated_at"`
	LifecycleSHA256    string           `json:"lifecycle_sha256"`
	Selection          *selectedSummary `json:"selection,omitempty"`
	AdoptionAuthorized bool             `json:"adoption_authorized"`
}

func (source *localEvidence) status(ctx context.Context, name string) (any, error) {
	return source.statusAt(ctx, name, time.Now())
}

func (source *localEvidence) statusAt(ctx context.Context, name string, now time.Time) (any, error) {
	if !source.mu.TryLock() {
		return nil, errors.New("evidence review already running")
	}
	defer source.mu.Unlock()
	if !roleNamePattern.MatchString(name) || now.IsZero() {
		return nil, errors.New("invalid selection review input")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := source.checkRoles(); err != nil {
		return nil, err
	}
	reads := newSelectionReads(ctx)
	library, life, err := source.selectionSnapshots(reads, name)
	if err != nil {
		return nil, err
	}
	records, err := source.checkSelectionEvidence(reads, life)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	status, err := role.ReviewSelectionSnapshot(library, life, records, now)
	if err != nil {
		return nil, err
	}
	if err := reads.recheck(); err != nil {
		return nil, err
	}
	return summarizeSelection(library.CurrentRevision, status), ctx.Err()
}

func summarizeSelection(revision string, status role.SelectionStatus) statusSummary {
	result := statusSummary{Schema: "fitr.mcp.role-status.v1", Role: status.Role, Revision: revision,
		Scope: "battery_screening", State: status.State, EvaluatedAt: status.EvaluatedAt, LifecycleSHA256: status.LifecycleDigest}
	if status.Selection != nil {
		result.Selection = &selectedSummary{ReceiptSHA256: status.ReceiptSHA256, Revision: status.Selection.SpecSHA256,
			EvidenceSHA256: status.Selection.Selected.Attachment.EvidenceSHA256, ExpiresAt: status.Selection.ExpiresAt}
	}
	return result
}

func (source *localEvidence) selectionSnapshots(reads *selectionReads, name string) (role.Library, *role.Lifecycle, error) {
	directory := filepath.Join(source.root, ".roles")
	if _, err := reads.directory(directory, maxDirectoryEntries); err != nil {
		return role.Library{}, nil, err
	}
	var library role.Library
	if err := reads.decode(filepath.Join(directory, name+".json"), maxSelectionMetadataBytes, &library); err != nil {
		return role.Library{}, nil, err
	}
	if library.Name != name {
		return role.Library{}, nil, errors.New("role identity differs from filename")
	}
	if err := library.Validate(); err != nil {
		return role.Library{}, nil, err
	}
	if _, err := reads.directory(filepath.Join(directory, ".lifecycle"), maxDirectoryEntries); err != nil {
		return role.Library{}, nil, err
	}
	path := filepath.Join(directory, ".lifecycle", name+".json")
	info, err := reads.file(path, maxSelectionMetadataBytes)
	if err != nil || info == nil {
		return library, nil, err
	}
	var life role.Lifecycle
	if err := reads.decode(path, maxSelectionMetadataBytes, &life); err != nil {
		return role.Library{}, nil, err
	}
	return library, &life, nil
}

func statusSchema() map[string]any {
	digest := map[string]any{"type": "string", "pattern": `^sha256:[0-9a-fA-F]{64}$`, "maxLength": 71}
	timestamp := map[string]any{"type": "string", "format": "date-time", "maxLength": 35}
	selected := objectSchema(map[string]any{"receipt_sha256": digest, "revision": digest, "evidence_sha256": digest, "expires_at": timestamp},
		"receipt_sha256", "revision", "evidence_sha256", "expires_at")
	return objectSchema(map[string]any{
		"schema": map[string]any{"const": "fitr.mcp.role-status.v1"}, "role": map[string]any{"type": "string", "pattern": roleNamePattern.String(), "maxLength": 64},
		"revision": digest, "scope": map[string]any{"const": "battery_screening"},
		"state": map[string]any{"enum": []string{"unselected", "qualified", "stale"}}, "evaluated_at": timestamp,
		"lifecycle_sha256": digest, "selection": selected, "adoption_authorized": map[string]any{"const": false},
	}, "schema", "role", "revision", "scope", "state", "evaluated_at", "lifecycle_sha256", "adoption_authorized")
}

func (source *localEvidence) checkSelectionEvidence(reads *selectionReads, life *role.Lifecycle) (record.Store, error) {
	records := record.Store{Dir: source.root}
	if life == nil {
		return records, nil
	}
	if len(life.Events) > maxSelectionEvents {
		return records, errors.New("selection lifecycle exceeds the read-only event limit")
	}
	// Validate the entire bounded metadata chain before selecting dependencies.
	// Historical stores are not read by CLI status and may have been removed;
	// only the incumbent's complete confirmation set determines qualification.
	if err := life.Validate(); err != nil {
		return records, err
	}
	stores := map[string]string{}
	for _, event := range life.Events {
		if event.Digest != life.IncumbentSHA256 || event.Selection == nil {
			continue
		}
		points := append([]role.ConfirmationPoint{event.Selection.Selected}, event.Selection.Points...)
		for _, point := range points {
			if err := source.checkSelectionPoint(reads, point, stores, &records); err != nil {
				return records, err
			}
		}
	}
	return records, nil
}
