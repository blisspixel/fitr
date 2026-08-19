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
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/advise"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/llamaserver"
	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/lock"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/openaicompat"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/retonr"
	"github.com/blisspixel/fitr/internal/score"
	"github.com/blisspixel/fitr/internal/stats"
)

const version = "0.2.0"

// Exit codes: small, documented, domain-specific. Not sysexits -- nobody uses it.
const (
	exitOK        = 0 // ran, and every measured need passed
	exitError     = 1 // something broke
	exitUsage     = 2 // bad invocation
	exitGates     = 3 // ran fine, but a need FAILED (useful as a CI gate)
	exitInterrupt = 130
)

func errPrint(msg, note, hint string) {
	fmt.Fprintf(os.Stderr, "error: %s\n", msg)
	if note != "" {
		fmt.Fprintf(os.Stderr, " note: %s\n", note)
	}
	if hint != "" {
		fmt.Fprintf(os.Stderr, " hint: %s\n", hint)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `fitr `+version+` - is this local model any good ON THIS DEVICE?

usage:
  fitr run <model> [--quick|--full] [-k N] [--profile P] [--display MODE] [--html]
  fitr advise <model> [--vram-gb N] [--ctx N] [--load] [--fit]
  fitr tune [model-a model-b]
  fitr export <model> [--out PATH] [--retonr]
  fitr board [--current]
  fitr diag <model>
  fitr doctor <model> [-n N]
  fitr device
  fitr profiles [new [name]]
  fitr calibrate <model-a> <model-b>
  fitr compare <model-a> <model-b>

flags:
  --display  auto|plain|json|none   output mode (default auto)
  --backend  auto|ollama|llama-server|openai   serving runtime (default auto-detect)
  --pull     fetch a missing model first (Ollama; HF links pull automatically)
  -k         repeats per noisy task (default 3, 1 with --quick)
             A single run is not a measurement: identical configs vary 10-20pp.
  -q         quiet (repeat for silent)      -v  verbose

exit codes:
  0 ok   1 error   2 usage   3 a need FAILED   130 interrupted

examples:
  fitr run qwen3-coder:30b --full
  fitr run https://huggingface.co/bartowski/Qwen2.5-Coder-7B-Instruct-GGUF
  fitr run some-new-model:tag -k 3 --pull
  fitr diag dolphin3:8b
  fitr doctor qwen3-coder:30b
  fitr advise qwen3:30b
  fitr advise ./model.gguf --vram-gb 8 --fit
  fitr tune
  fitr tune qwen3:30b qwen3:30b-q8
  fitr export qwen3:30b --out scorecard.html
  fitr profiles new
  fitr calibrate qwen3:30b-q8 qwen3:30b-q4
`)
}

func main() { os.Exit(run()) }

func run() int {
	if len(os.Args) < 2 {
		usage()
		return exitUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	switch os.Args[1] {
	case "run":
		return cmdRun(ctx, os.Args[2:])
	case "advise":
		return cmdAdvise(ctx, os.Args[2:])
	case "tune":
		return cmdTune(ctx, os.Args[2:])
	case "export":
		return cmdExport(ctx, os.Args[2:])
	case "board":
		return cmdBoard(ctx, os.Args[2:])
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
		errPrint(fmt.Sprintf("unknown command %q", os.Args[1]), "",
			"run `fitr --help` for usage")
		return exitUsage
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
	case "k", "n", "profile", "display", "q", "backend", "seedset", "vram-gb", "ctx", "out":
		return true
	}
	return false
}

// newBackend resolves which serving runtime to measure through.
// Selection order: --backend flag, then $FITR_BACKEND, then auto-probe
// (Ollama first - it is the default URL people have running - then
// llama-server, then any OpenAI-compatible server at the LM Studio port).
//
// A Hugging Face ref (pasted URL or hf.co/...) is an Ollama pull, not a
// label to file results under while measuring whatever else is already
// loaded. Prefer Ollama; refuse to pretend llama-server fetched it.
func newBackend(ctx context.Context, model, kind string, pull bool) (llm.Backend, int) {
	if kind == "" || kind == "auto" {
		kind = os.Getenv("FITR_BACKEND")
	}
	if isHFRef(model) && (kind == "" || kind == "auto" || kind == "ollama") {
		o := ollama.New()
		if !o.Reachable(ctx) {
			errPrint("Hugging Face refs need a running Ollama",
				"Ollama pulls GGUFs from hf.co/{user}/{repo}[:quant]; other servers already have a model loaded",
				"start `ollama serve` and re-run, or pass the name of a model already being served")
			return nil, exitError
		}
		return checkModel(ctx, o, model, pull)
	}
	switch kind {
	case "", "auto":
		found := llm.Discover(ctx)
		if len(found) == 0 {
			errPrint("no serving runtime reachable",
				"tried "+strings.Join(llm.Candidates(), ", "),
				"start one, or point fitr at it: OLLAMA_BASE_URL, LLAMA_SERVER_URL, FITR_OPENAI_URL, FITR_DISCOVER_URLS, or --backend")
			return nil, exitError
		}
		if len(found) > 1 {
			var extra []string
			for _, f := range found[1:] {
				extra = append(extra, f.Kind+" at "+f.URL)
			}
			fmt.Fprintf(os.Stderr, "! also found %s - using %s at %s; set --backend or a URL env to pick\n",
				strings.Join(extra, ", "), found[0].Kind, found[0].URL)
		}
		return checkModel(ctx, backendAt(found[0].Kind, found[0].URL), model, pull)
	case "ollama":
		o := ollama.New()
		if !o.Reachable(ctx) {
			errPrint("cannot reach Ollama at "+o.URL(),
				"every measurement needs a running server",
				"start it with `ollama serve`, or set OLLAMA_BASE_URL")
			return nil, exitError
		}
		return checkModel(ctx, o, model, pull)
	case "llama-server", "llamaserver":
		l := llamaserver.New()
		if !l.Reachable(ctx) {
			errPrint("cannot reach llama-server at "+l.URL(),
				"every measurement needs a running server",
				"start it with `llama-server -m model.gguf`, or set LLAMA_SERVER_URL")
			return nil, exitError
		}
		return checkModel(ctx, l, model, pull)
	case "openai":
		g := openaicompat.New()
		if !g.Reachable(ctx) {
			errPrint("cannot reach an OpenAI-compatible server at "+g.URL(),
				"every measurement needs a running server",
				"start LM Studio / vLLM / SGLang, or set FITR_OPENAI_URL")
			return nil, exitError
		}
		return checkModel(ctx, g, model, pull)
	default:
		errPrint(fmt.Sprintf("unknown backend %q", kind), "",
			"valid: auto, ollama, llama-server, openai")
		return nil, exitUsage
	}
}

// normalizeModelRef accepts pasted Hugging Face URLs and turns them into the
// hf.co/{user}/{repo}[:quant] form Ollama pulls natively - so "point fitr at
// an HF link" just works. Blob/resolve URLs keep the quant from the filename.
func normalizeModelRef(model string) string {
	m := strings.TrimSpace(model)
	m = strings.SplitN(m, "?", 2)[0]
	m = strings.SplitN(m, "#", 2)[0]
	m = strings.TrimRight(m, "/")
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
			return "hf.co/" + parseHFPath(m[len(prefix):])
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
		return base
	}
	cand := base[i+1:]
	if isQuantTag(cand) {
		return strings.ToUpper(cand)
	}
	return filepath.Base(base)
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

// sameServedModel treats "qwen3:30b" and "qwen3:30b:latest" as one tag.
// It does not guess across different names.
func sameServedModel(want, have string) bool {
	if want == have {
		return true
	}
	trim := func(s string) string {
		s = strings.TrimSuffix(s, ":latest")
		return strings.TrimSuffix(s, ":LATEST")
	}
	return trim(want) == trim(have)
}

// checkModel verifies the model label against what the backend serves. On
// Ollama a missing model is a hard error with a pull hint - or an automatic
// pull with progress when the caller allows it; a single-model server ignores
// the label at request time, so a mismatch there is a warning - the results
// would otherwise be filed under a name the server never saw.
func checkModel(ctx context.Context, b llm.Backend, model string, pull bool) (llm.Backend, int) {
	if model == "" {
		return b, exitOK
	}
	tags, err := b.Tags(ctx)
	if err != nil || len(tags) == 0 {
		return b, exitOK
	}
	found := false
	var near []string
	base := strings.SplitN(model, ":", 2)[0]
	for _, t := range tags {
		if t.Name == model {
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
		if isHFRef(model) {
			errPrint("Hugging Face refs need Ollama to pull",
				b.Name()+" is serving its own model, not fetching from Hugging Face",
				"start Ollama, or pass the served model name instead of an HF URL")
			return nil, exitUsage
		}
		if pull {
			fmt.Fprintf(os.Stderr, "! --pull is an Ollama feature; %s serves whatever is already loaded\n", b.Name())
		}
		fmt.Fprintf(os.Stderr, "! %s serves %q, not %q - results will be recorded under %q\n",
			b.Name(), tags[0].Name, model, model)
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
			fmt.Fprintf(os.Stderr, "  pulling %s from %s\n", model, src)
			last := ""
			err := o.Pull(ctx, model, func(status string, pct int) {
				line := status
				if pct >= 0 {
					line = fmt.Sprintf("%s %d%%", status, pct)
				}
				if line != last {
					fmt.Fprintf(os.Stderr, "\r  %-60s", line)
					last = line
				}
			})
			fmt.Fprintln(os.Stderr)
			if err != nil {
				errPrint("pull failed: "+err.Error(), "", "")
				return nil, exitError
			}
			return b, exitOK
		}
	}
	hint := "pull it first: `ollama pull " + model + "`, or re-run with --pull"
	if len(near) > 0 {
		hint = "did you mean: " + strings.Join(near, ", ")
	}
	errPrint(fmt.Sprintf("model %q is not installed", model),
		fmt.Sprintf("%d model(s) available", len(tags)), hint)
	return nil, exitUsage
}

// probeBackend is the no-error variant for commands that merely display state.
func probeBackend(ctx context.Context) llm.Backend {
	found := llm.Discover(ctx)
	if len(found) == 0 {
		return ollama.New()
	}
	return backendAt(found[0].Kind, found[0].URL)
}

func backendAt(kind, url string) llm.Backend {
	url = strings.TrimRight(url, "/")
	switch kind {
	case "llama-server", "llamaserver":
		c := llamaserver.New()
		if url != "" {
			c.BaseURL = url
		}
		return c
	case "openai":
		c := openaicompat.New()
		if url != "" {
			c.BaseURL = url
		}
		return c
	default:
		c := ollama.New()
		if url != "" {
			c.BaseURL = url
		}
		return c
	}
}

// ---------------------------------------------------------------- advise
// cmdAdvise answers: does this model fit on this box, and if not, which
// flag to try? SKIP when fit cannot be measured (no VRAM reading, no
// weights, no architecture) - never a fabricated GB number.
func cmdAdvise(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("advise", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vram := fs.Float64("vram-gb", -1, "available GPU memory in GB (skip detection)")
	ctxSize := fs.Int("ctx", 0, "requested context length (default: model's max)")
	backend := fs.String("backend", "auto", "auto|ollama|llama-server|openai")
	mode := fs.String("display", "auto", "auto|plain|json|none")
	pullFlag := fs.Bool("pull", false, "pull the model first if it is not installed")
	loadFlag := fs.Bool("load", false, "load the model on Ollama and read resident size (dummy allocation)")
	fitFlag := fs.Bool("fit", false, "run llama-fit-params on a GGUF if it is on PATH")
	if err := fs.Parse(permute(args)); err != nil {
		return exitUsage
	}
	if fs.NArg() < 1 {
		errPrint("missing model", "", "fitr advise <model>  or  fitr advise ./model.gguf")
		return exitUsage
	}
	raw := fs.Arg(0)
	model := normalizeModelRef(raw)

	in := advise.Input{Model: model, Ctx: *ctxSize}
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
		if in.Source == "" {
			in.Source = c.Name() + " (no architecture metadata)"
		}
		if running, err := c.PS(ctx); err == nil {
			for _, m := range running {
				if !sameServedModel(model, m.Name) || m.Size <= 0 {
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
		fmt.Fprintf(os.Stderr, "  llama-fit-params %s\n", ggufPath)
		used, cannot, err := advise.RunFitParams(ctx, ggufPath, *ctxSize)
		if err != nil {
			fmt.Fprintf(os.Stderr, " note: llama-fit-params not used: %s\n", err)
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
				fmt.Fprintf(os.Stderr, "  loading %s at num_ctx=%d to measure resident size\n", model, ctxLen)
				_, _, err = c.Generate(ctx, model, ".", ollama.Deterministic(1, ctxLen))
				if err != nil {
					in.LoadErr = err.Error()
				} else if running, err := c.PS(ctx); err == nil {
					for _, m := range running {
						if !sameServedModel(model, m.Name) || m.Size <= 0 {
							continue
						}
						in.ResidentB = m.Size
						in.ResidentSrc = "ollama /api/ps after --load"
						break
					}
				}
				if left, err := c.StopAll(ctx); err == nil && len(left) > 0 {
					fmt.Fprintf(os.Stderr, " note: still resident: %s\n", strings.Join(left, ", "))
				}
			}
		}
	}

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
		if rep.Tier == advise.Compatible || rep.Tier == advise.LowMemory {
			fmt.Fprintf(os.Stderr, "\n  next   fitr run %s --full\n", in.Model)
		}
	}
	return rep.ExitCode()
}

// ---------------------------------------------------------------- tune
// cmdTune does not run a sweep. llama-bench already owns throughput-only
// points, and a documented flash-attention quality regression is why
// throughput-only is not enough. Until fitr can restart a server, tune
// prints the request-level knobs and diffs two observed fingerprints.
func cmdTune(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("tune", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(permute(args)); err != nil {
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
    fitr run <model> --full
    fitr compare <a> <b>
  score quality + degeneracy + throughput jointly. llama-bench already owns
  throughput-only sweeps.

`)
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stdout, "  next   fitr tune <model-a> <model-b>   to diff two saved fingerprints")
		return exitOK
	}
	if fs.NArg() != 2 {
		errPrint("tune diffs two saved results", "", "fitr tune <model-a> <model-b>")
		return exitUsage
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
	fmt.Fprintf(os.Stdout, "  %s  vs  %s\n", a.Model, b.Model)
	d := a.Device.Diff(b.Device)
	if len(d) == 0 {
		fmt.Fprintln(os.Stdout, "  same fingerprint - quality is `fitr compare`'s job, not a device change")
		return exitOK
	}
	fmt.Fprintln(os.Stdout, "  fingerprint diffs (these runs are not comparable):")
	for _, x := range d {
		fmt.Fprintf(os.Stdout, "    %-22s  %s  ->  %s\n", x[0], emptyDash(x[1]), emptyDash(x[2]))
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
		if r.Model == name || strings.Contains(r.Model, name) {
			if hit == nil || r.StartedAt > hit.StartedAt {
				hit = r
			}
		}
	}
	return hit
}

// ---------------------------------------------------------------- run
func cmdRun(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	quick := fs.Bool("quick", false, "speed+memory+coding+tools")
	full := fs.Bool("full", false, "adds the 40-turn agentic task")
	k := fs.Int("k", 0, "repeats per noisy task")
	profileName := fs.String("profile", "", "device profile (default: auto-match)")
	mode := fs.String("display", "auto", "auto|plain|json|none")
	backend := fs.String("backend", "auto", "auto|ollama|llama-server|openai")
	seedset := fs.String("seedset", "", "pin the generated-instance seed set; two runs sharing "+
		"a seedset face IDENTICAL task instances, enabling a paired comparison")
	adaptive := fs.Bool("adaptive", false, "repeat generated checks until each gated need is "+
		"decided against its gate (Wald SPRT, alpha=beta=0.05) or 6 rounds pass")
	pullFlag := fs.Bool("pull", false, "pull the model first if it is not installed "+
		"(Ollama; supports hf.co/... and pasted Hugging Face URLs)")
	htmlFlag := fs.Bool("html", false, "write a self-contained HTML artifact next to the JSON")
	quiet := fs.Int("q", 0, "quiet level")
	verbose := fs.Bool("v", false, "verbose")
	if err := fs.Parse(permute(args)); err != nil {
		return exitUsage
	}
	if fs.NArg() < 1 {
		errPrint("missing model", "", "fitr run <model> --full")
		return exitUsage
	}
	model := normalizeModelRef(fs.Arg(0))

	level := "default"
	if *quick {
		level = "quick"
	} else if *full {
		level = "full"
	}
	reps := *k
	if reps == 0 {
		reps = 3
		if level == "quick" {
			reps = 1
		}
	}
	if *quiet > 1 {
		*mode = "none"
	} else if (*quiet > 0 || *verbose) && *mode == "auto" {
		*mode = "plain"
	}

	c, code := newBackend(ctx, model, *backend, *pullFlag)
	if code != exitOK {
		return code
	}
	disp := render.New(*mode)
	defer disp.Close()

	// Check tasks generate a FRESH instance per repeat, so one pass per task is
	// already a set of independent trials pooled per need. Repeats multiply
	// wall-clock across ~16 tasks, so they follow -k only when asked.
	checksReps := 1
	if *k > 0 {
		checksReps = *k
	}

	res, err := execute(ctx, c, model, runOpts{
		level: level, profile: *profileName, seedSet: *seedset,
		reps: reps, checksReps: checksReps, adaptive: *adaptive,
	}, disp)
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "\ninterrupted")
			return exitInterrupt
		}
		errPrint(err.Error(), "", "re-run with -v for detail")
		return exitError
	}

	path, err := save(res)
	if err != nil {
		errPrint("could not save result: "+err.Error(), "", "")
	}
	res.Meta.SavedPath = path
	disp.Result(res.Scorecard, res.Meta)

	htmlDest := ""
	if *htmlFlag {
		htmlDest = "auto"
	}
	htmlFile, htmlErr := writeHTMLArtifact(res, htmlDest, path)
	if htmlErr != nil {
		errPrint("could not write HTML: "+htmlErr.Error(), "", "")
	}
	if *quiet == 0 && render.Resolve(*mode) != "json" {
		fmt.Fprintf(os.Stderr, "\n  saved  %s\n", path)
		if htmlFile != "" {
			fmt.Fprintf(os.Stderr, "  html   %s\n", htmlFile)
		}
		fmt.Fprintf(os.Stderr, "  next   fitr board\n")
		if !*htmlFlag {
			fmt.Fprintf(os.Stderr, "         fitr export %s   for a shareable HTML scorecard\n", model)
		}
		if h := retonr.Hint(model); h != "" {
			fmt.Fprintf(os.Stderr, "         %s\n", h)
		}
		if reps < 3 {
			fmt.Fprintf(os.Stderr, "         fitr run %s -k 3   for a rankable result\n", model)
		}
	}
	if res.Scorecard.Fails > 0 {
		return exitGates
	}
	return exitOK
}

// ---------------------------------------------------------------- result
type Result struct {
	SchemaVersion int    `json:"schema_version"`
	Model         string `json:"model"`
	StartedAt     string `json:"started_at"`
	Level         string `json:"level"`
	// SeedSet names the instance set the generated checks were drawn from.
	// Unique per run by default (contamination resistance); pinned via
	// --seedset so two models can face IDENTICAL instances, which upgrades
	// `fitr compare` from an unpaired comparison to a paired one.
	SeedSet     string             `json:"seedset,omitempty"`
	Repeats     int                `json:"repeats"`
	WallSeconds float64            `json:"wall_s"`
	Device      device.Fingerprint `json:"device"`
	DeviceKey   string             `json:"device_key"`
	Profile     string             `json:"profile"`
	ModelMeta   ollama.ModelInfo   `json:"model_meta"`

	Speed      []eval.SpeedResult             `json:"speed_repeats"`
	DecodeSum  stats.Summary                  `json:"decode_summary"`
	TTFTSum    stats.Summary                  `json:"ttft_summary"`
	PrefillSum stats.Summary                  `json:"prefill_summary"`
	Memory     eval.MemoryResult              `json:"memory"`
	CodeWrite  []eval.ExecResult              `json:"code_write"`
	CodeFix    []eval.ExecResult              `json:"code_fix"`
	Checks     []eval.CheckOutcome            `json:"checks,omitempty"`
	Tools      []eval.ToolLoopResult          `json:"tools"`
	Withdrawal *eval.ToolLoopResult           `json:"tool_withdrawal,omitempty"`
	Agentic    *eval.ToolLoopResult           `json:"agentic,omitempty"`
	Refusal    map[string]eval.RefusalVerdict `json:"refusal,omitempty"`
	Refused    int                            `json:"refused_count"`
	Plumbing   *eval.PlumbingResult           `json:"plumbing,omitempty"`
	Rep        score.Repetition               `json:"repetition"`
	Density    score.Density                  `json:"density"`

	// Contamination lists models that refused to unload. A non-empty value
	// means every timing in this result is suspect.
	Contamination []string `json:"contamination,omitempty"`

	Scorecard score.Scorecard `json:"scorecard"`
	Meta      render.Meta     `json:"-"`
}

// runOpts carries the run configuration so execute's signature stays legible.
type runOpts struct {
	level, profile, seedSet string
	reps, checksReps        int
	adaptive                bool
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

	spec, err := eval.LoadSpec()
	if err != nil {
		return nil, err
	}
	// User tasks extend the battery without a fork. A malformed one is a hard
	// error with the filename in it - silently dropping your own task would
	// defeat the point of having one.
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
	fp := device.Detect(ctx, c)
	prof, err := device.SelectProfile(profileName, fp)
	if err != nil {
		return nil, err
	}
	info, _ := c.Show(ctx, model)

	res := &Result{
		SchemaVersion: spec.Version.ResultSchemaVersion,
		Model:         model,
		StartedAt:     time.Now().Format(time.RFC3339),
		Level:         level, Repeats: reps,
		Device: fp, DeviceKey: fp.Key(), Profile: prof.Name, ModelMeta: info,
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
	step := func(name, detail string, fn func() error) error {
		disp.Phase(name, detail)
		t := time.Now()
		if err := fn(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		disp.Done(name, time.Since(t).Seconds())
		return nil
	}

	// One model resident at a time is non-negotiable between phases. A model
	// that will not unload is recorded and warned about -- data marked suspect
	// beats data silently trusted.
	stopAll := func() {
		left, err := c.StopAll(ctx)
		if err != nil {
			disp.Note("could not confirm models unloaded: "+err.Error(), "warn")
			return
		}
		if len(left) == 0 {
			return
		}
		res.Contamination = appendUnique(res.Contamination, left...)
		disp.Note("still resident after unload: "+strings.Join(left, ", ")+
			" - timings in this run may be contaminated", "warn")
	}

	stopAll()
	if err := step("speed", fmt.Sprintf("x%d", reps), func() error {
		for i := range reps {
			// The nonce MUST vary: identical long prompts hit the prefix cache
			// and prefill becomes fiction.
			nonce := fmt.Sprintf("%s-%d", res.StartedAt, i)
			s, err := eval.RunSpeed(ctx, c, model, spec, nonce)
			if err != nil {
				return err
			}
			res.Speed = append(res.Speed, s)
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
	res.Device.InferenceDevice = device.InferenceDeviceFor(ctx, c, model)

	if err := step("memory", "resident @32K", func() error {
		m, err := eval.RunMemory(ctx, c, model, 32768)
		res.Memory = m
		return err
	}); err != nil {
		return nil, err
	}

	if err := step("coding", fmt.Sprintf("x%d", reps), func() error {
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
	// gate - and the scorecard's borderline annotation will say so.
	if level != "quick" && len(spec.Checks) > 0 {
		rounds := checksReps
		var sprts map[string]*stats.SPRT
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
					}
				}
			}
		}
		roundsRun := 0
		if err := step("checks", detail, func() error {
			for round := 0; round < rounds; round++ {
				roundsRun = round + 1
				for _, cs := range spec.Checks {
					seed := eval.InstanceSeed(res.SeedSet, cs.ID, round)
					o, err := eval.RunCheck(ctx, c, model, cs, seed)
					if err != nil {
						return err
					}
					res.Checks = append(res.Checks, o)
					if s, ok := sprts[cs.Need]; ok {
						s.Add(o.Pass)
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
			disp.Note(adaptiveSummary(sprts, roundsRun, len(res.Checks)), "")
		}
	}

	// Plumbing BEFORE capability: a tools failure is uninterpretable until you
	// know the template and parser work.
	if err := step("plumbing", "tool round-trip", func() error {
		p, err := eval.RunPlumbing(ctx, c, model, spec.Plumbing)
		if err == nil {
			res.Plumbing = &p
		}
		return nil
	}); err != nil {
		return nil, err
	}

	if err := step("tools", fmt.Sprintf("x%d", reps), func() error {
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

	if level != "quick" {
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
	}

	if level != "quick" {
		if err := step("refusal", "3 prompts", func() error {
			r, n, err := eval.RunRefusal(ctx, c, model, spec.Refusal)
			res.Refusal, res.Refused = r, n
			return err
		}); err != nil {
			return nil, err
		}
	}

	if level == "full" {
		if err := step("agentic", fmt.Sprintf("up to %d unsupervised turns", spec.Agentic.MaxTurns), func() error {
			stopAll()
			a, err := eval.RunToolLoop(ctx, c, model, spec.Agentic, filepath.Join(work, "ag"))
			if err != nil {
				return err
			}
			res.Agentic = &a
			return nil
		}); err != nil {
			return nil, err
		}
	}

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
	stopAll()

	res.Scorecard = score.Score(measure(res), prof)
	res.Meta = render.Meta{
		ParamSize: info.Details.ParameterSize, Quant: info.Details.QuantizationLevel,
		Family: info.Details.Family, GPU: fp.GPU, Driver: fp.GPUDriver,
		Device: res.Device.InferenceDevice, Profile: prof.Name, Repeats: reps,
		DecodeMean: res.DecodeSum.Mean, DecodeSD: res.DecodeSum.SD,
		DecodeMin: res.DecodeSum.Min, DecodeMax: res.DecodeSum.Max,
		DecodeN:     res.DecodeSum.N,
		PrefillMean: res.PrefillSum.Mean, PrefillSD: res.PrefillSum.SD,
		PrefillN: res.PrefillSum.N,
	}
	var decodes []float64
	for _, s := range res.Speed {
		decodes = append(decodes, s.DecodeTPS)
	}
	res.Meta.FirstRunSlow, res.Meta.FirstRunRatio = stats.FirstRunSlow(decodes)

	// State what this sample size CANNOT resolve, out loud, on every run.
	trials := len(res.CodeWrite) + len(res.CodeFix) + len(res.Tools) + len(res.Checks)
	if res.Agentic != nil {
		trials++
	}
	if trials > 0 {
		res.Meta.Trials = trials
		res.Meta.MDEpp = 100 * stats.MinDetectableEffect(trials, 1)
	}
	return res, nil
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
		Rep: r.Rep,
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
	if len(r.CodeWrite) > 0 {
		m.CodeKnown = true
		var wOK, fOK []bool
		for _, x := range r.CodeWrite {
			wOK = append(wOK, x.Pass)
		}
		for _, x := range r.CodeFix {
			fOK = append(fOK, x.Pass)
		}
		fw, ff := stats.Flakiness(wOK), stats.Flakiness(fOK)
		m.CodeWritePass = fw.Passes*2 > fw.N
		m.CodeFixPass = ff.Passes*2 > ff.N
		m.CodeFlaky = fw.Flaky || ff.Flaky
		m.CodePasses = fw.Passes + ff.Passes
		m.CodeRepeats = fw.N + ff.N
	}
	for _, ck := range r.Checks {
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
		pool.N++
		if ck.Pass {
			pool.Passes++
		}
	}
	if r.Refusal != nil {
		m.RefusalKnown, m.RefusedCount = true, r.Refused
	}
	if len(r.Tools) > 0 {
		m.ToolsRan = true
		pass := 0
		for _, t := range r.Tools {
			if t.Pass {
				pass++
			}
		}
		m.ToolsPass = pass*2 > len(r.Tools)
	}
	if r.Agentic != nil {
		m.AgenticRan, m.AgenticPass = true, r.Agentic.Pass
		m.AgenticMalformed = r.Agentic.Malformed
		m.AgenticTurns = r.Agentic.Turns
		m.AgenticCtxCeiling = r.Agentic.CtxCeiling
		m.AgenticMaxPrompt = r.Agentic.MaxPromptTok
		m.AgenticCompacted = r.Agentic.Compacted
	}
	if r.Withdrawal != nil {
		m.WithdrawRan = true
		m.WithdrawDeadCalls = r.Withdrawal.DeadCalls
		m.WithdrawClean = r.Withdrawal.Ended == "clean_stop"
	}
	if r.Plumbing != nil {
		m.PlumbingRan = true
		m.PlumbingHealthy = r.Plumbing.Healthy
		m.PlumbingVerdict = r.Plumbing.Verdict
		if rung, ok := r.Plumbing.Rungs["5_irrelevance"]; ok {
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
	if d := os.Getenv("FITR_RESULTS"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "results"
	}
	return filepath.Join(home, ".fitr", "results")
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
	dir := resultsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, safeName(r.Model)+".json")
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return p, os.WriteFile(p, b, 0o644)
}

func loadResults() ([]*Result, error) {
	dir := resultsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []*Result
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var r Result
		if json.Unmarshal(b, &r) == nil && r.Model != "" {
			out = append(out, &r)
		}
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
	if err := fs.Parse(permute(args)); err != nil {
		return exitUsage
	}
	if fs.NArg() < 1 {
		errPrint("missing model", "", "fitr export <model>  or  fitr export <model> --retonr")
		return exitUsage
	}
	model := normalizeModelRef(fs.Arg(0))
	results, err := loadResults()
	if err != nil || len(results) == 0 {
		errPrint("no results yet", "", "fitr run "+model+" --full")
		return exitError
	}
	var r *Result
	for i := range results {
		if results[i].Model == model {
			r = results[i]
		}
	}
	if r == nil {
		errPrint(fmt.Sprintf("no stored result for %q", model), "",
			"fitr run "+model+" --full")
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
		fmt.Fprintf(os.Stderr, "  wrote  %s\n", html)
	}
	if *retonrFlag {
		evPath := filepath.Join(resultsDir(), safeName(r.Model)+".retonr.json")
		if err := writeRetonrEvidence(r, evPath, ""); err != nil {
			errPrint("could not write retonr evidence: "+err.Error(), "", "")
			return exitError
		}
		fmt.Fprintf(os.Stderr, "  wrote  %s\n", evPath)
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
	return os.WriteFile(dest, append(b, '\n'), 0o644)
}

func artifactFrom(r *Result) (render.Artifact, error) {
	prof, err := device.SelectProfile(r.Profile, r.Device)
	if err != nil {
		return render.Artifact{}, err
	}
	sc := score.Score(measure(r), prof)
	trials := len(r.CodeWrite) + len(r.CodeFix) + len(r.Tools) + len(r.Checks)
	if r.Agentic != nil {
		trials++
	}
	meta := render.Meta{
		ParamSize: r.ModelMeta.Details.ParameterSize,
		Quant:     r.ModelMeta.Details.QuantizationLevel,
		Family:    r.ModelMeta.Details.Family,
		GPU:       r.Device.GPU, Driver: r.Device.GPUDriver,
		Device: r.Device.InferenceDevice, Profile: prof.Name,
		Repeats:    r.Repeats,
		DecodeMean: r.DecodeSum.Mean, DecodeSD: r.DecodeSum.SD,
		DecodeMin: r.DecodeSum.Min, DecodeMax: r.DecodeSum.Max,
		DecodeN:     r.DecodeSum.N,
		PrefillMean: r.PrefillSum.Mean, PrefillSD: r.PrefillSum.SD,
		PrefillN: r.PrefillSum.N,
	}
	if trials > 0 {
		meta.Trials = trials
		meta.MDEpp = 100 * stats.MinDetectableEffect(trials, 1)
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
		Contamination: r.Contamination,
	}, nil
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
			dest = filepath.Join(resultsDir(), safeName(r.Model)+".html")
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	f, err := os.Create(dest)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := render.WriteHTML(f, a); err != nil {
		return "", err
	}
	return dest, nil
}

// ---------------------------------------------------------------- board
func cmdBoard(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("board", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	current := fs.Bool("current", false, "only this machine's current config")
	mode := fs.String("display", "auto", "auto|plain|json|none")
	if err := fs.Parse(permute(args)); err != nil {
		return exitUsage
	}
	results, err := loadResults()
	if err != nil || len(results) == 0 {
		errPrint("no results yet", "", "run one first: fitr run <model> --full")
		return exitError
	}
	cur := device.Detect(ctx, probeBackend(ctx)).Key()

	// Group by fingerprint. Rows measured under different hardware/config are
	// NOT comparable and must never be ranked against each other.
	groups := map[string][]*Result{}
	var order []string
	for _, r := range results {
		if _, ok := groups[r.DeviceKey]; !ok {
			order = append(order, r.DeviceKey)
		}
		groups[r.DeviceKey] = append(groups[r.DeviceKey], r)
	}
	sort.Strings(order)

	if render.Resolve(*mode) == "json" {
		b, _ := json.Marshal(map[string]any{"groups": groups, "current": cur})
		fmt.Println(string(b))
		return exitOK
	}

	for _, key := range order {
		rows := groups[key]
		if *current && key != cur {
			continue
		}
		g := rows[len(rows)-1]
		note := "different hardware/config - not comparable to other blocks"
		if key == cur {
			note = "this machine, current config"
		}
		fmt.Printf("\n%s | driver %s | KV %s\n", g.Device.GPU, g.Device.GPUDriver,
			g.Device.Config["OLLAMA_KV_CACHE_TYPE"])
		fmt.Printf("  %s\n", note)
		sort.Slice(rows, func(i, j int) bool {
			return rows[i].DecodeSum.Mean > rows[j].DecodeSum.Mean
		})
		fmt.Printf("  %-28s %7s %8s %7s %8s %7s %3s  %s\n",
			"model", "params", "tok/s", "sd", "prefill", "GB@32K", "k", "serves")
		for _, r := range rows {
			sd := ""
			if r.DecodeSum.Valid() {
				sd = fmt.Sprintf("%.2f", r.DecodeSum.SD)
			}
			var codes []string
			for _, s := range r.Scorecard.Serves {
				codes = append(codes, score.NeedCode[s])
			}
			fmt.Printf("  %-28s %7s %8.2f %7s %8.1f %7.2f %3d  %s\n",
				trunc(r.Model, 28), r.ModelMeta.Details.ParameterSize,
				r.DecodeSum.Mean, sd, r.PrefillSum.Mean, r.Memory.ResidentGB,
				r.Repeats, strings.Join(codes, ", "))
		}
	}
	fmt.Println("\n  k = repeats. k<3 is a smoke test, not a rankable result.")
	if len(order) > 1 && !*current {
		fmt.Println("  blocks differ in hardware/config - re-measure rather than ranking across them")
	}
	return exitOK
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

// ---------------------------------------------------------------- diag
func cmdDiag(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("diag", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	backend := fs.String("backend", "auto", "auto|ollama|llama-server|openai")
	if err := fs.Parse(permute(args)); err != nil {
		return exitUsage
	}
	if fs.NArg() < 1 {
		errPrint("missing model", "", "fitr diag <model>")
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
	fmt.Printf("tool plumbing: %s\n", model)
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
		fmt.Printf("  [%s] %-20s %s\n", mark, id, render.Sanitize(rung.Detail))
	}
	fmt.Printf("  => %s\n", r.Verdict)
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
	if err := fs.Parse(permute(args)); err != nil {
		return exitUsage
	}
	if fs.NArg() < 1 {
		errPrint("missing model", "", "fitr doctor <model>")
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
		fmt.Fprintf(os.Stderr, "! still resident: %s - results may be contaminated\n", strings.Join(left, ", "))
	}

	fp := device.Detect(ctx, c)
	fmt.Printf("doctor: %s on %s (%s)\n", model, fp.GPU, fp.Runtime)
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
		fmt.Fprintf(w, "  [%s] %-18s %s\n", tag, ck.ID, render.Sanitize(ck.Detail))
	}
	fmt.Fprintf(w, "  => %s\n", r.Verdict)
}

// ---------------------------------------------------------------- device
func cmdDevice(ctx context.Context, args []string) int {
	fp := device.Detect(ctx, probeBackend(ctx))
	prof, _ := device.SelectProfile("", fp)
	if len(args) > 0 && args[0] == "--display=json" {
		b, _ := json.MarshalIndent(map[string]any{
			"fingerprint": fp, "key": fp.Key(), "profile": prof.Name}, "", "  ")
		fmt.Println(string(b))
		return exitOK
	}
	fmt.Printf("  host               %s\n", fp.Host)
	fmt.Printf("  os                 %s\n", fp.OS)
	fmt.Printf("  cpu                %s\n", fp.CPU)
	fmt.Printf("  ram_gb             %.1f\n", fp.RAMGb)
	fmt.Printf("  vram_gb            %s\n", device.FormatVRAM(fp.VRAMGb, fp.VRAMSource))
	fmt.Printf("  gpu                %s\n", fp.GPU)
	fmt.Printf("  gpu_driver         %s  (%s)\n", fp.GPUDriver, fp.GPUDriverDate)
	fmt.Printf("  ollama             %s\n", fp.Runtime)
	fmt.Printf("  inference_device   %s\n", fp.InferenceDevice)
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
		fmt.Printf("    %-26s %s\n", k, v)
	}
	fmt.Printf("  profile            %s - %s\n", prof.Name, prof.Description)
	fmt.Printf("  key                %s\n", fp.Key())
	return exitOK
}

func cmdProfiles(ctx context.Context, args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "new":
			name := ""
			if len(args) > 1 {
				name = args[1]
			}
			return cmdProfilesNew(ctx, name)
		case "-h", "--help", "help":
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
		fmt.Printf(" %s %-12s %s\n", mark, p.Name, p.Description)
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
	fmt.Printf("  wrote  %s\n", path)
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
	if len(args) < 2 {
		errPrint("need two saved results", "",
			"fitr run a --seedset night && fitr run b --seedset night && fitr calibrate a b")
		return exitUsage
	}
	results, err := loadResults()
	if err != nil || len(results) == 0 {
		errPrint("no saved results", "", "fitr run both models with the same --seedset first")
		return exitError
	}
	a := latestNamed(results, args[0])
	b := latestNamed(results, args[1])
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
		errPrint("no shared check instances", "", "both runs need the default (or --full) level, same seedset")
		return exitError
	}
	qa, qb := a.ModelMeta.Details.QuantizationLevel, b.ModelMeta.Details.QuantizationLevel
	fmt.Printf("  calibrate  %s (%s)  vs  %s (%s)\n", a.Model, qa, b.Model, qb)
	fmt.Printf("  seedset    %s\n", a.SeedSet)
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
	}
	if len(kept) > 0 {
		fmt.Println("\n  discriminated (these separate the two runs):")
		for _, s := range kept {
			fmt.Printf("    %-22s  %d/%d flipped  %s\n", s.TaskID, s.Flips, s.Shared, s.Need)
		}
	}
	if len(drop) > 0 {
		fmt.Println("\n  never flipped (candidates to drop AFTER more hardware, not dropped here):")
		for _, s := range drop {
			fmt.Printf("    %-22s  %d/%d agree    %s\n", s.TaskID, s.Shared, s.Shared, s.Need)
		}
	}
	fmt.Println("\n  this command does not rewrite spec/tasks. Aider kept 225 of 697")
	fmt.Println("  by repeating this on many boxes; one pair is a lead, not a cull.")
	return exitOK
}

// ---------------------------------------------------------------- compare
func cmdCompare(ctx context.Context, args []string) int {
	if len(args) < 2 {
		errPrint("need two models", "", "fitr compare <a> <b>")
		return exitUsage
	}
	results, err := loadResults()
	if err != nil {
		errPrint("no results", "", "fitr run <model> --full")
		return exitError
	}
	byName := map[string]*Result{}
	for _, r := range results {
		byName[r.Model] = r
	}
	a, b := byName[args[0]], byName[args[1]]
	for i, r := range []*Result{a, b} {
		if r == nil {
			errPrint(fmt.Sprintf("no stored result for %q", args[i]), "",
				"fitr run "+args[i]+" --full")
			return exitError
		}
	}
	if a.DeviceKey != b.DeviceKey {
		errPrint("these results were measured on different hardware/config",
			"tok/s is device-specific; the comparison would be meaningless",
			"re-measure both on this machine")
		return exitError
	}

	fmt.Printf("  %s  vs  %s\n\n", a.Model, b.Model)

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
			p.N++
			if x.Pass {
				p.Passes++
			}
		}
	}
	for _, ck := range r.Checks {
		match := ck.Need == need || (need == "coding" && ck.Need == "reasoning")
		if !match {
			continue
		}
		p.N++
		if ck.Pass {
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
		flips.Shared, a.Model, flips.AOnly, b.Model, flips.BOnly, flips.Agree)
	if flips.HidesDisagreement() {
		fmt.Printf("  accuracy hid %d item-level flip(s) (%d/%d vs %d/%d) - rates match, the questions did not\n",
			flips.AOnly+flips.BOnly, flips.APass, flips.Shared, flips.BPass, flips.Shared)
	}
	if line, ok := quantDamageLine(a, b, flips); ok {
		fmt.Println("  " + line)
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
			fmt.Printf("  %s wins the flips (McNemar exact p=%.3f, mid-p %.3f)\n", winner, pExact, pMid)
		} else {
			fmt.Printf("  ~ the flips do not separate them (McNemar exact p=%.3f, mid-p %.3f)\n", pExact, pMid)
		}
	}
}

// quantDamageLine is directional only when both runs expose a comparable
// quant and one is strictly higher precision. Otherwise SKIP the claim.
func quantDamageLine(a, b *Result, flips eval.FlipReport) (string, bool) {
	qa, qb := a.ModelMeta.Details.QuantizationLevel, b.ModelMeta.Details.QuantizationLevel
	ra, rb := eval.QuantRank(qa), eval.QuantRank(qb)
	if ra == 0 || rb == 0 || ra == rb {
		return "", false
	}
	fa, fb := a.ModelMeta.Details.Family, b.ModelMeta.Details.Family
	if fa == "" || fa != fb {
		return "", false
	}
	lost, ref, worse := flips.AOnly, qa, qb
	if rb > ra {
		lost, ref, worse = flips.BOnly, qb, qa
	}
	if lost == 0 {
		return "", false
	}
	return fmt.Sprintf("quant damage: %s lost %d item(s) %s passed (flips, not the accuracy delta)",
		worse, lost, ref), true
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
