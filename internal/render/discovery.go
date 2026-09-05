package render

import (
	"fmt"
	"io"
	"strings"
)

type DiscoveryCard struct {
	Role, Model, Harness, Claim, Next string
	Steps                             []DiscoveryStep
}

type DiscoveryStep struct{ Label, Text, Command string }

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
		if card.Claim != "" {
			Field(w, "  claim", 11, card.Claim, width)
		}
		if card.Harness != "" {
			Field(w, "  harness", 11, card.Harness+" (unverified)", width)
		}
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
