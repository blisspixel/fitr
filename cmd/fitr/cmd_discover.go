package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/discovery"
	"github.com/blisspixel/fitr/internal/render"
)

func cmdDiscover(_ context.Context, args []string) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(os.Stderr, "usage: fitr discover add <source> --role <role> [--model <reference>] [--harness <name>] [--claim <text>]")
		fmt.Fprintln(os.Stderr, "       fitr discover list|plan [--role <role>] [--display MODE]")
		return exitOK
	}
	action := args[0]
	if action != "add" && action != "list" && action != "plan" {
		errPrint("unknown discovery action", action, "use fitr discover add, list, or plan")
		return exitUsage
	}
	fs := flag.NewFlagSet("discover "+action, flag.ContinueOnError)
	role := fs.String("role", "", "intended role, such as coding, daily-driver, or classifier")
	model := fs.String("model", "", "candidate model reference; still unverified")
	harness := fs.String("harness", "", "intended harness and version; still unverified")
	claim := fs.String("claim", "", "source claim to investigate, not measured evidence")
	mode := fs.String("display", "auto", "auto|rich|plain|json|none")
	if code, ok := parseCommandFlags(fs, args[1:]); !ok {
		return code
	}
	if !render.ValidMode(*mode) {
		errPrint("invalid display mode", *mode, "use auto, rich, plain, json, or none")
		return exitUsage
	}
	directory := filepath.Join(resultsDir(), ".discovery")
	var ideas []discovery.Idea
	var err error
	if action == "add" {
		if fs.NArg() != 1 {
			errPrint("discovery add needs exactly one source", "", "fitr discover add <source> --role coding")
			return exitUsage
		}
		var idea discovery.Idea
		idea, err = discovery.New(fs.Arg(0), *model, *role, *harness, *claim, time.Now())
		if err != nil {
			errPrint(err.Error(), "", "provide a source, role, and bounded plain-text fields")
			return exitUsage
		}
		idea, err = discovery.Save(directory, idea)
		ideas = []discovery.Idea{idea}
	} else {
		if fs.NArg() != 0 || *model != "" || *harness != "" || *claim != "" {
			errPrint("list and plan accept only role and display filters", "", "fitr discover plan --role coding")
			return exitUsage
		}
		ideas, err = discovery.List(directory, *role)
	}
	if err != nil {
		errPrint("could not access discovery inbox: "+err.Error(), "", "check the private results directory")
		return exitError
	}
	return renderDiscovery(ideas, action == "plan", *mode)
}

func renderDiscovery(ideas []discovery.Idea, plan bool, mode string) int {
	if render.Resolve(mode) == "none" {
		return exitOK
	}
	if render.Resolve(mode) == "json" {
		var proposals []discovery.Proposal
		if plan {
			for _, idea := range ideas {
				proposals = append(proposals, discovery.Plan(idea))
			}
		}
		if err := json.NewEncoder(os.Stdout).Encode(struct {
			Schema    string               `json:"schema"`
			Ideas     []discovery.Idea     `json:"ideas"`
			Note      string               `json:"note"`
			Proposals []discovery.Proposal `json:"proposals,omitempty"`
		}{"fitr.discovery.inbox.v1", ideas, "Unmeasured ideas only. No sources fetched, models downloaded, or experiments executed.", proposals}); err != nil {
			return exitError
		}
		return exitOK
	}
	render.WriteDiscovery(os.Stdout, discoveryCards(ideas, plan), mode)
	return exitOK
}

func discoveryCards(ideas []discovery.Idea, plan bool) []render.DiscoveryCard {
	cards := make([]render.DiscoveryCard, 0, len(ideas))
	for _, idea := range ideas {
		card := render.DiscoveryCard{Role: idea.Role, Model: idea.Model, Harness: idea.Harness, Claim: idea.Claim,
			Next: "fitr discover plan --role " + shellCommandArg(idea.Role, runtime.GOOS),
		}
		if plan {
			card.Next = ""
			for _, step := range discovery.Plan(idea).Steps {
				detail := render.DiscoveryStep{Label: step.Code, Text: step.Text}
				if len(step.Argv) > 0 {
					quoted := make([]string, 0, len(step.Argv))
					for _, arg := range step.Argv {
						quoted = append(quoted, shellCommandArg(arg, runtime.GOOS))
					}
					detail.Command = strings.Join(quoted, " ")
				}
				card.Steps = append(card.Steps, detail)
			}
		}
		cards = append(cards, card)
	}
	return cards
}
