package main

import (
	"errors"
	"time"

	"github.com/blisspixel/fitr/internal/automation"
	"github.com/blisspixel/fitr/internal/fitting"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/role"
)

// confirmedDelegationSources projects one fresh confirmation. It certifies
// only the preselected candidate when a recomputed report is confirmed for
// that identity. Every other candidate stays out of the scope, so a loser's
// result cannot blank the winner's artifact or fail a class the winner
// passed. A terminal outcome other than adopted certifies nothing. The
// report is derived again from the sealed plan and the stored records.
func confirmedDelegationSources(plan automation.Plan, state automation.State, records record.Store, now time.Time) ([]fitting.ObservationSource, []string) {
	if state.Outcome != "" && state.Outcome != "adopted" {
		return nil, []string{"nothing was certified"}
	}
	if state.Confirmation == nil {
		return nil, []string{"fresh confirmation has no sealed plan; nothing was certified"}
	}
	points, err := autoGroupPoints(plan, records, state.ConfirmationStore, "confirmation", state.CompletedConfirmation)
	if err != nil {
		return nil, []string{"fresh confirmation evidence could not be resolved; nothing was certified"}
	}
	contextPoints, err := confirmationContextPoints(plan, state, records)
	if err != nil {
		return nil, []string{"fresh confirmation context evidence could not be resolved; nothing was certified"}
	}
	report, err := role.AnalyzeConfirmationContext(*state.Confirmation, points, contextPoints, now)
	if err != nil {
		return nil, []string{"fresh confirmation could not be evaluated; nothing was certified"}
	}
	selected, note := winnerRecords(*state.Confirmation, report, points, contextPoints)
	if note != "" {
		return nil, []string{note}
	}
	sources := sourcesFromRecords(selected, true)
	if len(sources) != len(selected) {
		return nil, []string{"fresh confirmation winner failed validation; nothing was certified"}
	}
	_, _, conflicts := fitting.ProjectObservations(sources)
	if len(conflicts) > 0 {
		return nil, []string{"winner evidence disagrees with itself; nothing was certified"}
	}
	return sources, nil
}

func confirmationContextPoints(plan automation.Plan, state automation.State, records record.Store) ([]*record.Record, error) {
	if state.Confirmation == nil || state.Confirmation.ContextPolicy == nil {
		return nil, nil
	}
	id, err := automation.ContextStoreID(plan, "confirmation")
	if err != nil {
		return nil, err
	}
	store, err := record.OpenManagedStore(records, id)
	if err != nil {
		return nil, err
	}
	if len(state.CompletedConfirmation) != len(plan.Candidates) {
		return nil, errors.New("context evidence does not cover the sealed schedule")
	}
	points := make([]*record.Record, 0, len(plan.Candidates))
	for index, candidate := range plan.Candidates {
		point, err := store.Read(candidate.Model)
		if err != nil {
			return nil, err
		}
		if err := validateAutoContextPoint(plan, point, state.Confirmation.SeedSet, state.Confirmation.ContextPlanSHA256); err != nil {
			return nil, err
		}
		event := state.CompletedConfirmation[index]
		if point.Model != candidate.Model || point.Completion == nil ||
			point.Completion.EvidenceSHA256 != event.ContextEvidenceSHA256 ||
			point.StableRunID() == event.RunID || point.Completion.EvidenceSHA256 == event.EvidenceSHA256 ||
			point.RuntimeBinding == nil || point.RuntimeBinding.ArtifactDigest != candidate.ArtifactDigest ||
			point.RuntimeBinding.ModelConfigurationSHA256 != candidate.ModelConfigurationSHA256 {
			return nil, errors.New("stored context evidence differs from the sealed point")
		}
		points = append(points, point)
	}
	return points, nil
}

// winnerRecords keeps the fresh battery record at the confirmed preselected
// position, plus that position's document record when the plan sealed one.
// The winner hash is the exploration identity on the confirmation plan, not
// the fresh record's own evidence hash.
func winnerRecords(plan role.ConfirmationPlan, report role.ConfirmationReport, points, contextPoints []*record.Record) ([]*record.Record, string) {
	if report.State != "confirmed" || report.WinnerEvidenceSHA256 == "" || report.WinnerEvidenceSHA256 != plan.ChosenEvidenceSHA256 {
		state := report.State
		if state == "" {
			state = "unresolved"
		}
		return nil, "fresh confirmation did not confirm the preselected choice (" + state + "); nothing was certified"
	}
	index := -1
	for i, candidate := range plan.Candidates {
		if candidate.EvidenceSHA256 != report.WinnerEvidenceSHA256 {
			continue
		}
		if index >= 0 {
			return nil, "fresh confirmation winner is not a single sealed candidate; nothing was certified"
		}
		index = i
	}
	if index < 0 || index >= len(points) || points[index] == nil || points[index].Completion == nil {
		return nil, "fresh confirmation winner was not in the sealed evidence; nothing was certified"
	}
	selected := []*record.Record{points[index]}
	if plan.ContextPolicy != nil {
		if index >= len(contextPoints) || contextPoints[index] == nil || contextPoints[index].Completion == nil {
			return nil, "fresh confirmation winner has no document record; nothing was certified"
		}
		selected = append(selected, contextPoints[index])
	}
	return selected, ""
}
