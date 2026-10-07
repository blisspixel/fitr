package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/advise"
	"github.com/blisspixel/fitr/internal/automation"
	"github.com/blisspixel/fitr/internal/buildinfo"
	"github.com/blisspixel/fitr/internal/fitting"
	"github.com/blisspixel/fitr/internal/lock"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/render"
)

const tailorGuide = `fitr tailor: one fitting for this machine and this work

  fitr tailor plan --role <name> --outcome <need> --candidate <model> --ctx <tokens>
    (--capacity-budget-gb <GiB> | --capacity-reserve-gb <GiB>)
  fitr tailor start <session-id> --approve-plan
  fitr tailor status|resume <session-id>
  fitr tailor adopt <session-id> --approve-adoption

The plan is the configuration used for advice, measurement, comparison, and
adoption. Start measures that plan through the selected Ollama endpoint. When
an owned runtime was approved together with a reserve, start hands the same
context and reserve to fitr auto. An absolute ceiling is not rewritten into
that reserve. One installed model stays a non-comparative assessment. Nothing
is downloaded, and an existing Ollama process is not stopped.

  --role           short name for this work
  --outcome        behavior that has to succeed; repeat for each one
  --candidate      installed model; repeat for two to four
  --ctx            context window to measure, in tokens
  --capacity-budget-gb    absolute ceiling, in GiB
  --capacity-reserve-gb   reserve subtracted from free memory, in GiB
  --memory-gb      role resident limit when the policy is a reserve
  --scope screen   optional screening; it does not qualify a coder
  --endpoint       optional explicit Ollama URL
  --approve-owned-runtime  optional; only with a reserve, and start then needs --runtime
  --workload       repository-planning, agentic-coding, code-review,
                   document-research, or structured-extraction
  --desired-context  tokens the work needs; a smaller --ctx does not satisfy it
  --context-meaning  total-window or usable-input, required with a context goal
  --harness        optional. hermes keeps a 64000 token floor
  --weight-quant   declared weight quantization, such as Q4_K_M
  --kv             KV cache precision: f16, q8_0, or q4_0

A workload preset expands into visible requirements. Weight quantization and
KV-cache precision are different settings. --kv q8_0 records that cache type
and records flash attention as required on the plan, because quantized KV is
refused without it. The flag does not enable flash attention on a server that
is already running, and it does not prove the server applied either setting.
An approved owned child can receive the same request in its own environment
when fitr starts that child. A client environment variable is not that proof.
Cloud is not an automatic route.

fitr uses OLLAMA_BASE_URL. The Ollama CLI uses OLLAMA_HOST. Pass --endpoint
when those two disagree.`

type tailorOutcomes []string

func (v *tailorOutcomes) String() string { return fmt.Sprint([]string(*v)) }
func (v *tailorOutcomes) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(*v) >= 8 {
		return errors.New("name one to eight outcomes")
	}
	*v = append(*v, value)
	return nil
}

type tailorJob struct {
	Session  fitting.Session
	Model    string
	Endpoint string
	Display  string
	Opts     runOpts
}

type tailorHistory struct {
	Point         fitting.Point
	Seed          string
	ContextTokens int
	Kind          fitting.CapacityKind
	CapacityBytes int64
	Endpoint      string
}

type tailorDeps struct {
	runPoint      func(context.Context, tailorJob) (string, string, error)
	history       func(fitting.Plan, string) (tailorHistory, bool)
	closeEvidence func(fitting.Session) error
	startOwned    func(context.Context, autoCommand) int
}

type tailorView struct {
	Schema         string                 `json:"schema"`
	Phase          string                 `json:"phase"`
	Lines          []string               `json:"lines"`
	Plan           fitting.Plan           `json:"plan"`
	Points         []fitting.Point        `json:"points,omitempty"`
	Recommendation fitting.Recommendation `json:"recommendation"`
}

func cmdTailor(ctx context.Context, args []string) int {
	return cmdTailorWith(ctx, args, tailorDeps{})
}

func cmdTailorWith(ctx context.Context, args []string, deps tailorDeps) int {
	if deps.runPoint == nil {
		deps.runPoint = executeTailorPoint
		if deps.closeEvidence == nil {
			deps.closeEvidence = closeFittingEvidence
		}
	}
	if deps.startOwned == nil {
		deps.startOwned = startAuto
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stdout, tailorGuide)
		return exitUsage
	}
	if args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		fmt.Fprintln(os.Stdout, tailorGuide)
		return exitOK
	}
	switch args[0] {
	case "plan":
		return tailorPlan(args[1:])
	case "start":
		return tailorStart(ctx, args[1:], deps)
	case "status":
		return tailorStatus(args[1:])
	case "resume":
		return tailorResume(ctx, args[1:], deps)
	case "adopt":
		return tailorAdopt(args[1:])
	default:
		errPrint("unknown tailor action", args[0], "fitr tailor --help")
		return exitUsage
	}
}

type tailorPlanFlags struct {
	role, scope, endpoint, kv, budget, reserve, memory, tiers, rate, display  string
	workload, meaning, harness, harnessVersion, weightQuant, weightPreference string
	weightMinimum, modelCard, projected                                       string
	outcomes                                                                  tailorOutcomes
	candidates                                                                autoCandidates
	alternatives, permittedKV                                                 tokenList
	ctxTokens, usableBytes, repeats, desired, minimum, harnessMin             int
	reserveSystem, reserveOutput, reserveReasoning, concurrency               int
	noFlash, owned, populated                                                 bool
}

func tailorPlan(args []string) int {
	flags, code, ok := parseTailorPlan(args)
	if !ok {
		return code
	}
	if code, ok = tailorRequiredAnswers(flags.role, flags.outcomes, flags.candidates, flags.ctxTokens, flags.budget, flags.reserve, flags.workload); !ok {
		return code
	}
	if code, ok = tailorContextMeaning(flags); !ok {
		return code
	}
	kind, amount, resident, code, ok := tailorCapacityAmounts(flags.budget, flags.reserve, flags.memory)
	if !ok {
		return code
	}
	req, code, ok := tailorRequest(flags, kind, amount, resident)
	if !ok {
		return code
	}
	return sealTailorPlan(req, flags.display)
}

func tailorRequest(flags tailorPlanFlags, kind fitting.CapacityKind, amount, resident int64) (fitting.Request, int, bool) {
	if flags.owned && flags.endpoint != "" {
		return fitting.Request{}, tailorQuestion("An owned runtime does not use --endpoint.", "The child has its own listener.", "remove --endpoint or omit --approve-owned-runtime"), false
	}
	resolved, err := fitting.ResolveEndpoint(fitting.EndpointInput{Explicit: flags.endpoint, FitEnv: os.Getenv("OLLAMA_BASE_URL"), CLIEnv: os.Getenv("OLLAMA_HOST")})
	if err != nil {
		errPrint(err.Error(), "fitr uses OLLAMA_BASE_URL. The Ollama CLI uses OLLAMA_HOST.", "pass --endpoint <url> or set one client URL")
		return fitting.Request{}, exitUsage, false
	}
	if flags.owned {
		resolved = fitting.Endpoint{URL: "owned-process", Source: "owned-runtime", Locality: "owned-process",
			Note: "The owned child gets its own listener. This plan does not send inference to OLLAMA_BASE_URL, and it does not stop an existing Ollama."}
	}
	var rate *float64
	if flags.rate != "" {
		parsed, parseErr := strconv.ParseFloat(flags.rate, 64)
		if parseErr != nil {
			errPrint("minimum rate must be a fraction above 0 and at most 1", "", "pass --minimum-rate <fraction>")
			return fitting.Request{}, exitUsage, false
		}
		rate = &parsed
	}
	var tiers []int
	if flags.tiers != "" {
		tiers, err = parseContextTiers(flags.tiers)
		if err != nil {
			errPrint(err.Error(), "tiers are payload bytes, not a token-window search", "fitr tailor --help")
			return fitting.Request{}, exitUsage, false
		}
	}
	req := fitting.Request{
		RoleName: flags.role, Outcomes: []string(flags.outcomes), MinimumRate: rate, Scope: flags.scope,
		Candidates: []string(flags.candidates), ContextTokens: flags.ctxTokens, CapacityKind: kind,
		CapacityBytes: amount, ResidentLimitBytes: resident, UsableContextBytes: flags.usableBytes,
		ContextTiers: tiers, Endpoint: resolved.URL, EndpointSource: resolved.Source,
		Locality: resolved.Locality, EndpointNote: resolved.Note, Repeats: flags.repeats,
		OwnedRuntime: flags.owned, BuildVersion: buildinfo.Version(), Now: time.Now(),
	}
	if flags.kv != "" || flags.noFlash {
		req.KVExplicit = true
		req.KVCacheType = flags.kv
		if req.KVCacheType == "" {
			req.KVCacheType = "f16"
		}
		// The boolean is the sealed plan's request. Draft refuses q8_0 and q4_0
		// when it is false. Plan time does not write OLLAMA_FLASH_ATTENTION
		// into a server that is already running.
		req.FlashAttention = !flags.noFlash
	}
	if err = applyTailorWorkload(flags, &req); err != nil {
		errPrint(err.Error(), "", "fitr tailor --help")
		return fitting.Request{}, exitUsage, false
	}
	return req, exitOK, true
}

func sealTailorPlan(req fitting.Request, display string) int {
	tasks, err := autoTaskSpec()
	if err != nil {
		return fittingFailure(err)
	}
	plan, err := fitting.Draft(req, tasks)
	if err != nil {
		errPrint(err.Error(), "", "fitr tailor --help")
		return exitUsage
	}
	sha, err := buildinfo.BinarySHA256()
	if err != nil {
		sha = "unavailable"
	}
	session := fitting.Session{Schema: fitting.SessionSchema, Plan: plan, ExecutableSHA256: sha, Phase: fitting.PhasePreviewed}
	if plan.Blocked {
		session.Phase = fitting.PhaseBlocked
	}
	if err := (fitting.Store{Results: resultsDir()}).Create(session); err != nil {
		return fittingFailure(err)
	}
	if code := printTailor(session, display); code != exitOK {
		return code
	}
	if plan.Blocked {
		return exitUnresolved
	}
	return exitOK
}

func tailorStart(ctx context.Context, args []string, deps tailorDeps) int {
	fs := flag.NewFlagSet("tailor start", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var approve bool
	var runtimePath, display string
	fs.BoolVar(&approve, "approve-plan", false, "measure the previewed plan")
	fs.StringVar(&runtimePath, "runtime", "", "owned runtime JSON whose context and reserve already match the plan")
	fs.StringVar(&display, "display", "auto", "auto|rich|plain|json|none")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 || !render.ValidMode(display) {
		errPrint("tailor start needs the session id", "", "fitr tailor --help")
		return exitUsage
	}
	session, guard, err := loadLockedFitting(fs.Arg(0))
	if err != nil {
		return fittingFailure(err)
	}
	defer func() { _ = guard.Release() }()
	if !approve {
		if code := printTailor(session, display); code != exitOK {
			return code
		}
		errPrint("the plan is previewed and has not started", "", "fitr tailor start "+session.Plan.ID+" --approve-plan")
		return exitUsage
	}
	if session.Plan.Blocked || session.Phase == fitting.PhaseBlocked {
		if code := printTailor(session, display); code != exitOK {
			return code
		}
		return exitUnresolved
	}
	if session.Phase == fitting.PhaseApproved && session.Plan.OwnedRuntime && session.AutoSessionID == "" {
		return startOwnedFitting(ctx, session, runtimePath, display, deps)
	}
	if session.Phase != fitting.PhasePreviewed {
		errPrint("this fitting already started", session.Phase, "fitr tailor resume "+session.Plan.ID)
		return exitUsage
	}
	if session.Plan.OwnedRuntime {
		return startOwnedFitting(ctx, session, runtimePath, display, deps)
	}
	if runtimePath != "" {
		errPrint("this plan does not launch an owned runtime", "", "omit --runtime")
		return exitUsage
	}
	return runApprovedFitting(ctx, session, display, deps)
}

func runApprovedFitting(ctx context.Context, session fitting.Session, display string, deps tailorDeps) int {
	session.Phase = fitting.PhaseApproved
	if err := (fitting.Store{Results: resultsDir()}).Save(session); err != nil {
		return fittingFailure(err)
	}
	updated, err := measureFitting(ctx, session, display, deps)
	if saveErr := (fitting.Store{Results: resultsDir()}).Save(updated); saveErr != nil && err == nil {
		err = saveErr
	}
	code := printTailor(updated, display)
	if err != nil {
		return fittingFailure(err)
	}
	if code != exitOK {
		return code
	}
	return exitUnresolved
}

func startOwnedFitting(ctx context.Context, session fitting.Session, runtimePath, display string, deps tailorDeps) int {
	if err := validateFittingTasks(session.Plan); err != nil {
		return fittingFailure(err)
	}
	if runtimePath == "" {
		return tailorQuestion("Which owned runtime file matches this plan?", "fitr will not replace an 8192 token and 2 GiB inspection file.", "pass --runtime <runtime.json>")
	}
	loaded, err := loadAutoRuntime(runtimePath)
	if err != nil {
		return fittingFailure(err)
	}
	expected, err := fitting.ApplyToRuntime(session.Plan, loaded)
	if err != nil {
		return fittingFailure(err)
	}
	if loaded.NumCtx != expected.NumCtx || loaded.ReserveBytes != expected.ReserveBytes || loaded.KVCacheType != expected.KVCacheType || loaded.FlashAttention != expected.FlashAttention {
		errPrint("owned runtime file does not match the approved context and reserve", "fitr will not replace it with the 8192 token and 2 GiB inspection defaults", "edit the runtime JSON to this plan, or draft a new plan")
		return exitUsage
	}
	session.Phase = fitting.PhaseApproved
	if err := (fitting.Store{Results: resultsDir()}).Save(session); err != nil {
		return fittingFailure(err)
	}
	if err := ensureFittingRole(session.Plan); err != nil {
		return fittingFailure(err)
	}
	command := autoCommand{
		action: "start", role: session.Plan.RoleName, runtimePath: runtimePath, mode: "establish",
		adoption: "manual", display: display, repeats: session.Plan.Repeats,
		candidates: append(autoCandidates(nil), session.Plan.Candidates...), contextTiers: session.Plan.ContextTiers,
		wall: time.Duration(session.Plan.WallSeconds) * time.Second, confirmationWall: time.Duration(session.Plan.ConfirmationWallSeconds) * time.Second,
		limits: automation.Limits{
			MaxRequests: session.Plan.MaxRequests, MaxRequestedOutputTokens: session.Plan.MaxRequestedOutputTokens,
			MaxPoints: session.Plan.MaxPoints, WallSeconds: session.Plan.WallSeconds,
			ConfirmationWallSeconds: session.Plan.ConfirmationWallSeconds,
		},
	}
	code := launchOwnedFitting(ctx, session, command, deps.startOwned)
	updated, loadErr := (fitting.Store{Results: resultsDir()}).Load(session.Plan.ID)
	if loadErr != nil {
		return fittingFailure(loadErr)
	}
	if printed := printTailor(updated, display); printed != exitOK {
		return printed
	}
	return code
}

func launchOwnedFitting(ctx context.Context, session fitting.Session, command autoCommand, start func(context.Context, autoCommand) int) int {
	ctx = context.WithValue(ctx, autoNotifyKey{}, autoSessionNotifier(func(id string) error {
		return rememberDelegatedAuto(resultsDir(), session.Plan.ID, id)
	}))
	return start(ctx, command)
}

func ensureFittingRole(plan fitting.Plan) error {
	digest, err := plan.Role.Digest()
	if err != nil {
		return err
	}
	roles, _ := autoStores()
	library, err := roles.Load(plan.RoleName)
	if errors.Is(err, os.ErrNotExist) {
		_, err = roles.Define(plan.Role)
		return err
	}
	if err != nil {
		return err
	}
	if library.CurrentRevision != digest {
		return errors.New("the role library has a different revision from this fitting plan")
	}
	return nil
}

func tailorStatus(args []string) int {
	session, display, code, ok := loadTailorCommand("status", args)
	if !ok {
		return code
	}
	if printed := printTailor(session, display); printed != exitOK {
		return printed
	}
	if session.Phase == fitting.PhasePreviewed {
		return exitOK
	}
	return exitUnresolved
}

func tailorResume(ctx context.Context, args []string, deps tailorDeps) int {
	session, display, code, ok := loadTailorCommand("resume", args)
	if !ok {
		return code
	}
	session, guard, err := loadLockedFitting(session.Plan.ID)
	if err != nil {
		return fittingFailure(err)
	}
	defer func() { _ = guard.Release() }()
	action, err := session.ResumeAction()
	if err != nil {
		if printed := printTailor(session, display); printed != exitOK {
			return printed
		}
		errPrint(err.Error(), "", "fitr tailor status "+session.Plan.ID)
		return exitUnresolved
	}
	switch action {
	case "status":
		return tailorStatus([]string{session.Plan.ID, "--display", display})
	case "auto":
		return resumeTailorAuto(ctx, session, display)
	case "measure":
		if session.Plan.OwnedRuntime {
			errPrint("the owned fitting has no auto session yet", "", "fitr tailor start "+session.Plan.ID+" --approve-plan --runtime <runtime.json>")
			return exitUsage
		}
		updated, measureErr := measureFitting(ctx, session, display, deps)
		if saveErr := (fitting.Store{Results: resultsDir()}).Save(updated); saveErr != nil && measureErr == nil {
			measureErr = saveErr
		}
		if printed := printTailor(updated, display); printed != exitOK {
			return printed
		}
		if measureErr != nil {
			return fittingFailure(measureErr)
		}
		return exitUnresolved
	default:
		errPrint("this fitting cannot resume", action, "fitr tailor status "+session.Plan.ID)
		return exitUsage
	}
}

func tailorAdopt(args []string) int {
	fs := flag.NewFlagSet("tailor adopt", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var approve bool
	var display string
	fs.BoolVar(&approve, "approve-adoption", false, "record the fitr role selection")
	fs.StringVar(&display, "display", "auto", "auto|rich|plain|json|none")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 || !render.ValidMode(display) {
		errPrint("tailor adopt needs the session id", "", "fitr tailor --help")
		return exitUsage
	}
	session, guard, err := loadLockedFitting(fs.Arg(0))
	if err != nil {
		return fittingFailure(err)
	}
	defer func() { _ = guard.Release() }()
	action, err := session.AdoptionAction()
	if err != nil {
		if printed := printTailor(session, display); printed != exitOK {
			return printed
		}
		errPrint(err.Error(), "No Ollama alias, external agent, or serving runtime was changed.", "fitr tailor status "+session.Plan.ID)
		return exitUnresolved
	}
	if action != "auto" {
		errPrint("adoption needs a finished owned fitting", "", "fitr tailor status "+session.Plan.ID)
		return exitUnresolved
	}
	fmt.Fprintln(os.Stdout, "Adoption records a fitr role selection only. It does not change an Ollama alias, configure an external agent, or restart a runtime.")
	if !approve {
		errPrint("adoption is waiting for approval", "", "fitr tailor adopt "+session.Plan.ID+" --approve-adoption")
		return exitUsage
	}
	autoSession, err := (automation.Store{Results: resultsDir()}).Open(session.AutoSessionID)
	if err != nil {
		return fittingFailure(err)
	}
	defer func() { _ = autoSession.Close() }()
	if err := adoptAuto(autoSession); err != nil {
		return autoFailure(err)
	}
	return showAuto(session.AutoSessionID, display)
}

func loadLockedFitting(id string) (fitting.Session, *lock.Lock, error) {
	store := fitting.Store{Results: resultsDir()}
	// Validate the ID through the store before using it as a lock name. Reload
	// under the lease so a concurrent completed phase cannot be overwritten.
	if _, err := store.Load(id); err != nil {
		return fitting.Session{}, nil, err
	}
	guard, err := lock.Acquire("fitting-"+id, "fitting "+id)
	if err != nil {
		return fitting.Session{}, nil, err
	}
	session, err := store.Load(id)
	if err != nil {
		_ = guard.Release()
		return fitting.Session{}, nil, err
	}
	return session, guard, nil
}

func measureFitting(ctx context.Context, session fitting.Session, display string, deps tailorDeps) (fitting.Session, error) {
	if session.Plan.OwnedRuntime || session.Plan.Blocked {
		return session, errors.New("this fitting does not measure through the selected endpoint")
	}
	if err := validateFittingTasks(session.Plan); err != nil {
		return session, err
	}
	session.ExplorationID = session.Plan.ID + "-explore"
	session.Phase = fitting.PhaseMeasuring
	store := fitting.Store{Results: resultsDir()}
	if err := store.Save(session); err != nil {
		return session, err
	}
	for _, model := range session.Plan.Candidates {
		if err := ctx.Err(); err != nil {
			return session, err
		}
		if session.Completed(model) {
			continue
		}
		point, used, err := historicalPoint(session.Plan, model, deps.history)
		if err != nil {
			return session, err
		}
		if !used {
			point, err = measureTailorCandidate(ctx, session, model, display, deps)
			if err != nil {
				return session, err
			}
		}
		session.Points = append(session.Points, point)
		if err := store.Save(session); err != nil {
			return session, err
		}
	}
	if deps.closeEvidence != nil {
		if err := deps.closeEvidence(session); err != nil {
			return session, fmt.Errorf("cleanup of the fitting evidence store failed: %w", err)
		}
	}
	session.Phase = fitting.PhaseMeasured
	return session, store.Save(session)
}

func validateFittingTasks(plan fitting.Plan) error {
	tasks, err := autoTaskSpec()
	if err != nil {
		return err
	}
	return plan.ValidateTasks(tasks)
}

func executeTailorPoint(ctx context.Context, job tailorJob) (string, string, error) {
	if job.Endpoint == "" || job.Endpoint == "owned-process" {
		return "", "", errors.New("fitting measurement needs the selected endpoint")
	}
	client := ollama.New()
	client.BaseURL = job.Endpoint
	display := render.New(job.Display)
	defer display.Close()
	display.Phase("endpoint", job.Endpoint+" source "+job.Session.Plan.EndpointSource+" locality "+job.Session.Plan.Locality)
	display.Phase("runtime", client.Version(ctx))
	result, err := execute(ctx, client, job.Model, job.Opts, display)
	if err != nil {
		return "", "", err
	}
	if result == nil || result.Completion == nil || result.Completion.EvidenceSHA256 == "" {
		return "", "", errors.New("fitting evidence has no digest")
	}
	evidence, err := record.CreateManagedStore(record.Store{Dir: resultsDir()}, record.ManagedStoreSpec{
		Schema: record.ManagedStoreSpecSchema, ID: job.Session.ExplorationID, SessionID: job.Session.Plan.ID, Purpose: "exploration",
	})
	if err != nil {
		return "", "", err
	}
	saved, err := evidence.Save(result)
	if err != nil {
		return "", "", err
	}
	return saved.RunID, result.Completion.EvidenceSHA256, nil
}

func closeFittingEvidence(session fitting.Session) error {
	if session.ExplorationID == "" {
		return errors.New("fitting evidence store is missing")
	}
	evidence, err := record.OpenManagedStore(record.Store{Dir: resultsDir()}, session.ExplorationID)
	if err != nil {
		return err
	}
	_, err = evidence.Close()
	return err
}

func fittingRunOpts(plan fitting.Plan) (runOpts, error) {
	if err := plan.Validate(); err != nil {
		return runOpts{}, err
	}
	gb := float64(plan.CapacityBytes) / advise.GiB
	opts := runOpts{
		level: "full", seedSet: plan.SeedSet, reps: plan.Repeats, checksReps: plan.Repeats,
		numCtx: plan.ContextTokens, memoryCtx: plan.ContextTokens,
	}
	opts.validatePrepared = func(run *runExecution) error {
		if run.provenance.SpecSHA256 != plan.TaskScheduleSHA256 {
			return errors.New("fitting task schedule changed; draft a new plan before inference")
		}
		return nil
	}
	switch plan.CapacityKind {
	case fitting.CapacityCeiling:
		opts.capacityBudgetGB = &gb
	case fitting.CapacityReserve:
		opts.capacityReserveGB = &gb
	default:
		return runOpts{}, errors.New("fitting capacity policy is missing")
	}
	return opts, nil
}

func printTailor(session fitting.Session, display string) int {
	recommendation := session.Recommendation()
	lines := append([]string(nil), session.StatusLines()...)
	lines = append(lines, fittingFailureLines(session)...)
	switch render.Resolve(display) {
	case "none":
		return exitOK
	case "json":
		return writeRoleJSON(tailorView{Schema: fitting.ViewSchema, Phase: session.Phase, Lines: lines, Plan: session.Plan, Points: session.Points, Recommendation: recommendation})
	default:
		for _, line := range render.WrapLines(lines, render.Width()) {
			fmt.Fprintln(os.Stdout, line)
		}
		return exitOK
	}
}

func fittingFailureLines(session fitting.Session) []string {
	if session.ExplorationID == "" || len(session.Points) == 0 {
		return nil
	}
	evidence, err := record.OpenManagedStore(record.Store{Dir: resultsDir()}, session.ExplorationID)
	if err != nil {
		return []string{"failure detail is unavailable. The response was not retained and cannot be reconstructed."}
	}
	var lines []string
	for _, point := range session.Points {
		rec, readErr := evidence.Read(point.Model)
		if readErr != nil {
			rec, readErr = evidence.ReadSaved(point.Model)
		}
		if readErr != nil || rec == nil {
			lines = append(lines, point.Model+": failure detail is unavailable. The response was not retained and cannot be reconstructed.")
			continue
		}
		for _, check := range rec.Checks {
			if line := fitting.FailureLine(check); line != "" {
				lines = append(lines, point.Model+": "+line)
			}
		}
	}
	return lines
}

func rememberDelegatedAuto(results, fittingID, autoID string) error {
	store := fitting.Store{Results: results}
	session, err := store.Load(fittingID)
	if err != nil {
		return err
	}
	session.Phase = fitting.PhaseDelegated
	session.AutoSessionID = autoID
	return store.Save(session)
}

func resumeTailorAuto(ctx context.Context, session fitting.Session, display string) int {
	autoSession, err := (automation.Store{Results: resultsDir()}).Open(session.AutoSessionID)
	if err != nil {
		return fittingFailure(err)
	}
	defer func() { _ = autoSession.Close() }()
	if err := resumeAuto(ctx, autoSession, display); err != nil {
		return autoFailure(err)
	}
	return showAuto(session.AutoSessionID, display)
}

func loadTailorCommand(action string, args []string) (fitting.Session, string, int, bool) {
	fs := flag.NewFlagSet("tailor "+action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var display string
	fs.StringVar(&display, "display", "auto", "auto|rich|plain|json|none")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return fitting.Session{}, display, code, false
	}
	if fs.NArg() != 1 || !render.ValidMode(display) {
		errPrint("tailor "+action+" needs the session id", "", "fitr tailor --help")
		return fitting.Session{}, display, exitUsage, false
	}
	session, err := (fitting.Store{Results: resultsDir()}).Load(fs.Arg(0))
	if err != nil {
		return fitting.Session{}, display, fittingFailure(err), false
	}
	return session, display, exitOK, true
}

func parseTailorPlan(args []string) (tailorPlanFlags, int, bool) {
	fs := flag.NewFlagSet("tailor plan", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	fs.Usage = func() { fmt.Fprintln(os.Stdout, tailorGuide) }
	var flags tailorPlanFlags
	fs.StringVar(&flags.role, "role", "", "short name for this work")
	fs.Var(&flags.outcomes, "outcome", "required behavior; repeat for each one")
	fs.Var(&flags.candidates, "candidate", "installed model; repeat for two to four")
	fs.IntVar(&flags.ctxTokens, "ctx", 0, "context window to measure, in tokens")
	fs.StringVar(&flags.budget, "capacity-budget-gb", "", "absolute ceiling in GiB")
	fs.StringVar(&flags.reserve, "capacity-reserve-gb", "", "reserve subtracted from free memory, in GiB")
	fs.StringVar(&flags.memory, "memory-gb", "", "role resident limit in GiB")
	fs.StringVar(&flags.scope, "scope", fitting.ScopeQualify, "qualify or screen")
	fs.StringVar(&flags.endpoint, "endpoint", "", "explicit Ollama base URL")
	fs.StringVar(&flags.kv, "kv", "", "requested cache type f16, q8_0, or q4_0; quantized KV records flash attention on the plan and does not set the server")
	fs.BoolVar(&flags.noFlash, "no-flash-attention", false, "record f16 KV without flash attention on the plan")
	fs.BoolVar(&flags.owned, "approve-owned-runtime", false, "approve an owned runtime for a reserve policy")
	fs.IntVar(&flags.usableBytes, "usable-context-bytes", 0, "document-byte floor; requires context tiers")
	fs.StringVar(&flags.tiers, "context-tiers", "", "document payload sizes in bytes")
	fs.StringVar(&flags.rate, "minimum-rate", "", "success-rate floor for outcomes that have one")
	fs.IntVar(&flags.repeats, "k", 0, "repeats, from 3 to 20")
	fs.StringVar(&flags.display, "display", "auto", "auto|rich|plain|json|none")
	bindTailorWorkloadFlags(fs, &flags)
	if code, ok := parseCommandFlags(fs, args); !ok {
		return tailorPlanFlags{}, code, false
	}
	if fs.NArg() != 0 || !render.ValidMode(flags.display) || (flags.scope != fitting.ScopeQualify && flags.scope != fitting.ScopeScreen) {
		errPrint("invalid tailor plan", "", "fitr tailor --help")
		return tailorPlanFlags{}, exitUsage, false
	}
	return flags, exitOK, true
}

func tailorRequiredAnswers(role string, outcomes tailorOutcomes, candidates autoCandidates, ctxTokens int, budgetText, reserveText, preset string) (int, bool) {
	switch {
	case role == "":
		return tailorQuestion("What should this fitting be called?", "Use a short lowercase name.", "pass --role <name>"), false
	case len(outcomes) == 0 && preset == "":
		return tailorQuestion("What work has to succeed?", "Coding requires independently checked code outcomes. Reasoning text and valid tool-call syntax do not establish it.", "pass --outcome <need> once for each required outcome"), false
	case len(candidates) == 0:
		return tailorQuestion("Which installed model should be measured?", "Nothing is downloaded, and a missing model is not invented.", "pass --candidate <model>. Repeat it for two to four models"), false
	case ctxTokens == 0:
		return tailorQuestion("What context window should be measured?", "There is no default window.", "pass --ctx <tokens>"), false
	case budgetText != "" && reserveText != "":
		return tailorQuestion("Pass either an absolute ceiling or a reserve.", "An absolute ceiling is not a reserve subtracted from free memory.", "use one of --capacity-budget-gb or --capacity-reserve-gb"), false
	case budgetText == "" && reserveText == "":
		return tailorQuestion("What memory policy applies?", "An absolute ceiling is not a reserve subtracted from free memory, and neither one is a hardware allocation cap.", "pass --capacity-budget-gb <GiB> or --capacity-reserve-gb <GiB>"), false
	default:
		return exitOK, true
	}
}

func tailorCapacityAmounts(budgetText, reserveText, memoryText string) (fitting.CapacityKind, int64, int64, int, bool) {
	kind := fitting.CapacityCeiling
	amountText := budgetText
	if reserveText != "" {
		kind = fitting.CapacityReserve
		amountText = reserveText
	}
	amount, err := tailorGiB(amountText, kind == fitting.CapacityCeiling)
	if err != nil {
		errPrint(err.Error(), "", "fitr tailor --help")
		return "", 0, 0, exitUsage, false
	}
	resident := amount
	if kind == fitting.CapacityReserve {
		if memoryText == "" {
			return "", 0, 0, tailorQuestion("What resident size may the role accept?", "A reserve subtracted from free memory is not that limit.", "pass --memory-gb <GiB>"), false
		}
		resident, err = tailorGiB(memoryText, true)
		if err != nil {
			errPrint(err.Error(), "", "fitr tailor --help")
			return "", 0, 0, exitUsage, false
		}
	} else if memoryText != "" {
		resident, err = tailorGiB(memoryText, true)
		if err != nil || resident != amount {
			errPrint("an absolute ceiling and the role resident limit are the same byte count", "", "omit --memory-gb or pass the same GiB value")
			return "", 0, 0, exitUsage, false
		}
	}
	return kind, amount, resident, exitOK, true
}

func historicalPoint(plan fitting.Plan, model string, history func(fitting.Plan, string) (tailorHistory, bool)) (fitting.Point, bool, error) {
	if history == nil {
		return fitting.Point{}, false, nil
	}
	prior, ok := history(plan, model)
	if !ok {
		return fitting.Point{}, false, nil
	}
	decision, err := fitting.ConsiderHistory(plan, model, prior.Seed, prior.ContextTokens, prior.Kind, prior.CapacityBytes, prior.Endpoint)
	if err != nil {
		return fitting.Point{}, false, err
	}
	if decision.Fresh {
		return fitting.Point{}, false, errors.New("historical evidence was labeled fresh confirmation")
	}
	if !decision.Reuse {
		return fitting.Point{}, false, nil
	}
	if prior.Point.Model != model || prior.Point.RunID == "" || !strings.HasPrefix(prior.Point.EvidenceSHA256, "sha256:") {
		return fitting.Point{}, false, errors.New("historical point does not identify this candidate")
	}
	return prior.Point, true, nil
}

func measureTailorCandidate(ctx context.Context, session fitting.Session, model, display string, deps tailorDeps) (fitting.Point, error) {
	opts, err := fittingRunOpts(session.Plan)
	if err != nil {
		return fitting.Point{}, err
	}
	runID, digest, err := deps.runPoint(ctx, tailorJob{Session: session, Model: model, Endpoint: session.Plan.Endpoint, Display: display, Opts: opts})
	if err != nil {
		return fitting.Point{}, err
	}
	if runID == "" || !strings.HasPrefix(digest, "sha256:") {
		return fitting.Point{}, errors.New("fitting evidence is missing its run id or digest")
	}
	return fitting.Point{Model: model, RunID: runID, EvidenceSHA256: digest}, nil
}

func tailorQuestion(msg, note, hint string) int {
	errPrint(msg, note, hint)
	return exitUsage
}

func tailorGiB(text string, positive bool) (int64, error) {
	value, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil {
		return 0, errors.New("capacity value must be a number of GiB")
	}
	return fitting.ParseGiB(value, positive)
}

func fittingFailure(err error) int {
	if errors.Is(err, context.Canceled) {
		errPrint("fitting interrupted", err.Error(), "fitr tailor resume <session-id>")
		return exitInterrupt
	}
	errPrint("fitting stopped: "+err.Error(), "", "fitr tailor --help")
	return exitError
}
