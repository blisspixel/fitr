package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/blisspixel/fitr/internal/atomicfile"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/experiment"
	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/render"
)

type servingCommand struct {
	mode, backend                 string
	concurrency, requests, warmup int
	ctx                           int
	pull                          bool
	positional                    []string
}

func cmdExperimentServing(ctx context.Context, args []string) int {
	command, code, ok := parseServingCommand(args)
	if !ok {
		return code
	}
	if len(command.positional) != 1 {
		errPrint("serving experiment needs exactly one model or bundle", "",
			"fitr experiment serving <model> --concurrency 4 -n 20")
		return exitUsage
	}
	input := command.positional[0]
	if servingBundlePath(input) {
		return reopenServingBundle(input, command)
	}
	if command.concurrency < experiment.MinServingConcurrency || command.concurrency > experiment.MaxServingConcurrency {
		errPrint("invalid concurrency", fmt.Sprintf("must be between %d and %d",
			experiment.MinServingConcurrency, experiment.MaxServingConcurrency), "use --concurrency 4")
		return exitUsage
	}
	if command.requests < experiment.MinServingRequests || command.requests > experiment.MaxServingRequests {
		errPrint("invalid request count", fmt.Sprintf("must be between %d and %d",
			experiment.MinServingRequests, experiment.MaxServingRequests), "use -n 20")
		return exitUsage
	}
	if command.warmup < experiment.MinServingWarmup || command.warmup > experiment.MaxServingWarmup {
		errPrint("invalid warmup count", fmt.Sprintf("must be between %d and %d",
			experiment.MinServingWarmup, experiment.MaxServingWarmup), "use --warmup 2")
		return exitUsage
	}
	return runServingExperiment(ctx, input, command)
}

func parseServingCommand(args []string) (servingCommand, int, bool) {
	cmd := servingCommand{
		mode:        "auto",
		backend:     "auto",
		concurrency: 4,
		requests:    20,
		warmup:      2,
		ctx:         4096,
	}
	fs := flag.NewFlagSet("experiment serving", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&cmd.mode, "display", cmd.mode, "auto|rich|plain|json|none")
	fs.StringVar(&cmd.backend, "backend", cmd.backend, "auto|ollama|llama-server|openai")
	fs.IntVar(&cmd.concurrency, "concurrency", cmd.concurrency, "declared concurrent requests / workers")
	fs.IntVar(&cmd.requests, "n", cmd.requests, "evaluated requests count")
	fs.IntVar(&cmd.warmup, "warmup", cmd.warmup, "explicit warm-up requests excluded from evaluation")
	fs.IntVar(&cmd.ctx, "ctx", cmd.ctx, "requested context tokens")
	fs.BoolVar(&cmd.pull, "pull", false, "pull a missing Ollama model before the experiment")

	code, ok := parseCommandFlags(fs, args)
	if !ok {
		return servingCommand{}, code, false
	}
	if !render.ValidMode(cmd.mode) {
		errPrint("invalid display mode", cmd.mode, "use auto, rich, plain, json, or none")
		return servingCommand{}, exitUsage, false
	}
	cmd.positional = append([]string(nil), fs.Args()...)
	return cmd, exitOK, true
}

func servingBundlePath(path string) bool {
	if strings.HasSuffix(strings.ToLower(path), ".json") {
		return true
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	prefix := make([]byte, 512)
	count, _ := file.Read(prefix)
	return strings.HasPrefix(strings.TrimSpace(string(prefix[:count])), "{")
}

func reopenServingBundle(path string, command servingCommand) int {
	if command.concurrency != 4 || command.requests != 20 || command.warmup != 2 ||
		command.ctx != 4096 || command.backend != "auto" || command.pull {
		errPrint("live serving flags cannot be applied to a saved bundle", path,
			"remove live flags or run a new serving experiment")
		return exitUsage
	}
	bundle, err := experiment.LoadServingBundle(path)
	if err != nil {
		errPrint("could not load serving bundle: "+err.Error(), path,
			"pass a bundle written by fitr experiment serving")
		return exitError
	}
	return renderServingExperiment(bundle.Report, command.mode)
}

func runServingExperiment(ctx context.Context, model string, command servingCommand) int {
	model = normalizeModelRef(model)
	backend, code := newBackendWithDisplay(ctx, model, command.backend, command.pull, nil)
	if code != exitOK {
		return code
	}
	resolved, err := resolveRunModel(ctx, backend, model)
	if err != nil {
		errPrint("could not bind serving model identity: "+err.Error(), "",
			"use a runtime that reports a verifiable artifact digest")
		return exitError
	}
	fingerprint := device.Detect(ctx, backend)
	plan, err := experiment.NewServingPlan(resolved.Identity, fingerprint.Key(), command.concurrency,
		command.requests, command.warmup, command.ctx)
	if err != nil {
		errPrint("could not create serving plan: "+err.Error(), "", "fix declared parameters and retry")
		return exitUsage
	}
	concurrencyObs := experiment.ObserveConcurrency(ctx, backend, command.concurrency)
	fmt.Fprintf(os.Stderr, "  serving  %s, concurrency %d (%s), %d request(s) (+%d warmup)\n",
		terminalText(model), plan.Concurrency, concurrencyObs.State, plan.Requests, plan.WarmupRequests)
	requests, exposureWallMillis, err := executeServingLoad(ctx, backend, plan)
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "\ninterrupted")
			return exitInterrupt
		}
		errPrint("serving experiment failed: "+err.Error(), "", "no incomplete bundle was saved")
		return exitError
	}
	bundle, err := experiment.NewServingBundle(plan, concurrencyObs, requests, exposureWallMillis)
	if err != nil {
		errPrint("could not analyze serving experiment: "+err.Error(), "", "")
		return exitError
	}
	path, err := saveServingBundle(bundle)
	if err != nil {
		errPrint("could not save serving bundle: "+err.Error(), "", "the completed receipt was not persisted")
		return exitError
	}
	fmt.Fprintf(os.Stderr, "  bundle   %s\n", terminalText(path))
	return renderServingExperiment(bundle.Report, command.mode)
}

type servingJob struct {
	index    int
	isWarmup bool
	enqueued time.Time
}

type servingReqResult struct {
	obs experiment.ServingRequestObservation
	err error
}

type servingTimingTracker struct {
	mu             sync.Mutex
	evaluatedStart time.Time
	evaluatedEnd   time.Time
}

func (t *servingTimingTracker) recordDispatch(isWarmup bool, dispatch time.Time) {
	if isWarmup {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.evaluatedStart.IsZero() || dispatch.Before(t.evaluatedStart) {
		t.evaluatedStart = dispatch
	}
}

func (t *servingTimingTracker) recordFinish(isWarmup bool, finish time.Time) {
	if isWarmup {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if finish.After(t.evaluatedEnd) {
		t.evaluatedEnd = finish
	}
}

func (t *servingTimingTracker) exposureWallMillis() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	exposure := t.evaluatedEnd.Sub(t.evaluatedStart).Milliseconds()
	if exposure <= 0 {
		return 1
	}
	return exposure
}

func runServingWorker(ctx context.Context, backend llm.Backend, model, prompt string, sampling ollama.Sampling,
	jobs <-chan servingJob, resChan chan<- servingReqResult, timing *servingTimingTracker) {
	for j := range jobs {
		if ctx.Err() != nil {
			resChan <- servingReqResult{err: ctx.Err()}
			return
		}
		dispatch := time.Now()
		queueMillis := dispatch.Sub(j.enqueued).Milliseconds()
		timing.recordDispatch(j.isWarmup, dispatch)

		out, metrics, err := backend.Generate(ctx, model, prompt, sampling)
		finish := time.Now()
		serverMillis := finish.Sub(dispatch).Milliseconds()
		timing.recordFinish(j.isWarmup, finish)

		obs := experiment.ServingRequestObservation{
			Index:                j.index,
			IsWarmup:             j.isWarmup,
			QueueDurationMillis:  queueMillis,
			ServerDurationMillis: serverMillis,
			PromptTokens:         metrics.PromptTokens,
			CompletionTokens:     metrics.EvalCount,
			AcceptedOutcome:      err == nil && len(strings.TrimSpace(out)) > 0,
		}
		if err != nil {
			obs.RefusalOrFault = err.Error()
		}
		if metrics.TTFTSeconds > 0 {
			ttft := int64(metrics.TTFTSeconds * 1000)
			obs.TTFTMillis = &ttft
		}
		resChan <- servingReqResult{obs: obs}
	}
}

func executeServingLoad(ctx context.Context, backend llm.Backend,
	plan experiment.ServingPlan) ([]experiment.ServingRequestObservation, int64, error) {
	totalRequests := plan.WarmupRequests + plan.Requests
	resChan := make(chan servingReqResult, totalRequests)
	jobs := make(chan servingJob, totalRequests)
	now := time.Now()
	for i := 1; i <= totalRequests; i++ {
		jobs <- servingJob{index: i, isWarmup: i <= plan.WarmupRequests, enqueued: now}
	}
	close(jobs)

	prompt := "Explain the role of concurrency and verified scheduling in local inference in 100 words."
	sampling := ollama.Deterministic(128, plan.RequestedContext)

	timing := &servingTimingTracker{}
	workers := min(plan.Concurrency, totalRequests)

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runServingWorker(ctx, backend, plan.Model.Resolved, prompt, sampling, jobs, resChan, timing)
		}()
	}

	wg.Wait()
	close(resChan)

	results := make([]experiment.ServingRequestObservation, totalRequests)
	for res := range resChan {
		if res.err != nil {
			return nil, 0, res.err
		}
		if res.obs.Index >= 1 && res.obs.Index <= totalRequests {
			results[res.obs.Index-1] = res.obs
		}
	}

	return results, timing.exposureWallMillis(), nil
}

func saveServingBundle(bundle experiment.ServingBundle) (string, error) {
	data, err := bundle.JSON()
	if err != nil {
		return "", err
	}
	directory := filepath.Join(resultsDir(), ".experiments")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	planID := strings.TrimPrefix(bundle.Plan.PlanSHA256, "sha256:")
	if len(planID) > 12 {
		planID = planID[:12]
	}
	path := filepath.Join(directory, "serving-"+planID+".json")
	if err := atomicfile.Write(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func renderServingExperiment(report experiment.ServingReport, mode string) int {
	if render.Resolve(mode) == "none" {
		return exitOK
	}
	if render.Resolve(mode) == "json" {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			errPrint("could not render serving experiment: "+err.Error(), "", "")
			return exitError
		}
		return exitOK
	}
	writeServingExperimentText(report)
	return exitOK
}

func writeServingExperimentText(report experiment.ServingReport) {
	fmt.Fprintf(os.Stdout, "SERVING CONCURRENCY EXPERIMENT  %s\n", strings.ToUpper(string(report.Stage)))
	fmt.Fprintf(os.Stdout, "  %-14s %s\n", "model", terminalText(report.Model.Resolved))
	concurrencyNote := string(report.Concurrency.State)
	if report.Concurrency.Evidence != "" {
		concurrencyNote += ": " + report.Concurrency.Evidence
	}
	fmt.Fprintf(os.Stdout, "  %-14s %d (%s)\n", "concurrency", report.Concurrency.DeclaredLevel, terminalText(concurrencyNote))
	wallSec := float64(report.TotalThroughput.ExposureWallMillis) / 1000.0
	fmt.Fprintf(os.Stdout, "  %-14s %.2fs (%d warmup request(s) excluded)\n", "exposure", wallSec, report.WarmupExcluded)

	fmt.Fprintln(os.Stdout, "\nTHROUGHPUT")
	fmt.Fprintf(os.Stdout, "  %-24s %.2f tok/s (completion: %.2f tok/s, prompt: %.2f tok/s)\n",
		"total throughput", report.TotalThroughput.TotalTokensPerSec,
		report.TotalThroughput.CompletionTokensPerSec, report.TotalThroughput.PromptTokensPerSec)
	fmt.Fprintf(os.Stdout, "  %-24s %.2f tok/s (p50: %s, p95: %s)\n",
		"per-request mean", report.PerRequestThroughput.MeanTokensPerSec,
		formatServingRate(report.PerRequestThroughput.P50TokensPerSec),
		formatServingRate(report.PerRequestThroughput.P95TokensPerSec))

	fmt.Fprintln(os.Stdout, "\nLATENCY & QUEUEING")
	fmt.Fprintf(os.Stdout, "  %-24s %.2f ms (p50: %s, p95: %s, max: %d ms)\n",
		"client queue mean", report.ClientQueue.MeanMillis,
		formatServingDuration(report.ClientQueue.P50Millis),
		formatServingDuration(report.ClientQueue.P95Millis), report.ClientQueue.MaxMillis)
	fmt.Fprintf(os.Stdout, "  %-24s %.2f ms (p50: %s, p95: %s, max: %d ms)\n",
		"server duration mean", report.ServerTiming.MeanMillis,
		formatServingDuration(report.ServerTiming.P50Millis),
		formatServingDuration(report.ServerTiming.P95Millis), report.ServerTiming.MaxMillis)
	if report.TTFT != nil {
		fmt.Fprintf(os.Stdout, "  %-24s %.2f ms (p50: %s, p95: %s)\n",
			"request TTFT mean", report.TTFT.MeanMillis,
			formatServingDuration(report.TTFT.P50Millis),
			formatServingDuration(report.TTFT.P95Millis))
	} else {
		fmt.Fprintf(os.Stdout, "  %-24s unobserved\n", "request TTFT")
	}

	fmt.Fprintln(os.Stdout, "\nWORK & OUTCOMES")
	fmt.Fprintf(os.Stdout, "  %-24s %d/%d (%.1f%%, %.2f/min)\n",
		"accepted outcomes", report.AcceptedWork.AcceptedCount, report.AcceptedWork.EvaluatedCount,
		report.AcceptedWork.AcceptedRate*100.0, report.AcceptedWork.AcceptedPerWallMinute)
	fmt.Fprintf(os.Stdout, "  %-24s %d faults, %d refusals\n",
		"faults / refusals", report.AcceptedWork.FaultCount, report.AcceptedWork.RefusalCount)

	if len(report.Gaps) > 0 {
		fmt.Fprintln(os.Stdout, "\nGAPS")
		for _, gap := range report.Gaps {
			fmt.Fprintln(os.Stdout, "  "+terminalText(gap))
		}
	}
	if report.NextAction != nil {
		fmt.Fprintln(os.Stdout, "\nNEXT")
		fmt.Fprintln(os.Stdout, "  "+terminalText(report.NextAction.Reason))
	}
}

func formatServingDuration(ms *float64) string {
	if ms == nil {
		return "withheld"
	}
	return fmt.Sprintf("%.2f ms", *ms)
}

func formatServingRate(rate *float64) string {
	if rate == nil {
		return "withheld"
	}
	return fmt.Sprintf("%.2f tok/s", *rate)
}
