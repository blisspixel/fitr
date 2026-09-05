package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/blisspixel/fitr/internal/discovery"
	"github.com/blisspixel/fitr/internal/render"
)

type discoveryInboxOutput struct {
	Schema    string                               `json:"schema"`
	Ideas     []discovery.Idea                     `json:"ideas"`
	Note      string                               `json:"note"`
	Sources   map[string][]discovery.SourceSummary `json:"sources"`
	Proposals []discovery.SourceProposal           `json:"proposals,omitempty"`
}

func renderDiscovery(ideas []discovery.Idea, proposals []discovery.SourceProposal, summaries map[string][]discovery.SourceSummary, mode string) int {
	var output bytes.Buffer
	switch render.Resolve(mode) {
	case "none":
		return exitOK
	case "json":
		err := json.NewEncoder(&output).Encode(discoveryInboxOutput{
			Schema: "fitr.discovery.inbox.v1", Ideas: ideas, Sources: summaries, Proposals: proposals,
			Note: "Unmeasured ideas only. Source receipts are operator associations, not runtime identity or quality evidence. No network, downloads or model execution.",
		})
		if err != nil {
			return exitError
		}
	default:
		render.WriteDiscovery(&output, discoveryCards(ideas, proposals, summaries), mode)
	}
	if _, err := output.WriteTo(os.Stdout); err != nil {
		return exitError
	}
	return exitOK
}

func discoveryCards(ideas []discovery.Idea, proposals []discovery.SourceProposal, summaries map[string][]discovery.SourceSummary) []render.DiscoveryCard {
	plans := make(map[string]discovery.SourceProposal, len(proposals))
	for _, proposal := range proposals {
		plans[proposal.Idea.ID] = proposal
	}
	cards := make([]render.DiscoveryCard, 0, len(ideas))
	for _, idea := range ideas {
		card := render.DiscoveryCard{ID: idea.ID, Role: idea.Role, Model: idea.Model, Harness: idea.Harness, Claim: idea.Claim,
			Next: "fitr discover plan " + shellCommandArg(idea.ID, runtime.GOOS),
		}
		for _, summary := range summaries[idea.ID] {
			card.Sources = append(card.Sources, render.DiscoverySource{Digest: summary.ResolutionSHA256,
				State: summary.MetadataState, Repo: summary.RepoID, Commit: summary.Commit})
		}
		if proposal, ok := plans[idea.ID]; ok {
			populateDiscoveryPlan(&card, proposal)
		}
		cards = append(cards, card)
	}
	return cards
}

func populateDiscoveryPlan(card *render.DiscoveryCard, proposal discovery.SourceProposal) {
	card.Next = ""
	for _, facet := range proposal.Facets {
		card.Facets = append(card.Facets, render.DiscoveryStep{Label: facet.Code, Text: facet.State + ": " + facet.Text})
	}
	if proposal.SelectedResolutionSHA256 != "" {
		card.Facets = append(card.Facets, render.DiscoveryStep{Label: "selected", Text: proposal.SelectedResolutionSHA256})
	}
	for _, step := range proposal.Steps {
		// Mutable idea aliases must never become executable advise/run arguments.
		card.Steps = append(card.Steps, render.DiscoveryStep{Label: step.Code, Text: step.Text})
	}
	if proposal.Selected == nil {
		return
	}
	for _, dependency := range proposal.Selected.Dependencies {
		details := []string{dependency.Kind + ": " + dependency.Status}
		if dependency.SourceFile != "" {
			details = append(details, "source "+dependency.SourceFile)
		}
		if dependency.TargetFile != "" {
			details = append(details, "target "+dependency.TargetFile)
		}
		card.Facets = append(card.Facets, render.DiscoveryStep{Label: "dependency", Text: strings.Join(details, " | ")})
	}
	for _, gap := range proposal.Selected.Gaps {
		card.Facets = append(card.Facets, render.DiscoveryStep{Label: "gap", Text: gap})
	}
	for _, file := range proposal.Selected.Files {
		details := []string{file.Path, file.State}
		if file.SizeBytes != nil {
			details = append(details, fmt.Sprintf("%d bytes declared", *file.SizeBytes))
		}
		if file.DeclaredSHA256 != "" {
			details = append(details, file.DeclaredSHA256+" (provider declared; local bytes unverified)")
		} else {
			details = append(details, "content SHA-256 unavailable")
		}
		card.Files = append(card.Files, strings.Join(details, " | "))
	}
}
