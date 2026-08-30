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
	currentFP := device.Fingerprint{}
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
		currentFP = fp
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
		currentFP = fp
		if free, ok := device.AvailableVRAM(ctx); ok {
			in.FreeGB = free
		}
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
				defer lk.Release() //nolint:errcheck // best effort on a advisory print
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

	in.Timings = adviseTimings(in.Model, verifiedModelArtifactDigest(ctx, c, model), currentFP)
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
