package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/autoruntime"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/fitting"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/render"
)

// errRuntimeMismatch means a session-owned runtime file is already present and
// its bytes are not the spec this plan would write. The file is left as it is.
var errRuntimeMismatch = errors.New("a runtime file already exists and does not match this plan")

func tailorSession(ctx context.Context, args []string, deps tailorDeps) int {
	if len(args) > 0 && fitting.ValidSessionID(args[0]) {
		return continueFitting(ctx, args, deps)
	}
	flags, code, ok := parseTailorPlan(args)
	if !ok {
		return code
	}
	deps.interactive = deps.interactive && sessionCanPrompt(flags.display)
	deps = withPrompt(deps)
	if code, ok = refuseUnsupportedOwned(flags, deps); !ok {
		return code
	}
	flags, code, ok = fillTailorAnswers(ctx, flags, deps)
	if !ok {
		return code
	}
	spec, code, ok := inspectBeforeCreate(ctx, flags, deps)
	if !ok {
		return code
	}
	req, code, ok := requestFromFlags(flags)
	if !ok {
		return code
	}
	session, err := makeTailorSession(req)
	if err != nil {
		return tailorDraftCode(err)
	}
	return previewAndMaybeStart(ctx, session, spec, flags, deps)
}

func continueFitting(ctx context.Context, args []string, deps tailorDeps) int {
	display, approve, code, ok := parseContinueFlags(args[1:])
	if !ok {
		return code
	}
	session, err := (fitting.Store{Results: resultsDir()}).Load(args[0])
	if err != nil {
		return fittingFailure(err)
	}
	if session.Phase != fitting.PhasePreviewed {
		return tailorResume(ctx, []string{args[0], "--display", display}, deps)
	}
	deps.interactive = deps.interactive && sessionCanPrompt(display)
	deps = withPrompt(deps)
	flags := tailorPlanFlags{approve: approve, display: display}
	if code := printTailor(session, display); code != exitOK {
		return code
	}
	if session.Plan.Blocked {
		return exitUnresolved
	}
	if code, ok = approveSealedPlan(ctx, session, flags, deps); !ok {
		return code
	}
	return launchApprovedSession(ctx, session, display, deps)
}

func parseContinueFlags(args []string) (string, bool, int, bool) {
	fs := flag.NewFlagSet("tailor continue", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var display string
	var approve bool
	fs.StringVar(&display, "display", "auto", "auto|rich|plain|json|none")
	fs.BoolVar(&approve, "approve-plan", false, "measure the previewed plan")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return "", false, code, false
	}
	if fs.NArg() != 0 || !render.ValidMode(display) {
		errPrint("tailor resume needs the session id", "", "fitr tailor --help")
		return "", false, exitUsage, false
	}
	return display, approve, exitOK, true
}

func sessionCanPrompt(display string) bool {
	switch render.Resolve(display) {
	case "json", "none":
		return false
	default:
		return true
	}
}

func withPrompt(deps tailorDeps) tailorDeps {
	if !deps.interactive || deps.prompt != nil {
		return deps
	}
	reader := deps.in
	if reader == nil {
		reader = bytes.NewReader(nil)
	}
	deps.prompt = bufio.NewReader(reader)
	return deps
}

func refuseUnsupportedOwned(flags tailorPlanFlags, deps tailorDeps) (int, bool) {
	if !flags.owned {
		return exitOK, true
	}
	err := ownedLaunchError(deps)
	if err == nil {
		return exitOK, true
	}
	errPrint(err.Error(), "The selected endpoint can still host an assessment. fitr does not switch providers or weaken the role.", "omit --approve-owned-runtime")
	return exitUnresolved, false
}

func ownedLaunchError(deps tailorDeps) error {
	if deps.ownedSupported != nil {
		return deps.ownedSupported()
	}
	_, err := autoruntime.Inspect(context.Background(), "", "")
	if err == nil || !strings.Contains(err.Error(), "requires Windows") {
		return nil
	}
	return err
}

func fillTailorAnswers(ctx context.Context, flags tailorPlanFlags, deps tailorDeps) (tailorPlanFlags, int, bool) {
	var code int
	var ok bool
	if flags, code, ok = fillRole(ctx, flags, deps); !ok {
		return flags, code, false
	}
	if flags, code, ok = fillWork(ctx, flags, deps); !ok {
		return flags, code, false
	}
	if flags, code, ok = fillCandidates(ctx, flags, deps); !ok {
		return flags, code, false
	}
	if flags, code, ok = fillContext(ctx, flags, deps); !ok {
		return flags, code, false
	}
	if flags, code, ok = fillMeaning(ctx, flags, deps); !ok {
		return flags, code, false
	}
	if flags, code, ok = fillCapacity(ctx, flags, deps); !ok {
		return flags, code, false
	}
	flags, code, ok = fillOwnedPaths(ctx, flags, deps)
	return flags, code, ok
}

func fillRole(ctx context.Context, flags tailorPlanFlags, deps tailorDeps) (tailorPlanFlags, int, bool) {
	if flags.role != "" {
		return flags, exitOK, true
	}
	answer, code, ok := missingAnswer(ctx, deps, "What should this fitting be called?", "What should this fitting be called?", "Use a short lowercase name.", "pass --role <name>")
	if !ok {
		return flags, code, false
	}
	flags.role = answer
	return flags, exitOK, true
}

func fillWork(ctx context.Context, flags tailorPlanFlags, deps tailorDeps) (tailorPlanFlags, int, bool) {
	if len(flags.outcomes) > 0 || flags.workload != "" {
		return flags, exitOK, true
	}
	answer, code, ok := missingAnswer(ctx, deps, "What work has to succeed? Name one outcome, or a workload preset.", "What work has to succeed?", "Coding requires independently checked code outcomes. Reasoning text and valid tool-call syntax do not establish it.", "pass --outcome <need> once for each required outcome, or --workload <preset>")
	if !ok {
		return flags, code, false
	}
	switch answer {
	case "conversation", "repository-planning", "agentic-coding", "code-review", "document-research", "structured-extraction":
		flags.workload = answer
	default:
		flags.outcomes = append(flags.outcomes, answer)
	}
	return flags, exitOK, true
}

func fillCandidates(ctx context.Context, flags tailorPlanFlags, deps tailorDeps) (tailorPlanFlags, int, bool) {
	if len(flags.candidates) > 0 {
		return flags, exitOK, true
	}
	answer, code, ok := missingAnswer(ctx, deps, "Which installed models should be measured? Separate names with commas.", "Which installed model should be measured?", "Nothing is downloaded, and a missing model is not invented.", "pass --candidate <model>. Repeat it for two to four models")
	if !ok {
		return flags, code, false
	}
	for _, part := range strings.Split(answer, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			flags.candidates = append(flags.candidates, part)
		}
	}
	if len(flags.candidates) == 0 {
		return flags, tailorQuestion("Which installed model should be measured?", "Nothing is downloaded, and a missing model is not invented.", "pass --candidate <model>"), false
	}
	return flags, exitOK, true
}

func fillContext(ctx context.Context, flags tailorPlanFlags, deps tailorDeps) (tailorPlanFlags, int, bool) {
	if flags.ctxTokens != 0 {
		return flags, exitOK, true
	}
	answer, code, ok := missingAnswer(ctx, deps, "What context window should be measured, in tokens?", "What context window should be measured?", "There is no default window.", "pass --ctx <tokens>")
	if !ok {
		return flags, code, false
	}
	parsed, err := strconv.Atoi(answer)
	if err != nil || parsed <= 0 {
		return flags, tailorQuestion("What context window should be measured?", "There is no default window.", "pass --ctx <tokens>"), false
	}
	flags.ctxTokens = parsed
	return flags, exitOK, true
}

func fillMeaning(ctx context.Context, flags tailorPlanFlags, deps tailorDeps) (tailorPlanFlags, int, bool) {
	goal := flags.desired > 0 || flags.minimum > 0 || flags.reserveSystem > 0 || flags.reserveOutput > 0 || flags.reserveReasoning > 0
	if !goal || flags.meaning == fitting.ContextTotalWindow || flags.meaning == fitting.ContextUsableInput {
		return flags, exitOK, true
	}
	answer, code, ok := missingAnswer(ctx, deps, "Does this context mean the total window or the usable input?", "Does this context mean the total window or the usable input?", "A harness floor, a vendor recommendation, and the desired working context are different numbers.", "pass --context-meaning total-window or --context-meaning usable-input")
	if !ok {
		return flags, code, false
	}
	if answer != fitting.ContextTotalWindow && answer != fitting.ContextUsableInput {
		return flags, tailorQuestion("Does this context mean the total window or the usable input?", "A harness floor, a vendor recommendation, and the desired working context are different numbers.", "pass --context-meaning total-window or --context-meaning usable-input"), false
	}
	flags.meaning = answer
	return flags, exitOK, true
}

func fillCapacity(ctx context.Context, flags tailorPlanFlags, deps tailorDeps) (tailorPlanFlags, int, bool) {
	if flags.budget == "" && flags.reserve == "" {
		answer, code, ok := missingAnswer(ctx, deps, "What memory policy applies? Type ceiling <GiB> or reserve <GiB>.", "What memory policy applies?", "An absolute ceiling is not a reserve subtracted from free memory, and neither one is a hardware allocation cap.", "pass --capacity-budget-gb <GiB> or --capacity-reserve-gb <GiB>")
		if !ok {
			return flags, code, false
		}
		budget, reserve, code, ok := parseCapacityAnswer(answer)
		if !ok {
			return flags, code, false
		}
		flags.budget, flags.reserve = budget, reserve
	}
	if flags.reserve != "" && flags.memory == "" {
		answer, code, ok := missingAnswer(ctx, deps, "What resident size may the role accept, in GiB?", "What resident size may the role accept?", "A reserve subtracted from free memory is not that limit.", "pass --memory-gb <GiB>")
		if !ok {
			return flags, code, false
		}
		flags.memory = answer
	}
	return flags, exitOK, true
}

func parseCapacityAnswer(answer string) (string, string, int, bool) {
	fields := strings.Fields(answer)
	if len(fields) != 2 {
		return "", "", tailorQuestion("What memory policy applies?", "An absolute ceiling is not a reserve subtracted from free memory.", "type ceiling <GiB> or reserve <GiB>"), false
	}
	switch fields[0] {
	case "ceiling":
		return fields[1], "", exitOK, true
	case "reserve":
		return "", fields[1], exitOK, true
	default:
		return "", "", tailorQuestion("What memory policy applies?", "An absolute ceiling is not a reserve subtracted from free memory.", "type ceiling <GiB> or reserve <GiB>"), false
	}
}

func fillOwnedPaths(ctx context.Context, flags tailorPlanFlags, deps tailorDeps) (tailorPlanFlags, int, bool) {
	if !flags.owned || (flags.runtimeExe != "" && flags.models != "") {
		return flags, exitOK, true
	}
	if flags.runtimeExe == "" {
		answer, code, ok := missingAnswer(ctx, deps, "Which owned runtime executable should be inspected?", "Which installation should this owned runtime inspect?", "fitr derives a new session file from the inspection and the sealed plan. It does not edit an unrelated runtime file.", "pass --runtime-executable <path> and --models <directory>")
		if !ok {
			return flags, code, false
		}
		flags.runtimeExe = answer
	}
	if flags.models == "" {
		answer, code, ok := missingAnswer(ctx, deps, "Which existing model-store directory should be inspected?", "Which existing model store should be inspected?", "fitr does not create a model store and does not download.", "pass --models <directory>")
		if !ok {
			return flags, code, false
		}
		flags.models = answer
	}
	return flags, exitOK, true
}

func missingAnswer(ctx context.Context, deps tailorDeps, prompt, question, note, hint string) (string, int, bool) {
	answer, code, asked := readTailorAnswer(ctx, deps, prompt)
	if !asked {
		if code != 0 {
			return "", code, false
		}
		return "", tailorQuestion(question, note, hint), false
	}
	if strings.TrimSpace(answer) == "" {
		return "", tailorQuestion(question, note, hint), false
	}
	return answer, exitOK, true
}

func readTailorAnswer(ctx context.Context, deps tailorDeps, prompt string) (string, int, bool) {
	if !deps.interactive {
		return "", 0, false
	}
	if err := ctx.Err(); err != nil {
		errPrint("fitting interrupted", err.Error(), "fitr tailor --help")
		return "", exitInterrupt, false
	}
	if deps.prompt == nil {
		errPrint("fitting stopped before the plan was approved", "Nothing was measured.", "fitr tailor --help")
		return "", exitUsage, false
	}
	fmt.Fprintln(os.Stdout, prompt)
	line, err := deps.prompt.ReadString('\n')
	if err != nil {
		errPrint("fitting stopped before the plan was approved", "Nothing was measured.", "fitr tailor --help")
		return "", exitUsage, false
	}
	return strings.TrimSpace(line), exitOK, true
}

func inspectBeforeCreate(ctx context.Context, flags tailorPlanFlags, deps tailorDeps) (autoruntime.Spec, int, bool) {
	if !flags.owned {
		return autoruntime.Spec{}, exitOK, true
	}
	if strings.TrimSpace(flags.runtimeExe) == "" || strings.TrimSpace(flags.models) == "" {
		return autoruntime.Spec{}, tailorQuestion("Which installation should this owned runtime inspect?", "fitr derives a new session file from the inspection and the sealed plan. It does not edit an unrelated runtime file.", "pass --runtime-executable <path> and --models <directory>"), false
	}
	spec, err := inspectRuntime(ctx, flags.runtimeExe, flags.models, deps)
	if err != nil {
		return autoruntime.Spec{}, fittingFailure(err), false
	}
	return spec, exitOK, true
}

func inspectRuntime(ctx context.Context, executable, modelStore string, deps tailorDeps) (autoruntime.Spec, error) {
	if deps.inspectRuntime != nil {
		return deps.inspectRuntime(ctx, executable, modelStore)
	}
	return autoruntime.Inspect(ctx, executable, modelStore)
}

func previewAndMaybeStart(ctx context.Context, session fitting.Session, spec autoruntime.Spec, flags tailorPlanFlags, deps tailorDeps) int {
	session, extras, err := stampTailorPreview(ctx, session, spec, deps)
	if err != nil {
		return fittingFailure(err)
	}
	if code := printTailorLines(session, flags.display, extras); code != exitOK {
		return code
	}
	if session.Plan.Blocked {
		return exitUnresolved
	}
	if code, ok := approveSealedPlan(ctx, session, flags, deps); !ok {
		return code
	}
	return launchApprovedSession(ctx, session, flags.display, deps)
}

func stampTailorPreview(ctx context.Context, session fitting.Session, spec autoruntime.Spec, deps tailorDeps) (fitting.Session, []string, error) {
	extras := previewExtras(ctx, session.Plan, deps)
	if session.Plan.OwnedRuntime {
		session.RuntimeExecutable = spec.Executable
		session.ModelStore = spec.ModelStore
		session.InspectedExecutableSHA256 = spec.ExecutableSHA256
		session.InspectedLibrariesSHA256 = spec.LibrariesSHA256
		session.InspectedRuntimeVersion = spec.RuntimeVersion
		extras = append(extras, fmt.Sprintf("owned installation observed executable %s libraries %s version %s. This is an inspection, not a recommendation. The path stays in the session file and is not copied into this receipt.", spec.ExecutableSHA256, spec.LibrariesSHA256, spec.RuntimeVersion))
	}
	names, listed, note := previewInventory(ctx, session, deps)
	if note != "" {
		extras = append(extras, note)
	}
	session.InventoryChecked = listed
	session.InventoryNames = names
	if err := (fitting.Store{Results: resultsDir()}).Save(session); err != nil {
		return session, nil, err
	}
	return session, extras, nil
}

func previewExtras(ctx context.Context, plan fitting.Plan, deps tailorDeps) []string {
	memory := "free memory was not read for this preview. The capacity line is the sealed policy, not a hardware allocation cap."
	if deps.memoryLine != nil {
		memory = deps.memoryLine(ctx)
	}
	why := "candidates " + strings.Join(plan.Candidates, ", ") + " were named for this plan. Inclusion is not a recommendation."
	if len(plan.Candidates) == 1 {
		why = "candidate " + plan.Candidates[0] + " was named for this plan. A second candidate was not invented. This is a non-comparative assessment."
	}
	return []string{
		memory,
		why,
		"downloads off. API spending off. External restart, paid routes, and adoption stay separate approvals.",
	}
}

func previewInventory(ctx context.Context, session fitting.Session, deps tailorDeps) ([]string, bool, string) {
	if session.Plan.OwnedRuntime || deps.listModels == nil {
		return nil, false, ""
	}
	names, err := deps.listModels(ctx, session.Plan.Endpoint)
	if err != nil {
		return nil, false, "installed models were not listed. Nothing was invented from that failure."
	}
	return names, true, inventoryLine(names)
}

func inventoryLine(names []string) string {
	if len(names) == 0 {
		return "installed models observed: none. A missing model is not downloaded."
	}
	return "installed models observed: " + strings.Join(names, ", ") + ". A listed name is not a recommendation."
}

func approveSealedPlan(ctx context.Context, session fitting.Session, flags tailorPlanFlags, deps tailorDeps) (int, bool) {
	if flags.approve {
		return exitOK, true
	}
	if !deps.interactive {
		errPrint("the plan is previewed and has not started", "", "fitr tailor "+session.Plan.ID+" --approve-plan")
		return exitUsage, false
	}
	if err := ctx.Err(); err != nil {
		errPrint("fitting interrupted", err.Error(), "fitr tailor "+session.Plan.ID+" --approve-plan")
		return exitInterrupt, false
	}
	fmt.Fprintln(os.Stdout, "Approve this sealed plan and start measurement? Type yes to measure.")
	if deps.prompt == nil {
		errPrint("fitting stopped before the sealed plan was approved", "Nothing was measured.", "fitr tailor start "+session.Plan.ID+" --approve-plan")
		return exitUsage, false
	}
	line, err := deps.prompt.ReadString('\n')
	if err != nil || !strings.EqualFold(strings.TrimSpace(line), "yes") {
		errPrint("fitting stopped before the sealed plan was approved", "Nothing was measured.", "fitr tailor start "+session.Plan.ID+" --approve-plan")
		return exitUsage, false
	}
	return exitOK, true
}

func launchApprovedSession(ctx context.Context, session fitting.Session, display string, deps tailorDeps) int {
	if err := recheckInventory(ctx, session, deps); err != nil {
		errPrint(err.Error(), "Nothing was measured.", "fitr tailor status "+session.Plan.ID)
		return exitUnresolved
	}
	if session.Plan.OwnedRuntime {
		return launchOwnedSession(ctx, session, display, deps)
	}
	return runApprovedFitting(ctx, session, display, deps)
}

func recheckInventory(ctx context.Context, session fitting.Session, deps tailorDeps) error {
	if !session.InventoryChecked || deps.listModels == nil || session.Plan.OwnedRuntime {
		return nil
	}
	names, err := deps.listModels(ctx, session.Plan.Endpoint)
	if err != nil {
		return errors.New("installed model inventory could not be read again before start")
	}
	if !sameNames(names, session.InventoryNames) {
		return errors.New("installed model inventory changed between preview and start")
	}
	return nil
}

func sameNames(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	a := append([]string(nil), left...)
	b := append([]string(nil), right...)
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func launchOwnedSession(ctx context.Context, session fitting.Session, display string, deps tailorDeps) int {
	if session.RuntimeExecutable == "" || session.ModelStore == "" {
		return tailorQuestion("Which installation should this owned runtime inspect?", "fitr derives a new session file from the inspection and the sealed plan. It does not edit an unrelated runtime file.", "pass --runtime-executable <path> and --models <directory>")
	}
	spec, err := inspectRuntime(ctx, session.RuntimeExecutable, session.ModelStore, deps)
	if err != nil {
		return fittingFailure(err)
	}
	if runtimeChanged(session, spec) {
		errPrint("owned runtime executable, libraries or version changed between preview and start", "The existing runtime file was not replaced.", "inspect the installation again and draft a new plan")
		return exitUnresolved
	}
	applied, err := fitting.ApplyToRuntime(session.Plan, spec)
	if err != nil {
		return fittingFailure(err)
	}
	path, err := sessionRuntimeFile(session, applied)
	if errors.Is(err, errRuntimeMismatch) {
		errPrint(err.Error(), "fitr did not replace it.", "draft a new plan")
		return exitUsage
	}
	if err != nil {
		return fittingFailure(err)
	}
	return startOwnedFitting(ctx, session, path, display, deps)
}

func runtimeChanged(saved fitting.Session, spec autoruntime.Spec) bool {
	if saved.InspectedExecutableSHA256 == "" && saved.InspectedLibrariesSHA256 == "" && saved.InspectedRuntimeVersion == "" {
		return false
	}
	return spec.ExecutableSHA256 != saved.InspectedExecutableSHA256 ||
		spec.LibrariesSHA256 != saved.InspectedLibrariesSHA256 ||
		spec.RuntimeVersion != saved.InspectedRuntimeVersion
}

func sessionRuntimeFile(session fitting.Session, applied autoruntime.Spec) (string, error) {
	path, err := (fitting.Store{Results: resultsDir()}).RuntimeSpecPath(session.Plan.ID)
	if err != nil {
		return "", err
	}
	err = writeExclusiveRuntime(path, applied)
	if err == nil {
		return path, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return "", err
	}
	return reuseRuntimeFile(path, applied)
}

func reuseRuntimeFile(path string, applied autoruntime.Spec) (string, error) {
	loaded, err := loadAutoRuntime(path)
	if err != nil {
		return "", err
	}
	if loaded.NumCtx != applied.NumCtx || loaded.ReserveBytes != applied.ReserveBytes || loaded.ExecutableSHA256 != applied.ExecutableSHA256 || loaded.KVCacheType != applied.KVCacheType || loaded.FlashAttention != applied.FlashAttention {
		return "", errRuntimeMismatch
	}
	return path, nil
}

func writeExclusiveRuntime(path string, spec autoruntime.Spec) error {
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if joined := errors.Join(writeErr, syncErr, closeErr); joined != nil {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return errors.Join(joined, removeErr)
		}
		return joined
	}
	return nil
}

func tailorObservedMemory(ctx context.Context) string {
	at := time.Now().UTC().Format(time.RFC3339Nano)
	available, source, ok := device.SystemMemoryAvailable(ctx)
	if !ok || available <= 0 || strings.TrimSpace(source) == "" {
		return "free memory unmeasured at " + at + ". The capacity line is the sealed policy, not a hardware allocation cap."
	}
	return fmt.Sprintf("free memory %s at %s (%s). This reading is not the sealed policy and is not a hardware allocation cap.", fitting.FormatGiB(available), at, source)
}

func tailorListInstalled(ctx context.Context, endpoint string) ([]string, error) {
	if endpoint == "" || endpoint == "owned-process" {
		return nil, errors.New("this plan has no selected endpoint to list")
	}
	client := ollama.New()
	client.BaseURL = endpoint
	tags, err := client.Tags(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag.Name != "" {
			names = append(names, tag.Name)
		}
	}
	return names, nil
}
