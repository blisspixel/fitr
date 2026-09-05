package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/discovery"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/source"
)

const discoveryHelp = `usage: fitr discover add <source> --role <role> [--model <reference>] [--harness <name>] [--claim <text>]
       fitr discover list|plan [--role <role>] [--display MODE]
       fitr discover attach-source <idea-id> <receipt.json> [--display MODE]
       fitr discover detach-source <idea-id> <resolution-sha256> [--display MODE]
       fitr discover plan <idea-id> [--source <resolution-sha256>] [--display MODE]`

type discoveryCommand struct {
	action, mode, role, model, harness, claim, selected string
	args                                                []string
}

func cmdDiscover(ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(os.Stderr, discoveryHelp)
		return exitOK
	}
	command, code, ok := parseDiscoveryCommand(args)
	if !ok {
		return code
	}
	if ctx.Err() != nil {
		return exitInterrupt
	}
	store := discovery.SourceStore{Directory: filepath.Join(resultsDir(), ".discovery")}
	ideaIDs, err := discoveryAction(store, command)
	if err != nil {
		return discoveryFailure(err)
	}
	proposals, err := store.Plans(ideaIDs, command.selected)
	if err != nil {
		return discoveryFailure(err)
	}
	return renderDiscoverySnapshot(proposals, command)
}

func renderDiscoverySnapshot(proposals []discovery.SourceProposal, command discoveryCommand) int {
	ideas := make([]discovery.Idea, 0, len(proposals))
	summaries := make(map[string][]discovery.SourceSummary, len(proposals))
	for _, proposal := range proposals {
		ideas = append(ideas, proposal.Idea)
		summaries[proposal.Idea.ID] = proposal.Sources
	}
	if command.action != "plan" {
		proposals = nil
	}
	return renderDiscovery(ideas, proposals, summaries, command.mode)
}

func parseDiscoveryCommand(args []string) (discoveryCommand, int, bool) {
	command := discoveryCommand{action: args[0]}
	fs := flag.NewFlagSet("discover "+command.action, flag.ContinueOnError)
	fs.StringVar(&command.mode, "display", "auto", "auto|rich|plain|json|none")
	switch command.action {
	case "add":
		fs.StringVar(&command.model, "model", "", "candidate model reference; still unverified")
		fs.StringVar(&command.harness, "harness", "", "intended harness and version; still unverified")
		fs.StringVar(&command.claim, "claim", "", "source claim to investigate, not measured evidence")
		fallthrough
	case "list", "plan":
		fs.StringVar(&command.role, "role", "", "intended role or role filter")
		if command.action == "plan" {
			fs.StringVar(&command.selected, "source", "", "full attached source resolution digest")
		}
	case "attach-source", "detach-source":
	default:
		errPrint("unknown discovery action", "", "fitr discover --help")
		return command, exitUsage, false
	}
	if code, ok := parseCommandFlags(fs, args[1:]); !ok {
		return command, code, false
	}
	command.args = append([]string(nil), fs.Args()...)
	if !render.ValidMode(command.mode) || !validDiscoveryArguments(command) {
		errPrint("invalid discovery arguments or display mode", "", "fitr discover --help")
		return command, exitUsage, false
	}
	if command.action == "add" {
		if _, err := discovery.New(command.args[0], command.model, command.role, command.harness, command.claim, time.Now()); err != nil {
			errPrint(err.Error(), "", "provide a source, role, and bounded plain-text fields")
			return command, exitUsage, false
		}
	}
	return command, exitOK, true
}

func validDiscoveryArguments(command discoveryCommand) bool {
	switch command.action {
	case "add":
		return len(command.args) == 1 && strings.TrimSpace(command.role) != ""
	case "list":
		return len(command.args) == 0
	case "plan":
		if len(command.args) == 0 {
			return command.selected == ""
		}
		return len(command.args) == 1 && validDiscoveryID(command.args[0]) && command.role == "" &&
			(command.selected == "" || validDiscoveryDigest(command.selected))
	case "attach-source":
		return len(command.args) == 2 && validDiscoveryID(command.args[0])
	case "detach-source":
		return len(command.args) == 2 && validDiscoveryID(command.args[0]) && validDiscoveryDigest(command.args[1])
	default:
		return false
	}
}

func validDiscoveryID(value string) bool {
	_, err := hex.DecodeString(value)
	return len(value) == 64 && strings.ToLower(value) == value && err == nil
}

func validDiscoveryDigest(value string) bool {
	value, ok := strings.CutPrefix(value, "sha256:")
	return ok && validDiscoveryID(value)
}

func discoveryAction(store discovery.SourceStore, command discoveryCommand) ([]string, error) {
	if command.action == "add" {
		idea, err := discovery.New(command.args[0], command.model, command.role, command.harness, command.claim, time.Now())
		if err != nil {
			return nil, err
		}
		idea, err = discovery.Save(store.Directory, idea)
		return []string{idea.ID}, err
	}
	if len(command.args) == 0 {
		return discoveryIdeaIDs(store.Directory, command.role)
	}
	switch command.action {
	case "attach-source":
		receipt, err := source.LoadResolution(command.args[1])
		if err != nil {
			return nil, err
		}
		if _, err := store.Attach(command.args[0], receipt, time.Now()); err != nil {
			return nil, err
		}
	case "detach-source":
		if err := store.Detach(command.args[0], command.args[1]); err != nil {
			return nil, err
		}
	}
	return []string{command.args[0]}, nil
}

func discoveryIdeaIDs(directory, role string) ([]string, error) {
	ideas, err := discovery.List(directory, role)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(ideas))
	for _, idea := range ideas {
		ids = append(ids, idea.ID)
	}
	return ids, nil
}

func discoveryFailure(err error) int {
	errPrint("could not access discovery inbox: "+err.Error(), "", "inspect the idea and source receipts with fitr discover --help")
	return exitError
}
