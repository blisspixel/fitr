package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/advise"
	"github.com/blisspixel/fitr/internal/buildinfo"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/lock"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/retonr"
	"github.com/blisspixel/fitr/internal/score"
	"github.com/blisspixel/fitr/internal/stats"
)

const memoryProbeCtx = 32768

// ---------------------------------------------------------------- run
func cmdRun(ctx context.Context, args []string) int {
	return cmdRunWithDisplay(ctx, args, nil)
}

// cmdRunWithDisplay keeps the measurement path shared by the line-oriented
// command and the full-screen interface. A supplied display receives the same
// phases, notices, and final scorecard as every other output mode.
func cmdRunWithDisplay(ctx context.Context, args []string, supplied render.Display) int {
	command, code, ok := parseRunCommand(args, supplied)
	if !ok {
		return code
	}
	c, code := newBackendWithDisplay(ctx, command.model, command.backend, command.pull, supplied)
	if code != exitOK {
		return code
	}
	disp := supplied
	if disp == nil {
		disp = render.New(command.mode)
	}
	defer disp.Close()

	res, err := execute(ctx, c, command.model, command.runOpts, disp)
	if err != nil {
		return reportRunFailure(ctx, supplied, disp, command.model, err)
	}
	return finishRunCommand(ctx, c, supplied, disp, command, res)
}

type runCommand struct {
	model   string
	backend string
	mode    string
	pull    bool
	html    bool
	quiet   countFlag
	runOpts
}

func parseRunCommand(args []string, supplied render.Display) (runCommand, int, bool) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	if supplied != nil {
		fs.SetOutput(io.Discard)
	} else {
		fs.SetOutput(os.Stderr)
	}
	reportError := func(message, note, hint string) { backendError(supplied, message, note, hint) }
	quick := fs.Bool("quick", false, "speed, memory, and plumbing; executable tasks default to SKIP")
	full := fs.Bool("full", false, "adds long-horizon tasks; executable tasks default to SKIP")
	checksOnly := fs.Bool("checks-only", false, "generated checks only for paired hardware calibration")
	k := fs.Int("k", 0, "repeats per noisy task")
	profileName := fs.String("profile", "", "device profile (default: auto-match)")
	mode := fs.String("display", "auto", "auto|rich|plain|json|none")
	backend := fs.String("backend", "auto", "auto|ollama|llama-server|openai")
	seedset := fs.String("seedset", "", "pin the generated-instance seed set; two runs sharing "+
		"a seedset face IDENTICAL task instances, enabling a paired comparison")
	pullFlag := fs.Bool("pull", false, "pull the model first if it is not installed "+
		"(Ollama; supports hf.co/... and pasted Hugging Face URLs)")
	htmlFlag := fs.Bool("html", false, "write a self-contained HTML artifact next to the JSON")
	ctxSize := fs.Int("ctx", 0, "request context (default 8192). Apply an advise num_ctx remedy here.")
	allowUnsafeExec := fs.Bool("allow-unsafe-exec", false,
		"run unisolated built-in executable diagnostics; observations remain INCONCLUSIVE")
	var quiet countFlag
	fs.Var(&quiet, "q", "quiet level")
	verbose := fs.Bool("v", false, "verbose")
	if err := fs.Parse(permute(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return runCommand{}, exitOK, false
		}
		if supplied != nil {
			reportError("invalid run arguments", err.Error(), "fitr top run <model> [run flags]")
		}
		return runCommand{}, exitUsage, false
	}
	if !render.ValidMode(*mode) {
		reportError("invalid display mode", *mode, "use auto, rich, plain, json, or none")
		return runCommand{}, exitUsage, false
	}
	if fs.NArg() < 1 {
		reportError("missing model", "", "fitr run <model>")
		return runCommand{}, exitUsage, false
	}
	if fs.NArg() > 1 {
		reportError("too many arguments", "run accepts exactly one model", "fitr run <model> [flags]")
		return runCommand{}, exitUsage, false
	}
	if *ctxSize < 0 {
		reportError("invalid context size", "--ctx cannot be negative", "omit --ctx for the default, or pass a positive token count")
		return runCommand{}, exitUsage, false
	}
	if selectedRunLevels(*quick, *full, *checksOnly) > 1 {
		reportError("choose one run level", "--quick, --full, and --checks-only are mutually exclusive", "")
		return runCommand{}, exitUsage, false
	}
	if *checksOnly && *seedset == "" {
		reportError("--checks-only requires --seedset", "calibration pairs must face identical generated instances", "")
		return runCommand{}, exitUsage, false
	}
	if *checksOnly && *htmlFlag {
		reportError("--checks-only cannot write a scorecard HTML", "calibration is paired evidence, not a standalone product verdict", "use `fitr calibrate a b --out pair.json` after both runs")
		return runCommand{}, exitUsage, false
	}
	if *checksOnly && *allowUnsafeExec {
		reportError("--checks-only cannot enable executable diagnostics",
			"the calibration battery is declarative and does not execute generated code", "remove --allow-unsafe-exec")
		return runCommand{}, exitUsage, false
	}
	level := selectedRunLevel(*quick, *full, *checksOnly)
	reps := runRepeats(level, *k)
	if reps < 1 {
		reportError("invalid repeat count", "-k must be at least 1", "")
		return runCommand{}, exitUsage, false
	}
	if quiet > 1 {
		*mode = "none"
	} else if (quiet > 0 || *verbose) && *mode == "auto" {
		*mode = "plain"
	}
	return runCommand{
		model: normalizeModelRef(fs.Arg(0)), backend: *backend, mode: *mode,
		pull: *pullFlag, html: *htmlFlag, quiet: quiet,
		runOpts: runOpts{
			level: level, profile: *profileName, seedSet: *seedset,
			reps: reps, checksReps: generatedCheckRepeats(level, *k, reps),
			numCtx: *ctxSize, allowUnsafeExec: *allowUnsafeExec,
		},
	}, exitOK, true
}

func selectedRunLevels(quick, full, checks bool) int {
	selected := 0
	for _, enabled := range []bool{quick, full, checks} {
		if enabled {
			selected++
		}
	}
	return selected
}

func selectedRunLevel(quick, full, checks bool) string {
	switch {
	case quick:
		return "quick"
	case full:
		return "full"
	case checks:
		return "checks"
	default:
		return "default"
	}
}

func runRepeats(level string, requested int) int {
	if requested != 0 {
		return requested
	}
	switch level {
	case "quick":
		return 1
	case "checks":
		return 5
	default:
		return 3
	}
}

// Check tasks generate a fresh instance per repeat. Outcomes within one
// family can remain correlated, so scoring clusters them by family. Repeats
// multiply wall-clock across all 22 specs and follow -k only when asked.
func generatedCheckRepeats(level string, requested, repeats int) int {
	if requested > 0 {
		return requested
	}
	if level == "checks" {
		return repeats
	}
	return 1
}

func reportRunFailure(ctx context.Context, supplied render.Display, disp render.Display,
	model string, err error) int {
	if failed, ok := disp.(runFailureTelemetry); ok && ctx.Err() == nil {
		failed.RunFailed(err)
	}
	if ctx.Err() != nil {
		if supplied == nil {
			fmt.Fprintln(os.Stderr, "\ninterrupted")
		}
		return exitInterrupt
	}
	if supplied == nil {
		errPrint(err.Error(), "", runFailureHint(err, model))
	}
	return exitError
}

func finishRunCommand(ctx context.Context, c llm.Backend, supplied render.Display,
	disp render.Display, command runCommand, res *Result) int {
	path, err := save(res)
	saveErr := err
	if status, ok := disp.(runSaveTelemetry); ok {
		status.RunSaveStatus(err == nil, err)
	}
	if err != nil {
		if supplied == nil {
			errPrint("could not save result: "+err.Error(), "", "")
		}
	}
	meta := resultMeta(res, res.Profile)
	meta.SavedPath = path

	htmlDest := ""
	if command.html {
		htmlDest = "auto"
	}
	htmlFile, htmlErr := writeHTMLArtifact(res, htmlDest, path)
	if htmlErr != nil {
		if supplied != nil {
			disp.Note("could not write HTML: "+htmlErr.Error(), "warn")
		}
	}
	disp.Result(res.Scorecard, meta)
	if htmlErr != nil && supplied == nil {
		errPrint("could not write HTML: "+htmlErr.Error(), "", "")
	}
	if supplied == nil && command.quiet == 0 && render.Resolve(command.mode) != "json" {
		writeRunNextSteps(ctx, c, command, res, path, htmlFile)
	}
	return runResultExitCode(res, command.level, saveErr, htmlErr)
}

func writeRunNextSteps(ctx context.Context, c llm.Backend, command runCommand,
	res *Result, path, htmlFile string) {
	if path != "" {
		fmt.Fprintf(os.Stderr, "\n  saved  %s\n", terminalText(path))
	}
	if command.level == "checks" {
		fmt.Fprintf(os.Stderr, "  next   run the paired model with --checks-only --seedset %s -k %d\n",
			terminalText(command.seedSet), command.checksReps)
		fmt.Fprintln(os.Stderr, "         fitr calibrate <reference> <candidate> --out pair.json")
		return
	}
	if htmlFile != "" {
		fmt.Fprintf(os.Stderr, "  html   %s\n", terminalText(htmlFile))
	}
	fmt.Fprintln(os.Stderr, "  next   fitr board")
	writeRunContextHint(ctx, c, command, res)
	if !command.html {
		fmt.Fprintf(os.Stderr, "         fitr export %s   for a shareable HTML scorecard\n", terminalText(res.Model))
	}
	if req := eval.ResolvedCtx(command.numCtx); req != eval.NumCtx {
		fmt.Fprintf(os.Stderr, "         fitr apply %s   to persist num_ctx=%d on the server\n", terminalText(res.Model), req)
	}
	if hint := retonr.Hint(res.Model); hint != "" {
		fmt.Fprintf(os.Stderr, "         %s\n", terminalText(hint))
	}
	if command.reps < 3 {
		fmt.Fprintf(os.Stderr, "         fitr run %s -k 3   for a rankable result\n", terminalText(res.Model))
	}
}

func writeRunContextHint(ctx context.Context, c llm.Backend, command runCommand, res *Result) {
	requested := eval.ResolvedCtx(command.numCtx)
	fit := largerFittingContext(ctx, c, res.Model, res.Device, requested)
	if fit <= 0 {
		return
	}
	fmt.Fprintf(os.Stderr,
		"         fitr run %s --ctx %d   this device fits a %d-token window; measured at %d\n",
		terminalText(res.Model), fit, fit, requested)
}

func execute(ctx context.Context, c llm.Backend, model string, opts runOpts,
	disp render.Display) (*Result, error) {
	// Exactly one eval per machine at a time. Two runs against one inference
	// server contaminate each other, and the damage is not recoverable after
	// the fact -- the numbers still look plausible.
	lk, err := lock.Acquire("eval", "eval of "+model)
	if err != nil {
		return nil, err
	}
	defer lk.Release() //nolint:errcheck // cleanup failure is not worth failing a run over

	run := &runExecution{ctx: ctx, backend: c, model: model, opts: opts, display: disp}
	if err := run.prepare(); err != nil {
		return nil, err
	}
	defer func() { _ = run.stopAll() }()
	if err := run.verifyContextAndSeal(); err != nil {
		return nil, err
	}
	work, err := os.MkdirTemp("", "evalkit_")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	started := time.Now()
	if err := run.measureBattery(work); err != nil {
		return nil, err
	}
	return run.complete(started)
}

type runExecution struct {
	ctx            context.Context
	backend        llm.Backend
	model          string
	opts           runOpts
	display        render.Display
	resolved       resolvedRunModel
	spec           *eval.Spec
	profile        device.Profile
	provenance     record.RunProvenance
	unsafeExecutor *eval.ExecutorReceipt
	result         *Result
	completed      []string
}

func (run *runExecution) prepare() error {
	if err := run.resolveIdentity(); err != nil {
		return err
	}
	if err := run.loadDefinition(); err != nil {
		return err
	}
	return run.initializeResult()
}

func (run *runExecution) resolveIdentity() error {
	// Identity is the first evidence gate. Resolve it before loading optional
	// tasks or printing execution-policy notes so an unverifiable endpoint
	// fails with one focused, actionable diagnostic.
	run.display.Phase("identity", "resolve the served artifact and seal its identity")
	identityStart := time.Now()
	resolved, err := resolveRunModel(run.ctx, run.backend, run.model)
	if err != nil {
		return err
	}
	run.display.Done("identity", time.Since(identityStart).Seconds())
	if resolved.Name != run.model {
		run.display.Note(fmt.Sprintf("runtime resolved %q as %q; the result uses the resolved identity",
			run.model, resolved.Name), "warn")
	}
	run.model = resolved.Name
	run.resolved = resolved
	return nil
}

func (run *runExecution) loadDefinition() error {
	spec, err := eval.LoadSpec()
	if err != nil {
		return err
	}
	run.spec = spec
	if run.opts.allowUnsafeExec && run.opts.level != "checks" {
		executor, err := eval.PreflightUnsafeExecutor(spec)
		if err != nil {
			return err
		}
		run.unsafeExecutor = &executor
		run.ctx = eval.WithUnsafeExecutor(run.ctx, executor)
		run.display.Note("unsafe executable diagnostics enabled: generated Python code is not sandboxed; "+
			"observations are INCONCLUSIVE and excluded from PASS/FAIL scoring", "warn")
	} else if run.opts.level != "checks" {
		run.display.Note("generated-code execution is disabled by default; coding and executable agent tasks are SKIP", "")
	}
	return run.loadUserChecks()
}

func (run *runExecution) loadUserChecks() error {
	// User tasks extend the battery without a fork. A malformed one is a hard
	// error with the filename in it - silently dropping your own task would
	// defeat the point of having one.
	if run.opts.level == "checks" {
		return nil
	}
	userChecks, err := eval.LoadUserChecks(eval.UserTasksDir())
	if err != nil {
		return err
	}
	if len(userChecks) == 0 {
		return nil
	}
	run.spec.Checks, err = eval.MergeChecks(run.spec.Checks, userChecks)
	if err != nil {
		return err
	}
	run.display.Note(fmt.Sprintf("%d user task(s) loaded from %s", len(userChecks), eval.UserTasksDir()), "")
	return nil
}

func (run *runExecution) initializeResult() error {
	effectiveHashes, err := eval.EffectiveHashes(run.spec)
	if err != nil {
		return err
	}
	fp := device.Detect(run.ctx, run.backend)
	run.profile, err = device.SelectProfile(run.opts.profile, fp)
	if err != nil {
		return err
	}
	softwareBuild, err := buildinfo.BinarySHA256()
	if err != nil {
		return fmt.Errorf("identify fitr executable: %w", err)
	}
	run.provenance, err = record.NewRunProvenance(effectiveHashes.TaskSetSHA256,
		effectiveHashes.SpecSHA256, run.profile, record.CurrentScoringPolicy(), record.SoftwareReceipt{
			FitrVersion: version, SoftwareBuildSHA256: softwareBuild,
			BackendProtocol: record.BackendProtocol(run.backend.Name()),
		})
	if err != nil {
		return fmt.Errorf("build run provenance: %w", err)
	}
	reqCtx := eval.ResolvedCtx(run.opts.numCtx)
	run.ctx = eval.WithNumCtx(run.ctx, reqCtx)
	run.result = &Result{
		SchemaVersion: run.spec.Version.ResultSchemaVersion,
		Model:         run.model,
		StartedAt:     time.Now().Format(time.RFC3339),
		Level:         run.opts.level, Repeats: run.opts.reps, NumCtx: reqCtx,
		Device: fp, DeviceKey: fp.Key(), Profile: run.profile.Name, ModelMeta: run.resolved.Info,
		ExecutionPolicy: record.ExecutionDisabled,
		TaskPlan: runTaskPlan(run.opts.level, run.opts.reps, run.opts.checksReps,
			len(run.spec.Checks), len(run.spec.Refusal.Prompts)),
	}
	if run.opts.allowUnsafeExec {
		run.result.ExecutionPolicy = record.ExecutionUnsafe
	}
	if identified, ok := run.display.(runIdentityTelemetry); ok {
		run.result.RunID = identified.RunID()
	}
	if suf := eval.CtxKeySuffix(reqCtx); suf != "" {
		run.result.DeviceKey += suf
		run.display.Note(fmt.Sprintf("num_ctx=%d (advise remedy applied; default is %d)", reqCtx, eval.NumCtx), "")
	}
	run.initializeSeedSet()
	return run.sealTaskPlans()
}

func (run *runExecution) initializeSeedSet() {
	// Fresh instances every run by default; a pinned seedset trades that
	// contamination resistance for pairing power, and says so.
	run.result.SeedSet = run.opts.seedSet
	if run.result.SeedSet == "" {
		run.result.SeedSet = run.result.StartedAt
		return
	}
	run.display.Note("seedset "+run.opts.seedSet+" pinned - instances repeat across runs that share it; "+
		"use a fresh one when you are not pairing runs", "")
}

func (run *runExecution) sealTaskPlans() error {
	var err error
	if run.result.TaskPlan.CheckTrialsLimit > 0 {
		run.result.TaskPlan.CheckPlanSHA256, err = record.FixedCheckPlanSHA256(
			run.spec.Checks, run.opts.checksReps, run.result.SeedSet)
		if err != nil {
			return fmt.Errorf("seal generated-check plan: %w", err)
		}
	}
	if run.result.TaskPlan.RefusalTrials > 0 {
		run.result.TaskPlan.RefusalPlanSHA256, err = record.FixedRefusalPlanSHA256(
			eval.RefusalPromptIDs(run.spec.Refusal))
		if err != nil {
			return fmt.Errorf("seal refusal plan: %w", err)
		}
	}
	return nil
}

func (run *runExecution) stopAll() error {
	// One model resident at a time is non-negotiable between phases. A model
	// that will not unload is recorded and warned about. The same owner also
	// establishes the clean state needed by the context-allocation preflight.
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(run.ctx), 5*time.Second)
	defer cleanupCancel()
	left, err := run.backend.StopAll(cleanupCtx)
	if err != nil {
		run.display.Note("could not confirm models unloaded: "+err.Error(), "warn")
		return err
	}
	if len(left) == 0 {
		return nil
	}
	run.result.Contamination = appendUnique(run.result.Contamination, left...)
	run.display.Note("still resident after unload: "+strings.Join(left, ", ")+
		" - timings in this run may be contaminated", "warn")
	return nil
}

func (run *runExecution) verifyContextAndSeal() error {
	if err := run.stopAll(); err != nil {
		return fmt.Errorf("establish clean runtime state: %w", err)
	}
	receipt, err := run.observeContext()
	if err != nil {
		return err
	}
	if err := run.sealFingerprint(receipt); err != nil {
		return err
	}
	run.writeConfigurationNotes()
	return nil
}

func (run *runExecution) observeContext() (device.ContextVerification, error) {
	receipt := device.ContextVerification{RequestedTokens: run.result.NumCtx}
	observer, ok := run.backend.(llm.EffectiveContextObserver)
	if !ok {
		run.observeInferenceDevice()
		return receipt, nil
	}
	if run.backend.Name() == "ollama" {
		probe, err := run.preloadContextProbe()
		if err != nil {
			return receipt, err
		}
		receipt.Probe = probe
	}
	run.observeInferenceDevice()
	effective, observed, err := observer.EffectiveContext(run.ctx, run.model)
	if err != nil {
		return receipt, fmt.Errorf("verify effective context: %w", err)
	}
	if observed {
		receipt.EffectiveTokens = &effective
		receipt.EffectiveSource = device.ContextSourceRuntimeReport
	}
	if run.backend.Name() == "ollama" {
		if err := run.stopAll(); err != nil {
			return receipt, fmt.Errorf("reset after context verification: %w", err)
		}
	}
	return receipt, nil
}

func (run *runExecution) preloadContextProbe() (*device.ContextProbe, error) {
	_, metrics, err := run.backend.Generate(run.ctx, run.model, "Reply with OK.",
		ollama.Deterministic(1, run.result.NumCtx))
	if err != nil {
		return nil, fmt.Errorf("preload model for context verification: %w", err)
	}
	if metrics.PromptTokens+metrics.CachedTokens <= 0 {
		return nil, nil
	}
	return &device.ContextProbe{
		PromptTokens: metrics.PromptTokens, CachedTokens: metrics.CachedTokens,
		MinimumExpectedTokens: 1, Source: device.ContextSourceGeneration,
	}, nil
}

func (run *runExecution) observeInferenceDevice() {
	run.result.Device.InferenceDevice = device.InferenceDeviceFor(run.ctx, run.backend, run.model)
}

func (run *runExecution) sealFingerprint(receipt device.ContextVerification) error {
	fingerprintV2, err := device.NewFingerprintV2(run.result.Device, receipt)
	if err != nil {
		// This is reached when a device probe came back empty, which on a
		// loaded machine means a probe was too slow rather than a device being
		// absent. Say which, because the bare validation message reads like a
		// broken machine and the remedy is simply to try again.
		return fmt.Errorf("build device fingerprint v2: %w "+
			"(a device probe returned nothing; on a busy machine this is usually a slow probe, "+
			"so re-run, and check `fitr device` if it repeats)", err)
	}
	run.result.DeviceV2 = &fingerprintV2
	if comparableKey, err := fingerprintV2.ComparabilityKey(); err == nil {
		run.result.DeviceKey = comparableKey
	} else {
		run.display.Note("effective context is unverified; this run remains visible but is excluded from ranking and comparison", "warn")
	}
	if receipt.State() == device.ContextAdjusted {
		run.display.Note(fmt.Sprintf("runtime allocated %d context tokens for the %d-token request; comparison uses the effective value",
			*receipt.EffectiveTokens, run.result.NumCtx), "warn")
	}
	if run.unsafeExecutor != nil {
		err = run.result.AttachManifestWithExecutor(run.resolved.Identity, *run.unsafeExecutor, run.provenance)
	} else {
		err = run.result.AttachManifest(run.resolved.Identity, run.provenance)
	}
	if err != nil {
		return fmt.Errorf("seal run manifest: %w", err)
	}
	return nil
}

func (run *runExecution) writeConfigurationNotes() {
	// Device identity is sealed into this run's fingerprint and its
	// comparability key. A fingerprint assembled from sources that disagree
	// about the machine produces no error on its own, so say it here, before
	// the measurement is attributed to a device that may not exist.
	for _, conflict := range run.result.Device.IdentityConflicts() {
		run.display.Note("device identity: "+conflict, "warn")
	}
	// Placement is part of the comparability key, so a partial or CPU-only run
	// cannot be ranked against a fully accelerator-resident one. Say what the
	// runtime reported without turning placement into an unsupported causal
	// diagnosis.
	if note := placementWarning(run.result.Device.InferenceDevice); note != "" {
		run.display.Note(note, "warn")
	}
	if run.profile.Name == "default" {
		run.display.Note("using the UNCALIBRATED default profile - verdicts are rough; "+
			"copy profiles/default.json and tune it for this box", "warn")
	}
	info := run.resolved.Info
	if device.DenseSizeHintExceeded(info.Details.ParameterSize, info.Details.Family, run.profile) {
		run.display.Note("dense "+info.Details.ParameterSize+" model exceeds this profile's interactive-size hint; "+
			"measure decode before deciding whether it is suitable", "warn")
	}
}

func (run *runExecution) step(name, detail string, fn func() error) error {
	// A run is single-flight and fail-closed: a step that errors abandons the
	// whole battery rather than saving what came before it. That is the right
	// default -- a transport fault means the measurement environment stopped
	// behaving, and the steps that already ran were measured under conditions
	// that no longer hold, so keeping them would be keeping evidence collected
	// on a machine that changed underneath.
	//
	// What was wrong was doing it silently. A late failure could discard a
	// minute of completed work with nothing said about what was lost, so the
	// operator could not tell a broken model from a broken box. The cost is
	// now stated.
	run.display.Phase(name, detail)
	started := time.Now()
	if err := fn(); err != nil {
		run.display.Note(fmt.Sprintf(
			"%s failed, so this run is abandoned and nothing is saved; %s. "+
				"Measurements taken before a fault are not kept, because the conditions "+
				"they were taken under no longer hold", name, abandonedStepSummary(run.completed)), "warn")
		return fmt.Errorf("%s: %w", name, err)
	}
	run.completed = append(run.completed, name)
	run.display.Done(name, time.Since(started).Seconds())
	return nil
}

func (run *runExecution) standardStep(name, detail string, fn func() error) error {
	if run.opts.level == "checks" {
		return nil
	}
	return run.step(name, detail, fn)
}

func (run *runExecution) measureBattery(work string) error {
	if err := run.stopAll(); err != nil {
		return fmt.Errorf("establish clean runtime state: %w", err)
	}
	if err := run.measureStandardPhases(work); err != nil {
		return err
	}
	if err := run.measureChecks(); err != nil {
		return err
	}
	toolReady, err := measureToolPhases(run.ctx, run.backend, run.model, run.spec,
		run.result, run.opts, work, run.display, run.step, run.standardStep)
	if err != nil {
		return err
	}
	if err := run.measureRefusal(); err != nil {
		return err
	}
	return run.measureAgentic(work, toolReady)
}

func (run *runExecution) measureStandardPhases(work string) error {
	if err := run.standardStep("speed", fmt.Sprintf("x%d", run.opts.reps), func() error {
		return measureSpeed(run.ctx, run.backend, run.model, run.spec, run.result, run.opts.reps, run.display)
	}); err != nil {
		return err
	}
	if err := run.standardStep("memory", "resident @32K", func() error {
		return measureMemory(run.ctx, run.backend, run.model, run.result, run.display)
	}); err != nil {
		return err
	}
	return run.standardStep("coding", fmt.Sprintf("x%d", run.opts.reps), func() error {
		return measureCoding(run.ctx, run.backend, run.model, run.spec,
			run.result, run.opts.reps, work, run.display)
	})
}

func (run *runExecution) measureChecks() error {
	if run.opts.level == "quick" || len(run.spec.Checks) == 0 {
		return nil
	}
	return measureGeneratedChecks(run.ctx, run.backend, run.model, run.spec, run.result,
		run.opts.checksReps, run.display, run.step)
}

func (run *runExecution) measureRefusal() error {
	if run.opts.level == "quick" || run.opts.level == "checks" {
		return nil
	}
	return run.step("refusal", "3 prompts", func() error {
		refusal, count, err := eval.RunRefusal(run.ctx, run.backend, run.model, run.spec.Refusal)
		run.result.Refusal, run.result.Refused = refusal, count
		return err
	})
}

func (run *runExecution) measureAgentic(work string, toolReady bool) error {
	if run.opts.level != "full" {
		return nil
	}
	if !toolReady {
		run.result.Agentic = &eval.ToolLoopResult{
			Outcome: eval.OutcomeSkipped, Ended: "plumbing_unavailable",
			Detail: "tool plumbing did not establish a usable protocol",
		}
		return nil
	}
	return run.step("agentic", fmt.Sprintf("up to %d unsupervised turns", run.spec.Agentic.MaxTurns), func() error {
		if err := run.stopAll(); err != nil {
			return fmt.Errorf("establish clean agentic runtime state: %w", err)
		}
		agent, err := eval.RunToolLoop(run.ctx, run.backend, run.model,
			run.spec.Agentic, filepath.Join(work, "ag"))
		if err != nil {
			return err
		}
		run.result.Agentic = &agent
		return nil
	})
}

func (run *runExecution) complete(started time.Time) (*Result, error) {
	// Inference placement is observed once, above, while the model is resident
	// for context verification, and is then sealed into DeviceV2 and the
	// comparability key. Re-probing here overwrote that sealed value from a
	// different residency state: the battery has finished and the model may be
	// unloaded, so the two observations disagree, res.Device and
	// DeviceV2.Device diverge, and the manifest check rejects the save --
	// discarding a completed measurement. A later reading taken under
	// different conditions does not correct the sealed one.

	// Degeneracy over the longest text this model produced.
	longest := longestRunOutput(run.result)
	run.result.Rep = score.RepetitionMetrics(longest)
	// Truncation is judged on free-running tasks only; the speed probe is
	// capped by design and would always look truncated.
	run.result.Density = score.InformationDensity(longest)
	run.result.WallSeconds = float64(int(time.Since(started).Seconds()*10)) / 10
	// The deferred cleanup still protects every early return. This final check
	// happens before scoring so a model that refuses the last unload cannot
	// leave a persisted PASS or FAIL claim behind.
	if err := run.stopAll(); err != nil {
		return nil, fmt.Errorf("verify final runtime state: %w", err)
	}
	counts, err := run.result.DeriveEvidenceCounts()
	if err != nil {
		return nil, fmt.Errorf("derive evidence denominators: %w", err)
	}
	run.result.EvidenceCounts = counts
	for phase, phaseCounts := range counts {
		if !phaseCounts.Complete() {
			return nil, fmt.Errorf("%s evidence did not complete its immutable denominator: %+v", phase, phaseCounts)
		}
	}
	run.result.Scorecard = score.Score(measure(run.result), run.profile)
	if err := run.result.CompleteEvidence(run.profile); err != nil {
		return nil, fmt.Errorf("seal completed evidence: %w", err)
	}
	return run.result, nil
}

func longestRunOutput(result *Result) string {
	longest := ""
	for _, execution := range append(append([]eval.ExecResult{}, result.CodeWrite...), result.CodeFix...) {
		if len(execution.Raw) > len(longest) {
			longest = execution.Raw
		}
	}
	for _, verdict := range result.Refusal {
		if len(verdict.Text) > len(longest) {
			longest = verdict.Text
		}
	}
	return longest
}

// measure folds raw results into what the scorer needs.
func measure(r *Result) score.Measured {
	return r.Measured()
}

func observeServingCtx(ctx context.Context, model, kind string) (int, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return 0, false
	}
	found, _ := llm.Discover(ctx)
	var b llm.Backend
	switch {
	case kind != "":
		url := ""
		for _, f := range found {
			if f.Kind == kind {
				url = f.URL
				break
			}
		}
		var err error
		b, err = backendAt(kind, url)
		if err != nil || b == nil {
			return 0, false
		}
	case len(found) > 0:
		var err error
		b, err = backendAt(found[0].Kind, found[0].URL)
		if err != nil || b == nil {
			return 0, false
		}
	default:
		return 0, false
	}
	listCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	running, err := b.PS(listCtx)
	if err != nil {
		return 0, false
	}
	return servingCtxFromRunning(running, model)
}

// largerFittingContext reports a context window this device can hold that is
// bigger than the one just measured, or 0 when there is nothing better to
// offer. Returning 0 on any uncertainty is deliberate: a suggestion that does
// not actually fit is worse than no suggestion.
//
// `fitr run` defaults to a fixed 8192 so that two models measured with
// defaults land in the same comparability block and `board` can rank them.
// That default is right, and it is also frequently far below what the card can
// do: a 30B MoE on 24 GB fits 73216. advise knows that number and run was
// throwing it away, so anyone who skipped advise measured a narrow window and
// was never told a wider one was available.
func largerFittingContext(ctx context.Context, c llm.Backend, model string, fp device.Fingerprint, used int) int {
	if c == nil || used <= 0 || fp.VRAMGb <= 0 {
		return 0
	}
	info, err := c.Show(ctx, model)
	if err != nil || len(info.Info) == 0 {
		return 0
	}
	in := advise.Input{
		Model: model, Backend: c.Name(),
		HaveGB: fp.VRAMGb, HaveSrc: fp.VRAMSource,
		Arch: advise.ArchFromKVs(info.Info), WeightsB: info.Size,
	}
	if in.WeightsB == 0 {
		in.WeightsB = weightsFromTags(ctx, c, model)
	}
	if kv := fp.Config["OLLAMA_KV_CACHE_TYPE"]; kv != "" {
		if n, ok := advise.KVElemBytes(kv); ok {
			in.KVBytes, in.KVSrc = n, "OLLAMA_KV_CACHE_TYPE="+kv
		}
	}
	fits := 0
	switch r := advise.Evaluate(in); r.Tier {
	case advise.Compatible:
		// The architecture's whole window fits on this card.
		fits = r.Ctx
	case advise.LowMemory:
		// The largest window that fits, which advise already solved for.
		if r.Flag == advise.ContextFlag(c.Name()) {
			fits = r.FlagValue
		}
	}
	if fits <= used {
		return 0
	}
	return fits
}

// abandonedStepSummary describes what a fail-closed run threw away, so the
// operator can weigh the cost instead of guessing at it.
func abandonedStepSummary(completed []string) string {
	if len(completed) == 0 {
		return "no step had completed yet"
	}
	return "already completed and now discarded: " + strings.Join(completed, ", ")
}

// placementWarning reports a non-resident placement without guessing why it
// changes performance. It mirrors doctor so the two surfaces cannot disagree
// about the same runtime receipt. An empty string means placement is fully
// offloaded or was not observed.
func placementWarning(placement string) string {
	switch {
	case placement == "" || placement == "GPU 100%" || placement == "unknown":
		return ""
	case placement == "CPU":
		return "runtime reported CPU-only placement with no offload; compare only with the same placement, or select the intended accelerator placement and re-run"
	case strings.HasPrefix(placement, "GPU "):
		return "runtime reported partial offload (" + placement + "); compare only with " +
			"the same placement, or free accelerator memory and re-run if full placement is the goal"
	}
	return ""
}

// measureSpeed runs the throughput probe and summarises it. Extracted from the
// battery so the phase can be exercised on its own: at 570 lines the enclosing
// function could only be tested end to end.
func measureSpeed(ctx context.Context, c llm.Backend, model string, spec *eval.Spec,
	res *Result, reps int, disp render.Display) error {
	for i := range reps {
		// The nonce MUST vary: identical long prompts hit the prefix cache
		// and prefill becomes fiction.
		nonce := fmt.Sprintf("%s-%d", res.StartedAt, i)
		s, err := eval.RunSpeed(ctx, c, model, spec, nonce)
		if err != nil {
			return err
		}
		res.Speed = append(res.Speed, s)
		if live, ok := disp.(liveTelemetry); ok {
			live.LiveSpeed(s, i+1, reps)
		}
	}
	var dec, ttft, pre []float64
	cached := 0
	for _, s := range res.Speed {
		dec = append(dec, s.DecodeTPS)
		ttft = append(ttft, s.TTFT)
		pre = append(pre, s.PrefillTPS)
		cached += s.CachedPromptTok
	}
	res.DecodeSum, res.TTFTSum, res.PrefillSum = stats.MeanSD(dec), stats.MeanSD(ttft), stats.MeanSD(pre)
	if cached > 0 {
		disp.Note(fmt.Sprintf("prefill probe hit the prompt cache (%d tokens) despite the "+
			"nonce - the prefill figure is partly fiction and should not be trusted", cached), "warn")
	}
	// Outlier census, hyperfine-style: flag, explain the likely cause,
	// name the fix. Needs n>=5; below that the estimator is degenerate
	// and stays silent rather than guessing.
	for i, isOut := range stats.ModifiedZOutliers(dec) {
		if isOut {
			disp.Note(fmt.Sprintf("decode run %d (%.2f tok/s) is a statistical outlier vs "+
				"median %.2f - another process likely interfered; the summary includes it, "+
				"a re-run on a quiet system would not", i+1, dec[i], stats.Median(dec)), "warn")
		}
	}
	return nil
}

// measureMemory records the resident footprint at a fixed 32K window.
func measureMemory(ctx context.Context, c llm.Backend, model string,
	res *Result, disp render.Display) error {
	m, err := eval.RunMemory(ctx, c, model, memoryProbeCtx)
	res.Memory = m
	if live, ok := disp.(liveTelemetry); ok && m.ResidentGB > 0 {
		live.LiveMemory(m.ResidentGB)
	}
	return err
}

// measureCoding generates and grades the code-write and code-fix tasks.
func measureCoding(ctx context.Context, c llm.Backend, model string, spec *eval.Spec,
	res *Result, reps int, work string, disp render.Display) error {
	for i := range reps {
		w, err := eval.RunExec(ctx, c, model, spec.CodeWrite, filepath.Join(work, fmt.Sprintf("cw%d", i)))
		if err != nil {
			return err
		}
		res.CodeWrite = append(res.CodeWrite, w)
		f, err := eval.RunExec(ctx, c, model, spec.CodeFix, filepath.Join(work, fmt.Sprintf("cf%d", i)))
		if err != nil {
			return err
		}
		res.CodeFix = append(res.CodeFix, f)
		if live, ok := disp.(liveTelemetry); ok {
			live.LiveProgress(i+1, reps, fmt.Sprintf("repeat %d of %d", i+1, reps))
		}
	}
	return nil
}

// measureToolPhases runs the tool plumbing probe and, only if it established a
// usable protocol, the tool loop and the mid-loop withdrawal. It reports
// whether tools were usable, because later phases need the same answer.
//
// A capability number measured through broken plumbing is not a capability
// number, so a plumbing failure records SKIP for the dependent phases rather
// than a zero score.
func measureToolPhases(ctx context.Context, c llm.Backend, model string, spec *eval.Spec,
	res *Result, opts runOpts, work string, disp render.Display,
	step, standardStep func(string, string, func() error) error) (bool, error) {
	level, reps := opts.level, opts.reps
	if err := measurePlumbingPhase(ctx, c, model, spec, res, standardStep); err != nil {
		return false, err
	}

	toolReady := res.Plumbing != nil && res.Plumbing.Outcome == eval.OutcomePass && res.Plumbing.Healthy
	if !toolReady && level != "checks" {
		markToolCapabilitySkipped(res, reps, disp)
	}
	if toolReady {
		if err := measureToolCapability(ctx, c, model, spec, res, reps, work, standardStep); err != nil {
			return toolReady, err
		}
	}

	if err := measureWithdrawalPhase(ctx, c, model, spec, res, level, toolReady, work, disp, step); err != nil {
		return toolReady, err
	}
	return toolReady, nil
}

// measurePlumbingPhase runs before every dependent capability phase. A tool
// result without a working protocol is not capability evidence.
func measurePlumbingPhase(ctx context.Context, c llm.Backend, model string, spec *eval.Spec,
	res *Result, standardStep func(string, string, func() error) error) error {
	return standardStep("plumbing", "tool round-trip", func() error {
		p, err := eval.RunPlumbing(ctx, c, model, spec.Plumbing)
		if err != nil {
			return err
		}
		res.Plumbing = &p
		return nil
	})
}

func markToolCapabilitySkipped(res *Result, reps int, disp render.Display) {
	reason := "tool capability not measured because plumbing is unavailable"
	if res.Plumbing != nil && res.Plumbing.Verdict != "" {
		reason = res.Plumbing.Verdict
	}
	disp.Note("tool and agent tasks skipped: "+reason, "warn")
	for range reps {
		res.Tools = append(res.Tools, eval.ToolLoopResult{
			Outcome: eval.OutcomeSkipped, Ended: "plumbing_unavailable", Detail: reason,
		})
	}
}

func measureToolCapability(ctx context.Context, c llm.Backend, model string, spec *eval.Spec,
	res *Result, reps int, work string,
	standardStep func(string, string, func() error) error) error {
	return standardStep("tools", fmt.Sprintf("x%d", reps), func() error {
		for i := range reps {
			t, err := eval.RunToolLoop(ctx, c, model, spec.Tools, filepath.Join(work, fmt.Sprintf("tl%d", i)))
			if err != nil {
				return err
			}
			res.Tools = append(res.Tools, t)
		}
		return nil
	})
}

func measureWithdrawalPhase(ctx context.Context, c llm.Backend, model string, spec *eval.Spec,
	res *Result, level string, toolReady bool, work string, disp render.Display,
	step func(string, string, func() error) error) error {
	if level == "quick" || level == "checks" {
		return nil
	}
	if !toolReady {
		res.Withdrawal = &eval.ToolLoopResult{
			Outcome: eval.OutcomeSkipped, Ended: "plumbing_unavailable",
			Detail: "tool plumbing did not establish a usable protocol",
		}
		return nil
	}
	return step("withdrawal", "a tool vanishes mid-loop", func() error {
		return runWithdrawalLoop(ctx, c, model, spec, res, work, disp)
	})
}

func runWithdrawalLoop(ctx context.Context, c llm.Backend, model string, spec *eval.Spec,
	res *Result, work string, disp render.Display) error {
	w, err := eval.RunToolLoop(ctx, c, model, spec.Withdrawal, filepath.Join(work, "wd"))
	if err == nil {
		res.Withdrawal = &w
		return nil
	}
	// The loop is self-contained, so a transport fault makes only this task
	// undecidable. Cancellation still aborts the complete run.
	if ctx.Err() != nil {
		return err
	}
	res.Withdrawal = &eval.ToolLoopResult{
		Outcome: eval.OutcomeInconclusive, Ended: "transport_fault",
		Detail: "the tool loop could not complete: " + render.SingleLine(err.Error()),
	}
	disp.Note("withdrawal could not complete, so restraint under change is "+
		"undecided on this run; every other measurement stands", "warn")
	return nil
}
