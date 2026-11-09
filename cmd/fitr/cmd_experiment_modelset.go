package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/blisspixel/fitr/internal/atomicfile"
	"github.com/blisspixel/fitr/internal/boundedio"
	"github.com/blisspixel/fitr/internal/experiment"
	"github.com/blisspixel/fitr/internal/render"
)

const modelSetUsage = `usage:
  fitr experiment modelset plan --members members.json --runtime ollama --device-key KEY --out plan.json
  fitr experiment modelset classify --plan plan.json --observation observation.json --out bundle.json
  fitr experiment modelset <modelset-bundle.json> [--display MODE]

plan seals the ordered artifacts, roles, contexts, and placement intentions
before any observation is read. classify derives an unresolved report from
that plan and one recorded body. An Ollama process list, separate responses,
llama-server props, an unpinned router body, and a sum of isolated
measurements do not establish co-residency. This command does not contact a
runtime and does not enter the single-model Board.
`

func cmdExperimentModelSet(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		writeAskedHelp(os.Stdout, modelSetUsage)
		return exitOK
	}
	switch args[0] {
	case "plan":
		return cmdModelSetPlan(args[1:])
	case "classify":
		return cmdModelSetClassify(args[1:])
	default:
		return cmdModelSetReopen(args)
	}
}

func cmdModelSetPlan(args []string) int {
	fs := commandFlags("experiment modelset plan")
	membersPath := fs.String("members", "", "ordered member JSON")
	runtimeName := fs.String("runtime", "", "ollama, llama-server, or openai")
	deviceKey := fs.String("device-key", "", "comparability key for this device")
	policy := fs.String("capacity-policy", "", "optional sha256 of a capacity policy")
	out := fs.String("out", "", "plan path")
	display := fs.String("display", "auto", "auto|rich|plain|json|none")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 || *membersPath == "" || *runtimeName == "" || *deviceKey == "" || *out == "" {
		errPrint("model-set plan needs --members, --runtime, --device-key, and --out",
			"members are supplied, not discovered",
			"fitr experiment modelset plan --members members.json --runtime ollama --device-key KEY --out plan.json")
		return exitUsage
	}
	if !render.ValidMode(*display) {
		errPrint("invalid display mode", *display, "use auto, rich, plain, json, or none")
		return exitUsage
	}
	return sealModelSetPlan(*membersPath, *runtimeName, *deviceKey, *policy, *out, *display)
}

func sealModelSetPlan(membersPath, runtimeName, deviceKey, policy, out, display string) int {
	data, err := boundedio.ReadFile(membersPath, 1<<20)
	if err != nil {
		errPrint("could not read model-set members: "+err.Error(), membersPath, "pass a JSON array of members")
		return exitError
	}
	members, err := experiment.DecodeModelSetMembers(data)
	if err != nil {
		errPrint("could not read model-set members: "+err.Error(), membersPath, "pass a JSON array of members")
		return exitError
	}
	plan, err := experiment.NewModelSetPlan(runtimeName, deviceKey, policy, members)
	if err != nil {
		errPrint("could not seal model-set plan: "+err.Error(), "", "fix the declared members and retry")
		return exitError
	}
	body, err := plan.JSON()
	if err != nil {
		errPrint("could not seal model-set plan: "+err.Error(), "", "")
		return exitError
	}
	if err := atomicfile.Write(out, body, 0o600); err != nil {
		errPrint("could not write model-set plan: "+err.Error(), out, "")
		return exitError
	}
	return renderModelSetPlan(plan, display, out)
}

func cmdModelSetClassify(args []string) int {
	fs := commandFlags("experiment modelset classify")
	planPath := fs.String("plan", "", "sealed model-set plan")
	observationPath := fs.String("observation", "", "recorded observation JSON")
	out := fs.String("out", "", "bundle path")
	display := fs.String("display", "auto", "auto|rich|plain|json|none")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 || *planPath == "" || *observationPath == "" || *out == "" {
		errPrint("model-set classify needs --plan, --observation, and --out",
			"the observation is a recorded body, not a live query",
			"fitr experiment modelset classify --plan plan.json --observation observation.json --out bundle.json")
		return exitUsage
	}
	if !render.ValidMode(*display) {
		errPrint("invalid display mode", *display, "use auto, rich, plain, json, or none")
		return exitUsage
	}
	return classifyModelSet(*planPath, *observationPath, *out, *display)
}

func classifyModelSet(planPath, observationPath, out, display string) int {
	planData, err := boundedio.ReadFile(planPath, 1<<20)
	if err != nil {
		errPrint("could not read model-set plan: "+err.Error(), planPath, "")
		return exitError
	}
	plan, err := experiment.DecodeModelSetPlan(planData)
	if err != nil {
		errPrint("model-set plan is not sealed: "+err.Error(), planPath, "")
		return exitError
	}
	observationData, err := boundedio.ReadFile(observationPath, 1<<20)
	if err != nil {
		errPrint("could not read model-set observation: "+err.Error(), observationPath, "")
		return exitError
	}
	observation, err := experiment.DecodeModelSetObservation(observationData)
	if err != nil {
		errPrint("could not read model-set observation: "+err.Error(), observationPath, "")
		return exitError
	}
	bundle, err := experiment.NewModelSetBundle(plan, observation)
	if err != nil {
		errPrint("could not derive model-set report: "+err.Error(), "", "")
		return exitError
	}
	body, err := bundle.JSON()
	if err != nil {
		errPrint("could not derive model-set report: "+err.Error(), "", "")
		return exitError
	}
	if err := atomicfile.Write(out, body, 0o600); err != nil {
		errPrint("could not write model-set bundle: "+err.Error(), out, "")
		return exitError
	}
	return renderModelSetReport(bundle.Report, display, out)
}

func cmdModelSetReopen(args []string) int {
	fs := commandFlags("experiment modelset")
	display := fs.String("display", "auto", "auto|rich|plain|json|none")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if !render.ValidMode(*display) {
		errPrint("invalid display mode", *display, "use auto, rich, plain, json, or none")
		return exitUsage
	}
	if fs.NArg() != 1 {
		errPrint("model-set needs a plan, a classify command, or one bundle",
			"this command does not contact a runtime", "fitr experiment modelset --help")
		return exitUsage
	}
	bundle, err := experiment.LoadModelSetBundle(fs.Arg(0))
	if err != nil {
		errPrint("could not reopen model-set bundle: "+err.Error(), fs.Arg(0),
			"pass a bundle written by fitr experiment modelset classify")
		return exitError
	}
	return renderModelSetReport(bundle.Report, *display, fs.Arg(0))
}

func renderModelSetPlan(plan experiment.ModelSetPlan, display, path string) int {
	if render.Resolve(display) == "none" {
		return exitOK
	}
	if render.Resolve(display) == "json" {
		return writeModelSetJSON(plan)
	}
	width := render.Width()
	render.Field(os.Stdout, "  plan", 16, plan.PlanSHA256, width)
	render.Field(os.Stdout, "  members", 16, strconv.Itoa(len(plan.Members)), width)
	render.Field(os.Stdout, "  runtime", 16, plan.Runtime, width)
	render.Field(os.Stdout, "  file", 16, path, width)
	fmt.Fprintln(os.Stdout, "  note: this plan does not contact a runtime or claim co-residency")
	return exitOK
}

func renderModelSetReport(report experiment.ModelSetReport, display, path string) int {
	if render.Resolve(display) == "none" {
		return exitOK
	}
	if render.Resolve(display) == "json" {
		return writeModelSetJSON(report)
	}
	width := render.Width()
	coResident := "unresolved"
	if report.CoResident != nil && !*report.CoResident {
		coResident = "no"
	}
	render.Field(os.Stdout, "  plan", 16, report.PlanSHA256, width)
	render.Field(os.Stdout, "  claim", 16, report.Claim, width)
	render.Field(os.Stdout, "  disposition", 16, report.Disposition, width)
	render.Field(os.Stdout, "  co-resident", 16, coResident, width)
	render.Field(os.Stdout, "  reason", 16, report.Reason, width)
	render.Field(os.Stdout, "  file", 16, path, width)
	fmt.Fprintln(os.Stdout, "  note: this report is not a fit verdict and does not enter the Board")
	return exitOK
}

func writeModelSetJSON(value any) int {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		errPrint("could not render model-set: "+err.Error(), "", "")
		return exitError
	}
	return exitOK
}
