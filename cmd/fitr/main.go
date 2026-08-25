// Command fitr answers: is this local model any good ON THIS DEVICE?
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
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/advise"
	"github.com/blisspixel/fitr/internal/atomicfile"
	"github.com/blisspixel/fitr/internal/buildinfo"
	"github.com/blisspixel/fitr/internal/calibration"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/llamaserver"
	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/lock"
	"github.com/blisspixel/fitr/internal/modelref"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/openaicompat"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/retonr"
	"github.com/blisspixel/fitr/internal/score"
	"github.com/blisspixel/fitr/internal/stats"
)

var version = buildinfo.Version()

// Exit codes: small, documented, domain-specific. Not sysexits -- nobody uses it.
const (
	exitOK        = 0 // ran, and every measured need passed
	exitError     = 1 // something broke
	exitUsage     = 2 // bad invocation
	exitGates     = 3 // ran fine, but a need FAILED (useful as a CI gate)
	exitInterrupt = 130
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

func usage() {
	fmt.Fprint(os.Stderr, `fitr `+version+` - is this local model any good ON THIS DEVICE?

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
  fitr profiles [new [name]]
  fitr calibrate <model-a> <model-b> [--out PATH] [--lineage PATH]
  fitr calibrate merge <pair.json>... [--out PATH]
  fitr compare <model-a> <model-b>

flags:
  --display  auto|rich|plain|json|none   output mode (default auto)
  --backend  auto|ollama|llama-server|openai   serving runtime (default auto-detect)
  --pull     fetch a missing model first (Ollama; HF links pull automatically)
  --allow-unsafe-exec  run unisolated built-in code diagnostics; never score them
  -k         repeats per noisy task (default 3; 1 quick; 5 checks-only)
             A single run is not a measurement: identical configs vary 10-20pp.
  -q         quiet (repeat for silent)      -v  verbose

exit codes:
  0 ok   1 error   2 usage   3 a need FAILED   130 interrupted

environment:
  FITR_OPENAI_URL       OpenAI-compatible endpoint
  FITR_OPENAI_API_KEY   bearer token; kept out of command history and process arguments
  FITR_OPENAI_MODEL_SHA256  independently obtained model digest required for measured OpenAI-compatible runs

examples:
  fitr
  fitr qwen3:30b
  fitr advise --display json
  fitr run qwen3-coder:30b
  fitr run https://huggingface.co/bartowski/Qwen2.5-Coder-7B-Instruct-GGUF
  fitr run some-new-model:tag -k 3 --pull
  fitr diag dolphin3:8b
  fitr doctor qwen3-coder:30b
  fitr advise qwen3:30b
  fitr advise ./model.gguf --vram-gb 8 --fit
  fitr run qwen3:30b --ctx 4096
  fitr apply qwen3:30b
  fitr tune
  fitr tune qwen3:30b qwen3:30b-q8
  fitr export qwen3:30b --out scorecard.html
  fitr view qwen3:30b
  fitr profiles new
  fitr run qwen3:8b-q8_0 --checks-only --seedset qwen3-8b -k 5
  fitr run qwen3:8b-q4_K_M --checks-only --seedset qwen3-8b -k 5
  fitr calibrate qwen3:8b-q8_0 qwen3:8b-q4_K_M --out pair.json --lineage conversion.json
`)
}

func main() { os.Exit(run()) }

func run() int {
	if len(os.Args) < 2 {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		return cmdStatus(ctx, nil)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	switch os.Args[1] {
	case "run":
		return cmdRun(ctx, os.Args[2:])
	case "advise":
		return cmdAdvise(ctx, os.Args[2:])
	case "apply":
		return cmdApply(ctx, os.Args[2:])
	case "tune":
		return cmdTune(ctx, os.Args[2:])
	case "export":
		return cmdExport(ctx, os.Args[2:])
	case "view":
		return cmdView(ctx, os.Args[2:])
	case "board":
		return cmdBoard(ctx, os.Args[2:])
	case "top":
		return cmdTop(ctx, os.Args[2:])
	case "diag":
		return cmdDiag(ctx, os.Args[2:])
	case "doctor":
		return cmdDoctor(ctx, os.Args[2:])
	case "device":
		return cmdDevice(ctx, os.Args[2:])
	case "profiles":
		return cmdProfiles(ctx, os.Args[2:])
	case "calibrate":
		return cmdCalibrate(ctx, os.Args[2:])
	case "compare":
		return cmdCompare(ctx, os.Args[2:])
	case "screenshots": // dev-only: regenerate docs/assets from mock data
		return cmdScreenshots(ctx, os.Args[2:])
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
	case "k", "n", "profile", "display", "backend", "seedset", "vram-gb", "ctx", "out", "lineage", "view":
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
		*c = *c + 1
		return nil
	case "false":
		*c = 0
		return nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return fmt.Errorf("quiet level must be a non-negative integer")
	}
	*c = countFlag(n)
	return nil
}

func (*countFlag) IsBoolFlag() bool { return true }

// newBackend resolves which serving runtime to measure through.
// Selection order: --backend flag, then $FITR_BACKEND, then auto-probe
// (Ollama first - it is the default URL people have running - then
// llama-server, then any OpenAI-compatible server at the LM Studio port).
//
// A Hugging Face ref (pasted URL or hf.co/...) is an Ollama pull, not a
// label to file results under while measuring whatever else is already
// loaded. Prefer Ollama; refuse to pretend llama-server fetched it.
func newBackend(ctx context.Context, model, kind string, pull bool) (llm.Backend, int) {
	return newBackendWithDisplay(ctx, model, kind, pull, nil)
}

func newBackendWithDisplay(ctx context.Context, model, kind string, pull bool, disp render.Display) (llm.Backend, int) {
	if kind == "" || kind == "auto" {
		kind = os.Getenv("FITR_BACKEND")
	}
	if isHFRef(model) && (kind == "" || kind == "auto" || kind == "ollama") {
		o := ollama.New()
		if !o.Reachable(ctx) {
			backendError(disp, "Hugging Face refs need a running Ollama",
				"Ollama pulls GGUFs from hf.co/{user}/{repo}[:quant]; other servers already have a model loaded",
				"start `ollama serve` and re-run, or pass the name of a model already being served")
			return nil, exitError
		}
		return checkModelWithDisplay(ctx, o, model, pull, disp)
	}
	switch kind {
	case "", "auto":
		found, err := llm.Discover(ctx)
		if err != nil {
			backendError(disp, "invalid runtime discovery configuration", err.Error(),
				"fix FITR_DISCOVER_URLS or the configured backend URL, then re-run")
			return nil, exitUsage
		}
		if len(found) == 0 {
			candidates, _ := llm.Candidates()
			backendError(disp, "no serving runtime reachable",
				"tried "+strings.Join(candidates, ", "),
				"start one, or point fitr at it: OLLAMA_BASE_URL, LLAMA_SERVER_URL, FITR_OPENAI_URL, FITR_DISCOVER_URLS, or --backend")
			return nil, exitError
		}
		if len(found) > 1 {
			var extra []string
			for _, f := range found[1:] {
				extra = append(extra, f.Kind+" at "+f.URL)
			}
			message := fmt.Sprintf("also found %s; using %s; set --backend or a URL environment variable to choose",
				strings.Join(extra, ", "), found[0].Kind)
			if disp != nil {
				disp.Note(message, "warn")
			} else {
				fmt.Fprintf(os.Stderr, "! also found %s - using %s at %s; set --backend or a URL env to pick\n",
					terminalText(strings.Join(extra, ", ")), terminalText(found[0].Kind), terminalText(found[0].URL))
			}
		}
		b, err := backendAt(found[0].Kind, found[0].URL)
		if err != nil {
			backendError(disp, "could not configure discovered runtime", err.Error(),
				"set --backend and the matching endpoint environment variable explicitly")
			return nil, exitError
		}
		return checkModelWithDisplay(ctx, b, model, pull, disp)
	case "ollama":
		o := ollama.New()
		if !o.Reachable(ctx) {
			backendError(disp, "cannot reach Ollama",
				"every measurement needs a running server",
				"start it with `ollama serve`, or set OLLAMA_BASE_URL")
			return nil, exitError
		}
		return checkModelWithDisplay(ctx, o, model, pull, disp)
	case "llama-server", "llamaserver":
		l := llamaserver.New()
		if !l.Reachable(ctx) {
			backendError(disp, "cannot reach llama-server",
				"every measurement needs a running server",
				"start it with `llama-server -m model.gguf`, or set LLAMA_SERVER_URL")
			return nil, exitError
		}
		return checkModelWithDisplay(ctx, l, model, pull, disp)
	case "openai":
		g := openaicompat.New()
		if !g.Reachable(ctx) {
			backendError(disp, "cannot reach an OpenAI-compatible server",
				"every measurement needs a running server",
				"start LM Studio / vLLM / SGLang, or set FITR_OPENAI_URL")
			return nil, exitError
		}
		return checkModelWithDisplay(ctx, g, model, pull, disp)
	default:
		backendError(disp, fmt.Sprintf("unknown backend %q", kind), "",
			"valid: auto, ollama, llama-server, openai")
		return nil, exitUsage
	}
}

// normalizeModelRef accepts pasted Hugging Face URLs and turns them into the
// hf.co/{user}/{repo}[:quant] form Ollama pulls natively - so "point fitr at
// an HF link" just works. Blob/resolve URLs keep the quant from the filename.
func normalizeModelRef(model string) string {
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
			path := strings.SplitN(m[len(prefix):], "?", 2)[0]
			path = strings.SplitN(path, "#", 2)[0]
			return "hf.co/" + parseHFPath(strings.TrimRight(path, "/"))
		}
	}
	return m
}

func parseHFPath(p string) string {
	path, tag, hasTag := strings.Cut(p, ":")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		if hasTag {
			return path + ":" + tag
		}
		return path
	}
	user, repo := parts[0], parts[1]
	if !hasTag && len(parts) >= 5 {
		switch parts[2] {
		case "blob", "resolve":
			if q := quantFromFilename(parts[len(parts)-1]); q != "" {
				tag, hasTag = q, true
			}
		}
	}
	if hasTag {
		return user + "/" + repo + ":" + tag
	}
	return user + "/" + repo
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
	rest := u
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

type resolvedRunModel struct {
	Name     string
	Info     ollama.ModelInfo
	Identity record.ModelIdentity
}

// selectResolvedModel makes the runtime listing authoritative. Mutable user
// aliases may select one exact listing, but ambiguous or absent selections are
// rejected. llama-server is the only exception because it serves exactly one
// launch-time model and ignores the request model field.
func selectResolvedModel(backend, requested string, models []ollama.ModelInfo) (ollama.ModelInfo, error) {
	var matches []ollama.ModelInfo
	for _, candidate := range models {
		if strings.TrimSpace(candidate.Name) != "" && modelref.SameServed(requested, candidate.Name) {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return ollama.ModelInfo{}, fmt.Errorf("model %q resolves to more than one runtime entry", requested)
	}
	if backend == "llama-server" && len(models) == 1 && strings.TrimSpace(models[0].Name) != "" {
		return models[0], nil
	}
	return ollama.ModelInfo{}, fmt.Errorf("model %q did not resolve to an exact runtime entry", requested)
}

func resolveRunModel(ctx context.Context, b llm.Backend, requested string) (resolvedRunModel, error) {
	models, err := b.Tags(ctx)
	if err != nil {
		return resolvedRunModel{}, fmt.Errorf("resolve model from %s: %w", b.Name(), err)
	}
	selected, err := selectResolvedModel(b.Name(), requested, models)
	if err != nil {
		return resolvedRunModel{}, err
	}
	resolved := selected.Name
	info := selected
	shown, err := b.Show(ctx, resolved)
	if err != nil {
		return resolvedRunModel{}, fmt.Errorf("inspect resolved model %q: %w", resolved, err)
	}
	info = mergeModelInfo(selected, shown)
	info.Name = resolved
	if verifier, ok := b.(llm.ModelDigestVerifier); ok {
		digest, err := verifier.VerifyModelDigest(resolved, info.ReportedDigest)
		if err != nil {
			return resolvedRunModel{}, fmt.Errorf("verify resolved model %q: %w", resolved, err)
		}
		info.Digest = digest
	}
	runtimeVersion := strings.TrimSpace(b.Version(ctx))
	if runtimeVersion == "" {
		runtimeVersion = b.Name() + " (version unavailable)"
	}
	localPath := info.Path
	identity, err := record.NewModelIdentity(presentationModelLabel(requested), resolved, b.Name(), runtimeVersion,
		info.Digest, localPath, info.Size)
	if err != nil {
		return resolvedRunModel{}, err
	}
	// The content digest identifies a local artifact without persisting its
	// directory. Paths remain process-local inspection inputs, never results.
	info.Path = ""
	return resolvedRunModel{Name: resolved, Info: info, Identity: identity}, nil
}

func mergeModelInfo(listed, shown ollama.ModelInfo) ollama.ModelInfo {
	out := listed
	if shown.Size > 0 {
		out.Size = shown.Size
	}
	if shown.Digest != "" {
		out.Digest = shown.Digest
	}
	if shown.ReportedDigest != "" {
		out.ReportedDigest = shown.ReportedDigest
	}
	if shown.Path != "" {
		out.Path = shown.Path
	}
	if len(shown.Capabilities) > 0 {
		out.Capabilities = shown.Capabilities
	}
	if len(shown.Info) > 0 {
		out.Info = shown.Info
	}
	if shown.Details.ParameterSize != "" {
		out.Details.ParameterSize = shown.Details.ParameterSize
	}
	if shown.Details.QuantizationLevel != "" {
		out.Details.QuantizationLevel = shown.Details.QuantizationLevel
	}
	if shown.Details.Family != "" {
		out.Details.Family = shown.Details.Family
	}
	return out
}

// checkModelWithDisplay verifies the model label against what the backend
// serves. On Ollama a missing model is a hard error with a pull hint, or an
// automatic pull when the caller allows it. A single-model server ignores the
// label at request time, so a mismatch there is a warning; otherwise results
// would be filed under a name the server never saw.
func checkModelWithDisplay(ctx context.Context, b llm.Backend, model string, pull bool, disp render.Display) (llm.Backend, int) {
	if model == "" {
		return b, exitOK
	}
	tags, err := b.Tags(ctx)
	if err != nil {
		backendError(disp, "could not list models from "+b.Name(), err.Error(),
			"check the runtime logs and its model inventory endpoint")
		return nil, exitError
	}
	found := false
	var near []string
	base := strings.SplitN(model, ":", 2)[0]
	for _, t := range tags {
		if modelref.SameServed(model, t.Name) {
			found = true
		}
		if strings.Contains(t.Name, base) {
			near = append(near, t.Name)
		}
	}
	if found {
		return b, exitOK
	}
	if b.Name() != "ollama" {
		if len(tags) == 0 {
			backendError(disp, b.Name()+" is serving no models", "the runtime returned an empty model inventory",
				"load a model in the runtime, then re-run fitr")
			return nil, exitError
		}
		if isHFRef(model) {
			backendError(disp, "Hugging Face refs need Ollama to pull",
				b.Name()+" is serving its own model, not fetching from Hugging Face",
				"start Ollama, or pass the served model name instead of an HF URL")
			return nil, exitUsage
		}
		if pull {
			if disp != nil {
				disp.Note("--pull is an Ollama feature; "+b.Name()+" serves whatever is already loaded", "warn")
			} else {
				fmt.Fprintf(os.Stderr, "! --pull is an Ollama feature; %s serves whatever is already loaded\n", terminalText(b.Name()))
			}
		}
		message := fmt.Sprintf("%s serves %q, not %q; the run manifest will record the resolved model", b.Name(), tags[0].Name, model)
		if disp != nil {
			disp.Note(message, "warn")
		} else {
			fmt.Fprintf(os.Stderr, "! %s serves %q, not %q - the run manifest will record %q\n",
				terminalText(b.Name()), terminalText(tags[0].Name), terminalText(model), terminalText(tags[0].Name))
		}
		return b, exitOK
	}
	// Pasting an HF URL is the request to fetch it. Regular Ollama tags
	// still need --pull so a typo does not start a multi-gigabyte download.
	if pull || isHFRef(model) {
		o, ok := b.(*ollama.Client)
		if ok {
			src := "Ollama"
			if isHFRef(model) {
				src = "Hugging Face via Ollama"
			}
			if disp != nil {
				disp.Phase("pull", src)
			} else {
				fmt.Fprintf(os.Stderr, "  pulling %s from %s\n", terminalText(model), terminalText(src))
			}
			last := ""
			err := o.Pull(ctx, model, func(status string, pct int) {
				line := terminalText(status)
				if pct >= 0 {
					line = fmt.Sprintf("%s %d%%", line, pct)
				}
				if line != last && disp == nil {
					fmt.Fprintf(os.Stderr, "\r  %-60s", line)
					last = line
				}
				if live, ok := disp.(liveTelemetry); ok && pct >= 0 {
					live.LiveProgress(pct, 100, terminalText(status))
				}
			})
			if disp != nil {
				if err == nil {
					disp.Done("pull", 0)
				}
			} else {
				fmt.Fprintln(os.Stderr)
			}
			if err != nil {
				backendError(disp, "model pull failed", err.Error(), "")
				return nil, exitError
			}
			return b, exitOK
		}
	}
	hint := "pull it first: `ollama pull " + model + "`, or re-run with --pull"
	if len(near) > 0 {
		hint = "did you mean: " + strings.Join(near, ", ")
	}
	backendError(disp, fmt.Sprintf("model %q is not installed", presentationModelLabel(model)),
		fmt.Sprintf("%d model(s) available", len(tags)), hint)
	return nil, exitUsage
}

func backendError(disp render.Display, message, note, hint string) {
	if disp == nil {
		errPrint(message, note, hint)
		return
	}
	if failed, ok := disp.(runFailureTelemetry); ok {
		failed.RunFailed(errors.New(message))
		return
	}
	detail := message
	if note != "" {
		detail += ": " + note
	}
	if hint != "" {
		detail += "; " + hint
	}
	disp.Note(detail, "warn")
}

// probeBackend is the no-error variant for commands that merely display state.
func probeBackend(ctx context.Context) llm.Backend {
	found, _ := llm.Discover(ctx)
	if len(found) == 0 {
		return ollama.New()
	}
	b, err := backendAt(found[0].Kind, found[0].URL)
	if err != nil {
		return ollama.New()
	}
	return b
}

func backendAt(kind, url string) (llm.Backend, error) {
	url = strings.TrimRight(url, "/")
	switch kind {
	case "llama-server", "llamaserver":
		c := llamaserver.New()
		if url != "" {
			c.BaseURL = url
		}
		return c, nil
	case "openai":
		if url == "" {
			return openaicompat.New(), nil
		}
		return openaicompat.NewAt(url, openaicompat.CredentialsDisabled)
	case "ollama":
		c := ollama.New()
		if url != "" {
			c.BaseURL = url
		}
		return c, nil
	default:
		return nil, fmt.Errorf("unknown backend %q", kind)
	}
}

func canonicalBackendKind(kind string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "auto":
		return strings.ToLower(strings.TrimSpace(kind)), true
	case "ollama":
		return "ollama", true
	case "llama-server", "llamaserver":
		return "llama-server", true
	case "openai":
		return "openai", true
	default:
		return "", false
	}
}

// ---------------------------------------------------------------- advise

// weightsFromTags recovers the on-disk weight size for a served model from the
// runtime's model list. Ollama's /api/show carries architecture metadata but
// no size field, so advise alone would SKIP every Ollama model for "weights
// were not measured" while bare `fitr` printed that model's size on the same
// screen -- inventory has always read the byte total from /api/tags. This is
// still a runtime reading, not an estimate: same source, same model identity.
// Returns 0 when the runtime does not list the model, which stays SKIP.
func weightsFromTags(ctx context.Context, c llm.Backend, model string) int64 {
	tags, err := c.Tags(ctx)
	if err != nil {
		return 0
	}
	for _, t := range tags {
		if modelref.SameServed(model, t.Name) && t.Size > 0 {
			return t.Size
		}
	}
	return 0
}

// cmdAdvise answers: does this model fit on this box, and if not, which
// flag to try? SKIP when fit cannot be measured (no VRAM reading, no
// weights, no architecture) - never a fabricated GB number.
func cmdAdvise(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("advise", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vram := fs.Float64("vram-gb", -1, "available GPU memory in GB (skip detection)")
	ctxSize := fs.Int("ctx", 0, "requested context length (default: model's max)")
	backend := fs.String("backend", "auto", "auto|ollama|llama-server|openai")
	mode := fs.String("display", "auto", "auto|rich|plain|json|none")
	pullFlag := fs.Bool("pull", false, "pull the model first if it is not installed")
	loadFlag := fs.Bool("load", false, "load the model on Ollama and read resident size (dummy allocation)")
	fitFlag := fs.Bool("fit", false, "run llama-fit-params on a GGUF if it is on PATH")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if !render.ValidMode(*mode) {
		errPrint("invalid display mode", *mode, "use auto, rich, plain, json, or none")
		return exitUsage
	}
	if _, ok := canonicalBackendKind(*backend); !ok {
		errPrint("invalid backend", *backend, "use auto, ollama, llama-server, or openai")
		return exitUsage
	}
	if *ctxSize < 0 {
		errPrint("invalid context size", "--ctx cannot be negative", "omit --ctx for the model default, or pass a positive token count")
		return exitUsage
	}
	if *vram < -1 {
		errPrint("invalid VRAM size", "--vram-gb cannot be negative", "omit --vram-gb for automatic detection, or pass a non-negative size")
		return exitUsage
	}
	if fs.NArg() > 1 {
		errPrint("too many arguments", "advise accepts at most one model", "fitr advise [model] [flags]")
		return exitUsage
	}
	if fs.NArg() < 1 {
		if *loadFlag || *fitFlag || *pullFlag || *ctxSize != 0 || *vram >= 0 {
			errPrint("missing model", "fit/load/ctx flags need a model", "fitr advise <model>  or  fitr advise ./model.gguf")
			return exitUsage
		}
		return cmdStatus(ctx, []string{"--display", *mode, "--backend", *backend})
	}
	raw := fs.Arg(0)
	model := normalizeModelRef(raw)

	in := advise.Input{Model: model, Ctx: *ctxSize}
	fpKey := ""
	if *backend != "" && *backend != "auto" {
		in.Backend = *backend
	}
	if *vram >= 0 {
		in.HaveGB = *vram
		in.HaveSrc = "--vram-gb"
	} else {
		fp := device.Detect(ctx, nil)
		in.HaveGB = fp.VRAMGb
		in.HaveSrc = fp.VRAMSource
		fpKey = fp.Key()
	}
	if kv := os.Getenv("OLLAMA_KV_CACHE_TYPE"); kv != "" {
		if n, ok := advise.KVElemBytes(kv); ok {
			in.KVBytes = n
			in.KVSrc = "OLLAMA_KV_CACHE_TYPE=" + kv
		}
	}

	var c llm.Backend
	ggufPath := ""
	if isLocalGGUF(raw) {
		kvs, size, err := advise.OpenGGUF(raw)
		if err != nil {
			errPrint("could not read GGUF: "+err.Error(), "", "")
			return exitError
		}
		in.Model = raw
		in.Arch = advise.ArchFromKVs(kvs)
		in.WeightsB = size
		in.Source = "GGUF metadata"
		ggufPath = raw
		if q := quantFromFilename(raw); isQuantTag(q) {
			in.Quant = strings.ToUpper(q)
		}
	} else {
		var code int
		c, code = newBackend(ctx, model, *backend, *pullFlag)
		if code != exitOK {
			return code
		}
		in.Backend = c.Name()
		fp := device.Detect(ctx, c)
		fpKey = fp.Key()
		if *vram < 0 {
			in.HaveGB = fp.VRAMGb
			in.HaveSrc = fp.VRAMSource
		}
		if kv := fp.Config["OLLAMA_KV_CACHE_TYPE"]; kv != "" {
			if n, ok := advise.KVElemBytes(kv); ok {
				in.KVBytes = n
				in.KVSrc = "OLLAMA_KV_CACHE_TYPE=" + kv
			}
		}
		info, err := c.Show(ctx, model)
		if err == nil {
			in.Quant = info.Details.QuantizationLevel
			if info.Size > 0 {
				in.WeightsB = info.Size
			}
			if len(info.Info) > 0 {
				in.Arch = advise.ArchFromKVs(info.Info)
				in.Source = "Ollama /api/show"
			}
			if info.Path != "" {
				ggufPath = info.Path
			}
			if !in.Arch.KVReady() && info.Path != "" {
				if kvs, size, err := advise.OpenGGUF(info.Path); err == nil {
					in.Arch = advise.ArchFromKVs(kvs)
					if in.WeightsB == 0 {
						in.WeightsB = size
					}
					in.Source = "GGUF at " + info.Path
				}
			}
		}
		if in.WeightsB == 0 {
			in.WeightsB = weightsFromTags(ctx, c, model)
		}
		if in.Source == "" {
			in.Source = c.Name() + " (no architecture metadata)"
		}
		if running, err := c.PS(ctx); err == nil {
			for _, m := range running {
				if !modelref.SameServed(model, m.Name) || m.Size <= 0 {
					continue
				}
				in.ResidentB = m.Size
				in.ResidentSrc = c.Name() + " /api/ps"
				break
			}
		}
	}

	if *fitFlag {
		if ggufPath == "" {
			errPrint("--fit needs a GGUF path", "", "pass ./model.gguf, or an Ollama model whose blob path is known")
			return exitUsage
		}
		fmt.Fprintf(os.Stderr, "  llama-fit-params %s\n", terminalText(ggufPath))
		used, cannot, err := advise.RunFitParams(ctx, ggufPath, *ctxSize)
		if err != nil {
			fmt.Fprintf(os.Stderr, " note: llama-fit-params not used: %s\n", terminalText(err.Error()))
		} else {
			in.FitB, in.FitCannot, in.FitSrc = used, cannot, "llama-fit-params"
		}
	}

	if *loadFlag {
		if c == nil || c.Name() != "ollama" {
			errPrint("--load needs a running Ollama", "",
				"pass an Ollama model tag; llama-server is already loaded, and a bare .gguf needs --fit")
			return exitUsage
		}
		if in.ResidentB == 0 {
			if in.WeightsB > 0 && in.HaveGB > 0 && float64(in.WeightsB) > in.HaveGB*advise.GiB {
				fmt.Fprintf(os.Stderr, " note: not loading: weights alone exceed the budget\n")
			} else {
				lk, err := lock.Acquire("eval", "advise --load "+model)
				if err != nil {
					errPrint(err.Error(), "", "")
					return exitError
				}
				defer lk.Release() //nolint:errcheck
				ctxLen := *ctxSize
				if ctxLen <= 0 {
					ctxLen = 2048
				}
				fmt.Fprintf(os.Stderr, "  loading %s at num_ctx=%d to measure resident size\n", terminalText(model), ctxLen)
				_, _, err = c.Generate(ctx, model, ".", ollama.Deterministic(1, ctxLen))
				if err != nil {
					in.LoadErr = err.Error()
				} else if running, err := c.PS(ctx); err == nil {
					for _, m := range running {
						if !modelref.SameServed(model, m.Name) || m.Size <= 0 {
							continue
						}
						in.ResidentB = m.Size
						in.ResidentSrc = "ollama /api/ps after --load"
						in.ResidentCtx = ctxLen
						break
					}
				}
				if left, err := c.StopAll(ctx); err == nil && len(left) > 0 {
					fmt.Fprintf(os.Stderr, " note: still resident: %s\n", terminalText(strings.Join(left, ", ")))
				}
			}
		}
	}

	in.Timings = adviseTimings(in.Model, fpKey)
	rep := advise.Evaluate(in)
	switch render.Resolve(*mode) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			errPrint(err.Error(), "", "")
			return exitError
		}
	case "none":
		// machine channel is the exit code
	default:
		advise.Write(os.Stdout, rep)
		if next := advise.AdviseNext(in.Model, rep.Tier, rep.FlagValue); next != "" {
			fmt.Fprintf(os.Stderr, "\n  next   %s\n", terminalText(next))
			if rep.FlagValue > 0 {
				fmt.Fprintf(os.Stderr, "         fitr apply %s   after a passing run, to persist num_ctx=%d\n",
					terminalText(in.Model), rep.FlagValue)
			}
		}
	}
	return rep.ExitCode()
}

func adviseTimings(model, deviceKey string) []advise.SavedTiming {
	if model == "" {
		return nil
	}
	stored, err := record.NewStore(resultsDir()).LoadCurrent()
	if err != nil {
		return nil
	}
	var out []advise.SavedTiming
	for _, rec := range stored.Records {
		if rec == nil || !modelref.SameServed(model, rec.Model) {
			continue
		}
		if rec.EvidenceIntegrityIssue() != "" || len(rec.Contamination) > 0 {
			continue
		}
		if deviceKey != "" && rec.Device.Key() != deviceKey {
			continue
		}
		ctxN := rec.ContextSize()
		if ctxN <= 0 {
			continue
		}
		out = append(out, advise.SavedTiming{
			Ctx: ctxN, DecodeTPS: rec.DecodeSum.Mean, PrefillTPS: rec.PrefillSum.Mean,
		})
	}
	return out
}

// ---------------------------------------------------------------- apply
// cmdApply prints the command to persist a measured context. It never
// restarts or mutates the serving process; that is the whole point.
func cmdApply(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	ctxSize := fs.Int("ctx", 0, "context to persist (default: latest result, else 4096 as the worked example)")
	backend := fs.String("backend", "auto", "auto|ollama|llama-server|openai")
	mode := fs.String("display", "auto", "auto|rich|plain|json|none")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if !render.ValidMode(*mode) {
		errPrint("invalid display mode", *mode, "use auto, rich, plain, json, or none")
		return exitUsage
	}
	if fs.NArg() > 1 {
		errPrint("too many arguments", "apply accepts at most one model", "fitr apply [model] [--ctx N]")
		return exitUsage
	}
	if *ctxSize < 0 {
		errPrint("invalid context size", "--ctx cannot be negative", "omit --ctx for the latest measured context, or pass a positive token count")
		return exitUsage
	}
	model := ""
	if fs.NArg() > 0 {
		model = normalizeModelRef(fs.Arg(0))
	}
	kind, ok := canonicalBackendKind(*backend)
	if !ok {
		errPrint("invalid backend", *backend, "use auto, ollama, llama-server, or openai")
		return exitUsage
	}
	n := *ctxSize
	if model != "" && n <= 0 {
		if all, err := loadResults(); err == nil {
			if r := latestNamed(all, model); r != nil {
				n = resultNumCtx(r)
			}
		}
		if n <= 0 {
			errPrint("no saved result for this model", "",
				"fitr run "+model+"   or   fitr apply "+model+" --ctx 4096")
			return exitError
		}
	}
	if kind == "" || kind == "auto" {
		found, err := llm.Discover(ctx)
		if err != nil {
			errPrint("invalid runtime discovery configuration", err.Error(),
				"fix FITR_DISCOVER_URLS or the configured backend URL, then re-run")
			return exitUsage
		}
		if len(found) > 0 {
			kind = found[0].Kind
		} else {
			kind = ""
		}
	}
	if n <= 0 {
		n = 4096
	}
	plan := advise.PlanApply(kind, model, n)
	if model != "" {
		if serving, ok := observeServingCtx(ctx, model, kind); ok {
			plan.ServingCtx = serving
			plan.ServingKnown = true
		}
	}
	switch render.Resolve(*mode) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(plan); err != nil {
			errPrint(err.Error(), "", "")
			return exitError
		}
	case "none":
	default:
		advise.WriteApply(os.Stdout, plan)
		if model == "" {
			fmt.Fprintln(os.Stdout, "\n  next   fitr apply <model> [--ctx N]   to pin a measured run")
		}
	}
	return exitOK
}

// ---------------------------------------------------------------- tune
// cmdTune does not run a sweep. llama-bench already owns throughput-only
// points, and a documented flash-attention quality regression is why
// throughput-only is not enough. Until fitr can restart a server, tune
// prints the request-level knobs and diffs two observed fingerprints.
func cmdTune(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("tune", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 && fs.NArg() != 2 {
		errPrint("tune diffs two saved results", "received an incomplete model pair", "fitr tune  or  fitr tune <model-a> <model-b>")
		return exitUsage
	}
	fmt.Fprint(os.Stdout, `  request-level knobs (no server restart):
    num_ctx / --ctx-size     KV cache size; this is what advise already names
    num_batch / -b           prefill chunk; quality can move with it
    num_gpu / -ngl           offload; partial GPU is a RAM benchmark wearing a GPU badge

  server-level (needs a restart fitr will not orchestrate):
    OLLAMA_FLASH_ATTENTION   documented quality regression at some settings
    OLLAMA_KV_CACHE_TYPE     f16 vs q8_0 vs q4_0; changes the fingerprint

  measure a point: set the knob, then
    fitr run <model> [--ctx N]
    fitr compare <a> <b>
  score quality + degeneracy + throughput jointly. llama-bench already owns
  throughput-only sweeps. Persist a measured ctx with fitr apply.

`)
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stdout, "  next   fitr tune <model-a> <model-b>   to diff two saved fingerprints")
		return exitOK
	}
	all, err := loadResults()
	if err != nil || len(all) == 0 {
		errPrint("no saved results", "", "run `fitr run` on both configs first")
		return exitError
	}
	a := latestNamed(all, fs.Arg(0))
	b := latestNamed(all, fs.Arg(1))
	if a == nil || b == nil {
		errPrint("need two saved results", "", "fitr board lists what is on disk")
		return exitUsage
	}
	fmt.Fprintf(os.Stdout, "  %s  vs  %s\n", terminalText(a.Model), terminalText(b.Model))
	d := a.Device.Diff(b.Device)
	if ca, cb := resultNumCtx(a), resultNumCtx(b); ca != cb {
		d = append([][3]string{{"num_ctx", fmt.Sprintf("%d", ca), fmt.Sprintf("%d", cb)}}, d...)
	}
	if len(d) == 0 {
		fmt.Fprintln(os.Stdout, "  same fingerprint - quality is `fitr compare`'s job, not a device change")
		return exitOK
	}
	fmt.Fprintln(os.Stdout, "  fingerprint diffs (these runs are not comparable):")
	for _, x := range d {
		fmt.Fprintf(os.Stdout, "    %-22s  %s  ->  %s\n", terminalText(x[0]), terminalText(emptyDash(x[1])), terminalText(emptyDash(x[2])))
	}
	return exitOK
}

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
		if level == "quick" {
			reps = 1
		} else if level == "checks" {
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
	adaptive                bool
	numCtx                  int
	allowUnsafeExec         bool
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
		res.DeviceKey = res.DeviceKey + suf
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
			for round := 0; round < rounds; round++ {
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

func runTaskPlan(level string, repeats, checkRepeats int, adaptive bool, checks, refusalPrompts int) record.TaskPlan {
	plan := record.TaskPlan{}
	if level != "checks" {
		plan.SpeedSamples = repeats
		plan.Memory = true
		plan.CodeTrials = 2 * repeats
		plan.Plumbing = true
		plan.ToolTrials = repeats
	}
	if level != "quick" {
		rounds := checkRepeats
		if adaptive {
			rounds = 6
		}
		plan.CheckTrialsLimit = checks * rounds
		plan.AdaptiveChecks = adaptive
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

func hasAdaptiveGates(spec *eval.Spec, profile device.Profile, level string) bool {
	if spec == nil || level == "quick" || len(spec.Checks) == 0 {
		return false
	}
	for _, check := range spec.Checks {
		if _, ok := profile.Float(check.Need, "pass_rate_min"); ok {
			return true
		}
	}
	return false
}

func allDecided(sprts map[string]*stats.SPRT) bool {
	if len(sprts) == 0 {
		return true
	}
	for _, s := range sprts {
		if s.State() == stats.SPRTContinue {
			return false
		}
	}
	return true
}

// adaptiveSummary discloses the sequential decision in plain words: what was
// decided, in how many trials, and what the sample could not separate.
func adaptiveSummary(sprts map[string]*stats.SPRT, rounds, instances int) string {
	if len(sprts) == 0 {
		return fmt.Sprintf("adaptive: no gated pools to decide; ran %d round(s)", rounds)
	}
	var bits []string
	for _, need := range []string{"structured_output", "instruction_precision", "user_tasks", "reasoning"} {
		s, ok := sprts[need]
		if !ok {
			continue
		}
		switch s.State() {
		case stats.SPRTAcceptH1:
			bits = append(bits, fmt.Sprintf("%s decided above its gate in %d trials", need, s.N))
		case stats.SPRTAcceptH0:
			bits = append(bits, fmt.Sprintf("%s decided below its gate in %d trials", need, s.N))
		default:
			bits = append(bits, fmt.Sprintf("%s undecided after %d trials - the sample cannot separate it from the gate", need, s.N))
		}
	}
	return fmt.Sprintf("adaptive: stopped after %d round(s), %d instances; %s",
		rounds, instances, strings.Join(bits, "; "))
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

// ---------------------------------------------------------------- export
// cmdExport writes a self-contained HTML scorecard from a saved result.
// Never automatic: the JSON in ~/.fitr is local storage; HTML is what you
// share, and it contains a hardware fingerprint.
func cmdExport(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	out := fs.String("out", "", "HTML path (default: <results>/<model>.html)")
	retonrFlag := fs.Bool("retonr", false, "also write opt-in evidence JSON for the retonr sister project")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() < 1 {
		errPrint("missing model", "", "fitr export <model>  or  fitr export <model> --retonr")
		return exitUsage
	}
	if fs.NArg() > 1 {
		errPrint("too many arguments", "export accepts exactly one model", "fitr export <model> [--out PATH] [--retonr]")
		return exitUsage
	}
	model := normalizeModelRef(fs.Arg(0))
	results, err := loadResults()
	if err != nil || len(results) == 0 {
		errPrint("no results yet", "", "fitr run "+model)
		return exitError
	}
	r := latestNamed(results, model)
	if r == nil {
		errPrint(fmt.Sprintf("no stored result for %q", model), "",
			"fitr run "+model)
		return exitError
	}
	path := *out
	if path == "" {
		path = "auto"
	}
	if !*retonrFlag || *out != "" {
		html, err := writeHTMLArtifact(r, path, "")
		if err != nil {
			errPrint("could not write HTML: "+err.Error(), "", "")
			return exitError
		}
		fmt.Fprintf(os.Stderr, "  wrote  %s\n", terminalText(html))
	}
	if *retonrFlag {
		evPath := filepath.Join(resultsDir(), record.ArtifactStem(r.Model)+".retonr.json")
		if err := writeRetonrEvidence(r, evPath, ""); err != nil {
			errPrint("could not write retonr evidence: "+err.Error(), "", "")
			return exitError
		}
		fmt.Fprintf(os.Stderr, "  wrote  %s\n", terminalText(evPath))
		fmt.Fprintln(os.Stderr, "  note   evidence for https://github.com/blisspixel/retonr; not a qualification")
	}
	return exitOK
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
	toolsBlocked := false
	for need, verdict := range sc.Needs {
		if strings.Contains(need, "tool") && verdict.State == score.Blocked {
			toolsBlocked = true
			break
		}
	}
	return render.Artifact{
		FitrVersion:   version,
		SchemaVersion: r.SchemaVersion,
		Model:         r.Model,
		StartedAt:     r.StartedAt,
		Level:         r.Level,
		Repeats:       r.Repeats,
		WallSeconds:   r.WallSeconds,
		Device:        r.Device,
		DeviceKey:     r.DeviceKey,
		Profile:       prof.Name,
		Scorecard:     sc,
		Meta:          meta,
		NextCommand:   advise.ResultNext(r.Model, r.Repeats, r.ContextSize(), r.Level, toolsBlocked),
		Contamination: r.Contamination,
	}, nil
}

func resultMeta(r *Result, profile string) render.Meta {
	meta := render.Meta{
		ParamSize: r.ModelMeta.Details.ParameterSize,
		Quant:     r.ModelMeta.Details.QuantizationLevel,
		Family:    r.ModelMeta.Details.Family,
		GPU:       r.Device.GPU, Driver: r.Device.GPUDriver,
		Device: r.Device.InferenceDevice, Profile: profile,
		StartedAt: r.StartedAt, Level: r.Level, WallSeconds: r.WallSeconds,
		NumCtx:     resultNumCtx(r),
		Repeats:    r.Repeats,
		DecodeMean: r.DecodeSum.Mean, DecodeSD: r.DecodeSum.SD,
		DecodeMin: r.DecodeSum.Min, DecodeMax: r.DecodeSum.Max,
		DecodeN:     r.DecodeSum.N,
		PrefillMean: r.PrefillSum.Mean, PrefillSD: r.PrefillSum.SD,
		PrefillN: r.PrefillSum.N,
		TTFTMean: r.TTFTSum.Mean, TTFTSD: r.TTFTSum.SD, TTFTN: r.TTFTSum.N,
		ResidentGB:  r.Memory.ResidentGB,
		Calibration: r.Level == "checks",
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
	// State what this sample size CANNOT resolve, out loud, on every run.
	trials := len(r.CodeWrite) + len(r.CodeFix) + len(r.Tools) + len(r.Checks)
	if r.Agentic != nil {
		trials++
	}
	if trials > 0 {
		meta.Trials = trials
		meta.MDEpp = 100 * stats.MinDetectableEffect(trials, 1)
	}
	return meta
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

// ---------------------------------------------------------------- view
// cmdView replays a saved measurement through the same terminal renderer used
// at run completion. With no model it selects the newest result, making the
// command a quick dashboard rather than a file-hunting exercise.
func cmdView(_ context.Context, args []string) int {
	fs := flag.NewFlagSet("view", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	mode := fs.String("display", "auto", "auto|rich|plain|json|none")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if !render.ValidMode(*mode) {
		errPrint("invalid display mode", *mode, "use auto, rich, plain, json, or none")
		return exitUsage
	}
	if fs.NArg() > 1 {
		errPrint("too many arguments", "view accepts one model or result path", "fitr view [model|result.json]")
		return exitUsage
	}

	var selected *Result
	if fs.NArg() == 1 {
		candidate := fs.Arg(0)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			r, err := record.NewStore(resultsDir()).Read(candidate)
			if err != nil {
				errPrint("not a fitr result", "expected a saved run JSON with a model", candidate)
				return exitError
			}
			selected = r
		} else {
			results, err := loadResults()
			if err != nil || len(results) == 0 {
				errPrint("no results yet", "", "fitr run "+normalizeModelRef(candidate))
				return exitError
			}
			selected = latestNamed(results, normalizeModelRef(candidate))
			if selected == nil {
				errPrint(fmt.Sprintf("no stored result for %q", candidate), "", "fitr board")
				return exitError
			}
		}
	} else {
		results, err := loadResults()
		if err != nil || len(results) == 0 {
			errPrint("no results yet", "", "run one first: fitr run <model>")
			return exitError
		}
		for _, result := range results {
			if selected == nil || startedAfter(result.StartedAt, selected.StartedAt) {
				selected = result
			}
		}
	}

	switch render.Resolve(*mode) {
	case "none":
		return exitOK
	case "json":
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(selected); err != nil {
			errPrint("could not render result: "+err.Error(), "", "")
			return exitError
		}
		return exitOK
	}
	scorecard := selected.Scorecard
	meta := resultMeta(selected, selected.Profile)
	if artifact, err := artifactFrom(selected); err == nil {
		scorecard, meta = artifact.Scorecard, artifact.Meta
	} else {
		scorecard = score.ExcludeEvidence(scorecard,
			"the scoring profile is unavailable, so the stored verdict cannot be reproduced")
	}
	display := render.New(*mode)
	defer display.Close()
	display.Result(scorecard, meta)
	return exitOK
}

// ---------------------------------------------------------------- board
func cmdBoard(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("board", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	current := fs.Bool("current", false, "only this machine, including its measured context variants")
	mode := fs.String("display", "auto", "auto|rich|plain|json|none")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if !render.ValidMode(*mode) {
		errPrint("invalid display mode", *mode, "use auto, rich, plain, json, or none")
		return exitUsage
	}
	if fs.NArg() != 0 {
		errPrint("unexpected argument", fs.Arg(0), "fitr board [--current] [--display MODE]")
		return exitUsage
	}
	results, err := loadResults()
	if err != nil || len(results) == 0 {
		errPrint("no results yet", "", "run one first: fitr run <model>")
		return exitError
	}
	curDevice := device.Detect(ctx, probeBackend(ctx))
	cur := curDevice.Key()

	// Group by fingerprint. Rows measured under different hardware/config are
	// NOT comparable and must never be ranked against each other. A |ctx=N
	// suffix is a config split, not a different box - labeled as such below.
	groups := map[string][]*Result{}
	var order []string
	excludedContext := 0
	for _, r := range results {
		key, keyErr := r.ComparableDeviceKey()
		if keyErr != nil {
			excludedContext++
			continue
		}
		if *current && !samePhysicalMachine(r.Device, curDevice) {
			continue
		}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], r)
	}
	sort.Strings(order)

	board := render.Board{}
	excludedContaminated := 0
	excludedUnverified := 0
	unscoredProfiles := 0
	visible := map[string][]*Result{}
	for _, key := range order {
		rows := groups[key]
		clean := make([]*Result, 0, len(rows))
		for _, result := range rows {
			if len(result.Contamination) > 0 {
				excludedContaminated++
				continue
			}
			if result.EvidenceIntegrityIssue() != "" {
				excludedUnverified++
				continue
			}
			clean = append(clean, result)
		}
		if len(clean) == 0 {
			continue
		}
		rows = clean
		g := rows[len(rows)-1]
		nctx := resultNumCtx(g)
		note := "different hardware/config or effective context; not comparable to other blocks"
		if samePhysicalMachine(g.Device, curDevice) {
			if nctx != eval.NumCtx {
				note = fmt.Sprintf("this machine, requested num_ctx=%d; effective context is part of this block", nctx)
			} else {
				note = "this machine, verified effective context and current config"
			}
		}
		sort.Slice(rows, func(i, j int) bool {
			return rows[i].DecodeSum.Mean > rows[j].DecodeSum.Mean
		})
		visible[key] = rows
		group := render.BoardGroup{
			GPU: g.Device.GPU, Driver: g.Device.GPUDriver,
			KV: g.Device.Config["OLLAMA_KV_CACHE_TYPE"], NumCtx: nctx, Note: note,
		}
		if g.DeviceV2 != nil {
			group.ContextState = string(g.DeviceV2.Context.State())
			if g.DeviceV2.Context.EffectiveTokens != nil {
				group.EffectiveCtx = *g.DeviceV2.Context.EffectiveTokens
			}
		}
		for _, r := range rows {
			var codes []string
			scorecard := r.Scorecard
			if artifact, err := artifactFrom(r); err == nil {
				scorecard = artifact.Scorecard
			} else {
				scorecard = score.ExcludeEvidence(scorecard,
					"the scoring profile is unavailable, so the stored verdict cannot be reproduced")
				unscoredProfiles++
			}
			for _, s := range scorecard.Serves {
				if code := score.NeedCode[s]; code != "" {
					codes = append(codes, code)
				}
			}
			var decodes []float64
			for _, sample := range r.Speed {
				decodes = append(decodes, sample.DecodeTPS)
			}
			group.Rows = append(group.Rows, render.BoardRow{
				Model: r.Model, ParamSize: r.ModelMeta.Details.ParameterSize,
				Quant:      r.ModelMeta.Details.QuantizationLevel,
				DecodeMean: r.DecodeSum.Mean, DecodeSD: r.DecodeSum.SD,
				PrefillMean: r.PrefillSum.Mean, ResidentGB: r.Memory.ResidentGB,
				DecodeSeries: decodes, Repeats: r.Repeats, Serves: codes,
			})
		}
		board.Results += len(rows)
		board.Groups = append(board.Groups, group)
	}
	if excludedContaminated > 0 {
		fmt.Fprintf(os.Stderr, "! INCONCLUSIVE: excluded %d contaminated result(s) from board ranking and claims\n",
			excludedContaminated)
	}
	if excludedUnverified > 0 {
		fmt.Fprintf(os.Stderr, "! INCONCLUSIVE: excluded %d result(s) without a valid evidence contract from board ranking and claims\n",
			excludedUnverified)
	}
	if excludedContext > 0 {
		fmt.Fprintf(os.Stderr, "! INCONCLUSIVE: excluded %d result(s) without verified effective context from board ranking and claims\n",
			excludedContext)
	}
	if unscoredProfiles > 0 {
		fmt.Fprintf(os.Stderr, "! INCONCLUSIVE: %d board row(s) have no reproducible qualification because their scoring profile is unavailable\n",
			unscoredProfiles)
	}
	if len(board.Groups) == 0 {
		if excludedContaminated > 0 || excludedUnverified > 0 || excludedContext > 0 {
			detail := "all matching results lacked claimable evidence"
			if excludedContaminated > 0 && excludedUnverified == 0 && excludedContext == 0 {
				detail = "all matching results were contaminated"
			} else if excludedUnverified > 0 && excludedContaminated == 0 && excludedContext == 0 {
				detail = "all matching results lacked a valid evidence contract"
			} else if excludedContext > 0 && excludedContaminated == 0 && excludedUnverified == 0 {
				detail = "all matching results lacked verified effective context"
			}
			errPrint("no conclusive results for this machine", detail,
				"re-run with the current fitr version after unloading all models")
		} else {
			errPrint("no results for this machine", "", "run fitr run <model>")
		}
		return exitError
	}
	if render.Resolve(*mode) == "json" {
		payload := map[string]any{"groups": visible, "current": cur}
		if excludedContaminated > 0 {
			payload["inconclusive_excluded"] = excludedContaminated
		}
		if excludedUnverified > 0 {
			payload["unverified_excluded"] = excludedUnverified
		}
		if excludedContext > 0 {
			payload["context_unverified_excluded"] = excludedContext
		}
		b, _ := json.Marshal(payload)
		fmt.Println(string(b))
		return exitOK
	}
	render.WriteBoard(os.Stdout, board, *mode)
	return exitOK
}

func resultNumCtx(r *Result) int {
	return r.ContextSize()
}

func samePhysicalMachine(a, b device.Fingerprint) bool {
	return a.Host != "" && a.Host == b.Host && a.OS == b.OS && a.CPU == b.CPU && a.GPU == b.GPU
}

// ---------------------------------------------------------------- diag
func cmdDiag(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("diag", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	backend := fs.String("backend", "auto", "auto|ollama|llama-server|openai")
	ctxSize := fs.Int("ctx", 0, "request context (default 8192)")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() < 1 {
		errPrint("missing model", "", "fitr diag <model>")
		return exitUsage
	}
	if fs.NArg() > 1 {
		errPrint("too many arguments", "diag accepts exactly one model", "fitr diag <model> [--ctx N]")
		return exitUsage
	}
	if *ctxSize < 0 {
		errPrint("invalid context size", "--ctx cannot be negative", "omit --ctx for the default, or pass a positive token count")
		return exitUsage
	}
	model := normalizeModelRef(fs.Arg(0))
	c, code := newBackend(ctx, model, *backend, false)
	if code != exitOK {
		return code
	}
	spec, err := eval.LoadSpec()
	if err != nil {
		errPrint(err.Error(), "", "")
		return exitError
	}
	fmt.Printf("tool plumbing: %s\n", terminalText(model))
	ctx = eval.WithNumCtx(ctx, *ctxSize)
	r, err := eval.RunPlumbing(ctx, c, model, spec.Plumbing)
	if err != nil {
		errPrint(err.Error(), "", "")
		return exitError
	}
	for _, id := range r.Order {
		rung := r.Rungs[id]
		mark := "FAIL"
		if rung.Pass {
			mark = "PASS"
		}
		fmt.Printf("  [%s] %-20s %s\n", mark, terminalText(id), terminalText(rung.Detail))
	}
	fmt.Printf("  => %s\n", terminalText(r.Verdict))
	if !r.Healthy {
		return exitGates
	}
	return exitOK
}

// ---------------------------------------------------------------- doctor
// cmdDoctor answers: can this box be measured fairly AT ALL? Every benchmark
// silently assumes yes; nothing else checks. ~60 seconds, worth running before
// believing any number - including ours.
func cmdDoctor(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	n := fs.Int("n", 5, "identical generations per determinism probe")
	backend := fs.String("backend", "auto", "auto|ollama|llama-server|openai")
	ctxSize := fs.Int("ctx", 0, "request context (default 8192)")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() < 1 {
		errPrint("missing model", "", "fitr doctor <model>")
		return exitUsage
	}
	if fs.NArg() > 1 {
		errPrint("too many arguments", "doctor accepts exactly one model", "fitr doctor <model> [-n N] [--ctx N]")
		return exitUsage
	}
	if *n < 2 {
		errPrint("invalid determinism repeat count", "-n must be at least 2", "omit -n for the default of 5, or pass 2 or more")
		return exitUsage
	}
	if *ctxSize < 0 {
		errPrint("invalid context size", "--ctx cannot be negative", "omit --ctx for the default, or pass a positive token count")
		return exitUsage
	}
	model := normalizeModelRef(fs.Arg(0))
	c, code := newBackend(ctx, model, *backend, false)
	if code != exitOK {
		return code
	}
	// Doctor generates, so it takes the same one-eval-per-machine lock a run
	// does, and clears residents so its timings are its own.
	lk, err := lock.Acquire("eval", "doctor of "+model)
	if err != nil {
		errPrint(err.Error(), "", "")
		return exitError
	}
	defer lk.Release() //nolint:errcheck // cleanup failure is not worth failing a run over
	if left, err := c.StopAll(ctx); err == nil && len(left) > 0 {
		fmt.Fprintf(os.Stderr, "! still resident: %s - results may be contaminated\n", terminalText(strings.Join(left, ", ")))
	}

	fp := device.Detect(ctx, c)
	fmt.Printf("doctor: %s on %s (%s)\n", terminalText(model), terminalText(fp.GPU), terminalText(fp.Runtime))
	ctx = eval.WithNumCtx(ctx, *ctxSize)
	r, err := eval.RunDoctor(ctx, c, model, *n, eval.DoctorOpts{
		Config: fp.Config,
		Placement: func(ctx context.Context) string {
			return device.InferenceDeviceFor(ctx, c, model)
		},
	})
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "\ninterrupted")
			return exitInterrupt
		}
		errPrint(err.Error(), "", "")
		return exitError
	}
	printDoctor(os.Stdout, r, false)
	if !r.Healthy {
		return exitGates
	}
	return exitOK
}

func printDoctor(w io.Writer, r eval.DoctorResult, color bool) {
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
		fmt.Fprintf(w, "  [%s] %-18s %s\n", tag, terminalText(ck.ID), terminalText(ck.Detail))
	}
	fmt.Fprintf(w, "  => %s\n", terminalText(r.Verdict))
}

// cmdStatus is what a bare `fitr` prints after install: this box, what is
// already serving, current evidence, and one next command per row.
func cmdStatus(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("fitr", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	mode := fs.String("display", "auto", "auto|rich|plain|json|none")
	backend := fs.String("backend", "auto", "auto|ollama|llama-server|openai")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if !render.ValidMode(*mode) {
		errPrint("invalid display mode", *mode, "use auto, rich, plain, json, or none")
		return exitUsage
	}
	if fs.NArg() > 0 {
		rest := []string{fs.Arg(0), "--display", *mode, "--backend", *backend}
		rest = append(rest, fs.Args()[1:]...)
		return cmdAdvise(ctx, rest)
	}
	return printInventory(ctx, *backend, *mode)
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
	fp := device.Detect(ctx, b)
	prof, err := device.SelectProfile("", fp)
	if err != nil {
		errPrint("could not select device profile", err.Error(),
			"repair or remove invalid files in "+device.UserProfilesDir())
		return exitError
	}
	inv := render.Inventory{
		Fitr:         version,
		CPU:          device.FormatCPU(fp.CPU),
		GPU:          fp.GPU,
		GPUBackend:   fp.GPUBackend,
		MemoryGB:     fp.VRAMGb,
		MemorySource: fp.VRAMSource,
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
			ServingCtx: row.ServingCtx, ServingKnown: row.ServingKnown,
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
	installed := make([]advise.InstalledModel, 0, len(tags))
	for _, tag := range tags {
		item := advise.InstalledModel{
			Name: tag.Name, Size: tag.Size, Quant: tag.Details.QuantizationLevel, Path: tag.Path,
		}
		if tag.Path != "" {
			if kvs, size, err := advise.OpenGGUF(tag.Path); err == nil {
				item.Arch = advise.ArchFromKVs(kvs)
				if item.Size == 0 {
					item.Size = size
				}
			}
		}
		installed = append(installed, item)
	}
	var loaded []string
	serving := map[string]int{}
	if running, err := b.PS(listCtx); err == nil {
		for _, m := range running {
			loaded = append(loaded, m.Name)
			if m.ContextLength > 0 {
				if prior, exists := serving[m.Name]; exists && prior != m.ContextLength {
					serving[m.Name] = 0
				} else {
					serving[m.Name] = m.ContextLength
				}
			}
		}
		for i := range installed {
			if size, ok := residentSizeFromRunning(running, installed[i].Name); ok {
				installed[i].ResidentB = size
			}
		}
	}
	var evidence []advise.InventoryEvidence
	warnings := []string{}
	stored, err := record.NewStore(resultsDir()).LoadCurrent()
	if err != nil {
		return advise.InventoryTable{}, nil, fmt.Errorf("load saved evidence: %w", err)
	}
	evidence = evidenceFromRecords(stored.Records)
	if len(stored.Warnings) > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"%d saved result file(s) could not be trusted; run fitr top history for details",
			len(stored.Warnings)))
	}
	return advise.Join(advise.InventoryQuery{
		Tags: installed, Loaded: loaded, Evidence: evidence,
		CurrentKey: fp.Key(), HaveGB: fp.VRAMGb, HaveSrc: fp.VRAMSource,
		Serving: serving,
	}), warnings, nil
}

func evidenceFromRecords(recs []*record.Record) []advise.InventoryEvidence {
	out := make([]advise.InventoryEvidence, 0, len(recs))
	for _, rec := range recs {
		if rec == nil {
			continue
		}
		toolsBlocked := false
		for need, verdict := range rec.Scorecard.Needs {
			if strings.Contains(need, "tool") && verdict.State == score.Blocked {
				toolsBlocked = true
				break
			}
		}
		out = append(out, advise.InventoryEvidence{
			Model:          rec.Model,
			DeviceKey:      rec.Device.Key(),
			Level:          rec.Level,
			IntegrityIssue: rec.EvidenceIntegrityIssue(),
			Contaminated:   len(rec.Contamination) > 0,
			Arch:           advise.ArchFromKVs(rec.ModelMeta.Info),
			WeightsB:       rec.ModelMeta.Size,
			NumCtx:         rec.ContextSize(),
			Repeats:        rec.Repeats,
			ToolsBlocked:   toolsBlocked,
		})
	}
	return out
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
	for _, note := range p.Notes {
		blob += " " + note
	}
	u := strings.ToUpper(blob)
	return strings.Contains(u, "UNCALIBRATED") || strings.Contains(u, "NOT CALIBRATED")
}

// ---------------------------------------------------------------- device
func cmdDevice(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("device", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	mode := fs.String("display", "auto", "auto|rich|plain|json|none")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if !render.ValidMode(*mode) {
		errPrint("invalid display mode", *mode, "use auto, rich, plain, json, or none")
		return exitUsage
	}
	if fs.NArg() != 0 {
		errPrint("unexpected argument", fs.Arg(0), "fitr device [--display MODE]")
		return exitUsage
	}
	fp := device.Detect(ctx, probeBackend(ctx))
	prof, err := device.SelectProfile("", fp)
	if err != nil {
		errPrint("could not select device profile", err.Error(),
			"repair or remove invalid files in "+device.UserProfilesDir())
		return exitError
	}
	switch render.Resolve(*mode) {
	case "json":
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(map[string]any{
			"fingerprint": fp, "key": fp.Key(), "profile": prof.Name,
		}); err != nil {
			errPrint("could not render device: "+err.Error(), "", "")
			return exitError
		}
		return exitOK
	case "none":
		return exitOK
	}
	fmt.Printf("  host               %s\n", terminalText(fp.Host))
	fmt.Printf("  os                 %s\n", terminalText(fp.OS))
	fmt.Printf("  cpu                %s\n", terminalText(device.FormatCPU(fp.CPU)))
	fmt.Printf("  ram_gb             %.1f\n", fp.RAMGb)
	fmt.Printf("  vram_gb            %s\n", terminalText(device.FormatVRAM(fp.VRAMGb, fp.VRAMSource)))
	fmt.Printf("  gpu                %s\n", terminalText(fp.GPU))
	fmt.Printf("  gpu_backend        %s\n", terminalText(emptyDash(fp.GPUBackend)))
	fmt.Printf("  gpu_driver         %s  (%s)\n", terminalText(fp.GPUDriver), terminalText(fp.GPUDriverDate))
	fmt.Printf("  runtime            %s\n", terminalText(fp.Runtime))
	fmt.Printf("  inference_device   %s\n", terminalText(fp.InferenceDevice))
	// Not part of the sealed fingerprint, but an acceptance row needs it: a
	// matrix whose finest grain is the operating system cannot see a defect
	// that only appears on one interpreter version.
	if tooling := device.ProbeTooling(ctx); tooling != "" {
		fmt.Printf("  probe_tooling      %s\n", terminalText(tooling))
	}
	for _, conflict := range fp.IdentityConflicts() {
		fmt.Printf("  ! identity         %s\n", terminalText(conflict))
	}
	fmt.Println("  config")
	keys := make([]string, 0, len(fp.Config))
	for k := range fp.Config {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := fp.Config[k]
		if v == "" {
			v = "(unset)"
		}
		fmt.Printf("    %-26s %s\n", terminalText(k), terminalText(v))
	}
	fmt.Printf("  profile            %s - %s\n", terminalText(prof.Name), terminalText(prof.Description))
	fmt.Printf("  key                %s\n", terminalText(fp.Key()))
	return exitOK
}

func cmdProfiles(ctx context.Context, args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "new":
			if len(args) > 2 {
				errPrint("too many arguments", "profiles new accepts at most one name", "fitr profiles new [name]")
				return exitUsage
			}
			name := ""
			if len(args) > 1 {
				name = args[1]
			}
			return cmdProfilesNew(ctx, name)
		case "-h", "--help", "help":
			if len(args) > 1 {
				errPrint("unexpected argument", args[1], "fitr profiles --help")
				return exitUsage
			}
			fmt.Fprint(os.Stderr, "usage: fitr profiles [new [name]]\n")
			return exitOK
		default:
			errPrint(fmt.Sprintf("unknown profiles subcommand %q", args[0]), "",
				"fitr profiles    or    fitr profiles new [name]")
			return exitUsage
		}
	}
	fp := device.Detect(ctx, probeBackend(ctx))
	active, _ := device.SelectProfile("", fp)
	profs, err := device.LoadProfiles()
	if err != nil {
		errPrint(err.Error(), "", "")
		return exitError
	}
	for _, p := range profs {
		mark := " "
		if p.Name == active.Name {
			mark = "*"
		}
		fmt.Printf(" %s %-12s %s\n", terminalText(mark), terminalText(p.Name), terminalText(p.Description))
	}
	fmt.Println("\n  * = auto-selected for this machine")
	fmt.Println("  next   fitr profiles new [name]   # UNCALIBRATED local copy; edit the gates")
	return exitOK
}

func cmdProfilesNew(ctx context.Context, name string) int {
	fp := device.Detect(ctx, probeBackend(ctx))
	p, err := device.ScaffoldProfile(name, fp)
	if err != nil {
		errPrint(err.Error(), "", "")
		return exitError
	}
	path, err := device.WriteProfile(device.UserProfilesDir(), p)
	if err != nil {
		errPrint(err.Error(), "", "pick a new name, or edit the existing file")
		return exitError
	}
	fmt.Printf("  wrote  %s\n", terminalText(path))
	fmt.Println("  UNCALIBRATED copy of default. Run models you already have opinions")
	fmt.Println("  about, then edit the gates so the verdicts match lived experience.")
	fmt.Println("  Do not publish these numbers as a calibrated community profile.")
	return exitOK
}

// cmdCalibrate reports which check items discriminated between two saved
// runs (typically two quants of the same model on a shared seedset). It
// does not rewrite the spec: dropping an item is a human decision after
// more than one box has spoken.
func cmdCalibrate(ctx context.Context, args []string) int {
	if len(args) > 0 && args[0] == "merge" {
		return cmdCalibrateMerge(args[1:])
	}
	fs := flag.NewFlagSet("calibrate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	out := fs.String("out", "", "write a privacy-safe calibration pair as JSON")
	lineagePath := fs.String("lineage", "", "publisher conversion manifest binding both artifacts to one base revision")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 2 {
		errPrint("need two saved results", "",
			"fitr run a --checks-only --seedset night && fitr run b --checks-only --seedset night && fitr calibrate a b")
		return exitUsage
	}
	results, err := loadResults()
	if err != nil || len(results) == 0 {
		errPrint("no saved results", "", "fitr run both models with the same --seedset first")
		return exitError
	}
	a := latestNamed(results, fs.Arg(0))
	b := latestNamed(results, fs.Arg(1))
	if a == nil || b == nil {
		errPrint("need two saved results", "", "fitr board lists what is on disk")
		return exitUsage
	}
	if a.SeedSet == "" || a.SeedSet != b.SeedSet {
		errPrint("runs did not share a seedset",
			fmt.Sprintf("%s seedset=%q, %s seedset=%q", a.Model, a.SeedSet, b.Model, b.SeedSet),
			"re-run both with the same --seedset so instances pair")
		return exitUsage
	}
	stats := eval.ItemStats(a.Checks, b.Checks)
	if len(stats) == 0 {
		errPrint("no shared check instances", "", "both runs need --checks-only (or the default/full level), the same seedset, and the same -k")
		return exitError
	}
	report, err := calibrationPair(a, b, stats)
	if err != nil {
		errPrint("invalid calibration pair", err.Error(), "pair the same model family and size on one unchanged device/config")
		return exitUsage
	}
	if err := attachCalibrateLineage(&report, *lineagePath); err != nil {
		errPrint("could not verify same-base lineage", err.Error(),
			"pass a fitr.lineage.conversion.v1 manifest with --lineage, or keep the pair exploratory")
		return exitError
	}
	qa, qb := a.ModelMeta.Details.QuantizationLevel, b.ModelMeta.Details.QuantizationLevel
	fmt.Printf("  calibrate  %s (%s)  vs  %s (%s)\n", terminalText(a.Model), terminalText(qa), terminalText(b.Model), terminalText(qb))
	fmt.Printf("  seedset    %s\n", terminalText(a.SeedSet))
	assessment := calibration.AssessPair(report)
	if assessment.SameBaseLineageVerified {
		fmt.Printf("  lineage    verified (%s)\n", terminalText(report.Lineage.Method))
		fmt.Printf("  evidence   unsigned: %s\n", terminalText(strings.Join(assessment.Reasons, "; ")))
	} else {
		fmt.Printf("  evidence   exploratory only: %s\n", terminalText(strings.Join(assessment.Reasons, "; ")))
	}
	var kept, drop []eval.ItemStat
	for _, s := range stats {
		if s.Discriminated() {
			kept = append(kept, s)
		} else {
			drop = append(drop, s)
		}
	}
	fmt.Printf("  %d items shared, %d discriminated, %d never flipped\n",
		len(stats), len(kept), len(drop))
	if ra, rb := eval.QuantRank(qa), eval.QuantRank(qb); ra == 0 || rb == 0 || ra == rb {
		fmt.Println("  note       dtypes are not a ranked pair; this is discrimination, not directional quant damage")
	} else if assessment.SameBaseLineageVerified {
		fmt.Println("  note       same-base lineage is verified; unsigned pairs still cannot create campaign readiness")
	} else {
		fmt.Println("  note       same-base revision lineage is unverified; flips are discrimination only, not directional quant damage")
	}
	if len(kept) > 0 {
		fmt.Println("\n  discriminated (these separate the two runs):")
		for _, s := range kept {
			fmt.Printf("    %-22s  %d/%d flipped  %s\n", terminalText(s.TaskID), s.Flips, s.Shared, terminalText(s.Need))
		}
	}
	if len(drop) > 0 {
		fmt.Println("\n  never flipped (not evidence to drop until more devices and model pairs agree):")
		for _, s := range drop {
			fmt.Printf("    %-22s  %d/%d agree    %s\n", terminalText(s.TaskID), s.Shared, s.Shared, terminalText(s.Need))
		}
	}
	if *out != "" {
		if err := calibration.WriteJSON(*out, report); err != nil {
			errPrint("could not write calibration report", err.Error(), "")
			return exitError
		}
		fmt.Printf("\n  wrote      %s (pseudonymous device, seedset, and local-model IDs; no prompts or raw output)\n", terminalText(*out))
	}
	fmt.Println("\n  this command does not rewrite spec/tasks. One pair is a lead, not a cull.")
	return exitOK
}

func calibrationPair(a, b *Result, stats []eval.ItemStat) (calibration.PairReport, error) {
	for index, result := range []*Result{a, b} {
		label := []string{"reference", "candidate"}[index]
		if result == nil {
			return calibration.PairReport{}, fmt.Errorf("%s result is unavailable", label)
		}
		if len(result.Contamination) > 0 {
			return calibration.PairReport{}, fmt.Errorf("%s result is contaminated by resident model(s): %s",
				label, strings.Join(result.Contamination, ", "))
		}
		if issue := result.EvidenceIntegrityIssue(); issue != "" {
			return calibration.PairReport{}, fmt.Errorf("%s result is unverified: %s", label, issue)
		}
	}
	if err := record.ProvenanceCompatibilityError(a, b); err != nil {
		return calibration.PairReport{}, fmt.Errorf("run provenance differs: %w", err)
	}
	aKey, aKeyErr := a.ComparableDeviceKey()
	bKey, bKeyErr := b.ComparableDeviceKey()
	if aKeyErr != nil || bKeyErr != nil {
		return calibration.PairReport{}, fmt.Errorf("verified effective context is required: %v; %v", aKeyErr, bKeyErr)
	}
	if aKey != bKey {
		return calibration.PairReport{}, errors.New("results were measured on different hardware or runtime configurations")
	}
	if diffs := a.Device.Diff(b.Device); len(diffs) > 0 {
		names := make([]string, 0, len(diffs))
		for _, diff := range diffs {
			names = append(names, diff[0])
		}
		return calibration.PairReport{}, fmt.Errorf("resolved device configuration differs: %s", strings.Join(names, ", "))
	}
	if a.NumCtx != b.NumCtx {
		return calibration.PairReport{}, fmt.Errorf("request context differs: %d vs %d", a.NumCtx, b.NumCtx)
	}
	paired := eval.PairFlips(a.Checks, b.Checks)
	if len(a.Checks) != len(b.Checks) || paired.Shared != len(a.Checks) {
		return calibration.PairReport{}, fmt.Errorf("check instances are incomplete: %d left, %d right, %d paired",
			len(a.Checks), len(b.Checks), paired.Shared)
	}
	fa, fb := strings.TrimSpace(a.ModelMeta.Details.Family), strings.TrimSpace(b.ModelMeta.Details.Family)
	if fa == "" || fb == "" || !strings.EqualFold(fa, fb) {
		return calibration.PairReport{}, fmt.Errorf("model family differs or is unknown: %q vs %q", fa, fb)
	}
	pa, pb := strings.TrimSpace(a.ModelMeta.Details.ParameterSize), strings.TrimSpace(b.ModelMeta.Details.ParameterSize)
	if pa == "" || pb == "" || !strings.EqualFold(pa, pb) {
		return calibration.PairReport{}, fmt.Errorf("parameter size differs or is unknown: %q vs %q", pa, pb)
	}
	spec, err := eval.LoadSpec()
	if err != nil {
		return calibration.PairReport{}, err
	}
	if a.SchemaVersion != b.SchemaVersion || a.SchemaVersion != spec.Version.ResultSchemaVersion {
		return calibration.PairReport{}, fmt.Errorf("result schema differs from this battery: %d, %d, current %d",
			a.SchemaVersion, b.SchemaVersion, spec.Version.ResultSchemaVersion)
	}
	fp := a.Device
	dev := calibration.Device{
		ID: calibration.PseudonymousDeviceID(aKey),
		OS: fp.OS, CPU: fp.CPU, RAMGB: fp.RAMGb, GPU: fp.GPU,
		GPUDriver: fp.GPUDriver, GPUDriverDate: fp.GPUDriverDate,
		GPUBackend: fp.GPUBackend, Runtime: fp.Runtime,
		InferenceDevice: fp.InferenceDevice, Config: fp.Config,
	}
	toRun := func(r *Result) calibration.Run {
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
	return calibration.NewPair(version, spec.Version.SpecVersion, a.SeedSet, dev, toRun(a), toRun(b), stats), nil
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

func cmdCalibrateMerge(args []string) int {
	fs := flag.NewFlagSet("calibrate merge", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	out := fs.String("out", "", "write the aggregate calibration summary as JSON")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() < 1 {
		errPrint("need calibration pair reports", "", "fitr calibrate merge pair-a.json pair-b.json --out summary.json")
		return exitUsage
	}
	reports := make([]calibration.PairReport, 0, fs.NArg())
	for _, path := range fs.Args() {
		r, err := calibration.ReadPair(path)
		if err != nil {
			errPrint("could not read calibration report", err.Error(), "")
			return exitError
		}
		reports = append(reports, r)
	}
	summary, err := calibration.Aggregate(reports)
	if err != nil {
		errPrint("could not aggregate calibration reports", err.Error(), "")
		return exitError
	}
	fmt.Printf("  calibration evidence  %d report(s), %d device(s), %d model pair(s), spec v%d\n",
		summary.Reports, summary.Devices, summary.ModelPairs, summary.SpecVersion)
	readiness := summary.Readiness
	fmt.Printf("  authenticated          %d/%d report(s), %d/%d device(s), %d/%d model families\n",
		readiness.DecisionGradeReports, summary.Reports,
		readiness.Devices, readiness.MinimumDevices,
		readiness.ModelFamilies, readiness.MinimumModelFamilies)
	fmt.Printf("  readiness              UNVERIFIED: %s\n", terminalText(strings.Join(readiness.Missing, "; ")))
	var exploratory []string
	for _, report := range reports {
		assessment := calibration.AssessPair(report)
		if !assessment.DecisionGrade {
			exploratory = append(exploratory, fmt.Sprintf("%s / %s: %s",
				report.Reference.Model, report.Candidate.Model, strings.Join(assessment.Reasons, "; ")))
		}
	}
	if len(exploratory) > 0 {
		fmt.Println("\n  exploratory reports excluded from readiness:")
		for _, reason := range exploratory {
			fmt.Printf("    %s\n", terminalText(reason))
		}
	}
	var observed, unseen []calibration.SummaryItem
	for _, item := range summary.Items {
		if item.Status == "observed" {
			observed = append(observed, item)
		} else {
			unseen = append(unseen, item)
		}
	}
	if len(observed) > 0 {
		fmt.Println("\n  discrimination observed:")
		for _, item := range observed {
			fmt.Printf("    %-22s  %d flip(s)/%d shared  on %d/%d device(s)\n",
				terminalText(item.TaskID), item.Flips, item.Shared, item.DiscriminatedDevices, item.Devices)
		}
	}
	if len(unseen) > 0 {
		fmt.Println("\n  discrimination not yet observed:")
		for _, item := range unseen {
			fmt.Printf("    %-22s  0/%d shared  across %d device(s)\n", terminalText(item.TaskID), item.Shared, item.Devices)
		}
	}
	if *out != "" {
		if err := calibration.WriteJSON(*out, summary); err != nil {
			errPrint("could not write calibration summary", err.Error(), "")
			return exitError
		}
		fmt.Printf("\n  wrote  %s\n", terminalText(*out))
	}
	fmt.Println("\n  evidence only: aggregation never deletes or rewrites a task.")
	return exitOK
}

// ---------------------------------------------------------------- compare
func cmdCompare(ctx context.Context, args []string) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Fprintln(os.Stderr, "usage: fitr compare <model-a> <model-b>")
		return exitOK
	}
	if len(args) != 2 {
		errPrint("need two models", "", "fitr compare <a> <b>")
		return exitUsage
	}
	results, err := loadResults()
	if err != nil {
		errPrint("no results", "", "fitr run <model>")
		return exitError
	}
	a, b := latestNamed(results, args[0]), latestNamed(results, args[1])
	for i, r := range []*Result{a, b} {
		if r == nil {
			errPrint(fmt.Sprintf("no stored result for %q", args[i]), "",
				"fitr run "+args[i])
			return exitError
		}
	}
	if len(a.Contamination) > 0 || len(b.Contamination) > 0 {
		fmt.Printf("  %s  vs  %s\n\n", terminalText(a.Model), terminalText(b.Model))
		fmt.Println("  INCONCLUSIVE  resident model contamination invalidates this comparison")
		for _, result := range []*Result{a, b} {
			if len(result.Contamination) == 0 {
				continue
			}
			fmt.Printf("  %-12s %s\n", terminalText(result.Model)+":",
				terminalText(strings.Join(result.Contamination, ", ")))
		}
		fmt.Println("  remedy       unload all models and re-run both measurements")
		return exitError
	}
	if issueA, issueB := a.EvidenceIntegrityIssue(), b.EvidenceIntegrityIssue(); issueA != "" || issueB != "" {
		fmt.Printf("  %s  vs  %s\n\n", terminalText(a.Model), terminalText(b.Model))
		fmt.Println("  INCONCLUSIVE  a valid sealed evidence contract is required for comparison")
		for i, issue := range []string{issueA, issueB} {
			if issue == "" {
				continue
			}
			result := []*Result{a, b}[i]
			fmt.Printf("  %-12s %s\n", terminalText(result.Model)+":", terminalText(issue))
		}
		fmt.Println("  remedy       re-run both models with the current fitr version")
		return exitError
	}
	if err := record.ProvenanceCompatibilityError(a, b); err != nil {
		fmt.Printf("  %s  vs  %s\n\n", terminalText(a.Model), terminalText(b.Model))
		fmt.Println("  INCONCLUSIVE  the runs used different task, profile, specification, scoring, or protocol provenance")
		fmt.Printf("  detail       %s\n", terminalText(err.Error()))
		fmt.Println("  remedy       compare runs produced by the same fitr battery and effective profile")
		return exitError
	}
	aKey, aKeyErr := a.ComparableDeviceKey()
	bKey, bKeyErr := b.ComparableDeviceKey()
	if aKeyErr != nil || bKeyErr != nil {
		fmt.Printf("  %s  vs  %s\n\n", terminalText(a.Model), terminalText(b.Model))
		fmt.Println("  INCONCLUSIVE  verified effective context is required for comparison")
		for i, keyErr := range []error{aKeyErr, bKeyErr} {
			if keyErr == nil {
				continue
			}
			result := []*Result{a, b}[i]
			fmt.Printf("  %-12s %s\n", terminalText(result.Model)+":", terminalText(keyErr.Error()))
		}
		fmt.Println("  remedy       re-run both models on a runtime that reports its allocated context")
		return exitError
	}
	if aKey != bKey {
		if a.DeviceV2 != nil && b.DeviceV2 != nil && reflect.DeepEqual(a.DeviceV2.Device, b.DeviceV2.Device) {
			aEffective, bEffective := 0, 0
			if a.DeviceV2.Context.EffectiveTokens != nil {
				aEffective = *a.DeviceV2.Context.EffectiveTokens
			}
			if b.DeviceV2.Context.EffectiveTokens != nil {
				bEffective = *b.DeviceV2.Context.EffectiveTokens
			}
			errPrint("these results used different request context",
				fmt.Sprintf("requested %d vs %d, effective %d vs %d; tok/s and quality both move with KV size",
					resultNumCtx(a), resultNumCtx(b), aEffective, bEffective),
				"compare two runs at the same --ctx, or re-measure")
			return exitError
		}
		// Naming the machine is misleading when both runs happened on it: the
		// usual cause is that its STATE moved between them -- a model resident
		// from something else, so one run was partly offloaded and the other
		// was not. Fingerprint.Diff already knows exactly which fields moved,
		// so say them instead of sending the operator to re-measure blind.
		note := incomparableNote(a.Device.Diff(b.Device))
		errPrint("these results were not measured under the same conditions", note,
			"re-measure both with the machine in the same state, then compare")
		return exitError
	}

	fmt.Printf("  %s  vs  %s\n\n", terminalText(a.Model), terminalText(b.Model))

	// Throughput: Fieller's interval is the correct one for "how many times
	// faster". When it cannot be computed honestly (single observation, or
	// denominator not separated from zero), the ratio prints without an
	// interval and therefore without a verdict.
	for _, p := range []struct {
		label string
		x, y  stats.Summary
	}{
		{"decode tok/s", a.DecodeSum, b.DecodeSum},
		{"prefill tok/s", a.PrefillSum, b.PrefillSum},
	} {
		lo, hi, ratio, ok := stats.FiellerRatio(p.x, p.y)
		if ok {
			verdict := "~ cannot separate"
			if lo > 1 {
				verdict = "first is faster"
			} else if hi < 1 {
				verdict = "second is faster"
			}
			fmt.Printf("  %-22s %8.2f vs %8.2f   %.2fx [%.2f .. %.2f]  %s\n",
				p.label, p.x.Mean, p.y.Mean, ratio, lo, hi, verdict)
		} else {
			ratio, _, _ := stats.RatioWithError(p.x, p.y)
			fmt.Printf("  %-22s %8.2f vs %8.2f   %.2fx (no interval - not enough data to support one)\n",
				p.label, p.x.Mean, p.y.Mean, ratio)
		}
	}
	fmt.Println()

	// Pass rates: the Newcombe difference interval is the sole arbiter. A
	// difference is claimed if and only if the interval excludes zero.
	for _, need := range []string{"coding", "structured_output", "instruction_precision", "user_tasks"} {
		pa, pb := poolOf(a, need), poolOf(b, need)
		if pa.N == 0 || pb.N == 0 {
			continue
		}
		lo, hi, ok := stats.NewcombeDiff(pa.Passes, pa.N, pb.Passes, pb.N)
		if !ok {
			continue
		}
		d := float64(pa.Passes)/float64(pa.N) - float64(pb.Passes)/float64(pb.N)
		verdict := "~ cannot separate"
		if lo > 0 {
			verdict = "first is better here"
		} else if hi < 0 {
			verdict = "second is better here"
		}
		fmt.Printf("  %-22s %d/%d vs %d/%d   diff %+.2f [%+.2f .. %+.2f]  %s\n",
			need, pa.Passes, pa.N, pb.Passes, pb.N, d, lo, hi, verdict)
	}

	// Paired analysis: when both runs faced IDENTICAL generated instances,
	// the item-level flips carry far more information than the two rates.
	fmt.Println()
	if a.SeedSet != "" && a.SeedSet == b.SeedSet {
		pairedCompare(a, b)
	} else if len(a.Checks) > 0 && len(b.Checks) > 0 {
		fmt.Println("  unpaired: the runs faced different generated instances.")
		fmt.Printf("  for a sharper paired test:  fitr run <model> --seedset shared1  (both models)\n")
	}

	fmt.Println("\n  note  a difference is claimed only when its 95% interval excludes zero;")
	fmt.Println("        \"cannot separate\" is a real answer, not a missing one.")
	return exitOK
}

// poolOf rebuilds the per-need trial pools from a stored result. coding pools
// the executed code tasks with the generated reasoning checks, mirroring the
// scorecard.
func poolOf(r *Result, need string) (p score.Pool) {
	if need == "coding" {
		for _, x := range append(append([]eval.ExecResult{}, r.CodeWrite...), r.CodeFix...) {
			pass, measured := eval.MeasuredOutcome(x.Outcome, x.Pass)
			if !measured {
				continue
			}
			p.N++
			if pass {
				p.Passes++
			}
		}
	}
	for _, ck := range r.Checks {
		match := ck.Need == need || (need == "coding" && ck.Need == "reasoning")
		if !match {
			continue
		}
		pass, measured := eval.MeasuredOutcome(ck.Outcome, ck.Pass)
		if !measured {
			continue
		}
		p.N++
		if pass {
			p.Passes++
		}
	}
	return p
}

// pairedCompare runs McNemar's exact test on instances both models faced.
// Concordant instances carry no information about the difference; only the
// flips decide, and with fewer than six of them no split can reach p<0.05 -
// which is reported as exactly that, never as a near-miss.
func pairedCompare(a, b *Result) {
	flips := eval.PairFlips(a.Checks, b.Checks)
	if flips.Shared == 0 {
		fmt.Println("  paired: seedsets match but no shared instances were found.")
		return
	}
	fmt.Printf("  paired on %d identical instances: %s alone passed %d, %s alone passed %d, agreed on %d\n",
		flips.Shared, terminalText(a.Model), flips.AOnly, terminalText(b.Model), flips.BOnly, flips.Agree)
	if flips.HidesDisagreement() {
		fmt.Printf("  accuracy hid %d item-level flip(s) (%d/%d vs %d/%d) - rates match, the questions did not\n",
			flips.AOnly+flips.BOnly, flips.APass, flips.Shared, flips.BPass, flips.Shared)
	}
	if line, ok := quantDamageLine(a, b, flips); ok {
		fmt.Println("  " + terminalText(line))
	}
	switch {
	case flips.AOnly+flips.BOnly == 0:
		fmt.Println("  identical outcomes on every shared instance - no evidence of any difference.")
	default:
		pExact, pMid, separable := stats.McNemarExact(flips.AOnly, flips.BOnly)
		if !separable {
			fmt.Printf("  %d discordant instance(s) - too few to separate at alpha=0.05 regardless of split.\n",
				flips.AOnly+flips.BOnly)
			return
		}
		winner := a.Model
		if flips.BOnly > flips.AOnly {
			winner = b.Model
		}
		if pExact < 0.05 {
			fmt.Printf("  %s wins the flips (McNemar exact p=%.3f, mid-p %.3f)\n", terminalText(winner), pExact, pMid)
		} else {
			fmt.Printf("  ~ the flips do not separate them (McNemar exact p=%.3f, mid-p %.3f)\n", pExact, pMid)
		}
	}
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
