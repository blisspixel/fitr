package role

import (
	"errors"
	"time"

	"github.com/blisspixel/fitr/internal/record"
)

// ReviewManaged screens an explicit ordered candidate set without modifying
// role attachments. All evidence is resolved under the fixed results root.
func ReviewManaged(spec Spec, results record.Store, ref record.ManagedStoreRef, models []string, now time.Time) (ReviewReport, error) {
	revision, err := spec.Digest()
	if err != nil {
		return ReviewReport{}, err
	}
	if now.IsZero() || len(models) < 2 || len(models) > 4 {
		return ReviewReport{}, errors.New("managed role review requires a time and two to four explicit models")
	}
	seen := map[string]bool{}
	for _, model := range models {
		if !roleTextValid(model, 512, false) || seen[model] {
			return ReviewReport{}, errors.New("managed role review has invalid or duplicate models")
		}
		seen[model] = true
	}
	store, err := record.ResolveManagedStore(results, ref)
	if err != nil {
		return ReviewReport{}, err
	}
	group, err := store.Spec()
	if err != nil || group.Purpose != "exploration" {
		return ReviewReport{}, errors.New("managed role review requires an exploration evidence group")
	}
	report := ReviewReport{Schema: ReviewSchema, Role: spec.Name, Revision: revision, Scope: "battery_screening",
		State: "empty", Candidates: []Candidate{}, EvaluatedAt: now.UTC().Format(time.RFC3339)}
	var qualified []*record.Record
	for _, model := range models {
		result, err := store.Read(model)
		if err != nil {
			return ReviewReport{}, err
		}
		candidate, result := reviewResult(Candidate{ID: result.Completion.EvidenceSHA256, RunID: result.StableRunID(), Model: result.Model, State: "unresolved"}, result, spec, now)
		report.Candidates = append(report.Candidates, candidate)
		if candidate.State == "eligible" {
			qualified = append(qualified, result)
		}
	}
	selectCandidates(&report, spec, qualified)
	return report, nil
}
