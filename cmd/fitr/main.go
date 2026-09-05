// Command fitr determines which local AI configuration works for a declared
// workload on this machine and shows the evidence behind that decision.
//
// Conventions borrowed from tools that got them right:
//   - progress to stderr, results to stdout, so output is pipeable (promptfoo)
//   - errors are error/note/hint, plain text on stderr even under --json;
//     nobody wraps diagnostics in JSON. The exit code is the machine channel
//     (rustc, gh, uv)
//   - -q silences chrome, -v hides the progress display so it cannot interleave
//     with diagnostics (uv)
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/advise"
	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/atomicfile"
	"github.com/blisspixel/fitr/internal/buildinfo"
	"github.com/blisspixel/fitr/internal/calibration"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/modelref"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/retonr"
	"github.com/blisspixel/fitr/internal/score"
	"github.com/blisspixel/fitr/internal/stats"
)

var version = buildinfo.Version()

// Exit codes: small, documented, domain-specific. Not sysexits -- nobody uses it.
const (
	exitOK         = 0 // ran, and every measured need passed
	exitError      = 1 // something broke
	exitUsage      = 2 // bad invocation
	exitGates      = 3 // ran fine, but a need FAILED (useful as a CI gate)
	exitUnresolved = 4 // required decision evidence is unresolved or blocked
	exitInterrupt  = 130
)

func errPrint(msg, note, hint string) {
	fmt.Fprintf(os.Stderr, "error: %s\n", render.SingleLine(msg))
	if note != "" {
		fmt.Fprintf(os.Stderr, " note: %s\n", render.SingleLine(note))
	}
	if hint != "" {
		fmt.Fprintf(os.Stderr, " hint: %s\n", render.SingleLine(hint))
	}
}

func terminalText(value string) string { return render.SingleLine(value) }

const usageText = ` - evidence for what local AI works on this machine

usage:
  fitr [--display MODE]             installed models, evidence, next command
  fitr <model>                      named advise (same as fitr advise <model>)
  fitr advise [model] [--vram-gb N] [--ctx N] [--load] [--fit]
  fitr run <model> [--quick|--full|--checks-only] [-k N] [--ctx N] [--profile P] [--display MODE] [--html]
  fitr apply [model] [--ctx N]
  fitr tune [model-a model-b]
  fitr export <model> [--out PATH] [--retonr]
  fitr view [model|result.json] [--display MODE]
  fitr board [--current]
  fitr top [--view live|result|board|history|inventory]
  fitr top view [model|result.json]
  fitr top run <model> [run flags]
  fitr top history [path|clear --yes]
  fitr diag <model>
  fitr doctor <model> [-n N]
  fitr device
  fitr update [--check] [--reinstall]
  fitr profiles [new [name]]
  fitr calibrate <model-a> <model-b> [--out PATH] [--lineage PATH]
  fitr calibrate merge <pair.json>... [--out PATH]
  fitr compare <model-a> <model-b>
  fitr decide [model|result.json] --spec decision.json [--display MODE]
  fitr experiment context <model> --ctx 4096,8192,... [-k N]
  fitr experiment context <result.json> <result.json>... [--display MODE]
  fitr experiment quant <result.json> <result.json>... --spec decision.json [--lineage conversion.json]
  fitr experiment confirm <model-a> <model-b> --spec decision.json [--ctx N] [-k N]
  fitr experiment confirm <confirmation-bundle.json> [--display MODE]
  fitr experiment workload <model> [-n N] [--ctx N] [--backend B]
  fitr experiment workload <workload-bundle.json> [--display MODE]
  fitr discover add <source> --role <role> [--model <reference>] [--harness <name>]
  fitr discover list|plan [--role <role>] [--display MODE]
  fitr role init <name> --quality <need> --memory-gb <limit> [--ctx N]
  fitr role define <role.json> | list | show <name> | review <name>
  fitr role attach <name> <result.json> | detach <name> <evidence-sha256>
  fitr role confirm <name|bundle.json> | adopt <name> <bundle.json> | status <name> | rollback <name>
  fitr mcp serve
  fitr source resolve hf --repo <owner/model> --revision <revision> --file <path> --out <receipt.json>
  fitr source show <receipt.json> [--display MODE]
  fitr cleanup plan <directory> [--min-age-days N] [--display MODE]

flags:
  --display  auto|rich|plain|json|none   output mode (default auto)
  --backend  auto|ollama|llama-server|openai   serving runtime (default auto-detect)
  --pull     fetch a missing model first (Ollama; HF repository aliases pull automatically)
  --allow-unsafe-exec  run unisolated built-in code diagnostics; never score them
  -k         repeats per noisy task (default 3; 1 quick; 5 checks-only)
             A single run is not a measurement: identical configs vary 10-20pp.
  -q         quiet (repeat for silent)      -v  verbose

exit codes:
  0 eligible/ok   1 error   2 usage   3 disproven   4 unresolved   130 interrupted

environment:
  FITR_OPENAI_URL       OpenAI-compatible endpoint
  FITR_OPENAI_API_KEY   bearer token; kept out of command history and process arguments
  FITR_OPENAI_MODEL_SHA256  independently obtained model digest required for measured OpenAI-compatible runs

examples:
  fitr
  fitr qwen3:30b
  fitr advise qwen3:30b --display json
  fitr run qwen3-coder:30b
  fitr run https://huggingface.co/bartowski/Qwen2.5-Coder-7B-Instruct-GGUF
  fitr run some-new-model:tag -k 3 --pull
  fitr diag dolphin3:8b
  fitr doctor qwen3-coder:30b
  fitr advise qwen3:30b
  fitr advise ./model.gguf --vram-gb 8 --fit
  fitr run qwen3:30b --ctx 4096
  fitr apply qwen3:30b
  fitr update --check
  fitr tune
  fitr tune qwen3:30b qwen3:30b-q8
  fitr export qwen3:30b --out scorecard.html
  fitr view qwen3:30b
  fitr profiles new
  fitr run qwen3:8b-q8_0 --checks-only --seedset qwen3-8b -k 5
  fitr run qwen3:8b-q4_K_M --checks-only --seedset qwen3-8b -k 5
  fitr calibrate qwen3:8b-q8_0 qwen3:8b-q4_K_M --out pair.json --lineage conversion.json
  fitr experiment quant q8-result.json q4-result.json --spec coding.json --lineage conversion.json
  fitr experiment confirm qwen3:8b-q8_0 qwen3:8b-q4_K_M --spec coding-confirm.json
  fitr experiment workload qwen3-coder:30b -n 3
`

func usage() { fmt.Fprint(os.Stderr, "fitr "+version+usageText) }

func main() { os.Exit(run()) }

func run() int {
	if len(os.Args) < 2 {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		return cmdStatus(ctx, nil)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if handler := commandHandler(os.Args[1]); handler != nil {
		return handler(ctx, os.Args[2:])
	}
	switch os.Args[1] {
	case "-h", "--help", "help":
		usage()
		return exitOK
	case "--version", "version":
		fmt.Println("fitr", version)
		return exitOK
	default:
		if strings.HasPrefix(os.Args[1], "-") {
			return cmdStatus(ctx, os.Args[1:])
		}
		// A positional that is not a command is a model: the named advise.
		return cmdAdvise(ctx, os.Args[1:])
	}
}

type commandFunc func(context.Context, []string) int

func commandHandler(name string) commandFunc {
	switch name {
	case "run":
		return cmdRun
	case "advise":
		return cmdAdvise
	case "apply":
		return cmdApply
	case "tune":
		return cmdTune
	case "export":
		return cmdExport
	case "view":
		return cmdView
	case "board":
		return cmdBoard
	case "top":
		return cmdTop
	case "diag":
		return cmdDiag
	case "doctor":
		return cmdDoctor
	case "device":
		return cmdDevice
	case "update":
		return cmdUpdate
	case "profiles":
		return cmdProfiles
	case "calibrate":
		return cmdCalibrate
	case "compare":
		return cmdCompare
	case "decide", "discover", "role", "experiment", "cleanup", "mcp", "source":
		return planningCommandHandler(name)
	case "screenshots": // dev-only: regenerate docs/assets from mock data
		return cmdScreenshots
	}
	return nil
}

func planningCommandHandler(name string) commandFunc {
	switch name {
	case "decide":
		return cmdDecide
	case "discover":
		return cmdDiscover
	case "role":
		return cmdRole
	case "mcp":
		return cmdMCP
	case "source":
		return cmdSource
	case "experiment":
		return cmdExperiment
	case "cleanup":
		return cmdCleanup
	default:
		return nil
	}
}

// permute moves positional arguments after flags.
//
// Go's flag package stops parsing at the first non-flag argument, so
// `fitr run mymodel --quick` would silently ignore --quick. That is the
// natural way to type it, so reorder rather than making the user adapt.
func permute(args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			// A value-taking flag written as `-k 3` consumes the next token.
			if !strings.Contains(a, "=") && i+1 < len(args) &&
				!strings.HasPrefix(args[i+1], "-") && takesValue(a) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positional = append(positional, a)
	}
	return append(flags, positional...)
}

func takesValue(flagArg string) bool {
	name := strings.TrimLeft(flagArg, "-")
	switch name {
	case "k", "n", "profile", "display", "backend", "seedset", "vram-gb", "ctx", "out", "lineage", "view", "spec",
		"capacity-budget-gb", "capacity-reserve-gb", "model", "role", "harness", "claim", "repo", "revision", "file", "source",
		"quality", "minimum-rate", "memory-gb", "max-age-days", "min-age-days":
		return true
	}
	return false
}

// parseCommandFlags keeps subcommand help on the success path. The standard
// flag package writes the requested help before returning flag.ErrHelp; that
// is not a bad invocation and must not become exit 2.
func parseCommandFlags(fs *flag.FlagSet, args []string) (int, bool) {
	err := fs.Parse(permute(args))
	if err == nil {
		if err := validateFlagModelRefs(fs); err != nil {
			errPrint(err.Error(), "", hfModelRefHint)
			return exitUsage, false
		}
		return exitOK, true
	}
	if errors.Is(err, flag.ErrHelp) {
		return exitOK, false
	}
	return exitUsage, false
}

type countFlag int

func (c *countFlag) String() string { return strconv.Itoa(int(*c)) }

func (c *countFlag) Set(value string) error {
	switch value {
	case "true":
		*c++
		return nil
	case "false":
		*c = 0
		return nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return errors.New("quiet level must be a non-negative integer")
	}
	*c = countFlag(n)
	return nil
}

func (*countFlag) IsBoolFlag() bool { return true }

const hfModelRefHint = "keep the exact link with `fitr discover add <source> --role <role>`; " +
	"load that artifact and pass its served name, or explicitly choose an unpinned hf.co/owner/repo[:quant] alias"

// normalizeModelRef rewrites only unpinned repository aliases. Unsupported
// URLs remain intact so no caller can silently discard an artifact identity.
func normalizeModelRef(model string) string {
	normalized, err := normalizedModelRef(model)
	if err != nil {
		return strings.TrimSpace(model)
	}
	return normalized
}

func normalizedModelRef(model string) (string, error) {
	m := strings.TrimSpace(model)
	lower := strings.ToLower(m)
	for _, prefix := range []string{
		"https://www.huggingface.co/",
		"http://www.huggingface.co/",
		"https://huggingface.co/",
		"http://huggingface.co/",
		"www.huggingface.co/",
		"huggingface.co/",
		"https://hf.co/",
		"http://hf.co/",
		"hf.co/",
	} {
		if strings.HasPrefix(lower, prefix) {
			return parseHFPath(strings.TrimRight(m[len(prefix):], "/"))
		}
	}
	return m, nil
}

func parseHFPath(path string) (string, error) {
	parts := strings.Split(path, "/")
	if len(parts) == 4 && parts[2] == "tree" && parts[3] == "main" {
		parts = parts[:2]
	}
	if len(parts) == 2 {
		repo, tag, hasTag := strings.Cut(parts[1], ":")
		if validHFAliasPart(parts[0]) && validHFAliasPart(repo) && (!hasTag || validHFAliasPart(tag)) {
			return "hf.co/" + strings.Join(parts, "/"), nil
		}
	}
	return "", errors.New("unsupported Hugging Face model reference: exact file, revision, and URL selectors cannot be preserved by a repository alias")
}

func validHFAliasPart(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && char != '-' && char != '_' && char != '.' {
			return false
		}
	}
	return true
}

func validateModelRefs(models ...string) error {
	for _, model := range models {
		if _, err := normalizedModelRef(model); err != nil {
			return err
		}
	}
	return nil
}

func validateFlagModelRefs(fs *flag.FlagSet) error {
	// Discovery captures source URLs as inert ideas, and other commands accept
	// local paths or prose. Only model-bearing positional arguments are checked.
	switch fs.Name() {
	case "advise", "apply", "export", "view", "diag", "doctor", "tune", "calibrate", "decide", "top view",
		"experiment confirm", "experiment workload", "experiment context":
		return validateModelRefs(fs.Args()...)
	default:
		return nil
	}
}

func quantFromFilename(file string) string {
	lower := strings.ToLower(file)
	if !strings.HasSuffix(lower, ".gguf") {
		return ""
	}
	base := file[:len(file)-len(".gguf")]
	// Underscores live inside the quant name (Q4_K_M, IQ4_XS); only '-' and
	// '.' separate the quant from the rest of the filename.
	i := strings.LastIndexAny(base, "-.")
	if i < 0 {
		if isQuantTag(base) {
			return strings.ToUpper(base)
		}
		return ""
	}
	cand := base[i+1:]
	if isQuantTag(cand) {
		return strings.ToUpper(cand)
	}
	return ""
}

func isQuantTag(s string) bool {
	u := strings.ToUpper(s)
	switch u {
	case "F16", "F32", "BF16":
		return true
	}
	var rest string
	switch {
	case strings.HasPrefix(u, "IQ"):
		rest = u[2:]
	case strings.HasPrefix(u, "Q"):
		rest = u[1:]
	default:
		return false
	}
	return rest != "" && rest[0] >= '0' && rest[0] <= '9'
}

func isHFRef(model string) bool { return strings.HasPrefix(model, "hf.co/") }

func isLocalGGUF(p string) bool {
	if !strings.HasSuffix(strings.ToLower(p), ".gguf") {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// ---------------------------------------------------------------- advise

func emptyDash(s string) string {
	if s == "" {
		return "(empty)"
	}
	return s
}

func latestNamed(all []*Result, name string) *Result {
	var hit *Result
	for _, r := range all {
		if modelref.SameServed(name, r.Model) {
			if hit == nil || startedAfter(r.StartedAt, hit.StartedAt) {
				hit = r
			}
		}
	}
	return hit
}

func startedAfter(candidate, current string) bool {
	candidateTime, candidateErr := time.Parse(time.RFC3339Nano, candidate)
	currentTime, currentErr := time.Parse(time.RFC3339Nano, current)
	if candidateErr == nil && currentErr == nil {
		return candidateTime.After(currentTime)
	}
	return candidateErr == nil && currentErr != nil
}

func runResultExitCode(res *Result, level string, saveErr, artifactErr error) int {
	if saveErr != nil || artifactErr != nil {
		return exitError
	}
	if level == "checks" {
		return exitOK
	}
	if res != nil && res.Scorecard.Fails > 0 {
		return exitGates
	}
	return exitOK
}

func runFailureHint(err error, model string) string {
	message := ""
	if err != nil {
		message = err.Error()
	}
	if strings.Contains(message, "FITR_OPENAI_MODEL_SHA256") {
		return "set FITR_OPENAI_MODEL_SHA256 from an independent SHA-256 and configure the endpoint to report the same digest"
	}
	if strings.Contains(message, "resolved model") || strings.Contains(message, "model identity") {
		return "run `fitr` to confirm the served model name and artifact identity, then retry"
	}
	return "fix the error above and retry; `fitr doctor " + model + "` checks serving-runtime health"
}

// ---------------------------------------------------------------- result
// Result remains an alias while command code moves onto the internal record
// boundary. Existing tests and helper signatures keep their source shape, and
// persisted JSON remains compatible with schema 4.
type Result = record.Record

// runOpts carries the run configuration so execute's signature stays legible.
type runOpts struct {
	level, profile, seedSet string
	reps, checksReps        int
	numCtx, memoryCtx       int
	experiment              *record.ExperimentBinding
	capacityBudgetGB        *float64
	capacityReserveGB       *float64
	allowUnsafeExec         bool
	validatePrepared        func(*runExecution) error
	validateCapacity        func(*runExecution) error
	validateContext         func(*runExecution) error
}

// liveTelemetry is an optional extension implemented by the full-screen
// display. Regular rich, plain, JSON, and silent displays are unchanged.
type liveTelemetry interface {
	LiveProgress(completed, total int, detail string)
	LiveSpeed(sample eval.SpeedResult, completed, total int)
	LiveMemory(residentGiB float64)
}

type runFailureTelemetry interface{ RunFailed(error) }
type runSaveTelemetry interface{ RunSaveStatus(bool, error) }
type runIdentityTelemetry interface{ RunID() string }

func runTaskPlan(level string, repeats, checkRepeats, checks, refusalPrompts int) record.TaskPlan {
	plan := record.TaskPlan{}
	if level != "checks" {
		plan.SpeedSamples = repeats
		plan.Memory = true
		plan.CodeTrials = 2 * repeats
		plan.Plumbing = true
		plan.ToolTrials = repeats
	}
	if level != "quick" {
		plan.CheckTrialsLimit = checks * checkRepeats
	}
	if level != "quick" && level != "checks" {
		plan.Withdrawal = true
		plan.RefusalTrials = refusalPrompts
	}
	if level == "full" {
		plan.AgenticTrials = 1
	}
	return plan
}

// ---------------------------------------------------------------- storage
func resultsDir() string {
	return record.DefaultDir()
}

func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

func save(r *Result) (string, error) {
	saved, err := record.NewStore(resultsDir()).Save(r)
	if err != nil {
		return "", err
	}
	return saved.CanonicalPath, nil
}

func loadResults() ([]*Result, error) {
	loaded, err := record.NewStore(resultsDir()).LoadCurrent()
	if err != nil {
		return nil, err
	}
	// The legacy command surface reads canonical latest results only. History is
	// intentionally exposed by `fitr top`, so deleting a canonical file retains
	// its established meaning for board, view, compare, calibrate, and export.
	seen := map[string]bool{}
	out := make([]*Result, 0, len(loaded.Records))
	for _, result := range loaded.Records {
		if seen[result.Model] {
			continue
		}
		seen[result.Model] = true
		out = append(out, result)
	}
	return out, nil
}

func writeRetonrEvidence(r *Result, dest, jsonPath string) error {
	a, err := artifactFrom(r)
	if err != nil {
		return err
	}
	plumbing := ""
	if r.Plumbing != nil {
		plumbing = r.Plumbing.Verdict
	}
	ev := retonr.FromScorecard(version, r.Model,
		r.ModelMeta.Details.QuantizationLevel, r.ModelMeta.Details.Family,
		r.ModelMeta.Details.ParameterSize, r.Level, r.Repeats,
		r.Device, r.DeviceKey, a.Profile, a.Scorecard, plumbing, jsonPath)
	b, err := json.MarshalIndent(ev, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(dest, append(b, '\n'), 0o644)
}

func artifactFrom(r *Result) (render.Artifact, error) {
	var (
		prof device.Profile
		sc   score.Scorecard
		err  error
	)
	if r.SchemaVersion >= record.EvidenceSchemaVersion {
		prof, err = r.OriginalProfile()
		sc = r.Scorecard
	} else {
		prof, err = device.SelectProfile(r.Profile, r.Device)
		if err == nil {
			sc = score.Score(measure(r), prof)
		}
	}
	if err != nil {
		return render.Artifact{}, err
	}
	if issue := r.EvidenceIntegrityIssue(); issue != "" {
		sc = score.ExcludeEvidence(sc, issue)
	}
	meta := resultMeta(r, prof.Name)
	modelLabel := presentationModelLabel(r.Model)
	sc.Model = modelLabel
	toolsBlocked := false
	for need, verdict := range sc.Needs {
		if strings.Contains(need, "tool") && verdict.State == score.Blocked {
			toolsBlocked = true
			break
		}
	}
	nextCommand := advise.ResultNext(modelLabel, r.Repeats, r.ContextSize(), r.Level, toolsBlocked)
	if meta.Analysis != nil {
		nextCommand = ""
		if len(meta.Analysis.NextActions) > 0 {
			nextCommand = analysisActionCommand(meta.Analysis.NextActions[0], modelLabel)
		}
	}
	return render.Artifact{
		FitrVersion:   version,
		SchemaVersion: r.SchemaVersion,
		Model:         modelLabel,
		StartedAt:     r.StartedAt,
		Level:         r.Level,
		Repeats:       r.Repeats,
		WallSeconds:   r.WallSeconds,
		Device:        render.NewShareDevice(r.Device),
		DeviceKey:     render.ShareFingerprintID(r.DeviceKey),
		Profile:       prof.Name,
		Scorecard:     sc,
		Meta:          render.NewShareMeta(meta),
		NextCommand:   nextCommand,
		Contamination: presentationModelLabels(r.Contamination),
	}, nil
}

func presentationModelLabels(models []string) []string {
	out := make([]string, 0, len(models))
	for _, model := range models {
		out = append(out, presentationModelLabel(model))
	}
	return out
}

func resultMeta(r *Result, profile string) render.Meta {
	meta := render.Meta{
		ParamSize: r.ModelMeta.Details.ParameterSize,
		Quant:     r.ModelMeta.Details.QuantizationLevel,
		Family:    r.ModelMeta.Details.Family,
		GPU:       r.Device.GPU, Driver: r.Device.GPUDriver,
		Device: r.Device.InferenceDevice, Profile: profile,
		StartedAt: r.StartedAt, Level: r.Level, WallSeconds: r.WallSeconds,
		NumCtx:      resultNumCtx(r),
		Repeats:     r.Repeats,
		Calibration: r.Level == "checks",
	}
	if report, err := analysis.FromRecord(r); err == nil {
		meta.Analysis = &report
	} else if r.SchemaVersion < record.EvidenceSchemaVersion {
		populateLegacyResultMeta(&meta, r)
	}
	// The caption explaining what a range is only earns its line when a range
	// is actually on screen.
	for _, v := range r.Scorecard.Needs {
		if strings.Contains(v.Why, "[") && strings.Contains(v.Why, "-") {
			meta.ShowsIntervals = true
			break
		}
	}
	return render.ResolvedMeta(meta)
}

func populateLegacyResultMeta(meta *render.Meta, r *Result) {
	meta.DecodeMean, meta.DecodeSD = r.DecodeSum.Mean, r.DecodeSum.SD
	meta.DecodeMin, meta.DecodeMax, meta.DecodeN = r.DecodeSum.Min, r.DecodeSum.Max, r.DecodeSum.N
	meta.PrefillMean, meta.PrefillSD, meta.PrefillN = r.PrefillSum.Mean, r.PrefillSum.SD, r.PrefillSum.N
	meta.TTFTMean, meta.TTFTSD, meta.TTFTN = r.TTFTSum.Mean, r.TTFTSum.SD, r.TTFTSum.N
	if resident, verified := r.Memory.VerifiedAt(memoryProbeCtx); verified {
		meta.ResidentGB, meta.ResidentContext = resident, memoryProbeCtx
	}
	if r.DeviceV2 != nil {
		meta.ContextState = string(r.DeviceV2.Context.State())
		if r.DeviceV2.Context.EffectiveTokens != nil {
			meta.EffectiveCtx = *r.DeviceV2.Context.EffectiveTokens
		}
	}
	for _, sample := range r.Speed {
		meta.DecodeSeries = append(meta.DecodeSeries, sample.DecodeTPS)
		meta.PrefillSeries = append(meta.PrefillSeries, sample.PrefillTPS)
		meta.TTFTSeries = append(meta.TTFTSeries, sample.TTFT)
	}
	meta.FirstRunSlow, meta.FirstRunRatio = stats.FirstRunSlow(meta.DecodeSeries)
}

func analysisActionCommand(action analysis.Action, model string) string {
	args := make([]string, 0, len(action.Argv))
	for _, arg := range action.Argv {
		if arg == analysis.CurrentModelPlaceholder {
			arg = model
		}
		args = append(args, shellCommandArg(arg, runtime.GOOS))
	}
	return strings.Join(args, " ")
}

// shellCommandArg renders one inert argv element for the shell convention of
// the built target. Single-quoted strings are literal in PowerShell and POSIX
// shells; only the way an embedded quote is represented differs. Keeping the
// safe set deliberately narrow prevents a model filename from turning a
// displayed next action into command substitution or another shell operator.
func shellCommandArg(arg, goos string) string {
	if safeUnquotedCommandArg(arg) {
		return arg
	}
	if goos == "windows" {
		return "'" + strings.ReplaceAll(arg, "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(arg, "'", `'"'"'`) + "'"
}

func safeUnquotedCommandArg(arg string) bool {
	if arg == "" {
		return false
	}
	for _, r := range arg {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			strings.ContainsRune("-._/:=+", r) {
			continue
		}
		return false
	}
	return true
}

func hasSupportedBoardDecode(result *Result) bool {
	if result == nil {
		return false
	}
	report, err := analysis.FromRecord(result)
	if err != nil {
		return false
	}
	observation := report.Performance.DecodeTPS
	return observation.Estimate != nil && observation.Status == analysis.StatusAvailable &&
		slices.Contains(observation.Supports, analysis.ClaimObservedDecode)
}

// writeHTMLArtifact is a no-op unless the caller asked. dest "auto" writes
// next to the JSON result; an explicit path is used as-is.
func writeHTMLArtifact(r *Result, dest, jsonPath string) (string, error) {
	if dest == "" {
		return "", nil
	}
	a, err := artifactFrom(r)
	if err != nil {
		return "", err
	}
	if dest == "auto" {
		if jsonPath != "" {
			dest = strings.TrimSuffix(jsonPath, ".json") + ".html"
		} else {
			dest = filepath.Join(resultsDir(), record.ArtifactStem(r.Model)+".html")
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	var html bytes.Buffer
	if err := render.WriteHTML(&html, a); err != nil {
		return "", err
	}
	if err := atomicfile.Write(dest, html.Bytes(), 0o600); err != nil {
		return "", err
	}
	return dest, nil
}

func resultNumCtx(r *Result) int {
	return r.ContextSize()
}

func samePhysicalMachine(a, b device.Fingerprint) bool {
	return a.Host != "" && a.Host == b.Host && a.OS == b.OS && a.CPU == b.CPU && a.GPU == b.GPU
}

// doctorIDWidth is the check-name column; doctorHang puts wrapped detail under
// it. A doctor detail is a diagnosis, so it runs long -- the JSON-determinism
// finding is 286 characters -- and it used to be printed after a padded column
// with nothing bounding it, which wrapped at whatever column the terminal
// happened to end at.
const (
	doctorIDWidth = 18
	doctorHang    = 2 + 6 + 1
)

func printDoctor(w io.Writer, r eval.DoctorResult, color bool) {
	width := render.Width()
	for _, ck := range r.Checks {
		tag := fmt.Sprintf("%-4s", ck.State)
		if color {
			switch ck.State {
			case "PASS":
				tag = "\x1b[32m" + tag + "\x1b[0m"
			case "WARN":
				tag = "\x1b[33m" + tag + "\x1b[0m"
			case "FAIL":
				tag = "\x1b[31m" + tag + "\x1b[0m"
			}
		}
		head := fmt.Sprintf("  [%s] %-*s", tag, doctorIDWidth, terminalText(ck.ID))
		detail := terminalText(ck.Detail)
		// Short enough to sit beside its check name; otherwise the whole
		// diagnosis becomes a block under it rather than a ragged tail.
		if inline := doctorHang + doctorIDWidth + 1; len(detail) <= width-inline {
			fmt.Fprintf(w, "%s %s\n", head, detail)
			continue
		}
		fmt.Fprintln(w, head)
		render.Field(w, "", doctorHang, detail, width)
	}
	render.Field(w, "  =>", doctorHang, terminalText(r.Verdict), render.Width())
}

func printInventory(ctx context.Context, backendKind, mode string) int {
	rawBackendKind := backendKind
	var ok bool
	backendKind, ok = canonicalBackendKind(backendKind)
	if !ok {
		errPrint("invalid backend", rawBackendKind, "use auto, ollama, llama-server, or openai")
		return exitUsage
	}
	found, err := llm.Discover(ctx)
	if err != nil {
		errPrint("invalid runtime discovery configuration", err.Error(),
			"fix FITR_DISCOVER_URLS or the configured backend URL, then re-run")
		return exitUsage
	}
	var b llm.Backend
	also := []string{}
	if backendKind != "" && backendKind != "auto" {
		url := ""
		for _, f := range found {
			if f.Kind == backendKind {
				url = f.URL
				break
			}
		}
		var err error
		b, err = backendAt(backendKind, url)
		if err != nil {
			errPrint("could not configure backend", err.Error(), "check the backend name and endpoint environment variable")
			return exitUsage
		}
	} else if len(found) > 0 {
		var err error
		b, err = backendAt(found[0].Kind, found[0].URL)
		if err != nil {
			errPrint("could not configure discovered runtime", err.Error(), "set --backend explicitly")
			return exitError
		}
		for _, f := range found[1:] {
			also = append(also, f.Kind+"  "+f.URL)
		}
	}
	return renderInventoryStatus(ctx, b, also, mode)
}

func renderInventoryStatus(ctx context.Context, b llm.Backend, also []string, mode string) int {
	fp := device.Detect(ctx, b)
	prof, err := device.SelectProfile("", fp)
	if err != nil {
		errPrint("could not select device profile", err.Error(),
			"repair or remove invalid files in "+device.UserProfilesDir())
		return exitError
	}
	freeVRAM, _ := device.AvailableVRAM(ctx)
	inv := render.Inventory{
		Fitr:         version,
		CPU:          device.FormatCPU(fp.CPU),
		GPU:          fp.GPU,
		GPUBackend:   fp.GPUBackend,
		MemoryGB:     fp.VRAMGb,
		MemorySource: fp.VRAMSource,
		FreeGB:       freeVRAM,
		Profile:      prof.Name,
		Uncalibrated: profileUncalibrated(prof),
		Also:         also,
	}
	if b == nil {
		inv.Empty = "none reachable"
		render.WriteInventory(os.Stdout, inv, mode)
		return exitOK
	}
	inv.RuntimeKind = b.Name()
	inv.RuntimeURL = b.URL()
	table, warnings, err := joinInstalled(ctx, b, fp)
	if err != nil {
		errPrint("could not list installed models: "+err.Error(), "",
			"is the runtime still up?")
		return exitError
	}
	inv.Warnings = warnings
	inv.Rows = make([]render.InventoryRow, 0, len(table.Rows))
	for _, row := range table.Rows {
		inv.Rows = append(inv.Rows, render.InventoryRow{
			Model: row.Model, State: row.State, Fit: row.Fit, SizeB: row.SizeB,
			Loaded: row.Loaded, Next: row.Next, Note: row.Note,
			Ctx: row.Ctx, Windows: row.Windows, MeasuredCtx: row.MeasuredCtx,
			ServingCtx: row.ServingCtx, ServingKnown: row.ServingKnown, Shape: row.Shape,
		})
	}
	inv.Hidden = table.Hidden
	if len(inv.Rows) == 0 {
		inv.Empty = "reachable, no models"
	}
	render.WriteInventory(os.Stdout, inv, mode)
	return exitOK
}

func joinInstalled(ctx context.Context, b llm.Backend, fp device.Fingerprint) (advise.InventoryTable, []string, error) {
	if b == nil {
		return advise.InventoryTable{}, nil, nil
	}
	listCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tags, err := b.Tags(listCtx)
	if err != nil {
		return advise.InventoryTable{}, nil, err
	}
	installed, warnings := installedInventoryModels(b, tags)
	loaded, serving, statusErr := loadedInventoryModels(listCtx, b, installed)
	if statusErr != nil {
		warnings = append(warnings, "runtime status unavailable; loaded markers and serving contexts are unknown: "+statusErr.Error())
	}
	stored, err := record.NewStore(resultsDir()).LoadCurrent()
	if err != nil {
		return advise.InventoryTable{}, nil, fmt.Errorf("load saved evidence: %w", err)
	}
	if len(stored.Warnings) > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"%d saved result file(s) could not be trusted; run fitr top history for details",
			len(stored.Warnings)))
	}
	return advise.Join(advise.InventoryQuery{
		Tags: installed, Loaded: loaded, Evidence: evidenceFromRecords(stored.Records),
		Current: fp, CurrentKey: fp.Key(), HaveGB: fp.VRAMGb, HaveSrc: fp.VRAMSource,
		Serving: serving,
	}), warnings, nil
}

func installedInventoryModels(b llm.Backend, tags []ollama.ModelInfo) ([]advise.InstalledModel, []string) {
	warnings := []string{}
	installed := make([]advise.InstalledModel, 0, len(tags))
	for _, tag := range tags {
		digest, warning := inventoryArtifactDigest(b, tag)
		if warning != "" {
			warnings = append(warnings, warning)
		}
		item := advise.InstalledModel{
			Name: tag.Name, Size: tag.Size, Quant: tag.Details.QuantizationLevel,
			Path: tag.Path, ArtifactDigest: digest,
		}
		enrichInstalledArtifact(&item)
		installed = append(installed, item)
	}
	return installed, warnings
}

func inventoryArtifactDigest(b llm.Backend, tag ollama.ModelInfo) (string, string) {
	if tag.Digest != "" || tag.ReportedDigest == "" {
		return tag.Digest, ""
	}
	verifier, ok := b.(llm.ModelDigestVerifier)
	if !ok {
		return "", ""
	}
	verified, err := verifier.VerifyModelDigest(tag.Name, tag.ReportedDigest)
	if err != nil {
		return "", fmt.Sprintf("model %s artifact identity could not be verified: %v", tag.Name, err)
	}
	return verified, ""
}

func enrichInstalledArtifact(item *advise.InstalledModel) {
	if item.Path == "" {
		return
	}
	kvs, size, err := advise.OpenGGUF(item.Path)
	if err != nil {
		return
	}
	item.Arch = advise.ArchFromKVs(kvs)
	if item.Size == 0 {
		item.Size = size
	}
}

func loadedInventoryModels(ctx context.Context, b llm.Backend,
	installed []advise.InstalledModel) ([]string, map[string]int, error) {
	var loaded []string
	serving := map[string]int{}
	running, err := b.PS(ctx)
	if err != nil {
		return loaded, serving, err
	}
	for _, model := range running {
		loaded = append(loaded, model.Name)
		recordServingContext(serving, model)
	}
	for i := range installed {
		if size, ok := residentSizeFromRunning(running, installed[i].Name); ok {
			installed[i].ResidentB = size
		}
	}
	return loaded, serving, nil
}

func recordServingContext(serving map[string]int, model ollama.RunningModel) {
	if model.ContextLength <= 0 {
		return
	}
	if prior, exists := serving[model.Name]; exists && prior != model.ContextLength {
		serving[model.Name] = 0
		return
	}
	serving[model.Name] = model.ContextLength
}

func evidenceFromRecords(recs []*record.Record) []advise.InventoryEvidence {
	out := make([]advise.InventoryEvidence, 0, len(recs))
	for _, rec := range recs {
		if rec == nil {
			continue
		}
		out = append(out, inventoryEvidenceFromRecord(rec))
	}
	return out
}

func inventoryEvidenceFromRecord(rec *record.Record) advise.InventoryEvidence {
	toolsBlocked := false
	for need, verdict := range rec.Scorecard.Needs {
		if strings.Contains(need, "tool") && verdict.State == score.Blocked {
			toolsBlocked = true
			break
		}
	}
	artifactDigest := ""
	if rec.Manifest != nil {
		artifactDigest = rec.Manifest.Model.RuntimeBoundDigest()
	}
	comparableIssue := ""
	numCtx := rec.ContextSize()
	if rec.DeviceV2 == nil {
		comparableIssue = "prior run has no verified effective-context receipt"
	} else if _, err := rec.ComparableDeviceKey(); err != nil {
		comparableIssue = "prior run is not comparable: " + err.Error()
	} else if rec.DeviceV2.Context.EffectiveTokens != nil {
		numCtx = *rec.DeviceV2.Context.EffectiveTokens
	}
	return advise.InventoryEvidence{
		Model:           rec.Model,
		ArtifactDigest:  artifactDigest,
		Device:          rec.Device,
		DeviceKey:       rec.Device.Key(),
		ComparableIssue: comparableIssue,
		Level:           rec.Level,
		IntegrityIssue:  rec.EvidenceIntegrityIssue(),
		Contaminated:    len(rec.Contamination) > 0,
		Arch:            advise.ArchFromKVs(rec.ModelMeta.Info),
		WeightsB:        rec.ModelMeta.Size,
		NumCtx:          numCtx,
		Repeats:         rec.Repeats,
		ToolsBlocked:    toolsBlocked,
	}
}

func servingCtxFromRunning(running []ollama.RunningModel, model string) (int, bool) {
	n, seen := 0, false
	for _, m := range running {
		if !advise.SameModel(m.Name, model) || m.ContextLength <= 0 {
			continue
		}
		if seen && m.ContextLength != n {
			return 0, false
		}
		n, seen = m.ContextLength, true
	}
	return n, seen
}

func residentSizeFromRunning(running []ollama.RunningModel, model string) (int64, bool) {
	var size int64
	seen := false
	for _, m := range running {
		if !advise.SameModel(m.Name, model) || m.Size <= 0 {
			continue
		}
		if seen && m.Size != size {
			return 0, false
		}
		size, seen = m.Size, true
	}
	return size, seen
}

func profileUncalibrated(p device.Profile) bool {
	if p.Name == "default" || p.Name == "" {
		return true
	}
	blob := p.Description
	var blobSb2408 strings.Builder
	for _, note := range p.Notes {
		blobSb2408.WriteString(" " + note)
	}
	blob += blobSb2408.String()
	u := strings.ToUpper(blob)
	return strings.Contains(u, "UNCALIBRATED") || strings.Contains(u, "NOT CALIBRATED")
}

func calibrationPair(a, b *Result, stats []eval.ItemStat) (calibration.PairReport, error) {
	if err := validateCalibrationResults(a, b); err != nil {
		return calibration.PairReport{}, err
	}
	aKey, err := validateCalibrationDevice(a, b)
	if err != nil {
		return calibration.PairReport{}, err
	}
	if err := validateCalibrationChecks(a, b); err != nil {
		return calibration.PairReport{}, err
	}
	if err := validateCalibrationModelShape(a, b); err != nil {
		return calibration.PairReport{}, err
	}
	spec, err := eval.LoadSpec()
	if err != nil {
		return calibration.PairReport{}, err
	}
	if a.SchemaVersion != b.SchemaVersion || a.SchemaVersion != spec.Version.ResultSchemaVersion {
		return calibration.PairReport{}, fmt.Errorf("result schema differs from this battery: %d, %d, current %d",
			a.SchemaVersion, b.SchemaVersion, spec.Version.ResultSchemaVersion)
	}
	return calibration.NewPair(version, spec.Version.SpecVersion, a.SeedSet,
		calibrationDevice(a.Device, aKey), calibrationRun(a), calibrationRun(b), stats), nil
}

func validateCalibrationResults(a, b *Result) error {
	for index, result := range []*Result{a, b} {
		label := []string{"reference", "candidate"}[index]
		if result == nil {
			return fmt.Errorf("%s result is unavailable", label)
		}
		if len(result.Contamination) > 0 {
			return fmt.Errorf("%s result is contaminated by resident model(s): %s",
				label, strings.Join(result.Contamination, ", "))
		}
		if issue := result.EvidenceIntegrityIssue(); issue != "" {
			return fmt.Errorf("%s result is unverified: %s", label, issue)
		}
	}
	if err := record.ProvenanceCompatibilityError(a, b); err != nil {
		return fmt.Errorf("run provenance differs: %w", err)
	}
	return nil
}

func validateCalibrationDevice(a, b *Result) (string, error) {
	aKey, aKeyErr := a.ComparableDeviceKey()
	bKey, bKeyErr := b.ComparableDeviceKey()
	if aKeyErr != nil || bKeyErr != nil {
		return "", fmt.Errorf("verified effective context is required: %w; %w", aKeyErr, bKeyErr)
	}
	if aKey != bKey {
		return "", errors.New("results were measured on different hardware or runtime configurations")
	}
	if diffs := a.Device.Diff(b.Device); len(diffs) > 0 {
		names := make([]string, 0, len(diffs))
		for _, diff := range diffs {
			names = append(names, diff[0])
		}
		return "", fmt.Errorf("resolved device configuration differs: %s", strings.Join(names, ", "))
	}
	if a.NumCtx != b.NumCtx {
		return "", fmt.Errorf("request context differs: %d vs %d", a.NumCtx, b.NumCtx)
	}
	return aKey, nil
}

func validateCalibrationChecks(a, b *Result) error {
	paired := eval.PairFlips(a.Checks, b.Checks)
	if len(a.Checks) != len(b.Checks) || paired.Shared != len(a.Checks) {
		return fmt.Errorf("check instances are incomplete: %d left, %d right, %d paired",
			len(a.Checks), len(b.Checks), paired.Shared)
	}
	return nil
}

func validateCalibrationModelShape(a, b *Result) error {
	fa, fb := strings.TrimSpace(a.ModelMeta.Details.Family), strings.TrimSpace(b.ModelMeta.Details.Family)
	if fa == "" || fb == "" || !strings.EqualFold(fa, fb) {
		return fmt.Errorf("model family differs or is unknown: %q vs %q", fa, fb)
	}
	pa, pb := strings.TrimSpace(a.ModelMeta.Details.ParameterSize), strings.TrimSpace(b.ModelMeta.Details.ParameterSize)
	if pa == "" || pb == "" || !strings.EqualFold(pa, pb) {
		return fmt.Errorf("parameter size differs or is unknown: %q vs %q", pa, pb)
	}
	return nil
}

func calibrationDevice(fp device.Fingerprint, key string) calibration.Device {
	return calibration.Device{
		ID: calibration.PseudonymousDeviceID(key),
		OS: fp.OS, CPU: fp.CPU, RAMGB: fp.RAMGb, GPU: fp.GPU,
		GPUDriver: fp.GPUDriver, GPUDriverDate: fp.GPUDriverDate,
		GPUBackend: fp.GPUBackend, Runtime: fp.Runtime,
		InferenceDevice: fp.InferenceDevice, Config: fp.Config,
	}
}

func calibrationRun(r *Result) calibration.Run {
	digest := ""
	if r.Manifest != nil {
		digest = r.Manifest.Model.RuntimeBoundDigest()
	}
	return calibration.Run{
		Model: r.Model, Quant: r.ModelMeta.Details.QuantizationLevel,
		Family: r.ModelMeta.Details.Family, ParameterSize: r.ModelMeta.Details.ParameterSize,
		StartedAt: r.StartedAt, NumCtx: r.NumCtx, ResultSchemaVersion: r.SchemaVersion,
		ArtifactDigest: digest,
	}
}

func attachCalibrateLineage(report *calibration.PairReport, lineagePath string) error {
	if report == nil {
		return errors.New("calibration pair is missing")
	}
	if strings.TrimSpace(lineagePath) != "" {
		manifest, err := calibration.ReadConversionManifest(lineagePath)
		if err != nil {
			return err
		}
		receipt, err := calibration.LineageFromConversion(manifest, report.Reference.ArtifactDigest, report.Candidate.ArtifactDigest)
		if err != nil {
			return err
		}
		return report.AttachLineage(receipt)
	}
	receipt, err := autoGGUFLineage(*report)
	if err != nil || receipt.Schema == "" {
		return err
	}
	return report.AttachLineage(receipt)
}

func autoGGUFLineage(report calibration.PairReport) (calibration.LineageReceipt, error) {
	refKVs, err := hashedRuntimeGGUF(report.Reference.ArtifactDigest)
	if err != nil {
		return calibration.LineageReceipt{}, err
	}
	candKVs, err := hashedRuntimeGGUF(report.Candidate.ArtifactDigest)
	if err != nil {
		return calibration.LineageReceipt{}, err
	}
	if refKVs == nil || candKVs == nil {
		return calibration.LineageReceipt{}, nil
	}
	receipt, err := calibration.LineageFromGGUF(refKVs, candKVs, report.Reference.ArtifactDigest, report.Candidate.ArtifactDigest)
	if err != nil {
		if errors.Is(err, calibration.ErrGGUFNoBaseDigest) || errors.Is(err, calibration.ErrGGUFNamedWithoutDigest) {
			return calibration.LineageReceipt{}, nil
		}
		return calibration.LineageReceipt{}, err
	}
	return receipt, nil
}

func hashedRuntimeGGUF(wantDigest string) (map[string]any, error) {
	if strings.TrimSpace(wantDigest) == "" {
		return nil, nil
	}
	path := ollamaBlobPath(wantDigest)
	if path == "" {
		return nil, nil
	}
	got, err := fileSHA256(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if got != wantDigest {
		return nil, fmt.Errorf("blob %s does not match runtime digest %s", path, wantDigest)
	}
	kvs, _, err := advise.OpenGGUF(path)
	if err != nil {
		//nolint:nilerr // an unreadable blob means no automatic lineage, not a run failure
		// Lineage from a runtime blob is a bonus, not a requirement: an
		// unreadable blob means no automatic lineage, and the caller falls
		// back to an explicit --lineage manifest or an exploratory pair.
		return nil, nil
	}
	return kvs, nil
}

func ollamaBlobPath(digest string) string {
	digest = strings.ToLower(strings.TrimSpace(digest))
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 {
		return ""
	}
	name := "sha256-" + strings.TrimPrefix(digest, "sha256:")
	roots := []string{}
	if env := strings.TrimSpace(os.Getenv("OLLAMA_MODELS")); env != "" {
		roots = append(roots, env)
	}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, filepath.Join(home, ".ollama", "models"))
	}
	if local := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); local != "" {
		roots = append(roots, filepath.Join(local, "Ollama", "models"))
	}
	for _, root := range roots {
		path := filepath.Join(root, "blobs", name)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path
		}
	}
	return ""
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// quantDamageLine explains why a tempting directional claim is inconclusive.
// Family names and parameter sizes do not prove that two quantized artifacts
// descend from the same base revision. Lineage receipts live on calibration
// pairs, not compare results, so compare still reports paired flips and never
// attributes them to quantization.
func quantDamageLine(a, b *Result, flips eval.FlipReport) (string, bool) {
	qa, qb := a.ModelMeta.Details.QuantizationLevel, b.ModelMeta.Details.QuantizationLevel
	ra, rb := eval.QuantRank(qa), eval.QuantRank(qb)
	if ra == 0 || rb == 0 || ra == rb {
		return "", false
	}
	fa, fb := a.ModelMeta.Details.Family, b.ModelMeta.Details.Family
	pa, pb := a.ModelMeta.Details.ParameterSize, b.ModelMeta.Details.ParameterSize
	if fa == "" || !strings.EqualFold(fa, fb) || pa == "" || !strings.EqualFold(pa, pb) {
		return "", false
	}
	lost := flips.AOnly
	if rb > ra {
		lost = flips.BOnly
	}
	if lost == 0 {
		return "", false
	}
	return "quant attribution INCONCLUSIVE: same-base revision lineage is unverified; see paired flips above", true
}

// appendUnique adds items not already present, preserving order.
func appendUnique(dst []string, items ...string) []string {
	for _, it := range items {
		found := slices.Contains(dst, it)
		if !found {
			dst = append(dst, it)
		}
	}
	return dst
}

// incomparableNote turns a fingerprint delta into the reason two runs cannot
// be ranked together. Naming the machine is misleading when both runs happened
// on it, which is the common case: what moved was its state, not its hardware.
func incomparableNote(diff [][3]string) string {
	if len(diff) == 0 {
		return "tok/s is device-specific; the comparison would be meaningless"
	}
	parts := make([]string, 0, len(diff))
	for _, d := range diff {
		parts = append(parts, fmt.Sprintf("%s %q vs %q", d[0], d[1], d[2]))
	}
	return "differs by " + strings.Join(parts, "; ")
}
