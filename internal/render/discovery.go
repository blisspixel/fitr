package render

import (
	"fmt"
	"io"
	"strings"
)

type DiscoveryCard struct {
	ID                                string
	Role, Model, Harness, Claim, Next string
	Steps                             []DiscoveryStep
	Sources                           []DiscoverySource
	Facets                            []DiscoveryStep
	Files                             []string
}

type DiscoveryStep struct{ Label, Text, Command string }
type DiscoverySource struct{ Digest, State, Repo, Commit string }

// WriteDiscovery uses the CLI's common width, palette and plain-output rules.
// The role and model lead; evidence state is always explicit text.
func WriteDiscovery(w io.Writer, cards []DiscoveryCard, mode string) {
	p, _ := inventoryStyle(Resolve(mode) == "rich")
	width := Width()
	fmt.Fprintf(w, "  %s\n", p.wrap(p.Head, "fitr / discovery"))
	fmt.Fprintf(w, "  %s\n", p.wrap(p.Muted, fmt.Sprintf("%d ideas | private inbox", len(cards))))
	fmt.Fprintf(w, "  %s\n", p.wrap(p.Muted, strings.Repeat("-", width-4)))
	if len(cards) == 0 {
		Field(w, "  ", 2, "Start with something you heard about. Capture a source and the role it might serve.", width)
		Field(w, "  next", 11, "fitr discover add <source> --role coding", width)
	}
	for index, card := range cards {
		fmt.Fprintln(w)
		role := fmt.Sprintf("%02d  %s", index+1, strings.ToUpper(SingleLine(card.Role)))
		Field(w, "  ", 2, role, width)
		model := card.Model
		if model == "" {
			model = "Artifact not yet resolved"
		}
		for _, line := range wrap(SingleLine(model), width-4) {
			fmt.Fprintf(w, "  %s\n", p.wrap(p.Accent, line))
		}
		fmt.Fprintf(w, "  %s\n", p.wrap(p.Warn, "[UNMEASURED]"))
		if card.ID != "" {
			Field(w, "  idea", 11, card.ID, width)
		}
		if card.Model != "" {
			Field(w, "  ", 2, "Model hint from the idea; exact runtime binding is unverified.", width)
		}
		if card.Claim != "" {
			Field(w, "  claim", 11, card.Claim, width)
		}
		if card.Harness != "" {
			Field(w, "  harness", 11, card.Harness+" (unverified)", width)
		}
		writeDiscoveryDetails(w, card, width)
		for _, step := range card.Steps {
			Field(w, "  "+step.Label, 15, step.Text, width)
			if step.Command != "" {
				Field(w, "", 15, step.Command, width)
			}
		}
		if card.Next != "" {
			Field(w, "  next", 11, card.Next, width)
		}
	}
	fmt.Fprintln(w)
	Field(w, "  ", 2, "Source claims remain unmeasured. No downloads or execution.", width)
}

func writeDiscoveryDetails(w io.Writer, card DiscoveryCard, width int) {
	if len(card.Sources) == 0 {
		Field(w, "  metadata", 11, "No source receipt attached", width)
	}
	for _, attached := range card.Sources {
		fmt.Fprintln(w)
		Field(w, "  metadata", 11, attached.State+" | "+attached.Repo, width)
		if attached.Commit != "" {
			Field(w, "  commit", 11, attached.Commit, width)
		}
		writeDiscoveryDigest(w, "receipt", attached.Digest, width)
	}
	if len(card.Sources) > 0 {
		Field(w, "  ", 2, "Operator association only; the original claim remains unverified.", width)
	}
	for _, file := range card.Files {
		Field(w, "  file", 11, file, width)
	}
	for _, facet := range card.Facets {
		if facet.Label == "selected" {
			writeDiscoveryDigest(w, facet.Label, facet.Text, width)
			continue
		}
		Field(w, "  "+facet.Label, 16, facet.Text, width)
	}
}

func writeDiscoveryDigest(w io.Writer, label, digest string, width int) {
	// A full sha256: digest fits an 80-column value row and remains copyable.
	// Narrow terminals still wrap safely through the common field renderer.
	Field(w, "  ", 2, label, width)
	Field(w, "    ", 4, digest, width)
}
