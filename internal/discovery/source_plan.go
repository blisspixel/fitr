package discovery

import (
	"errors"

	"github.com/blisspixel/fitr/internal/source"
)

type SourceFacet struct {
	Code  string `json:"code"`
	State string `json:"state"`
	Text  string `json:"text"`
}

type SourceProposal struct {
	Schema                   string             `json:"schema"`
	Idea                     Idea               `json:"idea"`
	State                    string             `json:"state"`
	Sources                  []SourceSummary    `json:"sources"`
	SelectedResolutionSHA256 string             `json:"selected_resolution_sha256,omitempty"`
	Selected                 *source.Resolution `json:"selected,omitempty"`
	Facets                   []SourceFacet      `json:"facets"`
	Steps                    []Step             `json:"steps"`
}

// Plan inspects one idea through the same bounded snapshot used by Plans.
func (store SourceStore) Plan(ideaID, selectedResolutionSHA256 string) (SourceProposal, error) {
	plans, err := store.Plans([]string{ideaID}, selectedResolutionSHA256)
	if err != nil {
		return SourceProposal{}, err
	}
	return plans[0], nil
}

// Plans validates the managed store once under one lock, then builds plans in
// input order. IDs must be unique and bounded; explicit selection requires one
// idea. An empty request returns no plans. No receipt is merged or promoted to
// runtime-bound evidence, and no step contains an executable recipe.
func (store SourceStore) Plans(ideaIDs []string, selectedResolutionSHA256 string) (plans []SourceProposal, err error) {
	if len(ideaIDs) > maximumIdeas || (selectedResolutionSHA256 != "" && len(ideaIDs) != 1) {
		return nil, errors.New("source plans require at most 1000 ideas and a single idea for explicit source selection")
	}
	seen := make(map[string]bool, len(ideaIDs))
	for _, id := range ideaIDs {
		if !sourceIDPattern.MatchString(id) || seen[id] {
			return nil, errors.New("source plan idea IDs must be full and unique")
		}
		seen[id] = true
	}
	if selectedResolutionSHA256 != "" {
		if _, err := sourceDigestFilename(selectedResolutionSHA256); err != nil {
			return nil, err
		}
	}
	if len(ideaIDs) == 0 {
		return []SourceProposal{}, nil
	}
	paths, guard, err := store.acquire(ideaIDs[0])
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, guard.Release()) }()
	all, _, err := paths.readAll()
	if err != nil {
		return nil, err
	}
	plans = make([]SourceProposal, 0, len(ideaIDs))
	for _, id := range ideaIDs {
		idea, err := paths.idea(id)
		if err != nil {
			return nil, err
		}
		plan, err := buildSourcePlan(idea, all[id], selectedResolutionSHA256)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

func buildSourcePlan(idea Idea, attachments []SourceAttachment, selectedResolutionSHA256 string) (SourceProposal, error) {
	plan := SourceProposal{Schema: SourcePlanSchema, Idea: idea, State: "source_missing", Sources: summarizeSources(attachments),
		Steps: []Step{{Code: "inspect", Text: "Inspect the source metadata and its declared file identities; operator association does not verify the original source claim."},
			{Code: "dependencies", Text: "Investigate shards, projector, encoders, tokenizer and cross-repository dependencies without downloading or executing content."},
			{Code: "runtime", Text: "Bind locally verified bytes to an exact runtime configuration before measuring this candidate."},
			{Code: "quality", Text: "Declare quality floors and collect task evidence before comparison or adoption."}}}
	if len(attachments) > 1 {
		plan.State = "selection_required"
	}
	if selectedResolutionSHA256 == "" && len(attachments) == 1 {
		selectedResolutionSHA256 = attachments[0].ResolutionSHA256
	}
	for _, attachment := range attachments {
		if attachment.ResolutionSHA256 == selectedResolutionSHA256 {
			receipt := attachment.Resolution
			plan.Selected, plan.SelectedResolutionSHA256, plan.State = &receipt, selectedResolutionSHA256, "source_selected"
		}
	}
	if selectedResolutionSHA256 != "" && plan.Selected == nil {
		return SourceProposal{}, errors.New("selected source digest is not attached to this idea")
	}
	plan.Facets = sourceFacets(plan.Selected, plan.State)
	return plan, nil
}

func sourceFacets(receipt *source.Resolution, state string) []SourceFacet {
	metadataState, fileState := state, "unresolved"
	if receipt != nil {
		metadataState = receipt.State
		fileState = "declared"
		if receipt.State != "resolved" {
			fileState = "incomplete"
		}
	}
	return []SourceFacet{
		{Code: "metadata", State: metadataState, Text: "Remote file metadata observations only; provider-declared hashes are not verified local bytes."},
		{Code: "files", State: fileState, Text: "Only the selected receipt's explicit files belong to this plan; receipts are never merged."},
		{Code: "dependencies", State: "unverified", Text: "Filename candidates and shard gaps do not establish compatible or complete dependencies."},
		{Code: "runtime", State: "unbound", Text: "No installed artifact, context, device or runtime configuration is bound."},
		{Code: "quality", State: "unmeasured", Text: "The idea and its original source claim remain unmeasured; no role qualification or adoption is authorized."},
	}
}
