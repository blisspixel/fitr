package role

import (
	"errors"
	"time"

	"github.com/blisspixel/fitr/internal/record"
)

// ReviewManaged screens an explicit ordered candidate set without modifying
// role attachments. All evidence is resolved under the fixed results root.
func ReviewManaged(spec Spec, results record.Store, ref record.ManagedStoreRef, models []string, now time.Time) (ReviewReport, error) {
	return ReviewManagedWithContext(spec, results, ref, nil, models, now)
}

// ReviewManagedWithContext screens the battery group and, when a sibling
// context store is supplied, applies only that store's usable-context floor.
// Behavior, capacity and performance stay on the battery records. Quant
// comparison does too. A missing sibling leaves a byte floor unresolved.
func ReviewManagedWithContext(spec Spec, results record.Store, ref record.ManagedStoreRef, contextRef *record.ManagedStoreRef, models []string, now time.Time) (ReviewReport, error) {
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
	contextStore, err := resolveExplorationStore(results, contextRef)
	if err != nil {
		return ReviewReport{}, err
	}
	report := ReviewReport{Schema: ReviewSchema, Role: spec.Name, Revision: revision, Scope: "battery_screening",
		State: "empty", Candidates: []Candidate{}, EvaluatedAt: now.UTC().Format(time.RFC3339)}
	var qualified []*record.Record
	var sharedPlan string
	for _, model := range models {
		battery, err := store.Read(model)
		if err != nil {
			return ReviewReport{}, err
		}
		// reviewResult returns the record only once the battery alone is
		// eligible. A usable-context floor is unresolved on that battery, so
		// the sibling has to be applied to the original point. Quant
		// comparison still receives the battery record, never the context run.
		candidate, qualifiedRecord := reviewResult(Candidate{ID: battery.Completion.EvidenceSHA256, RunID: battery.StableRunID(), Model: battery.Model, State: "unresolved"}, battery, spec, now)
		if contextRef != nil {
			candidate, qualifiedRecord, sharedPlan, err = applyManagedContext(candidate, spec, battery, contextStore, model, sharedPlan)
			if err != nil {
				return ReviewReport{}, err
			}
		}
		report.Candidates = append(report.Candidates, candidate)
		if candidate.State == "eligible" && qualifiedRecord != nil {
			qualified = append(qualified, qualifiedRecord)
		}
	}
	selectCandidates(&report, spec, qualified)
	return report, nil
}

func resolveExplorationStore(results record.Store, contextRef *record.ManagedStoreRef) (record.ManagedStore, error) {
	if contextRef == nil {
		return record.ManagedStore{}, nil
	}
	contextStore, err := record.ResolveManagedStore(results, *contextRef)
	if err != nil {
		return record.ManagedStore{}, err
	}
	contextGroup, err := contextStore.Spec()
	if err != nil || contextGroup.Purpose != "exploration" {
		return record.ManagedStore{}, errors.New("managed context review requires an exploration evidence group")
	}
	return contextStore, nil
}

func applyManagedContext(candidate Candidate, spec Spec, battery *record.Record, contextStore record.ManagedStore, model, sharedPlan string) (Candidate, *record.Record, string, error) {
	contextRecord, err := contextStore.Read(model)
	if err != nil {
		return Candidate{}, nil, sharedPlan, err
	}
	digest := contextRecord.TaskPlan.ContextPlanSHA256
	if digest == "" || (sharedPlan != "" && digest != sharedPlan) {
		return Candidate{}, nil, sharedPlan, errors.New("context records do not share one sealed plan")
	}
	candidate, err = overlayUsableContext(candidate, spec, battery, contextRecord, digest)
	if err != nil {
		return Candidate{}, nil, sharedPlan, err
	}
	if candidate.State != "eligible" {
		return candidate, nil, digest, nil
	}
	return candidate, battery, digest, nil
}
