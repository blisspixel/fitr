package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/role"
)

func cmdRole(_ context.Context, args []string) int {
	if len(args) == 0 {
		args = []string{"list"}
	}
	if args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(os.Stderr, "usage: fitr role init <name> --quality <need> --memory-gb <limit> [--minimum-rate 0.9] [--ctx 8192]")
		fmt.Fprintln(os.Stderr, "       fitr role define <role.json> | list | show <name> | review <name>")
		fmt.Fprintln(os.Stderr, "       fitr role attach <name> <result.json> | detach <name> <evidence-sha256>")
		return exitOK
	}
	action := args[0]
	switch action {
	case "init", "define", "list", "show", "review", "attach", "detach":
	default:
		errPrint("unknown role action", action, "fitr role --help")
		return exitUsage
	}
	options, positional, code := parseRoleOptions(action, args[1:])
	if code != exitOK || positional == nil {
		return code
	}
	store := role.Store{Dir: filepath.Join(resultsDir(), ".roles")}
	if action == "list" {
		libraries, err := store.List()
		if err != nil {
			return roleFailure(err)
		}
		return writeRoleLibraries(libraries, options.display)
	}
	library, err := loadRoleAction(store, action, positional, options)
	if err != nil {
		return roleFailure(err)
	}
	switch action {
	case "show":
		return showRole(library, options.display)
	case "review":
		return reviewRole(library, options.display)
	default:
		return writeRoleLibraries([]role.Library{library}, options.display)
	}
}

type roleOptions struct {
	display, quality string
	minimum, memory  float64
	context, age     int
}

func parseRoleOptions(action string, args []string) (roleOptions, []string, int) {
	fs := flag.NewFlagSet("role "+action, flag.ContinueOnError)
	options := roleOptions{}
	fs.StringVar(&options.display, "display", "auto", "auto|rich|plain|json|none")
	if action == "init" {
		fs.StringVar(&options.quality, "quality", "", "required behavioral evidence, such as user_tasks or structured_output")
		fs.Float64Var(&options.minimum, "minimum-rate", 0.9, "minimum independently checked quality rate")
		fs.Float64Var(&options.memory, "memory-gb", 0, "maximum observed resident memory in GiB (required)")
		fs.IntVar(&options.context, "ctx", 8192, "minimum verified context and resident-memory measurement context")
		fs.IntVar(&options.age, "max-age-days", 30, "maximum evidence age before a new measurement is required")
	}
	if code, ok := parseCommandFlags(fs, args); !ok {
		return options, nil, code
	}
	if !render.ValidMode(options.display) || !roleArgCount(action, fs.NArg()) {
		errPrint("invalid role arguments or display mode", "", "fitr role --help")
		return options, nil, exitUsage
	}
	if action == "init" {
		spec := initialRoleSpec(fs.Arg(0), options.quality, options.minimum, options.memory, options.context, options.age)
		if err := spec.Validate(); err != nil {
			errPrint(err.Error(), "", "provide a quality need, positive memory budget and supported context")
			return options, nil, exitUsage
		}
	}
	return options, append([]string{}, fs.Args()...), exitOK
}

func loadRoleAction(store role.Store, action string, args []string, options roleOptions) (role.Library, error) {
	switch action {
	case "init":
		return store.Define(initialRoleSpec(args[0], options.quality, options.minimum, options.memory, options.context, options.age))
	case "define":
		spec, err := role.LoadSpec(args[0])
		if err != nil {
			return role.Library{}, err
		}
		return store.Define(spec)
	case "attach":
		path, err := filepath.Abs(args[1])
		if err != nil {
			return role.Library{}, err
		}
		attachment, err := role.AttachRecord(path, record.Store{Dir: resultsDir()})
		if err != nil {
			return role.Library{}, err
		}
		return store.Attach(args[0], attachment)
	case "detach":
		return store.Detach(args[0], args[1])
	default:
		return store.Load(args[0])
	}
}

func reviewRole(library role.Library, mode string) int {
	report, err := role.Review(library, record.Store{Dir: resultsDir()}, time.Now())
	if err != nil {
		return roleFailure(err)
	}
	if render.Resolve(mode) == "json" {
		if code := writeRoleJSON(report); code != exitOK {
			return code
		}
	} else if render.Resolve(mode) != "none" {
		render.WriteRoleReview(os.Stdout, report, mode)
	}
	switch report.State {
	case "no-qualified-candidate":
		return exitGates
	case "single-qualified", "exploration-lead":
		return exitOK
	default:
		return exitUnresolved
	}
}

func showRole(library role.Library, mode string) int {
	spec, err := library.CurrentSpec()
	if err != nil {
		return roleFailure(err)
	}
	if render.Resolve(mode) == "none" {
		return exitOK
	}
	// show produces the editable role definition; define seals a new revision.
	return writeRoleJSON(spec)
}

func roleArgCount(action string, count int) bool {
	switch action {
	case "list":
		return count == 0
	case "attach", "detach":
		return count == 2
	default:
		return count == 1
	}
}

func initialRoleSpec(name, quality string, minimum, memory float64, contextSize, age int) role.Spec {
	return role.Spec{
		Schema: role.SpecSchema, Name: name, MaxAgeDays: age,
		Decision: decision.DecisionSpec{
			Schema: decision.SpecSchema, Name: name, Evidence: decision.EvidenceDecide,
			Requirements: []decision.Requirement{
				{ID: "quality", Behavior: &decision.BehaviorRequirement{Need: quality, MinimumRate: &minimum}},
				{ID: "context", Context: &decision.ContextRequirement{MinimumEffectiveTokens: contextSize}},
				{ID: "memory", Capacity: &decision.CapacityRequirement{MaximumResidentGB: memory, RequestedContext: contextSize}},
			},
		},
		Preferences: []role.Preference{{Requirement: "quality", Weight: 1, Worst: 0, Best: 1}},
	}
}

func writeRoleJSON(value any) int {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return exitError
	}
	return exitOK
}

func writeRoleLibraries(libraries []role.Library, mode string) int {
	switch render.Resolve(mode) {
	case "none":
		return exitOK
	case "json":
		return writeRoleJSON(struct {
			Schema string         `json:"schema"`
			Roles  []role.Library `json:"roles"`
		}{"fitr.role.index.v1", libraries})
	default:
		render.WriteRoleLibraries(os.Stdout, libraries, mode)
		return exitOK
	}
}

func roleFailure(err error) int {
	errPrint("could not process role: "+err.Error(), "", "inspect the role definition and canonical evidence paths")
	return exitError
}
