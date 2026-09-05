package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/blisspixel/fitr/internal/advise"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/lock"
	"github.com/blisspixel/fitr/internal/modelref"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/render"
)

// cmdAdvise answers: does this model fit on this box, and if not, which
// flag to try? SKIP when fit cannot be measured (no VRAM reading, no
// weights, no architecture) - never a fabricated GB number.
func cmdAdvise(ctx context.Context, args []string) int {
	command, code, ok := parseAdviseCommand(args)
	if !ok {
		return code
	}
	if command.raw == "" {
		return cmdStatus(ctx, []string{"--display", command.mode, "--backend", command.backend})
	}

	in, currentFP := initialAdviseInput(ctx, command)
	c, ggufPath, currentFP, code := resolveAdviseSource(ctx, command, &in, currentFP)
	if code != exitOK {
		return code
	}
	if code := measureAdviseFit(ctx, command, ggufPath, &in); code != exitOK {
		return code
	}
	release, code := measureAdviseLoad(ctx, command, c, &in)
	if code != exitOK {
		return code
	}
	if release != nil {
		defer release()
	}

	in.Timings = adviseTimings(in.Model, verifiedModelArtifactDigest(ctx, c, command.model), currentFP)
	return writeAdviseReport(command.mode, in)
}

type adviseCommand struct {
	raw     string
	model   string
	backend string
	mode    string
	vram    float64
	ctxSize int
	pull    bool
	load    bool
	fit     bool
}

func parseAdviseCommand(args []string) (adviseCommand, int, bool) {
	fs := flag.NewFlagSet("advise", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vram := fs.Float64("vram-gb", -1, "GPU or unified-memory planning budget in GB")
	ctxSize := fs.Int("ctx", 0, "requested context length (default: model's max)")
	backend := fs.String("backend", "auto", "auto|ollama|llama-server|openai")
	mode := fs.String("display", "auto", "auto|rich|plain|json|none")
	pullFlag := fs.Bool("pull", false, "pull the model first if it is not installed")
	loadFlag := fs.Bool("load", false, "load the model on Ollama and observe its runtime allocation")
	fitFlag := fs.Bool("fit", false, "report llama-fit-params device projection for a GGUF (non-verdict)")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return adviseCommand{}, code, false
	}
	if !render.ValidMode(*mode) {
		errPrint("invalid display mode", *mode, "use auto, rich, plain, json, or none")
		return adviseCommand{}, exitUsage, false
	}
	if _, ok := canonicalBackendKind(*backend); !ok {
		errPrint("invalid backend", *backend, "use auto, ollama, llama-server, or openai")
		return adviseCommand{}, exitUsage, false
	}
	if *ctxSize < 0 {
		errPrint("invalid context size", "--ctx cannot be negative", "omit --ctx for the model default, or pass a positive token count")
		return adviseCommand{}, exitUsage, false
	}
	if *vram < -1 {
		errPrint("invalid VRAM size", "--vram-gb cannot be negative", "omit --vram-gb for automatic detection, or pass a non-negative size")
		return adviseCommand{}, exitUsage, false
	}
	if fs.NArg() > 1 {
		errPrint("too many arguments", "advise accepts at most one model", "fitr advise [model] [flags]")
		return adviseCommand{}, exitUsage, false
	}
	if fs.NArg() < 1 {
		if *loadFlag || *fitFlag || *pullFlag || *ctxSize != 0 || *vram >= 0 {
			errPrint("missing model", "fit/load/ctx flags need a model", "fitr advise <model>  or  fitr advise ./model.gguf")
			return adviseCommand{}, exitUsage, false
		}
		return adviseCommand{backend: *backend, mode: *mode}, exitOK, true
	}
	if *loadFlag && *ctxSize <= 0 {
		errPrint("--load needs an explicit context", "the runtime receipt must be bound to the requested point",
			"fitr advise <model> --load --ctx 8192")
		return adviseCommand{}, exitUsage, false
	}
	raw := fs.Arg(0)
	return adviseCommand{
		raw: raw, model: normalizeModelRef(raw), backend: *backend, mode: *mode,
		vram: *vram, ctxSize: *ctxSize, pull: *pullFlag, load: *loadFlag, fit: *fitFlag,
	}, exitOK, true
}

func initialAdviseInput(ctx context.Context, command adviseCommand) (advise.Input, device.Fingerprint) {
	in := advise.Input{Model: command.model, Ctx: command.ctxSize}
	currentFP := device.Detect(ctx, nil)
	applyAdviseDeviceEvidence(&in, currentFP)
	if command.backend != "" && command.backend != "auto" {
		in.Backend = command.backend
	}
	if command.vram >= 0 {
		in.HaveGB = command.vram
		in.HaveSrc = "--vram-gb"
	} else {
		in.HaveGB = currentFP.VRAMGb
		in.HaveSrc = currentFP.VRAMSource
	}
	setAdviseKV(&in, os.Getenv("OLLAMA_KV_CACHE_TYPE"))
	return in, currentFP
}

func applyAdviseDeviceEvidence(in *advise.Input, fp device.Fingerprint) {
	if fp.VRAMSource == device.NVIDIAUnifiedMemorySource ||
		fp.VRAMSource == device.NVIDIAUnifiedProbeSource ||
		device.IsNVIDIAUnifiedMemoryGPU(fp.GPU) {
		in.NVIDIAUnifiedMemory = true
	}
}

func setAdviseKV(in *advise.Input, kv string) {
	if kv == "" {
		return
	}
	if n, ok := advise.KVElemBytes(kv); ok {
		in.KVBytes = n
		in.KVSrc = "OLLAMA_KV_CACHE_TYPE=" + kv
	}
}

func resolveAdviseSource(ctx context.Context, command adviseCommand, in *advise.Input,
	currentFP device.Fingerprint) (llm.Backend, string, device.Fingerprint, int) {
	if isLocalGGUF(command.raw) {
		path, code := readLocalAdviseSource(command.raw, in)
		return nil, path, currentFP, code
	}
	return readBackendAdviseSource(ctx, command, in)
}

func readLocalAdviseSource(path string, in *advise.Input) (string, int) {
	kvs, size, err := advise.OpenGGUF(path)
	if err != nil {
		errPrint("could not read GGUF: "+err.Error(), "", "")
		return "", exitError
	}
	in.Model = path
	in.Arch = advise.ArchFromKVs(kvs)
	in.WeightsB = size
	in.Source = "GGUF metadata"
	if q := quantFromFilename(path); isQuantTag(q) {
		in.Quant = strings.ToUpper(q)
	}
	return path, exitOK
}

func readBackendAdviseSource(ctx context.Context, command adviseCommand,
	in *advise.Input) (llm.Backend, string, device.Fingerprint, int) {
	c, code := newBackend(ctx, command.model, command.backend, command.pull)
	if code != exitOK {
		return nil, "", device.Fingerprint{}, code
	}
	in.Backend = c.Name()
	fp := device.Detect(ctx, c)
	applyAdviseDeviceEvidence(in, fp)
	if free, ok := device.AvailableVRAM(ctx); ok {
		in.FreeGB = free
	}
	if command.vram < 0 {
		in.HaveGB = fp.VRAMGb
		in.HaveSrc = fp.VRAMSource
	}
	setAdviseKV(in, fp.Config["OLLAMA_KV_CACHE_TYPE"])
	ggufPath := readShownAdviseSource(ctx, c, command.model, in)
	if in.WeightsB == 0 {
		in.WeightsB = weightsFromTags(ctx, c, command.model)
	}
	if in.Source == "" {
		in.Source = c.Name() + " (no architecture metadata)"
	}
	readResidentAdviseSource(ctx, c, command.model, in, c.Name()+" runtime status")
	return c, ggufPath, fp, exitOK
}

func readShownAdviseSource(ctx context.Context, c llm.Backend, model string, in *advise.Input) string {
	info, err := c.Show(ctx, model)
	if err != nil {
		return ""
	}
	in.Quant = info.Details.QuantizationLevel
	if info.Digest != "" {
		in.ArtifactDigest = info.Digest
	}
	if info.Size > 0 {
		in.WeightsB = info.Size
	}
	if len(info.Info) > 0 {
		in.Arch = advise.ArchFromKVs(info.Info)
		in.Source = "Ollama /api/show"
	}
	if !in.Arch.KVReady() && info.Path != "" {
		readAdviseGGUFFallback(info.Path, in)
	}
	return info.Path
}

func readAdviseGGUFFallback(path string, in *advise.Input) {
	kvs, size, err := advise.OpenGGUF(path)
	if err != nil {
		return
	}
	in.Arch = advise.ArchFromKVs(kvs)
	if in.WeightsB == 0 {
		in.WeightsB = size
	}
	in.Source = "GGUF at " + path
}

func readResidentAdviseSource(ctx context.Context, c llm.Backend, model string,
	in *advise.Input, source string) {
	in.ResidentB = 0
	in.ResidentSrc = ""
	in.ResidentCtx = 0
	running, err := c.PS(ctx)
	if err != nil {
		return
	}
	var matched *ollama.RunningModel
	for _, runningModel := range running {
		if !modelref.SameServed(model, runningModel.Name) || runningModel.Size <= 0 {
			continue
		}
		if matched == nil {
			candidate := runningModel
			matched = &candidate
			continue
		}
		if matched.Size != runningModel.Size || matched.ContextLength != runningModel.ContextLength {
			return
		}
	}
	if matched != nil {
		in.ResidentB = matched.Size
		in.ResidentSrc = source
		in.ResidentCtx = matched.ContextLength
	}
}

func measureAdviseFit(ctx context.Context, command adviseCommand, ggufPath string, in *advise.Input) int {
	if !command.fit {
		return exitOK
	}
	if ggufPath == "" {
		errPrint("--fit needs a GGUF path", "", "pass ./model.gguf, or an Ollama model whose blob path is known")
		return exitUsage
	}
	fmt.Fprintf(os.Stderr, "  llama-fit-params %s\n", terminalText(ggufPath))
	used, cannot, err := advise.RunFitParams(ctx, ggufPath, command.ctxSize)
	if err != nil {
		fmt.Fprintf(os.Stderr, " note: llama-fit-params not used: %s\n", terminalText(err.Error()))
		return exitOK
	}
	in.FitB, in.FitCannot, in.FitSrc = used, cannot, "llama-fit-params"
	return exitOK
}

func measureAdviseLoad(ctx context.Context, command adviseCommand, c llm.Backend,
	in *advise.Input) (func(), int) {
	if !command.load {
		return nil, exitOK
	}
	if c == nil || c.Name() != "ollama" {
		errPrint("--load needs a running Ollama", "",
			"pass an Ollama model tag; llama-server is already loaded, and a bare .gguf needs --fit")
		return nil, exitUsage
	}
	if in.ResidentB != 0 && in.ResidentCtx == command.ctxSize {
		return nil, exitOK
	}
	if in.WeightsB > 0 && in.HaveGB > 0 && float64(in.WeightsB) > in.HaveGB*advise.GiB {
		fmt.Fprintln(os.Stderr, " note: not loading: weights alone exceed the budget")
		return nil, exitOK
	}
	return loadAdviseResident(ctx, command, c, in)
}

func loadAdviseResident(ctx context.Context, command adviseCommand, c llm.Backend,
	in *advise.Input) (func(), int) {
	lk, err := lock.Acquire("eval", "advise --load "+command.model)
	if err != nil {
		errPrint(err.Error(), "", "")
		return nil, exitError
	}
	ctxLen := command.ctxSize
	if ctxLen <= 0 {
		ctxLen = 2048
	}
	fmt.Fprintf(os.Stderr, "  loading %s at num_ctx=%d to measure resident size\n", terminalText(command.model), ctxLen)
	_, _, err = c.Generate(ctx, command.model, ".", ollama.Deterministic(1, ctxLen))
	if err != nil {
		in.LoadErr = err.Error()
	} else {
		readResidentAdviseSource(ctx, c, command.model, in, "ollama /api/ps after --load")
	}
	if left, stopErr := c.StopAll(ctx); stopErr == nil && len(left) > 0 {
		fmt.Fprintf(os.Stderr, " note: still resident: %s\n", terminalText(strings.Join(left, ", ")))
	}
	return func() { _ = lk.Release() }, exitOK
}

func writeAdviseReport(mode string, in advise.Input) int {
	rep := advise.Evaluate(in)
	switch render.Resolve(mode) {
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

// ---------------------------------------------------------------- apply
// cmdApply prints the command to persist a measured context. It never
// restarts or mutates the serving process; that is the whole point.
func cmdApply(ctx context.Context, args []string) int {
	command, code, ok := parseApplyCommand(args)
	if !ok {
		return code
	}
	n, code, ok := resolveApplyContext(command.model, command.numCtx)
	if !ok {
		return code
	}
	kind, code, ok := resolveApplyBackend(ctx, command.backend)
	if !ok {
		return code
	}
	if n <= 0 {
		n = 4096
	}
	plan := advise.PlanApply(kind, command.model, n)
	if command.model != "" {
		if serving, observed := observeServingCtx(ctx, command.model, kind); observed {
			plan.ServingCtx = serving
			plan.ServingKnown = true
		}
	}
	return writeApplyPlan(command.mode, command.model, plan)
}

type applyCommand struct {
	model   string
	backend string
	mode    string
	numCtx  int
}

func parseApplyCommand(args []string) (applyCommand, int, bool) {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	ctxSize := fs.Int("ctx", 0, "context to persist (default: latest result, else 4096 as the worked example)")
	backend := fs.String("backend", "auto", "auto|ollama|llama-server|openai")
	mode := fs.String("display", "auto", "auto|rich|plain|json|none")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return applyCommand{}, code, false
	}
	if !render.ValidMode(*mode) {
		errPrint("invalid display mode", *mode, "use auto, rich, plain, json, or none")
		return applyCommand{}, exitUsage, false
	}
	if fs.NArg() > 1 {
		errPrint("too many arguments", "apply accepts at most one model", "fitr apply [model] [--ctx N]")
		return applyCommand{}, exitUsage, false
	}
	if *ctxSize < 0 {
		errPrint("invalid context size", "--ctx cannot be negative", "omit --ctx for the latest measured context, or pass a positive token count")
		return applyCommand{}, exitUsage, false
	}
	model := ""
	if fs.NArg() > 0 {
		model = normalizeModelRef(fs.Arg(0))
	}
	kind, ok := canonicalBackendKind(*backend)
	if !ok {
		errPrint("invalid backend", *backend, "use auto, ollama, llama-server, or openai")
		return applyCommand{}, exitUsage, false
	}
	return applyCommand{model: model, backend: kind, mode: *mode, numCtx: *ctxSize}, exitOK, true
}

func resolveApplyContext(model string, requested int) (int, int, bool) {
	n := requested
	if model != "" && n <= 0 {
		if all, err := loadResults(); err == nil {
			if r := latestNamed(all, model); r != nil {
				n = resultNumCtx(r)
			}
		}
		if n <= 0 {
			errPrint("no saved result for this model", "",
				"fitr run "+model+"   or   fitr apply "+model+" --ctx 4096")
			return 0, exitError, false
		}
	}
	return n, exitOK, true
}

func resolveApplyBackend(ctx context.Context, kind string) (string, int, bool) {
	if kind == "" || kind == "auto" {
		found, err := llm.Discover(ctx)
		if err != nil {
			errPrint("invalid runtime discovery configuration", err.Error(),
				"fix FITR_DISCOVER_URLS or the configured backend URL, then re-run")
			return "", exitUsage, false
		}
		if len(found) > 0 {
			kind = found[0].Kind
		} else {
			kind = ""
		}
	}
	return kind, exitOK, true
}

func writeApplyPlan(mode, model string, plan advise.ApplyPlan) int {
	switch render.Resolve(mode) {
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
		d = append([][3]string{{"num_ctx", strconv.Itoa(ca), strconv.Itoa(cb)}}, d...)
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

func adviseTimings(model, artifactDigest string, current device.Fingerprint) []advise.SavedTiming {
	if model == "" || artifactDigest == "" || current.Key() == "||||||" {
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
		evidence := inventoryEvidenceFromRecord(rec)
		if !advise.EvidenceReusable(
			advise.InstalledModel{Name: model, ArtifactDigest: artifactDigest},
			evidence,
			advise.InventoryQuery{Current: current, CurrentKey: current.Key()},
		) {
			continue
		}
		ctxN := evidence.NumCtx
		if ctxN <= 0 {
			continue
		}
		out = append(out, advise.SavedTiming{
			Ctx: ctxN, DecodeTPS: rec.DecodeSum.Mean, PrefillTPS: rec.PrefillSum.Mean,
		})
	}
	return out
}
