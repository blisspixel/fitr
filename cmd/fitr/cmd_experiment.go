package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/atomicfile"
	"github.com/blisspixel/fitr/internal/calibration"
	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/experiment"
	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/workload"
)

const experimentUsage = `usage:
  fitr experiment context <model> --ctx 4096,8192,16384 [-k N] [--backend B]
  fitr experiment context <context-bundle.json> [--display MODE]
  fitr experiment context <result.json> <result.json>... [--display MODE]
  fitr experiment quant <result.json> <result.json>... --spec decision.json [--lineage conversion.json]
  fitr experiment confirm <model> <model>... --spec decision.json [--ctx N] [-k N]
  fitr experiment confirm <confirmation-bundle.json> [--display MODE]
  fitr experiment workload <model> [-n 3] [--ctx N] [--backend B]
  fitr experiment workload <workload-bundle.json> [--display MODE]

Context analysis is exploratory. Every input must be a sealed fitr result.
Requested context is the treatment; artifact, backend/runtime, device
configuration, and measurement protocol must remain equal for comparison.
`

func cmdExperiment(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, experimentUsage)
		return exitUsage
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(os.Stderr, experimentUsage)
		return exitOK
	case "context":
		return cmdExperimentContext(ctx, args[1:])
	case "quant":
		return cmdExperimentQuant(args[1:])
	case "confirm":
		return cmdExperimentConfirm(ctx, args[1:])
	case "workload":
		return cmdExperimentWorkload(ctx, args[1:])
	default:
		errPrint("unknown experiment", args[0], "fitr experiment context --help")
		return exitUsage
	}
}

type confirmationCommand struct {
	mode, backend, profile, specPath string
	repeats, ctx                     int
	positional                       []string
}

func cmdExperimentConfirm(ctx context.Context, args []string) int {
	command, code, ok := parseConfirmationCommand(args)
	if !ok {
		return code
	}
	if len(command.positional) == 1 && confirmationBundlePath(command.positional[0]) {
		return reopenConfirmationBundle(command.positional[0], command)
	}
	if len(command.positional) < 2 || len(command.positional) > 4 {
		errPrint("confirmation needs between two and four models", "the candidate set is sealed before inference",
			"fitr experiment confirm <model-a> <model-b> --spec decision.json")
		return exitUsage
	}
	if command.specPath == "" {
		errPrint("confirmation needs a decision specification", "", "pass --spec decision.json")
		return exitUsage
	}
	if command.repeats < 3 || command.repeats > 20 || command.ctx < 1 || command.ctx > 16*1024*1024 {
		errPrint("invalid confirmation measurement plan", "-k must be 3..20 and --ctx must be positive and bounded", "use -k 3")
		return exitUsage
	}
	return runConfirmationExperiment(ctx, command)
}

func parseConfirmationCommand(args []string) (confirmationCommand, int, bool) {
	fs := flag.NewFlagSet("experiment confirm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	mode := fs.String("display", "auto", "auto|rich|plain|json|none")
	backend := fs.String("backend", "auto", "auto|ollama|llama-server|openai")
	profile := fs.String("profile", "", "device profile (default: auto-match)")
	specPath := fs.String("spec", "", "confirmation decision specification JSON")
	repeats := fs.Int("k", 3, "fresh samples and task repeats per candidate")
	requestedContext := fs.Int("ctx", 8192, "requested context tokens")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return confirmationCommand{}, code, false
	}
	if !render.ValidMode(*mode) {
		errPrint("invalid display mode", *mode, "use auto, rich, plain, json, or none")
		return confirmationCommand{}, exitUsage, false
	}
	return confirmationCommand{
		mode: *mode, backend: *backend, profile: *profile, specPath: *specPath,
		repeats: *repeats, ctx: *requestedContext, positional: append([]string(nil), fs.Args()...),
	}, exitOK, true
}

func confirmationBundlePath(path string) bool {
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		return true
	}
	return strings.HasSuffix(strings.ToLower(path), ".json")
}

func reopenConfirmationBundle(path string, command confirmationCommand) int {
	if command.backend != "auto" || command.profile != "" || command.specPath != "" ||
		command.repeats != 3 || command.ctx != 8192 {
		errPrint("live confirmation flags cannot be applied to a saved bundle", path,
			"remove live flags or start a new confirmation experiment")
		return exitUsage
	}
	bundle, err := experiment.LoadConfirmationBundle(path)
	if err != nil {
		errPrint("could not load confirmation bundle: "+err.Error(), path,
			"pass a bundle written by fitr experiment confirm")
		return exitError
	}
	return renderConfirmationExperiment(bundle, command.mode)
}

func runConfirmationExperiment(ctx context.Context, command confirmationCommand) int {
	spec, err := decision.LoadSpec(command.specPath)
	if err != nil {
		errPrint("could not load confirmation spec: "+err.Error(), command.specPath, "fix the spec and retry")
		return exitUsage
	}
	models := append([]string(nil), command.positional...)
	for index := range models {
		models[index] = normalizeModelRef(models[index])
	}
	backend, code := newBackendWithDisplay(ctx, models[0], command.backend, false, nil)
	if code != exitOK {
		return code
	}
	identities := make([]record.ModelIdentity, 0, len(models))
	for _, model := range models {
		resolved, resolveErr := resolveRunModel(ctx, backend, model)
		if resolveErr != nil {
			errPrint("could not bind confirmation candidate: "+resolveErr.Error(), model,
				"install every candidate and use a runtime that reports artifact identity")
			return exitError
		}
		identities = append(identities, resolved.Identity)
	}
	fingerprint := device.Detect(ctx, backend)
	plan, err := experiment.NewConfirmationPlan(identities, fingerprint.Key(), spec, command.ctx, command.repeats)
	if err != nil {
		errPrint("could not create confirmation plan: "+err.Error(), "", "use a confirm-level spec and distinct candidates")
		return exitUsage
	}
	return executeConfirmationPlan(ctx, backend, plan, spec, command)
}

func executeConfirmationPlan(ctx context.Context, backend llm.Backend, plan experiment.ConfirmationPlan,
	spec decision.DecisionSpec, command confirmationCommand) int {
	display := render.New("none")
	defer display.Close()
	results := make([]*record.Record, 0, len(plan.Candidates))
	for index, candidate := range plan.Candidates {
		fmt.Fprintf(os.Stderr, "  confirm  %d/%d  %s\n", index+1, len(plan.Candidates), terminalText(candidate.Resolved))
		binding := experiment.ConfirmationPlanBinding(plan, index+1)
		result, err := execute(ctx, backend, candidate.Resolved, runOpts{
			level: "full", profile: command.profile, seedSet: plan.SeedSet,
			reps: command.repeats, checksReps: command.repeats,
			numCtx: command.ctx, memoryCtx: command.ctx, experiment: &binding,
		}, display)
		if err != nil {
			if ctx.Err() != nil {
				fmt.Fprintln(os.Stderr, "\ninterrupted")
				return exitInterrupt
			}
			errPrint("confirmation point failed: "+err.Error(),
				fmt.Sprintf("%d completed point(s) remain saved; no confirmation bundle was created", len(results)),
				"resolve the failure and start a new predeclared confirmation")
			return exitError
		}
		path, saveErr := save(result)
		if saveErr != nil {
			errPrint("could not save confirmation point: "+saveErr.Error(), "no confirmation bundle was created", "")
			return exitError
		}
		fmt.Fprintf(os.Stderr, "  saved    %s\n", terminalText(path))
		results = append(results, result)
	}
	bundle, err := experiment.NewConfirmationBundle(plan, spec, results)
	if err != nil {
		errPrint("could not analyze confirmation: "+err.Error(), "the sealed point records remain saved", "")
		return exitError
	}
	path, err := saveConfirmationBundle(bundle)
	if err != nil {
		errPrint("could not save confirmation bundle: "+err.Error(), "the sealed point records remain saved", "")
		return exitError
	}
	fmt.Fprintf(os.Stderr, "  bundle   %s\n", terminalText(path))
	return renderConfirmationExperiment(bundle, command.mode)
}

type workloadCommand struct {
	mode, backend string
	trials, turns int
	timeout, ctx  int
	pull          bool
	positional    []string
}

func cmdExperimentWorkload(ctx context.Context, args []string) int {
	command, code, ok := parseWorkloadCommand(args)
	if !ok {
		return code
	}
	if len(command.positional) != 1 {
		errPrint("workload experiment needs exactly one model or bundle", "",
			"fitr experiment workload <model> -n 3")
		return exitUsage
	}
	input := command.positional[0]
	if workloadBundlePath(input) {
		return reopenWorkloadBundle(input, command)
	}
	if err := workload.ValidatePlanBounds(command.trials, command.turns, command.timeout, command.ctx); err != nil {
		errPrint("invalid workload plan: "+err.Error(), "", "fix the declared bounds and retry")
		return exitUsage
	}
	return runWorkloadExperiment(ctx, input, command)
}

func workloadBundlePath(path string) bool {
	if strings.HasSuffix(strings.ToLower(path), ".json") {
		return true
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	prefix := make([]byte, 512)
	count, _ := file.Read(prefix)
	return strings.HasPrefix(strings.TrimSpace(string(prefix[:count])), "{")
}

func parseWorkloadCommand(args []string) (workloadCommand, int, bool) {
	fs := flag.NewFlagSet("experiment workload", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	mode := fs.String("display", "auto", "auto|rich|plain|json|none")
	backend := fs.String("backend", "auto", "auto|ollama|llama-server|openai")
	trials := fs.Int("n", 3, "predeclared workflow trials")
	turns := fs.Int("max-turns", 12, "maximum model turns per trial")
	timeout := fs.Int("timeout", 180, "timeout in seconds per trial")
	requestedContext := fs.Int("ctx", 8192, "requested context tokens")
	pull := fs.Bool("pull", false, "pull a missing Ollama model before the experiment")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return workloadCommand{}, code, false
	}
	if !render.ValidMode(*mode) {
		errPrint("invalid display mode", *mode, "use auto, rich, plain, json, or none")
		return workloadCommand{}, exitUsage, false
	}
	return workloadCommand{
		mode: *mode, backend: *backend, trials: *trials, turns: *turns,
		timeout: *timeout, ctx: *requestedContext, pull: *pull,
		positional: append([]string(nil), fs.Args()...),
	}, exitOK, true
}

func reopenWorkloadBundle(path string, command workloadCommand) int {
	if command.trials != 3 || command.turns != 12 || command.timeout != 180 ||
		command.ctx != 8192 || command.backend != "auto" || command.pull {
		errPrint("live workload flags cannot be applied to a saved bundle", path,
			"remove live flags or run a new workload experiment")
		return exitUsage
	}
	bundle, err := workload.LoadBundle(path)
	if err != nil {
		errPrint("could not load workload bundle: "+err.Error(), path,
			"pass a bundle written by fitr experiment workload")
		return exitError
	}
	return renderWorkloadExperiment(bundle, command.mode)
}

func runWorkloadExperiment(ctx context.Context, model string, command workloadCommand) int {
	model = normalizeModelRef(model)
	backend, code := newBackendWithDisplay(ctx, model, command.backend, command.pull, nil)
	if code != exitOK {
		return code
	}
	resolved, err := resolveRunModel(ctx, backend, model)
	if err != nil {
		errPrint("could not bind workload model identity: "+err.Error(), "",
			"use a runtime that reports a verifiable artifact digest")
		return exitError
	}
	fingerprint := device.Detect(ctx, backend)
	sealed, err := workload.NewPlan(resolved.Identity, fingerprint.Key(), command.trials,
		command.turns, command.timeout, command.ctx)
	if err != nil {
		errPrint("could not create workload plan: "+err.Error(), "", "fix the declared bounds and retry")
		return exitUsage
	}
	fmt.Fprintf(os.Stderr, "  workload  %s v%d, %d predeclared trial(s)\n",
		workload.WorkflowID, workload.WorkflowVersion, command.trials)
	bundle, err := sealed.Run(ctx, backend)
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "\ninterrupted")
			return exitInterrupt
		}
		errPrint("workload experiment failed: "+err.Error(), "", "no incomplete bundle was saved")
		return exitError
	}
	if err := verifyWorkloadBindings(ctx, backend, sealed.Plan); err != nil {
		errPrint("workload evidence binding changed: "+err.Error(), "the completed receipt was not saved",
			"restore the runtime configuration and start a new predeclared experiment")
		return exitError
	}
	path, err := saveWorkloadBundle(bundle)
	if err != nil {
		errPrint("could not save workload bundle: "+err.Error(), "", "the completed receipt was not persisted")
		return exitError
	}
	fmt.Fprintf(os.Stderr, "  bundle    %s\n", terminalText(path))
	return renderWorkloadExperiment(bundle, command.mode)
}

func verifyWorkloadBindings(ctx context.Context, backend llm.Backend, plan workload.Plan) error {
	observed, err := resolveRunModel(ctx, backend, plan.Model.Resolved)
	if err != nil {
		return fmt.Errorf("recheck model identity: %w", err)
	}
	want, got := plan.Model, observed.Identity
	got.Requested = want.Requested
	if got != want {
		return errors.New("runtime-bound model identity differs from the sealed plan")
	}
	if observedDevice := device.Detect(ctx, backend).Key(); observedDevice != plan.DeviceKey {
		return errors.New("device configuration differs from the sealed plan")
	}
	return nil
}

func cmdExperimentQuant(args []string) int {
	fs := flag.NewFlagSet("experiment quant", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	mode := fs.String("display", "auto", "auto|rich|plain|json|none")
	specPath := fs.String("spec", "", "decision specification JSON")
	lineagePath := fs.String("lineage", "", "same-base conversion manifest JSON")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if !render.ValidMode(*mode) {
		errPrint("invalid display mode", *mode, "use auto, rich, plain, json, or none")
		return exitUsage
	}
	if strings.TrimSpace(*specPath) == "" || fs.NArg() < 2 {
		errPrint("quant experiment needs a decision spec and at least two result paths", "--spec is required",
			"fitr experiment quant <result.json> <result.json> --spec decision.json")
		return exitUsage
	}
	spec, err := decision.LoadSpec(*specPath)
	if err != nil {
		errPrint("could not load decision specification: "+err.Error(), "", "fix the spec and retry")
		return exitError
	}
	conversion, code := loadQuantConversion(*lineagePath)
	if code != exitOK {
		return code
	}
	results, code := loadExperimentResults(fs.Args(), "quant candidate")
	if code != exitOK {
		return code
	}
	report, err := experiment.AnalyzeQuant(results, spec, conversion)
	if err != nil {
		errPrint("could not analyze quant experiment: "+err.Error(), "",
			"inspect every source with fitr view <result.json> --display json --full")
		return exitError
	}
	return renderQuantExperiment(report, *mode)
}

func loadQuantConversion(path string) (*calibration.ConversionManifest, int) {
	if strings.TrimSpace(path) == "" {
		return nil, exitOK
	}
	manifest, err := calibration.ReadConversionManifest(path)
	if err != nil {
		errPrint("could not load conversion manifest: "+err.Error(), path,
			"use a fitr.lineage.conversion.v1 document that names every candidate artifact")
		return nil, exitError
	}
	return &manifest, exitOK
}

func loadExperimentResults(paths []string, label string) ([]*record.Record, int) {
	results := make([]*record.Record, 0, len(paths))
	store := record.NewStore(resultsDir())
	for _, path := range paths {
		result, err := store.Read(path)
		if err != nil {
			errPrint("could not read "+label+": "+err.Error(), path,
				"pass a complete sealed result JSON file")
			return nil, exitError
		}
		results = append(results, result)
	}
	return results, exitOK
}

func cmdExperimentContext(ctx context.Context, args []string) int {
	command, code, ok := parseContextExperimentCommand(args)
	if !ok {
		return code
	}
	if strings.TrimSpace(command.contexts) != "" {
		return runLiveContextCommand(ctx, command)
	}
	return analyzeStoredContextCommand(command)
}

type contextExperimentCommand struct {
	mode, contexts, backend, profile string
	repeats                          int
	pull                             bool
	positional                       []string
}

func parseContextExperimentCommand(args []string) (contextExperimentCommand, int, bool) {
	fs := flag.NewFlagSet("experiment context", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	mode := fs.String("display", "auto", "auto|rich|plain|json|none")
	contextsArg := fs.String("ctx", "", "comma-separated requested contexts for a live exploratory plan")
	repeats := fs.Int("k", 3, "performance samples per context point")
	backend := fs.String("backend", "auto", "auto|ollama|llama-server|openai")
	profile := fs.String("profile", "", "device profile (default: auto-match)")
	pull := fs.Bool("pull", false, "pull a missing Ollama model before the experiment")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return contextExperimentCommand{}, code, false
	}
	if !render.ValidMode(*mode) {
		errPrint("invalid display mode", *mode, "use auto, rich, plain, json, or none")
		return contextExperimentCommand{}, exitUsage, false
	}
	return contextExperimentCommand{
		mode: *mode, contexts: *contextsArg, repeats: *repeats, backend: *backend,
		profile: *profile, pull: *pull, positional: append([]string(nil), fs.Args()...),
	}, exitOK, true
}

func runLiveContextCommand(ctx context.Context, command contextExperimentCommand) int {
	if len(command.positional) != 1 {
		errPrint("live context experiment needs exactly one model", "--ctx declares the point plan",
			"fitr experiment context <model> --ctx 4096,8192,16384")
		return exitUsage
	}
	contexts, err := parseContextPoints(command.contexts)
	if err != nil {
		errPrint("invalid context plan", err.Error(), "use two or more distinct positive token counts separated by commas")
		return exitUsage
	}
	if command.repeats < 1 || command.repeats > 20 {
		errPrint("invalid performance sample count", "-k must be between 1 and 20", "use -k 3 for an exploratory sweep")
		return exitUsage
	}
	return runContextExperiment(ctx, command.positional[0], contexts, command.repeats,
		command.backend, command.profile, command.pull, command.mode)
}

func analyzeStoredContextCommand(command contextExperimentCommand) int {
	if command.repeats != 3 || command.backend != "auto" || command.profile != "" || command.pull {
		errPrint("live-run flags require --ctx", "result-file analysis does not contact a runtime",
			"remove run flags, or use fitr experiment context <model> --ctx 4096,8192")
		return exitUsage
	}
	if len(command.positional) == 1 {
		bundle, err := experiment.LoadContextBundle(command.positional[0])
		if err != nil {
			errPrint("could not load context bundle: "+err.Error(), command.positional[0],
				"pass a bundle written by fitr experiment context")
			return exitError
		}
		report, err := bundle.Validate()
		if err != nil {
			errPrint("could not validate context bundle: "+err.Error(), "", "re-run the experiment from its sealed point plan")
			return exitError
		}
		return renderContextExperiment(report, command.mode)
	}
	if len(command.positional) < 2 {
		errPrint("context experiment needs at least two result paths", "one point cannot show a context treatment",
			"fitr experiment context <result.json> <result.json>...")
		return exitUsage
	}
	results, code := loadExperimentResults(command.positional, "context point")
	if code != exitOK {
		return code
	}
	report, err := experiment.AnalyzeContext(results)
	if err != nil {
		errPrint("could not analyze context experiment: "+err.Error(), "",
			"inspect each source with fitr view <result.json> --display json --full")
		return exitError
	}
	return renderContextExperiment(report, command.mode)
}

func runContextExperiment(ctx context.Context, model string, contexts []int, repeats int, backend, profile string,
	pull bool, mode string) int {
	model = normalizeModelRef(model)
	plan, planSHA256, err := experiment.NewContextPlan(model, contexts, repeats)
	if err != nil {
		errPrint("could not create context plan: "+err.Error(), "", "fix the declared contexts and retry")
		return exitUsage
	}
	c, code := newBackendWithDisplay(ctx, model, backend, pull, nil)
	if code != exitOK {
		return code
	}
	display := render.New("none")
	defer display.Close()
	results := make([]*record.Record, 0, len(contexts))
	for index, requested := range contexts {
		fmt.Fprintf(os.Stderr, "  point  %d/%d  requested context %d\n", index+1, len(contexts), requested)
		binding := experiment.ContextPlanBinding(planSHA256, index+1, len(contexts))
		result, runErr := execute(ctx, c, model, runOpts{
			level: plan.Level, profile: profile, seedSet: plan.SeedSet,
			reps: repeats, checksReps: 1, numCtx: requested, memoryCtx: requested,
			experiment: &binding,
		}, display)
		if runErr != nil {
			if ctx.Err() != nil {
				fmt.Fprintln(os.Stderr, "\ninterrupted")
				return exitInterrupt
			}
			remaining := len(contexts) - index - 1
			note := fmt.Sprintf("%d completed point(s) remain saved; %d later point(s) were not run", len(results), remaining)
			errPrint("context point failed: "+runErr.Error(), note,
				"resolve the failed point before starting a new predeclared experiment")
			return exitError
		}
		path, saveErr := save(result)
		if saveErr != nil {
			errPrint("could not save context point: "+saveErr.Error(), "the experiment stopped before later points",
				"fix result storage and start a new predeclared experiment")
			return exitError
		}
		fmt.Fprintf(os.Stderr, "  saved  %s\n", terminalText(path))
		results = append(results, result)
	}
	return finishContextExperiment(plan, planSHA256, results, mode)
}

func finishContextExperiment(plan experiment.ContextPlan, planSHA256 string,
	results []*record.Record, mode string) int {
	report, err := experiment.AnalyzePlannedContext(results, plan, planSHA256)
	if err != nil {
		errPrint("could not analyze predeclared context experiment: "+err.Error(), "",
			"the sealed point records remain saved for inspection")
		return exitError
	}
	bundle, err := experiment.NewContextBundle(plan, planSHA256, results)
	if err != nil {
		errPrint("could not build context bundle: "+err.Error(), "the sealed point records remain saved", "")
		return exitError
	}
	bundlePath, err := saveContextBundle(bundle)
	if err != nil {
		errPrint("could not save context bundle: "+err.Error(), "the sealed point records remain saved", "")
		return exitError
	}
	fmt.Fprintf(os.Stderr, "  bundle %s\n", terminalText(bundlePath))
	return renderContextExperiment(report, mode)
}

func saveContextBundle(bundle experiment.ContextBundle) (string, error) {
	data, err := bundle.JSON()
	if err != nil {
		return "", err
	}
	directory := filepath.Join(resultsDir(), ".experiments")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	planID := strings.TrimPrefix(bundle.PlanSHA256, "sha256:")
	if len(planID) > 12 {
		planID = planID[:12]
	}
	runID := "no-run"
	if len(bundle.PointRecords) > 0 {
		runID = bundle.PointRecords[0].StableRunID()
	}
	path := filepath.Join(directory, "context-"+planID+"-"+runID+".json")
	if err := atomicfile.Write(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func renderContextExperiment(report experiment.ContextReport, mode string) int {
	if render.Resolve(mode) == "none" {
		return exitOK
	}
	if render.Resolve(mode) == "json" {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			errPrint("could not render context experiment: "+err.Error(), "", "")
			return exitError
		}
		return exitOK
	}
	writeContextExperimentText(report)
	return exitOK
}

func renderQuantExperiment(report experiment.QuantReport, mode string) int {
	if render.Resolve(mode) == "none" {
		return exitOK
	}
	if render.Resolve(mode) == "json" {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			errPrint("could not render quant experiment: "+err.Error(), "", "")
			return exitError
		}
		return exitOK
	}
	writeQuantExperimentText(report)
	return exitOK
}

func writeQuantExperimentText(report experiment.QuantReport) {
	title := "QUANT CONFIGURATION EXPERIMENT"
	if report.Stage == experiment.StageConfirm {
		title = "CONFIGURATION CONFIRMATION"
	}
	comparison := "UNRESOLVED"
	if report.Comparison.Ready {
		comparison = "READY"
	}
	lineage := "UNVERIFIED"
	if report.Lineage.Verified {
		lineage = "VERIFIED"
	}
	fmt.Fprintf(os.Stdout, "%s  %s\n", title, strings.ToUpper(string(report.Stage)))
	fmt.Fprintf(os.Stdout, "  %-12s %s\n", "comparison", comparison)
	fmt.Fprintf(os.Stdout, "  %-12s %s\n", "lineage", lineage)
	fmt.Fprintf(os.Stdout, "  %-12s %s\n", "claim", terminalText(report.Lineage.Scope))
	fmt.Fprintf(os.Stdout, "  %-12s %s\n", "spec", terminalText(report.SpecName))
	writeQuantCandidates(report.Candidates)
	writeQuantFrontier(report.Frontier)
	writeQuantConclusion(report)
}

func writeQuantCandidates(candidates []experiment.QuantCandidate) {
	fmt.Fprintln(os.Stdout, "\nCANDIDATES")
	fmt.Fprintln(os.Stdout, "  eligibility  quant       model                    decode       request TTFT  resident")
	for _, candidate := range candidates {
		fmt.Fprintf(os.Stdout, "  %-11s  %-10s  %-23s  %-11s  %-12s  %s\n",
			strings.ToUpper(string(candidate.Evaluation.Eligibility)), terminalText(candidate.Quant),
			terminalText(candidate.Subject.ResolvedModel), formatQuantMetric(candidate.Metrics["decode_tps"]),
			formatQuantMetric(candidate.Metrics["request_ttft_seconds"]),
			formatQuantMetric(candidate.Metrics["resident_bytes"]))
		if digest := analysis.ShortDigest(candidate.Subject.ArtifactDigest); digest != "" {
			fmt.Fprintf(os.Stdout, "  %-11s  artifact sha256:%s", "", digest)
			if bytes := candidate.Metrics["artifact_bytes"]; bytes.Estimate != nil {
				fmt.Fprintf(os.Stdout, "  %.1f GB file", *bytes.Estimate/(1024*1024*1024))
			}
			fmt.Fprintln(os.Stdout)
		}
	}
	if quantLabelsCollide(candidates) {
		fmt.Fprintln(os.Stdout, "  note  quant string is not identity; files above share a recipe label")
	}
}

func quantLabelsCollide(candidates []experiment.QuantCandidate) bool {
	seen := map[string]string{}
	for _, candidate := range candidates {
		label := strings.TrimSpace(candidate.Quant)
		digest := candidate.Subject.ArtifactDigest
		if label == "" || digest == "" {
			continue
		}
		if previous, ok := seen[label]; ok && previous != digest {
			return true
		}
		seen[label] = digest
	}
	return false
}

func writeQuantFrontier(frontier experiment.QuantFrontier) {
	fmt.Fprintln(os.Stdout, "\nCONSERVATIVE FRONTIER")
	if len(frontier.Nondominated) == 0 {
		fmt.Fprintln(os.Stdout, "  no eligible candidate has decision-bearing frontier evidence")
	} else {
		for _, candidate := range frontier.Nondominated {
			fmt.Fprintln(os.Stdout, "  "+terminalText(candidate))
		}
	}
	for _, dominance := range frontier.Dominance {
		fmt.Fprintf(os.Stdout, "  %s dominates %s on %s\n", terminalText(dominance.Dominant),
			terminalText(dominance.Dominated), terminalText(strings.Join(dominance.Metrics, ", ")))
	}
}

func writeQuantConclusion(report experiment.QuantReport) {
	if report.Objective != nil {
		fmt.Fprintln(os.Stdout, "\nOBJECTIVE")
		fmt.Fprintf(os.Stdout, "  %-12s %-20s %s\n", strings.ToUpper(report.Objective.State),
			terminalText(report.Objective.Metric), terminalText(report.Objective.Reason))
		if report.Objective.Candidate != "" {
			fmt.Fprintf(os.Stdout, "  %-12s %s\n", "candidate", terminalText(report.Objective.Candidate))
		}
	}
	if len(report.Gaps) > 0 {
		fmt.Fprintln(os.Stdout, "\nGAPS")
		for _, gap := range report.Gaps {
			fmt.Fprintln(os.Stdout, "  "+terminalText(gap))
		}
	}
	if report.NextAction != nil {
		fmt.Fprintln(os.Stdout, "\nNEXT")
		fmt.Fprintln(os.Stdout, "  "+terminalText(report.NextAction.Reason))
	}
}

func formatQuantMetric(metric experiment.MetricEstimate) string {
	if metric.Estimate == nil || metric.Status != analysis.StatusAvailable {
		return "n/a"
	}
	switch metric.Unit {
	case analysis.UnitTokensPerSecond:
		return fmt.Sprintf("%.2f tok/s", *metric.Estimate)
	case analysis.UnitSeconds:
		return fmt.Sprintf("%.3fs", *metric.Estimate)
	case analysis.UnitBytes:
		return fmt.Sprintf("%.2f GB", *metric.Estimate/(1024*1024*1024))
	default:
		return fmt.Sprintf("%.4g %s", *metric.Estimate, metric.Unit)
	}
}

func saveWorkloadBundle(bundle workload.Bundle) (string, error) {
	data, err := bundle.JSON()
	if err != nil {
		return "", err
	}
	directory := filepath.Join(resultsDir(), ".experiments")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	planID := strings.TrimPrefix(bundle.Plan.PlanSHA256, "sha256:")
	if len(planID) > 12 {
		planID = planID[:12]
	}
	path := filepath.Join(directory, "workload-"+planID+".json")
	if err := atomicfile.Write(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func saveConfirmationBundle(bundle experiment.ConfirmationBundle) (string, error) {
	data, err := bundle.JSON()
	if err != nil {
		return "", err
	}
	directory := filepath.Join(resultsDir(), ".experiments")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	planID := strings.TrimPrefix(bundle.Plan.PlanSHA256, "sha256:")
	if len(planID) > 12 {
		planID = planID[:12]
	}
	path := filepath.Join(directory, "confirmation-"+planID+".json")
	if err := atomicfile.Write(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func renderConfirmationExperiment(bundle experiment.ConfirmationBundle, mode string) int {
	if code := renderQuantExperiment(bundle.Report, mode); code != exitOK {
		return code
	}
	if bundle.Report.Objective != nil && bundle.Report.Objective.State == "confirmed" {
		return exitOK
	}
	eligible, disproven := 0, 0
	for _, candidate := range bundle.Report.Candidates {
		switch candidate.Evaluation.Eligibility {
		case decision.DecisionEligible:
			eligible++
		case decision.DecisionIneligible:
			disproven++
		}
	}
	if eligible == 0 && disproven > 0 {
		return exitGates
	}
	return exitUnresolved
}

func renderWorkloadExperiment(bundle workload.Bundle, mode string) int {
	resolved := render.Resolve(mode)
	if resolved == "json" {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(bundle); err != nil {
			errPrint("could not render workload experiment: "+err.Error(), "", "")
			return exitError
		}
	} else if resolved != "none" {
		writeWorkloadExperimentText(bundle)
	}
	return workloadExitCode(bundle.Report.Counts)
}

func writeWorkloadExperimentText(bundle workload.Bundle) {
	report := bundle.Report
	fmt.Fprintln(os.Stdout, "VALIDATED WORK EXPERIMENT")
	fmt.Fprintf(os.Stdout, "  %-12s %s v%d\n", "workflow", terminalText(bundle.Plan.Workflow), bundle.Plan.WorkflowVersion)
	fmt.Fprintf(os.Stdout, "  %-12s %s\n", "coverage", strings.ToUpper(report.Coverage))
	fmt.Fprintf(os.Stdout, "  %-12s %d planned, %d accepted, %d rejected, %d timed out, %d infrastructure\n",
		"outcomes", report.Counts.Planned, report.Counts.Accepted, report.Counts.Rejected,
		report.Counts.TimedOut, report.Counts.InfrastructureFault)
	if report.MedianAcceptedMillis != nil {
		fmt.Fprintf(os.Stdout, "  %-12s %.3fs\n", "median", *report.MedianAcceptedMillis/1000)
	}
	if report.AcceptedOutcomesPerHour.Estimate != nil {
		fmt.Fprintf(os.Stdout, "  %-12s %.3f accepted outcomes/hour\n", "rate",
			*report.AcceptedOutcomesPerHour.Estimate)
	}
	fmt.Fprintln(os.Stdout, "\nTRIALS")
	for _, trial := range bundle.Trials {
		fmt.Fprintf(os.Stdout, "  %-3d %-20s %8.3fs  %2d turns  %2d tools  %2d authority violations\n",
			trial.Index, strings.ToUpper(string(trial.Outcome)), float64(trial.ElapsedMillis)/1000,
			trial.Turns, trial.ToolCalls, trial.AuthorityViolations)
	}
	for _, trial := range report.TrialAnalysis {
		if timing := trial.Timing; timing != nil {
			fmt.Fprintf(os.Stdout, "  timing       worker %.3fs (model %.3fs, tools %.3fs), queue %.3fs, verifier %.3fs\n",
				float64(timing.WorkerMillis)/1000, float64(timing.ModelMillis)/1000,
				float64(timing.ToolMillis)/1000, float64(timing.VerifierQueueMillis)/1000,
				float64(timing.VerifierMillis)/1000)
		}
	}
	if bundle.Plan.Contract != nil {
		fmt.Fprintln(os.Stdout, "  proof        deterministic assertion; retries not permitted; human wait and escalation unsupported")
		fmt.Fprintln(os.Stdout, "  context      requested only; effective context is not established")
	}
	if len(report.Gaps) > 0 {
		fmt.Fprintln(os.Stdout, "\nGAPS")
		for _, gap := range report.Gaps {
			fmt.Fprintln(os.Stdout, "  "+terminalText(gap))
		}
	}
}

func workloadExitCode(counts workload.OutcomeCounts) int {
	switch {
	case counts.Rejected > 0:
		return exitGates
	case counts.TimedOut > 0 || counts.InfrastructureFault > 0:
		return exitUnresolved
	default:
		return exitOK
	}
}

func parseContextPoints(value string) ([]int, error) {
	parts := strings.Split(value, ",")
	if len(parts) < 2 {
		return nil, errors.New("at least two context points are required")
	}
	if len(parts) > 16 {
		return nil, errors.New("at most 16 context points are supported")
	}
	contexts := make([]int, 0, len(parts))
	seen := make(map[int]bool, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		contextTokens, err := strconv.Atoi(part)
		if err != nil || contextTokens < 1 || contextTokens > 16*1024*1024 {
			return nil, fmt.Errorf("invalid context token count %q", part)
		}
		if seen[contextTokens] {
			return nil, fmt.Errorf("duplicate context token count %d", contextTokens)
		}
		seen[contextTokens] = true
		contexts = append(contexts, contextTokens)
	}
	return contexts, nil
}

func writeContextExperimentText(report experiment.ContextReport) {
	comparison := "UNRESOLVED"
	if report.Comparison.Ready {
		comparison = "READY"
	}
	fmt.Fprintf(os.Stdout, "CONTEXT EXPERIMENT  %s\n", strings.ToUpper(string(report.Stage)))
	fmt.Fprintf(os.Stdout, "  %-12s %s\n", "comparison", comparison)
	fmt.Fprintf(os.Stdout, "  %-12s %s\n", "treatment", terminalText(report.Comparison.Treatment))
	fmt.Fprintln(os.Stdout, "\nREQUIRED-EQUAL FACTORS")
	for _, factor := range report.Comparison.Factors {
		fmt.Fprintf(os.Stdout, "  %-12s %-24s %s\n", strings.ToUpper(string(factor.State)),
			terminalText(factor.Code), terminalText(factor.Reason))
	}
	fmt.Fprintln(os.Stdout, "\nPOINTS")
	fmt.Fprintln(os.Stdout, "  requested  effective  state        decode       prefill      request TTFT  resident")
	for _, point := range report.Points {
		fmt.Fprintf(os.Stdout, "  %-9s  %-9s  %-11s  %-11s  %-11s  %-12s  %s\n",
			formatTokenContext(point.Context.Requested), formatOptionalContext(point.Context.Effective),
			point.State, formatPerformance(point.Performance.DecodeTPS),
			formatPerformance(point.Performance.PrefillTPS),
			formatPerformance(point.Performance.TTFTSeconds), formatResident(point.Capacity.Resident))
	}
	if len(report.Gaps) > 0 {
		fmt.Fprintln(os.Stdout, "\nGAPS")
		for _, gap := range report.Gaps {
			fmt.Fprintln(os.Stdout, "  "+terminalText(gap))
		}
	}
	if report.NextAction != nil {
		fmt.Fprintln(os.Stdout, "\nNEXT")
		if len(report.NextAction.Argv) > 0 {
			arguments := make([]string, 0, len(report.NextAction.Argv))
			for _, argument := range report.NextAction.Argv {
				arguments = append(arguments, terminalText(argument))
			}
			fmt.Fprintln(os.Stdout, "  "+strings.Join(arguments, " "))
		}
		fmt.Fprintln(os.Stdout, "  "+terminalText(report.NextAction.Reason))
	}
}

func formatTokenContext(tokens int) string {
	if tokens <= 0 {
		return "n/a"
	}
	if tokens%1024 == 0 {
		return fmt.Sprintf("%dK", tokens/1024)
	}
	return strconv.Itoa(tokens)
}

func formatOptionalContext(tokens *int) string {
	if tokens == nil {
		return "unverified"
	}
	return formatTokenContext(*tokens)
}

func formatPerformance(observation analysis.PerformanceObservation) string {
	if observation.Estimate == nil {
		return "n/a"
	}
	suffix := ""
	if observation.Status == analysis.StatusDescriptiveOnly {
		suffix = " desc"
	}
	switch observation.Unit {
	case analysis.UnitTokensPerSecond:
		return fmt.Sprintf("%.2f tok/s%s", *observation.Estimate, suffix)
	case analysis.UnitSeconds:
		return fmt.Sprintf("%.3fs%s", *observation.Estimate, suffix)
	default:
		return fmt.Sprintf("%.4g %s%s", *observation.Estimate, observation.Unit, suffix)
	}
}

func formatResident(observation *analysis.ResidentObservation) string {
	if observation == nil || observation.Estimate == nil {
		return "n/a"
	}
	suffix := ""
	if observation.Status == analysis.StatusDescriptiveOnly {
		suffix = " desc"
	}
	return fmt.Sprintf("%.2f GB%s", float64(*observation.Estimate)/(1024*1024*1024), suffix)
}
