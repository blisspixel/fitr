package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/lock"
)

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
