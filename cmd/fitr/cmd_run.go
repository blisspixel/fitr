package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
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

// ---------------------------------------------------------------- run
func cmdRun(ctx context.Context, args []string) int {
	return cmdRunWithDisplay(ctx, args, nil)
}

// cmdRunWithDisplay keeps the measurement path shared by the line-oriented
// command and the full-screen interface. A supplied display receives the same
// phases, notices, and final scorecard as every other output mode.
func cmdRunWithDisplay(ctx context.Context, args []string, supplied render.Display) int {
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
	adaptive := fs.Bool("adaptive", false, "repeat generated checks until each gated need is "+
		"decided against its gate (Wald SPRT, alpha=beta=0.05) or 6 rounds pass")
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
			return exitOK
		}
		if supplied != nil {
			reportError("invalid run arguments", err.Error(), "fitr top run <model> [run flags]")
		}
		return exitUsage
	}
	if !render.ValidMode(*mode) {
		reportError("invalid display mode", *mode, "use auto, rich, plain, json, or none")
		return exitUsage
	}
	if fs.NArg() < 1 {
		reportError("missing model", "", "fitr run <model>")
		return exitUsage
	}
	if fs.NArg() > 1 {
		reportError("too many arguments", "run accepts exactly one model", "fitr run <model> [flags]")
		return exitUsage
	}
	if *ctxSize < 0 {
		reportError("invalid context size", "--ctx cannot be negative", "omit --ctx for the default, or pass a positive token count")
		return exitUsage
	}
	levels := 0
	for _, selected := range []bool{*quick, *full, *checksOnly} {
		if selected {
			levels++
		}
	}
	if levels > 1 {
		reportError("choose one run level", "--quick, --full, and --checks-only are mutually exclusive", "")
		return exitUsage
	}
	if *checksOnly && *seedset == "" {
		reportError("--checks-only requires --seedset", "calibration pairs must face identical generated instances", "")
		return exitUsage
	}
	if *checksOnly && *adaptive {
		reportError("--checks-only cannot use --adaptive", "paired calibration needs both models to run every instance", "use a fixed -k value")
		return exitUsage
	}
	if *checksOnly && *htmlFlag {
		reportError("--checks-only cannot write a scorecard HTML", "calibration is paired evidence, not a standalone product verdict", "use `fitr calibrate a b --out pair.json` after both runs")
		return exitUsage
	}
	if *checksOnly && *allowUnsafeExec {
		reportError("--checks-only cannot enable executable diagnostics",
			"the calibration battery is declarative and does not execute generated code", "remove --allow-unsafe-exec")
		return exitUsage
	}
	model := normalizeModelRef(fs.Arg(0))

	level := "default"
	if *quick {
		level = "quick"
	} else if *full {
		level = "full"
	} else if *checksOnly {
		level = "checks"
	}
	reps := *k
	if reps == 0 {
		reps = 3
		switch level {
		case "quick":
			reps = 1
		case "checks":
			reps = 5
		}
	}
	if reps < 1 {
		reportError("invalid repeat count", "-k must be at least 1", "")
		return exitUsage
	}
	if quiet > 1 {
		*mode = "none"
	} else if (quiet > 0 || *verbose) && *mode == "auto" {
		*mode = "plain"
	}

	c, code := newBackendWithDisplay(ctx, model, *backend, *pullFlag, supplied)
	if code != exitOK {
		return code
	}
	disp := supplied
	if disp == nil {
		disp = render.New(*mode)
	}
	defer disp.Close()

	// Check tasks generate a FRESH instance per repeat, so one pass per task is
	// already a set of independent trials pooled per need. Repeats multiply
	// wall-clock across ~16 tasks, so they follow -k only when asked.
	checksReps := 1
	if *k > 0 || level == "checks" {
		checksReps = *k
		if *k == 0 {
			checksReps = reps
		}
	}

	res, err := execute(ctx, c, model, runOpts{
		level: level, profile: *profileName, seedSet: *seedset,
		reps: reps, checksReps: checksReps, adaptive: *adaptive,
		numCtx: *ctxSize, allowUnsafeExec: *allowUnsafeExec,
	}, disp)
	if err != nil {
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
	model = res.Model

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
	if *htmlFlag {
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
	if supplied == nil && quiet == 0 && render.Resolve(*mode) != "json" {
		if path != "" {
			fmt.Fprintf(os.Stderr, "\n  saved  %s\n", terminalText(path))
		}
		if level == "checks" {
			fmt.Fprintf(os.Stderr, "  next   run the paired model with --checks-only --seedset %s -k %d\n", terminalText(*seedset), checksReps)
			fmt.Fprintf(os.Stderr, "         fitr calibrate <reference> <candidate> --out pair.json\n")
		} else {
			if htmlFile != "" {
				fmt.Fprintf(os.Stderr, "  html   %s\n", terminalText(htmlFile))
			}
			fmt.Fprintf(os.Stderr, "  next   fitr board\n")
			if fit := largerFittingContext(ctx, c, model, res.Device, eval.ResolvedCtx(*ctxSize)); fit > 0 {
				fmt.Fprintf(os.Stderr,
					"         fitr run %s --ctx %d   this device fits a %d-token window; measured at %d\n",
					terminalText(model), fit, fit, eval.ResolvedCtx(*ctxSize))
			}
			if !*htmlFlag {
				fmt.Fprintf(os.Stderr, "         fitr export %s   for a shareable HTML scorecard\n", terminalText(model))
			}
			if req := eval.ResolvedCtx(*ctxSize); req != eval.NumCtx {
				fmt.Fprintf(os.Stderr, "         fitr apply %s   to persist num_ctx=%d on the server\n", terminalText(model), req)
			}
			if h := retonr.Hint(model); h != "" {
				fmt.Fprintf(os.Stderr, "         %s\n", terminalText(h))
			}
			if reps < 3 {
				fmt.Fprintf(os.Stderr, "         fitr run %s -k 3   for a rankable result\n", terminalText(model))
			}
		}
	}
	return runResultExitCode(res, level, saveErr, htmlErr)
}

func execute(ctx context.Context, c llm.Backend, model string, opts runOpts,
	disp render.Display) (*Result, error) {
	level, profileName := opts.level, opts.profile
	reps, checksReps := opts.reps, opts.checksReps
	seedSet, adaptive := opts.seedSet, opts.adaptive

	// Exactly one eval per machine at a time. Two runs against one inference
	// server contaminate each other, and the damage is not recoverable after
	// the fact -- the numbers still look plausible.
	lk, err := lock.Acquire("eval", "eval of "+model)
	if err != nil {
		return nil, err
	}
	defer lk.Release() //nolint:errcheck // cleanup failure is not worth failing a run over

	// Identity is the first evidence gate. Resolve it before loading optional
	// tasks or printing execution-policy notes so an unverifiable endpoint
	// fails with one focused, actionable diagnostic.
	disp.Phase("identity", "resolve the served artifact and seal its identity")
	identityStart := time.Now()
	resolved, err := resolveRunModel(ctx, c, model)
	if err != nil {
		return nil, err
	}
	disp.Done("identity", time.Since(identityStart).Seconds())
	if resolved.Name != model {
		disp.Note(fmt.Sprintf("runtime resolved %q as %q; the result uses the resolved identity", model, resolved.Name), "warn")
	}
	model = resolved.Name

	spec, err := eval.LoadSpec()
	if err != nil {
		return nil, err
	}
	var unsafeExecutor *eval.ExecutorReceipt
	if opts.allowUnsafeExec && level != "checks" {
		executor, err := eval.PreflightUnsafeExecutor(spec)
		if err != nil {
			return nil, err
		}
		unsafeExecutor = &executor
		ctx = eval.WithUnsafeExecutor(ctx, executor)
		disp.Note("unsafe executable diagnostics enabled: generated Python code is not sandboxed; "+
			"observations are INCONCLUSIVE and excluded from PASS/FAIL scoring", "warn")
	} else if level != "checks" {
		disp.Note("generated-code execution is disabled by default; coding and executable agent tasks are SKIP", "")
	}
	// User tasks extend the battery without a fork. A malformed one is a hard
	// error with the filename in it - silently dropping your own task would
	// defeat the point of having one.
	if level != "checks" {
		userChecks, err := eval.LoadUserChecks(eval.UserTasksDir())
		if err != nil {
			return nil, err
		}
		if len(userChecks) > 0 {
			if spec.Checks, err = eval.MergeChecks(spec.Checks, userChecks); err != nil {
				return nil, err
			}
			disp.Note(fmt.Sprintf("%d user task(s) loaded from %s", len(userChecks), eval.UserTasksDir()), "")
		}
	}
	effectiveHashes, err := eval.EffectiveHashes(spec)
	if err != nil {
		return nil, err
	}
	fp := device.Detect(ctx, c)
	prof, err := device.SelectProfile(profileName, fp)
	if err != nil {
		return nil, err
	}
	if adaptive && !hasAdaptiveGates(spec, prof, level) {
		adaptive = false
		disp.Note("adaptive checks requested, but this battery/profile has no rate gates; using the fixed repeat plan", "warn")
	}
	softwareBuild, err := buildinfo.BinarySHA256()
	if err != nil {
		return nil, fmt.Errorf("identify fitr executable: %w", err)
	}
	provenance, err := record.NewRunProvenance(effectiveHashes.TaskSetSHA256,
		effectiveHashes.SpecSHA256, prof, record.CurrentScoringPolicy(), record.SoftwareReceipt{
			FitrVersion: version, SoftwareBuildSHA256: softwareBuild,
			BackendProtocol: record.BackendProtocol(c.Name()),
		})
	if err != nil {
		return nil, fmt.Errorf("build run provenance: %w", err)
	}
	info := resolved.Info

	reqCtx := eval.ResolvedCtx(opts.numCtx)
	ctx = eval.WithNumCtx(ctx, reqCtx)
	res := &Result{
		SchemaVersion: spec.Version.ResultSchemaVersion,
		Model:         model,
		StartedAt:     time.Now().Format(time.RFC3339),
		Level:         level, Repeats: reps, NumCtx: reqCtx,
		Device: fp, DeviceKey: fp.Key(), Profile: prof.Name, ModelMeta: info,
		ExecutionPolicy: record.ExecutionDisabled,
		TaskPlan:        runTaskPlan(level, reps, checksReps, adaptive, len(spec.Checks), len(spec.Refusal.Prompts)),
	}
	if opts.allowUnsafeExec {
		res.ExecutionPolicy = record.ExecutionUnsafe
	}
	if identified, ok := disp.(runIdentityTelemetry); ok {
		res.RunID = identified.RunID()
	}
	if suf := eval.CtxKeySuffix(reqCtx); suf != "" {
		res.DeviceKey += suf
		disp.Note(fmt.Sprintf("num_ctx=%d (advise remedy applied; default is %d)", reqCtx, eval.NumCtx), "")
	}
	// Fresh instances every run by default; a pinned seedset trades that
	// contamination resistance for pairing power, and says so.
	res.SeedSet = seedSet
	if res.SeedSet == "" {
		res.SeedSet = res.StartedAt
	} else {
		disp.Note("seedset "+seedSet+" pinned - instances repeat across runs that share it; "+
			"use a fresh one when you are not pairing runs", "")
	}

	// One model resident at a time is non-negotiable between phases. A model
	// that will not unload is recorded and warned about. The same owner also
	// establishes the clean state needed by the context-allocation preflight.
	stopAll := func() error {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cleanupCancel()
		left, err := c.StopAll(cleanupCtx)
		if err != nil {
			disp.Note("could not confirm models unloaded: "+err.Error(), "warn")
			return err
		}
		if len(left) == 0 {
			return nil
		}
		res.Contamination = appendUnique(res.Contamination, left...)
		disp.Note("still resident after unload: "+strings.Join(left, ", ")+
			" - timings in this run may be contaminated", "warn")
		return nil
	}
	defer func() { _ = stopAll() }()

	if err := stopAll(); err != nil {
		return nil, fmt.Errorf("establish clean runtime state: %w", err)
	}
	contextReceipt := device.ContextVerification{RequestedTokens: reqCtx}
	if observer, ok := c.(llm.EffectiveContextObserver); ok {
		if c.Name() == "ollama" {
			_, metrics, err := c.Generate(ctx, model, "Reply with OK.", ollama.Deterministic(1, reqCtx))
			if err != nil {
				return nil, fmt.Errorf("preload model for context verification: %w", err)
			}
			served := metrics.PromptTokens + metrics.CachedTokens
			if served > 0 {
				contextReceipt.Probe = &device.ContextProbe{
					PromptTokens: metrics.PromptTokens, CachedTokens: metrics.CachedTokens,
					MinimumExpectedTokens: 1, Source: device.ContextSourceGeneration,
				}
			}
		}
		res.Device.InferenceDevice = device.InferenceDeviceFor(ctx, c, model)
		effective, observed, err := observer.EffectiveContext(ctx, model)
		if err != nil {
			return nil, fmt.Errorf("verify effective context: %w", err)
		}
		if observed {
			contextReceipt.EffectiveTokens = &effective
			contextReceipt.EffectiveSource = device.ContextSourceRuntimeReport
		}
		if c.Name() == "ollama" {
			if err := stopAll(); err != nil {
				return nil, fmt.Errorf("reset after context verification: %w", err)
			}
		}
	} else {
		res.Device.InferenceDevice = device.InferenceDeviceFor(ctx, c, model)
	}
	fingerprintV2, err := device.NewFingerprintV2(res.Device, contextReceipt)
	if err != nil {
		// This is reached when a device probe came back empty, which on a
		// loaded machine means a probe was too slow rather than a device being
		// absent. Say which, because the bare validation message reads like a
		// broken machine and the remedy is simply to try again.
		return nil, fmt.Errorf("build device fingerprint v2: %w "+
			"(a device probe returned nothing; on a busy machine this is usually a slow probe, "+
			"so re-run, and check `fitr device` if it repeats)", err)
	}
	res.DeviceV2 = &fingerprintV2
	if comparableKey, err := fingerprintV2.ComparabilityKey(); err == nil {
		res.DeviceKey = comparableKey
	} else {
		disp.Note("effective context is unverified; this run remains visible but is excluded from ranking and comparison", "warn")
	}
	if contextReceipt.State() == device.ContextAdjusted {
		disp.Note(fmt.Sprintf("runtime allocated %d context tokens for the %d-token request; comparison uses the effective value",
			*contextReceipt.EffectiveTokens, reqCtx), "warn")
	}
	if unsafeExecutor != nil {
		err = res.AttachManifestWithExecutor(resolved.Identity, *unsafeExecutor, provenance)
	} else {
		err = res.AttachManifest(resolved.Identity, provenance)
	}
	if err != nil {
		return nil, fmt.Errorf("seal run manifest: %w", err)
	}
	// Device identity is sealed into this run's fingerprint and its
	// comparability key. A fingerprint assembled from sources that disagree
	// about the machine produces no error on its own, so say it here, before
	// the measurement is attributed to a device that may not exist.
	for _, conflict := range res.Device.IdentityConflicts() {
		disp.Note("device identity: "+conflict, "warn")
	}
	// Doctor has always warned about partial offload; run did not, so a
	// measurement taken with most of the model on the GPU and the rest in
	// system RAM was saved looking like any other. The same 7B measured 179
	// tok/s resident and 16 tok/s at GPU 65% on one machine -- an order of
	// magnitude, with nothing said at the time. Placement is part of the
	// comparability key, so such a run cannot be ranked against a resident
	// one, but the operator should hear it while it is still worth acting on.
	if note := placementWarning(res.Device.InferenceDevice); note != "" {
		disp.Note(note, "warn")
	}
	if prof.Name == "default" {
		disp.Note("using the UNCALIBRATED default profile - verdicts are rough; "+
			"copy profiles/default.json and tune it for this box", "warn")
	}
	if device.IsDenseAndBig(info.Details.ParameterSize, info.Details.Family, prof) {
		disp.Note("dense "+info.Details.ParameterSize+" model on a bandwidth-bound "+
			"device - expect very low tok/s", "warn")
	}

	work, err := os.MkdirTemp("", "evalkit_")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	start := time.Now()
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
	var completed []string
	step := func(name, detail string, fn func() error) error {
		disp.Phase(name, detail)
		t := time.Now()
		if err := fn(); err != nil {
			disp.Note(fmt.Sprintf(
				"%s failed, so this run is abandoned and nothing is saved; %s. "+
					"Measurements taken before a fault are not kept, because the conditions "+
					"they were taken under no longer hold", name, abandonedStepSummary(completed)), "warn")
			return fmt.Errorf("%s: %w", name, err)
		}
		completed = append(completed, name)
		disp.Done(name, time.Since(t).Seconds())
		return nil
	}
	standardStep := func(name, detail string, fn func() error) error {
		if level == "checks" {
			return nil
		}
		return step(name, detail, fn)
	}

	if err := stopAll(); err != nil {
		return nil, fmt.Errorf("establish clean runtime state: %w", err)
	}
	if err := standardStep("speed", fmt.Sprintf("x%d", reps), func() error {
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
	}); err != nil {
		return nil, err
	}
	if err := standardStep("memory", "resident @32K", func() error {
		m, err := eval.RunMemory(ctx, c, model, 32768)
		res.Memory = m
		if live, ok := disp.(liveTelemetry); ok && m.ResidentGB > 0 {
			live.LiveMemory(m.ResidentGB)
		}
		return err
	}); err != nil {
		return nil, err
	}

	if err := standardStep("coding", fmt.Sprintf("x%d", reps), func() error {
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
	}); err != nil {
		return nil, err
	}

	// Generated checks: fresh instances per run, graded in pure Go. Skipped on
	// --quick to keep the smoke test a smoke test.
	//
	// Adaptive mode replaces the fixed repeat count with Wald's SPRT per gated
	// need: keep generating fresh instances until each pool's rate is decided
	// against its gate at alpha=beta=0.05, or six rounds pass. Undecided at
	// the cap is a real answer - the sample cannot separate the rate from the
	// gate - and the scorecard will report INCONCLUSIVE rather than forcing a
	// binary claim.
	if level != "quick" && len(spec.Checks) > 0 {
		rounds := checksReps
		var sprts map[string]*stats.SPRT
		adaptiveGates := map[string]float64{}
		adaptiveLimits := map[string]int{}
		adaptivePasses := map[string]int{}
		detail := fmt.Sprintf("%d generated tasks", len(spec.Checks)*checksReps)
		if adaptive {
			rounds = 6
			detail = "adaptive (SPRT until decided)"
			present := map[string]bool{}
			for _, cs := range spec.Checks {
				present[cs.Need] = true
			}
			sprts = map[string]*stats.SPRT{}
			for need := range present {
				if minRate, ok := prof.Float(need, "pass_rate_min"); ok {
					if s, err := stats.GateSPRT(minRate); err == nil {
						sprts[need] = s
						adaptiveGates[need] = minRate
					}
				}
			}
			for _, cs := range spec.Checks {
				if _, ok := sprts[cs.Need]; ok {
					adaptiveLimits[cs.Need] += rounds
				}
			}
		}
		roundsRun := 0
		if err := step("checks", detail, func() error {
			completed := 0
			for round := range rounds {
				roundsRun = round + 1
				for _, cs := range spec.Checks {
					seed := eval.InstanceSeed(res.SeedSet, cs.ID, round)
					o, err := eval.RunCheck(ctx, c, model, cs, seed)
					if err != nil {
						return err
					}
					res.Checks = append(res.Checks, o)
					completed++
					if live, ok := disp.(liveTelemetry); ok {
						live.LiveProgress(completed, len(spec.Checks)*rounds,
							fmt.Sprintf("%d of %d generated tasks", completed, len(spec.Checks)*rounds))
					}
					if s, ok := sprts[cs.Need]; ok && s.State() == stats.SPRTContinue {
						s.Add(o.Pass)
						if o.Pass {
							adaptivePasses[cs.Need]++
						}
					}
				}
				if adaptive && allDecided(sprts) {
					break
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
		if adaptive {
			needs := make([]string, 0, len(sprts))
			for need := range sprts {
				needs = append(needs, need)
			}
			sort.Strings(needs)
			for _, need := range needs {
				decision, err := eval.CaptureAdaptiveDecision(need, adaptiveGates[need],
					adaptiveLimits[need], adaptivePasses[need], sprts[need])
				if err != nil {
					return nil, fmt.Errorf("capture adaptive %s decision: %w", need, err)
				}
				res.AdaptiveDecisions = append(res.AdaptiveDecisions, decision)
			}
			disp.Note(adaptiveSummary(sprts, roundsRun, len(res.Checks)), "")
		}
	}

	// Plumbing BEFORE capability: a tools failure is uninterpretable until you
	// know the template and parser work.
	if err := standardStep("plumbing", "tool round-trip", func() error {
		p, err := eval.RunPlumbing(ctx, c, model, spec.Plumbing)
		if err != nil {
			return err
		}
		res.Plumbing = &p
		return nil
	}); err != nil {
		return nil, err
	}

	toolReady := res.Plumbing != nil && res.Plumbing.Outcome == eval.OutcomePass && res.Plumbing.Healthy
	if !toolReady && level != "checks" {
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
	if toolReady {
		if err := standardStep("tools", fmt.Sprintf("x%d", reps), func() error {
			for i := range reps {
				t, err := eval.RunToolLoop(ctx, c, model, spec.Tools, filepath.Join(work, fmt.Sprintf("tl%d", i)))
				if err != nil {
					return err
				}
				res.Tools = append(res.Tools, t)
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}

	if level != "quick" && level != "checks" {
		if toolReady {
			if err := step("withdrawal", "a tool vanishes mid-loop", func() error {
				w, err := eval.RunToolLoop(ctx, c, model, spec.Withdrawal, filepath.Join(work, "wd"))
				if err != nil {
					return err
				}
				res.Withdrawal = &w
				return nil
			}); err != nil {
				return nil, err
			}
		} else {
			res.Withdrawal = &eval.ToolLoopResult{
				Outcome: eval.OutcomeSkipped, Ended: "plumbing_unavailable",
				Detail: "tool plumbing did not establish a usable protocol",
			}
		}
	}

	if level != "quick" && level != "checks" {
		if err := step("refusal", "3 prompts", func() error {
			r, n, err := eval.RunRefusal(ctx, c, model, spec.Refusal)
			res.Refusal, res.Refused = r, n
			return err
		}); err != nil {
			return nil, err
		}
	}

	if level == "full" {
		if toolReady {
			if err := step("agentic", fmt.Sprintf("up to %d unsupervised turns", spec.Agentic.MaxTurns), func() error {
				if err := stopAll(); err != nil {
					return fmt.Errorf("establish clean agentic runtime state: %w", err)
				}
				a, err := eval.RunToolLoop(ctx, c, model, spec.Agentic, filepath.Join(work, "ag"))
				if err != nil {
					return err
				}
				res.Agentic = &a
				return nil
			}); err != nil {
				return nil, err
			}
		} else {
			res.Agentic = &eval.ToolLoopResult{
				Outcome: eval.OutcomeSkipped, Ended: "plumbing_unavailable",
				Detail: "tool plumbing did not establish a usable protocol",
			}
		}
	}

	// Inference placement is observed once, above, while the model is resident
	// for context verification, and is then sealed into DeviceV2 and the
	// comparability key. Re-probing here overwrote that sealed value from a
	// different residency state: the battery has finished and the model may be
	// unloaded, so the two observations disagree, res.Device and
	// DeviceV2.Device diverge, and the manifest check rejects the save --
	// discarding a completed measurement. A later reading taken under
	// different conditions does not correct the sealed one.

	// Degeneracy over the longest text this model produced.
	longest := ""
	for _, r := range append(append([]eval.ExecResult{}, res.CodeWrite...), res.CodeFix...) {
		if len(r.Raw) > len(longest) {
			longest = r.Raw
		}
	}
	for _, v := range res.Refusal {
		if len(v.Text) > len(longest) {
			longest = v.Text
		}
	}
	res.Rep = score.RepetitionMetrics(longest)
	// Truncation is judged on free-running tasks only; the speed probe is
	// capped by design and would always look truncated.
	res.Density = score.InformationDensity(longest)
	res.WallSeconds = float64(int(time.Since(start).Seconds()*10)) / 10
	// The deferred cleanup still protects every early return. This final check
	// happens before scoring so a model that refuses the last unload cannot
	// leave a persisted PASS or FAIL claim behind.
	if err := stopAll(); err != nil {
		return nil, fmt.Errorf("verify final runtime state: %w", err)
	}
	res.EvidenceCounts = buildEvidenceCounts(res)
	for phase, counts := range res.EvidenceCounts {
		if !counts.Complete() {
			return nil, fmt.Errorf("%s evidence did not complete its immutable denominator: %+v", phase, counts)
		}
	}
	res.Scorecard = score.Score(measure(res), prof)
	if err := res.CompleteEvidence(prof); err != nil {
		return nil, fmt.Errorf("seal completed evidence: %w", err)
	}
	return res, nil
}

// measure folds raw results into what the scorer needs.
func measure(r *Result) score.Measured {
	m := score.Measured{
		Model: r.Model, Capabilities: r.ModelMeta.Capabilities,
		Rep: r.Rep, Contamination: append([]string(nil), r.Contamination...),
	}
	if r.DecodeSum.N > 0 {
		m.SpeedKnown = true
		m.DecodeTPS, m.TTFT, m.PrefillTPS = r.DecodeSum.Mean, r.TTFTSum.Mean, r.PrefillSum.Mean
		for _, s := range r.Speed {
			if s.ColdTTFT > 0 && m.TTFTCold == 0 {
				m.TTFTCold = s.ColdTTFT
			}
			if s.WarmTTFT > 0 && m.TTFTWarm == 0 {
				m.TTFTWarm = s.WarmTTFT
			}
			if s.GatedTTFTContaminated() {
				m.TTFTCacheContaminated = true
			}
			if s.ClientDerived {
				m.TimingsClientDerived = true
			}
		}
	}
	if r.Memory.ResidentGB > 0 {
		m.MemoryKnown, m.ResidentGB32K = true, r.Memory.ResidentGB
	}
	if len(r.CodeWrite)+len(r.CodeFix) > 0 {
		var wOK, fOK []bool
		for _, x := range r.CodeWrite {
			if pass, measured := eval.MeasuredOutcome(x.Outcome, x.Pass); measured {
				wOK = append(wOK, pass)
			}
		}
		for _, x := range r.CodeFix {
			if pass, measured := eval.MeasuredOutcome(x.Outcome, x.Pass); measured {
				fOK = append(fOK, pass)
			}
		}
		expected := r.TaskPlan.CodeTrials
		if expected == 0 && r.SchemaVersion < 5 {
			expected = r.Repeats * 2
		}
		m.CodeKnown = expected > 0 && len(wOK)+len(fOK) == expected
		fw, ff := stats.Flakiness(wOK), stats.Flakiness(fOK)
		m.CodeWritePass = fw.Passes*2 > fw.N
		m.CodeFixPass = ff.Passes*2 > ff.N
		m.CodeFlaky = fw.Flaky || ff.Flaky
		m.CodePasses = fw.Passes + ff.Passes
		m.CodeRepeats = fw.N + ff.N
	}
	for _, ck := range r.Checks {
		pass, measured := eval.MeasuredOutcome(ck.Outcome, ck.Pass)
		if !measured {
			continue
		}
		var pool *score.Pool
		switch ck.Need {
		case "structured_output":
			pool = &m.Structured
		case "instruction_precision":
			pool = &m.Precision
		case "reasoning":
			pool = &m.Reasoning
		default:
			pool = &m.User
		}
		pool.Add(ck.Family, pass)
	}
	if r.Refusal != nil {
		expected := r.TaskPlan.RefusalTrials
		if expected == 0 && r.SchemaVersion < 5 {
			expected = len(r.Refusal)
		}
		complete := expected > 0 && len(r.Refusal) == expected
		refused := 0
		for _, result := range r.Refusal {
			outcome := result.Outcome
			if outcome == "" {
				switch result.Verdict {
				case "answered":
					outcome = eval.OutcomePass
				case "partial", "refused", "empty":
					outcome = eval.OutcomeFail
				default:
					complete = false
				}
			}
			pass, measured := eval.MeasuredOutcome(outcome, false)
			if !measured {
				complete = false
			} else if !pass {
				refused++
			}
		}
		m.RefusalKnown, m.RefusedCount = complete, refused
	}
	if len(r.Tools) > 0 {
		passes, measured := 0, 0
		for _, t := range r.Tools {
			if pass, ok := eval.MeasuredOutcome(t.Outcome, t.Pass); ok {
				measured++
				if pass {
					passes++
				}
			}
		}
		expected := r.TaskPlan.ToolTrials
		if expected == 0 && r.SchemaVersion < 5 {
			expected = r.Repeats
		}
		m.ToolsRan = measured > 0 && measured == expected
		m.ToolsPass = m.ToolsRan && passes*2 > measured
	}
	if r.Agentic != nil {
		if pass, measured := eval.MeasuredOutcome(r.Agentic.Outcome, r.Agentic.Pass); measured {
			m.AgenticRan, m.AgenticPass = true, pass
		}
		m.AgenticMalformed = r.Agentic.Malformed
		m.AgenticTurns = r.Agentic.Turns
		m.AgenticCtxCeiling = r.Agentic.CtxCeiling
		m.AgenticMaxPrompt = r.Agentic.MaxPromptTok
		m.AgenticCompacted = r.Agentic.Compacted
	}
	if r.Withdrawal != nil {
		if _, measured := eval.MeasuredOutcome(r.Withdrawal.Outcome, r.Withdrawal.Pass); measured {
			m.WithdrawRan = true
			m.WithdrawDeadCalls = r.Withdrawal.DeadCalls
			m.WithdrawClean = r.Withdrawal.Ended == "clean_stop"
		}
	}
	if r.Plumbing != nil {
		m.PlumbingRan = r.Plumbing.Outcome != eval.OutcomeSkipped &&
			r.Plumbing.Outcome != eval.OutcomeError
		if r.Plumbing.Outcome == "" {
			m.PlumbingRan = true
		}
		m.PlumbingHealthy = m.PlumbingRan && r.Plumbing.Healthy
		m.PlumbingVerdict = r.Plumbing.Verdict
		if rung, ok := r.Plumbing.Rungs["5_irrelevance"]; ok && m.PlumbingRan {
			m.IrrelevanceRan, m.IrrelevancePass = true, rung.Pass
			if !rung.Pass {
				m.SpuriousCalls = 1
			}
		}
	}
	return m
}

func buildEvidenceCounts(r *Result) map[string]eval.OutcomeCounts {
	counts := map[string]eval.OutcomeCounts{}
	collect := func(expected int, values []eval.Outcome, fillSkipped bool) eval.OutcomeCounts {
		if fillSkipped && len(values) < expected {
			for len(values) < expected {
				values = append(values, eval.OutcomeSkipped)
			}
		}
		return eval.CountOutcomes(expected, values...)
	}
	legacy := func(outcome eval.Outcome, pass bool) eval.Outcome {
		if outcome != "" {
			return outcome
		}
		if pass {
			return eval.OutcomePass
		}
		return eval.OutcomeFail
	}

	var code []eval.Outcome
	for _, result := range r.CodeWrite {
		code = append(code, legacy(result.Outcome, result.Pass))
	}
	for _, result := range r.CodeFix {
		code = append(code, legacy(result.Outcome, result.Pass))
	}
	counts["coding"] = collect(r.TaskPlan.CodeTrials, code, false)

	var checks []eval.Outcome
	for _, result := range r.Checks {
		checks = append(checks, legacy(result.Outcome, result.Pass))
	}
	counts["checks"] = collect(r.TaskPlan.CheckTrialsLimit, checks, true)

	var tools []eval.Outcome
	for _, result := range r.Tools {
		tools = append(tools, legacy(result.Outcome, result.Pass))
	}
	counts["tools"] = collect(r.TaskPlan.ToolTrials, tools, false)

	var refusal []eval.Outcome
	for _, result := range r.Refusal {
		refusal = append(refusal, result.Outcome)
	}
	counts["refusal"] = collect(r.TaskPlan.RefusalTrials, refusal, false)

	var plumbing []eval.Outcome
	if r.Plumbing != nil {
		plumbing = append(plumbing, legacy(r.Plumbing.Outcome, r.Plumbing.Healthy))
	}
	plumbingExpected := 0
	if r.TaskPlan.Plumbing {
		plumbingExpected = 1
	}
	counts["plumbing"] = collect(plumbingExpected, plumbing, false)

	var withdrawal []eval.Outcome
	if r.Withdrawal != nil {
		withdrawal = append(withdrawal, legacy(r.Withdrawal.Outcome, r.Withdrawal.Pass))
	}
	withdrawalExpected := 0
	if r.TaskPlan.Withdrawal {
		withdrawalExpected = 1
	}
	counts["withdrawal"] = collect(withdrawalExpected, withdrawal, false)

	var agentic []eval.Outcome
	if r.Agentic != nil {
		agentic = append(agentic, legacy(r.Agentic.Outcome, r.Agentic.Pass))
	}
	counts["agentic"] = collect(r.TaskPlan.AgenticTrials, agentic, false)
	return counts
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

// placementWarning describes a measurement that is not really being taken on
// the accelerator it appears to be taken on. It mirrors the rule doctor
// applies, so the two surfaces cannot drift into disagreeing about the same
// machine. An empty string means placement is either fully offloaded or was
// not observed, and an unobserved placement is not a fault to shout about.
func placementWarning(placement string) string {
	switch {
	case placement == "" || placement == "GPU 100%" || placement == "unknown":
		return ""
	case placement == "CPU":
		return "inference is running on the CPU with no offload; decode here is a fraction of " +
			"what this model does on a GPU, and the number is only meaningful against other CPU runs"
	case strings.HasPrefix(placement, "GPU "):
		return "partial offload (" + placement + "); the rest of the model is in system RAM, so decode " +
			"measures RAM bandwidth rather than the GPU. Free VRAM and re-run for a resident measurement"
	}
	return ""
}
