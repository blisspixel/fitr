package main

import (
	"errors"
	"time"

	"github.com/blisspixel/fitr/internal/automation"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/role"
)

func describeAuto(plan automation.Plan, state automation.State, now time.Time) (render.AutoStatus, int) {
	view := render.AutoStatus{SessionID: plan.ID, Role: plan.Spec.Name, State: state.Outcome,
		Incumbent: "Current selection not inspected", CandidateCount: len(plan.Candidates),
		ExplorationPoints: len(state.CompletedExploration), ConfirmationPoints: len(state.CompletedConfirmation),
		Requests: state.Requests, RequestLimit: plan.Limits.MaxRequests,
		OutputTokens: state.RequestedOutputTokens, OutputTokenLimit: plan.Limits.MaxRequestedOutputTokens, ExpiresAt: plan.ExpiresAt}
	if view.State == "" {
		view.State = state.Phase
	}
	if state.ActivePoint > 0 && state.ActivePoint <= len(plan.Candidates) {
		view.Candidate = plan.Candidates[state.ActivePoint-1].Model
	}
	if state.Confirmation != nil {
		for _, candidate := range state.Confirmation.Candidates {
			if candidate.EvidenceSHA256 == state.Confirmation.ChosenEvidenceSHA256 {
				view.Choice = candidate.Model.Resolved
			}
		}
	}
	view.Label, view.Next = autoStateText(view.State, plan.ID)
	code := exitOK
	if state.Outcome != "" && state.Outcome != "adopted" {
		code = exitUnresolved
	}
	if state.Outcome == "" && (now.Before(state.LastObservedAt) || !now.Before(state.Deadline(plan))) {
		view.State, view.Label = "expired", "Session allowance no longer permits work"
		view.Gap = "The original wall deadline or recorded clock prevents further collection or first adoption."
		view.Next = "fitr role status " + plan.Spec.Name
		code = exitUnresolved
	}
	if state.Outcome == "" && view.State != "expired" && (state.Phase == "exploration" || state.Phase == "comparing") {
		view.ProtectedRequests = int64(len(plan.Candidates)) * plan.PointRequests
		view.ProtectedOutputTokens = int64(len(plan.Candidates)) * plan.PointRequestedOutputTokens
	}
	return view, code
}

func autoStateText(state, id string) (string, string) {
	labels := map[string]string{
		"exploration":        "Collecting evidence for the fixed shortlist",
		"comparing":          "Exploration complete; quality comparison pending",
		"confirmation":       "Fresh confirmation started; adoption pending",
		"awaiting_adoption":  "Fresh confirmation complete; adoption pending",
		"adopted":            "Confirmed choice adopted in fitr",
		"no_qualified":       "No candidate clears the role's quality and resource floors",
		"overlap":            "Evidence does not establish a clear preference",
		"incumbent_retained": "Confirmation did not establish the choice; selection unchanged",
		"unresolved":         "Evidence is insufficient to select a model",
		"blocked":            "A required preflight check blocked this session",
		"budget_exhausted":   "The session reached its bounded allowance",
		"cancelled":          "Session cancelled; selection unchanged",
		"failed":             "Session stopped before acceptance",
		"stale":              "Session definitions or evidence changed",
	}
	next := ""
	switch state {
	case "exploration", "comparing":
		next = "fitr auto resume " + id
	case "confirmation":
		next = "fitr auto status " + id
	case "awaiting_adoption":
		next = "fitr auto adopt " + id
	}
	return labels[state], next
}

func autoSelectedStatus(view *render.AutoStatus, plan automation.Plan) {
	roles, records := autoStores()
	status, err := roles.ReviewSelection(plan.Spec.Name, records, time.Now())
	if err != nil {
		view.Incumbent = "Current selection unavailable; inspect role status"
		return
	}
	view.Incumbent = "No model selected"
	if status.Selection != nil {
		view.Incumbent = status.Selection.Selected.Model.Resolved + " (" + status.State + ")"
	}
}

// Terminal exploration reviews describe current evidence without reopening or
// changing the session's historical decision.
func autoExplorationStatus(view *render.AutoStatus, plan automation.Plan, state automation.State, records record.Store, now time.Time) {
	switch state.Outcome {
	case "no_qualified", "overlap", "unresolved":
	default:
		return
	}
	const unavailable = "Exploration review unavailable: original evidence is missing, changed or could not be validated. The recorded session outcome is unchanged."
	if _, err := autoGroupPoints(plan, records, state.ExplorationStore, "exploration", state.CompletedExploration); err != nil {
		view.Gap = unavailable
		return
	}
	models := make([]string, len(plan.Candidates))
	for index, candidate := range plan.Candidates {
		models[index] = candidate.Model
	}
	contextRef, err := autoExplorationContext(plan, state, records)
	if err != nil {
		view.Gap = unavailable
		return
	}
	review, err := role.ReviewManagedWithContext(plan.Spec, records, *state.ExplorationStore, contextRef, models, now)
	if err != nil {
		view.Gap = unavailable
		return
	}
	view.Review = &review
}

// autoExplorationContext reopens the sibling document store when the session
// sealed one. A missing sibling is not an unmeasured floor: the battery review
// would hide the schedule that decided the outcome.
func autoExplorationContext(plan automation.Plan, state automation.State, records record.Store) (*record.ManagedStoreRef, error) {
	if plan.ContextPolicy == nil {
		return nil, nil
	}
	id, err := automation.ContextStoreID(plan, "exploration")
	if err != nil {
		return nil, err
	}
	store, err := record.OpenManagedStore(records, id)
	if err != nil {
		return nil, err
	}
	ref, err := store.Ref()
	if err != nil {
		return nil, err
	}
	if len(state.CompletedExploration) != len(plan.Candidates) {
		return nil, errors.New("context evidence does not cover the sealed schedule")
	}
	for index, event := range state.CompletedExploration {
		point, err := store.Read(plan.Candidates[index].Model)
		if err != nil || point.Completion == nil || point.Completion.EvidenceSHA256 != event.ContextEvidenceSHA256 {
			return nil, errors.New("stored context evidence differs from the sealed point")
		}
	}
	return &ref, nil
}
