package main

import (
	"path/filepath"
	"sort"
	"time"

	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/role"
	"github.com/blisspixel/fitr/internal/top"
)

// attachRoleSignatures adds the same fitted or refit sentence role status
// prints, and only on the run that selection names. A role-store problem
// leaves the measurements visible and the line absent.
func attachRoleSignatures(snapshot *top.Snapshot) {
	if snapshot == nil {
		return
	}
	roles := role.Store{Dir: filepath.Join(resultsDir(), ".roles")}
	libraries, err := roles.List()
	if err != nil || len(libraries) == 0 {
		return
	}
	records := record.NewStore(resultsDir())
	present := make(map[string]bool, len(snapshot.History))
	for _, run := range snapshot.History {
		present[run.ID] = true
	}
	now := snapshot.UpdatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var signatures []top.Signature
	for _, library := range libraries {
		status, reviewErr := roles.ReviewSelection(library.Name, records, now)
		if reviewErr != nil {
			continue
		}
		signature, ok := signatureForSnapshot(status, present)
		if ok {
			signatures = append(signatures, signature)
		}
	}
	sort.Slice(signatures, func(i, j int) bool {
		if signatures[i].Body == signatures[j].Body {
			return signatures[i].RunID < signatures[j].RunID
		}
		return signatures[i].Body < signatures[j].Body
	})
	snapshot.Signatures = signatures
}

func signatureForSnapshot(status role.SelectionStatus, present map[string]bool) (top.Signature, bool) {
	if status.Selection == nil {
		return top.Signature{}, false
	}
	runID := status.Selection.Selected.Attachment.RunID
	word, body, ok := render.MatchingRunSignature(status, runID)
	if !ok || !present[runID] {
		return top.Signature{}, false
	}
	return top.Signature{Word: word, Body: body, RunID: runID}, true
}
