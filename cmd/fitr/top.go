package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blisspixel/fitr/internal/advise"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/modelref"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/score"
	"github.com/blisspixel/fitr/internal/session"
	"github.com/blisspixel/fitr/internal/top"
)

const presentationSnapshotSchema = "fitr.presentation.snapshot.v2"

// cmdTop is deliberately opt-in. Normal commands keep their line-oriented
// stream contract and never take over the terminal.
func cmdTop(ctx context.Context, args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "run":
			return cmdTopRun(ctx, args[1:])
		case "view":
			return cmdTopView(ctx, args[1:])
		case "history":
			return cmdTopHistory(ctx, args[1:])
		}
	}
	return cmdTopBrowse(ctx, args)
}

func cmdTopHistory(ctx context.Context, args []string) int {
	if len(args) == 0 {
		return cmdTopBrowse(ctx, []string{"--view", "history"})
	}
	switch args[0] {
	case "path":
		if len(args) != 1 {
			errPrint("history path takes no arguments", "", "fitr top history path")
			return exitUsage
		}
		fmt.Println(terminalText(record.NewStore(resultsDir()).HistoryDir()))
		return exitOK
	case "clear":
		fs := flag.NewFlagSet("top history clear", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		yes := fs.Bool("yes", false, "confirm permanent history deletion")
		if code, ok := parseCommandFlags(fs, args[1:]); !ok {
			return code
		}
		if fs.NArg() != 0 {
			errPrint("unexpected history clear argument", fs.Arg(0), "fitr top history clear --yes")
			return exitUsage
		}
		store := record.NewStore(resultsDir())
		if !*yes {
			errPrint("history clear needs --yes", "this permanently removes archived runs but keeps canonical latest results", "review first: fitr top history path")
			return exitUsage
		}
		count, err := store.ClearHistory()
		if err != nil {
			errPrint("could not clear history: "+err.Error(), "", "")
			return exitError
		}
		fmt.Printf("cleared %d archived result(s); canonical latest results were kept\n", count)
		return exitOK
	case "-h", "--help", "help":
		if len(args) != 1 {
			errPrint("unexpected history help argument", args[1], "fitr top history --help")
			return exitUsage
		}
		fmt.Fprintln(os.Stderr, "usage: fitr top history [path|clear --yes]")
		return exitOK
	default:
		errPrint("unknown history action", args[0], "use fitr top history, fitr top history path, or fitr top history clear --yes")
		return exitUsage
	}
}

func cmdTopBrowse(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("top", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	viewName := fs.String("view", "inventory", "live|result|board|history|inventory")
	snapshotOnly := fs.Bool("snapshot", false, "write a privacy-safe presentation snapshot as JSON")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 {
		errPrint("unexpected argument", fs.Arg(0), "fitr top  or  fitr top view [model|result.json]")
		return exitUsage
	}
	view, ok := parseTopView(*viewName)
	if !ok {
		errPrint("invalid top view", *viewName, "use live, result, board, history, or inventory")
		return exitUsage
	}
	data, warnings, err := loadTopSnapshot(nil)
	if err != nil {
		errPrint("could not load result history: "+err.Error(), "", "")
		return exitError
	}
	attachTopInventory(ctx, &data)
	data.Generation = 1
	if *snapshotOnly {
		return writeTopSnapshot(data, warnings, "")
	}
	if !interactiveTerminal() {
		errPrint("fitr top needs an interactive terminal", "stdout or stdin is not a terminal, or TERM=dumb", "use fitr top --snapshot, fitr view, or fitr board instead")
		return exitUsage
	}
	state := top.NewState(data)
	if view == top.ViewInventory && len(data.Inventory) == 0 && len(data.Board) > 0 {
		view = top.ViewBoard
	}
	state.View = view
	state.Error = warningSummary(warnings)
	return runTopBrowser(ctx, state, nil, nil, nil)
}

func cmdTopView(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("top view", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	snapshotOnly := fs.Bool("snapshot", false, "write a privacy-safe presentation snapshot as JSON")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() > 1 {
		errPrint("too many arguments", "top view accepts one model or result path", "fitr top view [model|result.json]")
		return exitUsage
	}
	var candidate *string
	if fs.NArg() == 1 {
		value := fs.Arg(0)
		candidate = &value
	}
	data, warnings, err := loadTopSnapshot(candidate)
	if err != nil {
		errPrint("could not open result: "+err.Error(), "", "fitr top")
		return exitError
	}
	attachTopInventory(ctx, &data)
	if len(data.History) == 0 {
		errPrint("no results yet", "", "run one first: fitr top run <model>")
		return exitError
	}
	selectedID := ""
	if candidate != nil {
		selected, err := selectTopRun(data, *candidate)
		if err != nil {
			errPrint(err.Error(), "", "fitr top")
			return exitError
		}
		selectedID = selected.ID
	}
	data.Generation = 1
	if *snapshotOnly {
		return writeTopSnapshot(data, warnings, selectedID)
	}
	if !interactiveTerminal() {
		errPrint("fitr top needs an interactive terminal", "stdout or stdin is not a terminal, or TERM=dumb", "use fitr top view --snapshot or fitr view instead")
		return exitUsage
	}
	state := top.NewState(data)
	state.View = top.ViewResult
	state.Error = warningSummary(warnings)
	if selectedID != "" {
		state.Selected[top.ViewResult] = selectedID
		state.Selected[top.ViewHistory] = selectedID
	}
	return runTopBrowser(ctx, state, nil, nil, nil)
}

func cmdTopRun(ctx context.Context, args []string) int {
	if hasFlag(args, "help") || hasFlag(args, "h") {
		fmt.Fprintln(os.Stderr, "usage: fitr top run <model> [run flags]")
		return cmdRun(ctx, []string{"--help"})
	}
	if hasFlag(args, "display") {
		errPrint("--display is not valid with fitr top run", "top is already an explicit display mode", "use fitr run <model> --display MODE for stream output")
		return exitUsage
	}
	preview, err := previewTopRun(args)
	if err != nil {
		errPrint(err.Error(), "", "fitr top run <model> [run flags]")
		return exitUsage
	}
	if !interactiveTerminal() {
		errPrint("fitr top needs an interactive terminal", "stdout or stdin is not a terminal, or TERM=dumb", "use fitr run <model> --display plain|json instead")
		return exitUsage
	}
	initial, warnings, sink, code := prepareTopRun(ctx, preview)
	if code != exitOK {
		return code
	}
	initial.Generation = 1
	initial.Live = liveFromSession(sink.Snapshot())
	state := top.NewState(initial)
	state.View = top.ViewLive
	state.Error = warningSummary(warnings)

	events := make(chan top.Event, 64)
	subscription, err := sink.Subscribe(32)
	if err != nil {
		errPrint("could not subscribe to live session: "+err.Error(), "", "")
		return exitError
	}
	defer subscription.Close()
	uiCtx, cancelUI := context.WithCancel(ctx)
	defer cancelUI()
	go relaySession(uiCtx, subscription, events)

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	sequence := &atomic.Uint64{}
	sequence.Store(initial.Generation)
	display := &topDisplay{ctx: runCtx, sink: sink, events: events, sequence: sequence, saved: true}
	runDone := make(chan int, 1)
	go runTopMeasurement(runCtx, args, display, runDone)

	code = runTopBrowser(uiCtx, state, cancelRun, runDone, sequence, events)
	cancelUI()
	return finishTopMeasurement(code, cancelRun, runDone)
}

func prepareTopRun(ctx context.Context, preview topRunPreview) (top.Snapshot, []record.FileWarning, *session.Sink, int) {
	initial, warnings, err := loadTopSnapshot(nil)
	if err != nil {
		errPrint("could not load result history: "+err.Error(), "", "")
		return top.Snapshot{}, nil, nil, exitError
	}
	attachTopInventory(ctx, &initial)
	sink, err := session.NewSink(session.Options{})
	if err != nil {
		errPrint("could not create live session: "+err.Error(), "", "")
		return top.Snapshot{}, nil, nil, exitError
	}
	_, err = sink.Start(session.RunInfo{
		Model: preview.model, Profile: preview.profile, Level: preview.level,
		NumCtx: preview.numCtx, Repeats: preview.repeats,
	})
	if err != nil {
		errPrint("could not start live session: "+err.Error(), "", "")
		return top.Snapshot{}, nil, nil, exitError
	}
	return initial, warnings, sink, exitOK
}

func runTopMeasurement(ctx context.Context, args []string, display *topDisplay, done chan<- int) {
	code := cmdRunWithDisplay(ctx, args, display)
	display.finish(code, ctx.Err())
	done <- code
}

func finishTopMeasurement(browserCode int, cancel context.CancelFunc, done <-chan int) int {
	if browserCode != exitOK {
		return browserCode
	}
	select {
	case completed := <-done:
		return completed
	default:
		cancel()
		return <-done
	}
}

// runTopBrowser owns the terminal. When a measurement is active, cancellation
// waits for the runner and its server cleanup before tcell restores the screen.
func runTopBrowser(ctx context.Context, state top.State, cancel context.CancelFunc, done chan int, sequence *atomic.Uint64, supplied ...chan top.Event) int {
	uiCtx, cancelUI := context.WithCancel(ctx)
	defer cancelUI()
	events := make(chan top.Event, 8)
	if len(supplied) > 0 && supplied[0] != nil {
		events = supplied[0]
	}
	if sequence == nil {
		sequence = &atomic.Uint64{}
		sequence.Store(max(state.Snapshot.Generation, 1))
	}
	var waitOnce sync.Once
	runnerTimedOut := false
	waitForRun := func() {
		if cancel == nil || done == nil {
			return
		}
		waitOnce.Do(func() {
			cancel()
			if !waitForRunCompletion(done, 12*time.Second) {
				runnerTimedOut = true
			}
		})
	}
	app := top.App{
		Initial: state, Events: events,
		Theme:  top.DefaultTheme(os.Getenv("NO_COLOR") != ""),
		Glyphs: top.DefaultGlyphs(os.Getenv("FITR_ASCII") != ""),
		OnEffect: func(effect top.Effect) {
			switch effect.Kind {
			case top.EffectCancelRun:
				waitForRun()
			case top.EffectReload:
				generation := sequence.Add(1)
				go reloadTopSnapshot(uiCtx, events, generation)
			}
		},
	}
	final, err := app.Run(uiCtx)
	cancelUI()
	if runnerTimedOut {
		errPrint("run cancellation did not finish within 12 seconds", "the terminal was restored; the serving backend did not stop promptly", "check the server before starting another measurement")
		return exitError
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		errPrint("top stopped: "+err.Error(), "", "your terminal was restored")
		waitForRun()
		return exitError
	}
	if ctx.Err() != nil || final.Interrupted {
		waitForRun()
		return exitInterrupt
	}
	return exitOK
}

func waitForRunCompletion(done chan int, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case code := <-done:
		done <- code
		return true
	case <-timer.C:
		return false
	}
}

func reloadTopSnapshot(ctx context.Context, events chan top.Event, generation uint64) {
	data, warnings, err := loadTopSnapshot(nil)
	if err != nil {
		emitTopEvent(ctx, events, top.ErrorEvent{Err: err, Generation: generation})
		return
	}
	attachTopInventory(ctx, &data)
	data.Generation = generation
	emitTopEvent(ctx, events, top.SnapshotEvent{Snapshot: data})
	if warning := warningSummary(warnings); warning != "" {
		emitTopEvent(ctx, events, top.ErrorEvent{Err: errors.New(warning), Generation: generation})
	}
}

type topRunPreview struct {
	model, profile, level string
	repeats, numCtx       int
}

type topRunPreviewFlags struct {
	quick, full, checks, html, unsafeExec *bool
	k, numCtx                             *int
	profile, seedset                      *string
}

func previewTopRun(args []string) (topRunPreview, error) {
	fs := flag.NewFlagSet("top run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	options := registerTopRunPreviewFlags(fs)
	if err := fs.Parse(permute(args)); err != nil {
		return topRunPreview{}, err
	}
	if err := validateTopRunPreview(fs, options); err != nil {
		return topRunPreview{}, err
	}
	level := selectedRunLevel(*options.quick, *options.full, *options.checks)
	return topRunPreview{
		model: presentationModelLabel(normalizeModelRef(fs.Arg(0))), profile: *options.profile, level: level,
		repeats: runRepeats(level, *options.k), numCtx: eval.ResolvedCtx(*options.numCtx),
	}, nil
}

func registerTopRunPreviewFlags(fs *flag.FlagSet) topRunPreviewFlags {
	options := topRunPreviewFlags{
		quick: fs.Bool("quick", false, ""), full: fs.Bool("full", false, ""),
		checks: fs.Bool("checks-only", false, ""), k: fs.Int("k", 0, ""),
		profile: fs.String("profile", "", ""), seedset: fs.String("seedset", "", ""),
		html: fs.Bool("html", false, ""), unsafeExec: fs.Bool("allow-unsafe-exec", false, ""),
		numCtx: fs.Int("ctx", 0, ""),
	}
	_ = fs.String("display", "auto", "")
	_ = fs.String("backend", "auto", "")
	_ = fs.Bool("pull", false, "")
	var quiet countFlag
	fs.Var(&quiet, "q", "")
	_ = fs.Bool("v", false, "")
	return options
}

func validateTopRunPreview(fs *flag.FlagSet, options topRunPreviewFlags) error {
	if fs.NArg() != 1 {
		return errors.New("top run needs exactly one model")
	}
	if selectedRunLevels(*options.quick, *options.full, *options.checks) > 1 {
		return errors.New("--quick, --full, and --checks-only are mutually exclusive")
	}
	if *options.checks && *options.seedset == "" {
		return errors.New("--checks-only requires --seedset")
	}
	if *options.checks && *options.html {
		return errors.New("--checks-only cannot use --html")
	}
	if *options.checks && *options.unsafeExec {
		return errors.New("--checks-only cannot use --allow-unsafe-exec")
	}
	level := selectedRunLevel(*options.quick, *options.full, *options.checks)
	repeats := runRepeats(level, *options.k)
	if repeats < 1 {
		return errors.New("-k must be at least 1")
	}
	if *options.numCtx < 0 {
		return errors.New("--ctx cannot be negative")
	}
	return nil
}

type topDisplay struct {
	ctx          context.Context
	sink         *session.Sink
	events       chan top.Event
	sequence     *atomic.Uint64
	currentPhase string
	saved        bool
	once         sync.Once
}

var _ render.Display = (*topDisplay)(nil)
var _ liveTelemetry = (*topDisplay)(nil)

func (d *topDisplay) RunID() string { return d.sink.RunID() }

func (d *topDisplay) Phase(name, detail string) {
	d.currentPhase = name
	_, _ = d.sink.PhaseStarted(name, presentationMessage(detail), 0)
}

func (d *topDisplay) Note(message, level string) {
	noticeLevel := session.NoticeInfo
	if level == "warn" {
		noticeLevel = session.NoticeWarning
	}
	_, _ = d.sink.Notify(session.Notice{Level: noticeLevel, Message: presentationMessage(message)})
}

func (d *topDisplay) Done(name string, _ float64) {
	_, _ = d.sink.PhaseCompleted(name)
	if d.currentPhase == name {
		d.currentPhase = ""
	}
}

func (d *topDisplay) Result(sc score.Scorecard, _ render.Meta) {
	d.once.Do(func() {
		_, _ = d.sink.Complete(session.Completion{Passes: sc.Passes, Fails: sc.Fails, Unproven: sc.Unproven, Saved: d.saved})
		data, warnings, err := loadTopSnapshot(nil)
		if err != nil {
			emitTopEvent(d.ctx, d.events, top.ErrorEvent{Err: err})
			return
		}
		attachTopInventory(d.ctx, &data)
		generation := d.sequence.Add(1)
		data.Generation = generation
		data.Live = liveFromSession(d.sink.Snapshot())
		emitTopEvent(d.ctx, d.events, top.SnapshotEvent{Snapshot: data})
		if warning := warningSummary(warnings); warning != "" {
			emitTopEvent(d.ctx, d.events, top.ErrorEvent{Err: errors.New(warning), Generation: generation})
		}
	})
}

func (*topDisplay) Emit(any) {}
func (*topDisplay) Close()   {}

func (d *topDisplay) RunFailed(err error) {
	d.once.Do(func() {
		_, _ = d.sink.Fail(session.Failure{
			Code: "run_failed", Summary: presentationError(err), Remedy: "review the error and retry",
		})
		emitTopEvent(d.ctx, d.events, top.LiveEvent{Live: liveFromSession(d.sink.Snapshot())})
	})
}

func (d *topDisplay) RunSaveStatus(saved bool, err error) {
	d.saved = saved
	if err != nil {
		_, _ = d.sink.Notify(session.Notice{
			Level: session.NoticeWarning, Code: "save_failed",
			Message: "the measurement completed but its result could not be saved",
			Remedy:  "check the result directory permissions and free space",
		})
	}
}

func (d *topDisplay) LiveProgress(completed, total int, detail string) {
	if d.currentPhase == "" || total <= 0 {
		return
	}
	_, _ = d.sink.PhaseProgress(d.currentPhase, completed, total, presentationMessage(detail))
}

func (d *topDisplay) LiveSpeed(sample eval.SpeedResult, completed, total int) {
	d.LiveProgress(completed, total, fmt.Sprintf("repeat %d of %d", completed, total))
	for _, metric := range []session.MetricSample{
		{Phase: d.currentPhase, Metric: session.MetricDecodeTPS, Value: sample.DecodeTPS, Sample: completed, Total: total, Source: metricSource(sample)},
		{Phase: d.currentPhase, Metric: session.MetricPrefillTPS, Value: sample.PrefillTPS, Sample: completed, Total: total, Source: metricSource(sample)},
		{Phase: d.currentPhase, Metric: session.MetricTTFTSeconds, Value: sample.TTFT, Sample: completed, Total: total, Source: metricSource(sample), Cached: sample.GatedTTFTContaminated()},
	} {
		if metric.Value > 0 {
			_, _ = d.sink.Observe(metric)
		}
	}
}

func (d *topDisplay) LiveMemory(residentGiB float64) {
	if d.currentPhase == "" || residentGiB <= 0 {
		return
	}
	_, _ = d.sink.Observe(session.MetricSample{
		Phase: d.currentPhase, Metric: session.MetricResidentGiB,
		Value: residentGiB, Source: session.SourceDevice,
	})
}

func (d *topDisplay) finish(code int, runErr error) {
	d.once.Do(func() {
		if runErr != nil || code == exitInterrupt {
			_, _ = d.sink.Cancel(session.Cancellation{Reason: session.CancelInterrupt, Message: "measurement cancelled"})
		} else {
			_, _ = d.sink.Fail(session.Failure{Code: "run_failed", Summary: "measurement failed", Remedy: "review the error and retry"})
		}
		emitTopEvent(d.ctx, d.events, top.LiveEvent{Live: liveFromSession(d.sink.Snapshot())})
	})
}

func metricSource(sample eval.SpeedResult) session.MetricSource {
	if sample.ClientDerived {
		return session.SourceClient
	}
	return session.SourceServer
}

func relaySession(ctx context.Context, subscription *session.Subscription, events chan top.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case update, ok := <-subscription.C:
			if !ok {
				return
			}
		drain:
			for {
				select {
				case newer, more := <-subscription.C:
					if !more {
						break drain
					}
					update = newer
				default:
					break drain
				}
			}
			if update.Snapshot.State == session.StateCompleted || update.Snapshot.State == session.StateFailed || update.Snapshot.State == session.StateCancelled {
				return
			}
			emitTopEvent(ctx, events, top.LiveEvent{Live: liveFromSession(update.Snapshot)})
		}
	}
}

// emitTopEvent is cancellation-aware. Live sampling reaches this channel only
// through the nonblocking session subscription, so backpressure here cannot
// alter measurement timing. Final delivery unblocks when the run is canceled.
func emitTopEvent(ctx context.Context, events chan top.Event, event top.Event) bool {
	select {
	case events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func liveFromSession(snapshot session.Snapshot) top.Live {
	live := top.Live{
		Active:    snapshot.State == session.StateRunning,
		Completed: snapshot.State == session.StateCompleted || snapshot.State == session.StateFailed || snapshot.State == session.StateCancelled,
		Cancelled: snapshot.State == session.StateCancelled,
		RunID:     snapshot.RunID, Sequence: snapshot.LastSequence,
		StartedAt: snapshot.StartedAt, UpdatedAt: snapshot.UpdatedAt,
	}
	if snapshot.Completion != nil {
		live.Saved = snapshot.Completion.Saved
	}
	if snapshot.Run != nil {
		live.Model = snapshot.Run.Model
		live.Placement = strings.TrimSpace(snapshot.Run.GPU + " " + snapshot.Run.Backend)
		live.Repeats = snapshot.Run.Repeats
	}
	for index, phase := range snapshot.Phases {
		live.Phases = append(live.Phases, top.LivePhase{
			Name: phase.Name, Detail: phase.Detail, State: string(phase.State),
			Completed: phase.Completed, Total: phase.Total,
		})
		if phase.State == session.PhaseRunning || index == len(snapshot.Phases)-1 {
			live.Phase, live.Detail = phase.Name, phase.Detail
			live.CompletedSteps, live.TotalSteps = phase.Completed, phase.Total
		}
	}
	for _, series := range snapshot.Metrics {
		values := make([]float64, 0, len(series.Samples))
		for _, observation := range series.Samples {
			values = append(values, observation.Sample.Value)
		}
		if len(values) == 0 {
			continue
		}
		latest := values[len(values)-1]
		switch series.Metric {
		case session.MetricDecodeTPS:
			live.Decode, live.DecodeSeries = latest, values
		case session.MetricPrefillTPS:
			live.Prefill, live.PrefillSeries = latest, values
		case session.MetricTTFTSeconds:
			live.TTFT, live.TTFTSeries = latest, values
		case session.MetricResidentGiB:
			live.MemoryGB = latest
		}
	}
	for _, notice := range snapshot.Notices {
		if notice.Notice.Level == session.NoticeWarning {
			live.Warnings = append(live.Warnings, notice.Notice.Message)
		}
	}
	if snapshot.Failure != nil {
		live.Error = snapshot.Failure.Summary
	}
	if snapshot.Cancellation != nil {
		live.Error = snapshot.Cancellation.Message
	}
	return live
}

func attachTopInventory(ctx context.Context, snapshot *top.Snapshot) {
	if snapshot == nil {
		return
	}
	found, _ := llm.Discover(ctx)
	if len(found) == 0 {
		return
	}
	b, err := backendAt(found[0].Kind, found[0].URL)
	if err != nil {
		return
	}
	fp := device.Detect(ctx, b)
	table, _, err := joinInstalled(ctx, b, fp)
	if err != nil {
		return
	}
	snapshot.Inventory = snapshot.Inventory[:0]
	for _, row := range table.Rows {
		snapshot.Inventory = append(snapshot.Inventory, top.InventoryItem{
			ID: row.Model, Model: row.Model, State: row.State, Fit: row.Fit,
			SizeB: row.SizeB, Loaded: row.Loaded, Next: row.Next, Note: row.Note,
			Ctx: row.Ctx, Windows: row.Windows,
		})
	}
}

func loadTopSnapshot(candidate *string) (top.Snapshot, []record.FileWarning, error) {
	store := record.NewStore(resultsDir())
	history, err := store.LoadAll()
	if err != nil {
		return top.Snapshot{}, nil, err
	}
	current, err := store.LoadCurrent()
	if err != nil {
		return top.Snapshot{}, history.Warnings, err
	}
	records, err := addTopCandidate(store, history.Records, candidate)
	if err != nil {
		return top.Snapshot{}, history.Warnings, err
	}
	return buildTopSnapshotWithBoard(records, current.Records), history.Warnings, nil
}

func addTopCandidate(store record.Store, records []*Result, candidate *string) ([]*Result, error) {
	if candidate == nil {
		return records, nil
	}
	if !topCandidateIsFile(*candidate) {
		return records, nil
	}
	selected, err := store.Read(*candidate)
	if err != nil {
		return nil, err
	}
	id := selected.EnsureRunID()
	for _, existing := range records {
		if existing.StableRunID() == id {
			return records, nil
		}
	}
	return append([]*Result{selected}, records...), nil
}

func topCandidateIsFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func buildTopSnapshot(records []*Result) top.Snapshot {
	return buildTopSnapshotWithBoard(records, records)
}

func buildTopSnapshotWithBoard(historyRecords, boardRecords []*Result) top.Snapshot {
	snapshot := top.Snapshot{UpdatedAt: time.Now().UTC()}
	for _, result := range historyRecords {
		if result != nil {
			snapshot.History = append(snapshot.History, presentTopRun(result))
		}
	}
	groups, groupRecords := collectTopBoardGroups(boardRecords)
	appendTopBoardGroups(&snapshot, groups, groupRecords)
	return snapshot
}

func collectTopBoardGroups(boardRecords []*Result) (map[string][]top.Run, map[string]*Result) {
	groups := make(map[string][]top.Run)
	groupRecords := make(map[string]*Result)
	seenBoard := make(map[string]bool)
	for _, result := range boardRecords {
		if result == nil {
			continue
		}
		run := presentTopRun(result)
		// The board is a ranking surface, so only reconciled canonical current
		// records can enter it. History and explicit paths remain display-only.
		if len(result.Contamination) > 0 || result.EvidenceIntegrityIssue() != "" {
			continue
		}
		groupKey, err := result.ComparableDeviceKey()
		if err != nil {
			continue
		}
		boardKey := groupKey + "\x00" + result.Model
		if seenBoard[boardKey] {
			continue
		}
		seenBoard[boardKey] = true
		groups[groupKey] = append(groups[groupKey], run)
		if groupRecords[groupKey] == nil {
			groupRecords[groupKey] = result
		}
	}
	return groups, groupRecords
}

func appendTopBoardGroups(snapshot *top.Snapshot, groups map[string][]top.Run,
	groupRecords map[string]*Result) {
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		runs := groups[key]
		sort.SliceStable(runs, func(i, j int) bool { return runs[i].DecodeMean > runs[j].DecodeMean })
		representative := groupRecords[key]
		title := topBoardTitle(representative)
		note := "same hardware, runtime, and config; rows are comparable"
		if len(runs) < 2 {
			note = "one saved model for this hardware, runtime, and config"
		}
		snapshot.Board = append(snapshot.Board, top.BoardGroup{
			ID: privacyID(key), Title: title, Note: note, Comparable: true, Runs: runs,
		})
	}
}

func topBoardTitle(representative *Result) string {
	title := representative.Device.GPU
	if title == "" {
		title = representative.Device.InferenceDevice
	}
	if title == "" {
		title = "unknown device"
	}
	if representative.Device.Runtime != "" {
		title = strings.TrimSpace(title + " | " + representative.Device.Runtime)
	}
	return title
}

func presentTopRun(result *Result) top.Run {
	started, _ := time.Parse(time.RFC3339Nano, result.StartedAt)
	scorecard := topScorecard(result)
	modelLabel := presentationModelLabel(result.Model)
	deviceID, hardwareID := topDeviceIDs(result)
	toolsBlocked := topToolsBlocked(scorecard)
	return top.Run{
		ID: result.StableRunID(), Model: modelLabel,
		Family: result.ModelMeta.Details.Family, ParamSize: result.ModelMeta.Details.ParameterSize,
		Quant:    result.ModelMeta.Details.QuantizationLevel,
		DeviceID: deviceID, HardwareID: hardwareID, Device: result.Device.GPU,
		Driver: result.Device.GPUDriver, Runtime: result.Device.Runtime,
		Config: topRunConfig(result), Profile: result.Profile, Level: result.Level, UseFor: scorecard.UseFor,
		StartedAt: started, Duration: time.Duration(result.WallSeconds * float64(time.Second)),
		Context: result.ContextSize(), Repeats: result.Repeats,
		DecodeMean: result.DecodeSum.Mean, DecodeSD: result.DecodeSum.SD,
		PrefillMean: result.PrefillSum.Mean, TTFTMean: result.TTFTSum.Mean,
		MemoryGB: verifiedResidentGB(result.Memory), DecodeSeries: topDecodeSeries(result),
		Serves: topServes(scorecard), Warnings: topWarnings(result), Verdicts: topVerdicts(scorecard),
		NextCommand: advise.ResultNext(modelLabel, result.Repeats, result.ContextSize(), result.Level, toolsBlocked),
	}
}

func topScorecard(result *Result) score.Scorecard {
	scorecard := result.Scorecard
	if artifact, err := artifactFrom(result); err == nil {
		scorecard = artifact.Scorecard
	} else {
		scorecard = score.ExcludeEvidence(scorecard,
			"the scoring profile is unavailable, so the stored verdict cannot be reproduced")
	}
	scorecard = score.ExcludeContamination(scorecard, result.Contamination)
	if issue := result.EvidenceIntegrityIssue(); issue != "" {
		scorecard = score.ExcludeEvidence(scorecard, issue)
	}
	return scorecard
}

func topVerdicts(scorecard score.Scorecard) []top.Verdict {
	verdicts := make([]top.Verdict, 0, len(scorecard.Needs))
	for _, need := range score.SortedNeeds(scorecard.Needs) {
		verdict := scorecard.Needs[need]
		label := score.NeedLabel[need]
		if label == "" {
			label = need
		}
		verdicts = append(verdicts, top.Verdict{Need: need, Label: label, State: string(verdict.State), Why: verdict.Why})
	}
	return verdicts
}

func topServes(scorecard score.Scorecard) []string {
	serves := make([]string, 0, len(scorecard.Serves))
	for _, need := range scorecard.Serves {
		if code := score.NeedCode[need]; code != "" {
			serves = append(serves, code)
		}
	}
	return serves
}

func topWarnings(result *Result) []string {
	warnings := make([]string, 0, len(result.Contamination)+2)
	for _, contaminated := range result.Contamination {
		warnings = append(warnings, "INCONCLUSIVE, resident model: "+presentationModelLabel(contaminated))
	}
	if issue := result.EvidenceIntegrityIssue(); issue != "" {
		warnings = append(warnings, "INCONCLUSIVE, "+issue)
	}
	if _, err := result.ComparableDeviceKey(); err != nil {
		warnings = append(warnings, "INCONCLUSIVE, "+err.Error()+"; excluded from board and comparison")
	}
	if result.Profile == "default" {
		warnings = append(warnings, "uncalibrated default profile")
	}
	warnings = append(warnings, topTimingWarnings(result)...)
	warnings = append(warnings, topMemoryWarnings(result)...)
	if result.Repeats < 3 {
		warnings = append(warnings, "fewer than 3 repeats; this result is not rankable")
	}
	return warnings
}

func topTimingWarnings(result *Result) []string {
	var warnings []string
	clientDerived, cachedGated, cachedPrefill, gatedCacheUnknown, prefillCacheUnknown := false, false, false, false, false
	for _, sample := range result.Speed {
		clientDerived = clientDerived || sample.ClientDerived
		cachedGated = cachedGated || sample.GatedTTFTContaminated()
		cachedPrefill = cachedPrefill || sample.PrefillContaminated()
		gatedCacheUnknown = gatedCacheUnknown || !sample.GatedCacheReceiptValid()
		prefillCacheUnknown = prefillCacheUnknown || !sample.PrefillCacheReceiptValid()
	}
	if clientDerived {
		warnings = append(warnings, "timings are client-derived wall-clock observations")
	}
	if cachedGated {
		warnings = append(warnings, "the gated TTFT prompt was cache-contaminated")
	}
	if cachedPrefill {
		warnings = append(warnings, "the prefill probe observed cached tokens and is excluded from uncached-prefill claims")
	}
	if gatedCacheUnknown && len(result.Speed) > 0 {
		warnings = append(warnings, "the backend did not report gated TTFT cache state")
	}
	if prefillCacheUnknown && len(result.Speed) > 0 {
		warnings = append(warnings, "the backend did not prove prefill cache state")
	}
	return warnings
}

func topMemoryWarnings(result *Result) []string {
	var warnings []string
	if result.TaskPlan.Memory && result.Memory.Outcome == eval.OutcomeSkipped {
		reason := strings.TrimSpace(result.Memory.UnavailableReason)
		if reason == "" {
			reason = "the runtime supplied no allocation receipt"
		}
		warnings = append(warnings, "the requested 32K memory probe was unavailable: "+reason)
	}
	if result.Memory.ResidentGB > 0 {
		switch {
		case result.Memory.EffectiveCtx == nil:
			warnings = append(warnings, "the requested 32K memory probe has no verified effective context")
		case *result.Memory.EffectiveCtx != result.Memory.RequestedCtx:
			warnings = append(warnings, fmt.Sprintf("the memory probe requested ctx=%d but the runtime reported ctx=%d",
				result.Memory.RequestedCtx, *result.Memory.EffectiveCtx))
		}
	}
	return warnings
}

func topDecodeSeries(result *Result) []float64 {
	decodeSeries := make([]float64, 0, len(result.Speed))
	for _, sample := range result.Speed {
		decodeSeries = append(decodeSeries, sample.DecodeTPS)
	}
	return decodeSeries
}

func topRunConfig(result *Result) string {
	config := fmt.Sprintf("ctx requested=%d", result.ContextSize())
	if result.DeviceV2 != nil {
		if result.DeviceV2.Context.EffectiveTokens != nil {
			config += fmt.Sprintf(" effective=%d", *result.DeviceV2.Context.EffectiveTokens)
		} else {
			config += " effective=unverified"
		}
	}
	if backend := result.Device.GPUBackend; backend != "" {
		config += " | " + backend
	}
	var runtimeConfig strings.Builder
	for _, key := range []string{"OLLAMA_KV_CACHE_TYPE", "OLLAMA_FLASH_ATTENTION"} {
		if value := result.Device.Config[key]; value != "" {
			runtimeConfig.WriteString(" | " + strings.ToLower(strings.TrimPrefix(key, "OLLAMA_")) + "=" + value)
		}
	}
	return config + runtimeConfig.String()
}

func topToolsBlocked(scorecard score.Scorecard) bool {
	for need, verdict := range scorecard.Needs {
		if strings.Contains(need, "tool") && verdict.State == score.Blocked {
			return true
		}
	}
	return false
}

func topDeviceIDs(result *Result) (deviceID, hardwareID string) {
	if comparableKey, err := result.ComparableDeviceKey(); err == nil {
		deviceID = privacyID(comparableKey)
		hardwareID = privacyID(eval.HardwareKey(result.Device.Key()))
	}
	return deviceID, hardwareID
}

func verifiedResidentGB(memory eval.MemoryResult) float64 {
	resident, _ := memory.VerifiedAt(memoryProbeCtx)
	return resident
}

func selectTopRun(snapshot top.Snapshot, candidate string) (top.Run, error) {
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		selected, readErr := record.NewStore(resultsDir()).Read(candidate)
		if readErr != nil {
			return top.Run{}, readErr
		}
		id := selected.StableRunID()
		for _, run := range snapshot.History {
			if run.ID == id {
				return run, nil
			}
		}
	}
	normalized := normalizeModelRef(candidate)
	for _, run := range snapshot.History {
		if modelref.SameServed(normalized, run.Model) {
			return run, nil
		}
	}
	return top.Run{}, fmt.Errorf("no stored result for %q", candidate)
}

func parseTopView(value string) (top.View, bool) {
	switch strings.ToLower(value) {
	case "live":
		return top.ViewLive, true
	case "result":
		return top.ViewResult, true
	case "board":
		return top.ViewBoard, true
	case "history":
		return top.ViewHistory, true
	case "inventory":
		return top.ViewInventory, true
	default:
		return top.ViewLive, false
	}
}

func writeTopSnapshot(snapshot top.Snapshot, warnings []record.FileWarning, selectedRunID string) int {
	payload := struct {
		Schema        string       `json:"schema"`
		SelectedRunID string       `json:"selected_run_id,omitempty"`
		Snapshot      top.Snapshot `json:"snapshot"`
		Warnings      []string     `json:"warnings,omitempty"`
	}{Schema: presentationSnapshotSchema, SelectedRunID: selectedRunID, Snapshot: snapshot}
	if warning := warningSummary(warnings); warning != "" {
		payload.Warnings = append(payload.Warnings, warning)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(payload); err != nil {
		errPrint("could not write presentation snapshot: "+err.Error(), "", "")
		return exitError
	}
	return exitOK
}

func warningSummary(warnings []record.FileWarning) string {
	if len(warnings) == 0 {
		return ""
	}
	if len(warnings) == 1 {
		return "1 saved result could not be read; press r after repairing it"
	}
	return fmt.Sprintf("%d saved results could not be read; press r after repairing them", len(warnings))
}

func privacyID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

func presentationModelLabel(value string) string {
	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		clean := strings.TrimSuffix(parsed.Path, "/")
		if base := filepath.Base(clean); base != "." && base != "/" && base != "" {
			return base
		}
		return "remote model"
	}
	isLocal := filepath.IsAbs(value) || strings.HasPrefix(value, "/") ||
		strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") ||
		strings.HasPrefix(value, `.\`) || strings.HasPrefix(value, `..\`) ||
		strings.HasPrefix(lower, "file://") || strings.Contains(value, `\`) ||
		(strings.HasSuffix(lower, ".gguf") && strings.Contains(value, "/") && !strings.HasPrefix(lower, "hf.co/"))
	if !isLocal {
		return value
	}
	clean := strings.TrimPrefix(strings.TrimPrefix(value, "file://"), "file:")
	clean = strings.ReplaceAll(clean, `\`, "/")
	if base := filepath.Base(clean); base != "." && base != string(filepath.Separator) && base != "" {
		return base
	}
	return "local model"
}

var (
	presentationURLPattern          = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s]+`)
	presentationNetworkPathPattern  = regexp.MustCompile(`(^|[\s("'=])(?:\\\\|//)[^\r\n,;]+`)
	presentationWindowsPathPattern  = regexp.MustCompile(`(?i)\b[A-Z]:[\\/][^\r\n,;]+`)
	presentationUnixPathPattern     = regexp.MustCompile(`(^|[\s("'=])/(?:[^\r\n,;]+)`)
	presentationRelativePathPattern = regexp.MustCompile(`(^|[\s("'=])\.{1,2}[\\/](?:[^\r\n,;]+)`)
)

func presentationMessage(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	return presentationError(errors.New(message))
}

func presentationError(err error) string {
	if err == nil {
		return "measurement failed"
	}
	message := err.Error()
	for _, item := range []struct{ path, label string }{
		{eval.UserTasksDir(), "<tasks>"},
		{resultsDir(), "<results>"},
		{os.TempDir(), "<temp>"},
	} {
		if item.path != "" {
			message = strings.ReplaceAll(message, item.path, item.label)
		}
	}
	if home, homeErr := os.UserHomeDir(); homeErr == nil && home != "" {
		message = strings.ReplaceAll(message, home, "<home>")
	}
	message = presentationURLPattern.ReplaceAllString(message, "<endpoint>")
	message = presentationNetworkPathPattern.ReplaceAllString(message, "${1}<path>")
	message = presentationWindowsPathPattern.ReplaceAllString(message, "<path>")
	message = presentationUnixPathPattern.ReplaceAllString(message, "${1}<path>")
	message = presentationRelativePathPattern.ReplaceAllString(message, "${1}<path>")
	message = strings.TrimSpace(message)
	if message == "" {
		return "measurement failed"
	}
	return message
}

func hasFlag(args []string, name string) bool {
	long, short := "--"+name, "-"+name
	for _, arg := range args {
		if arg == long || arg == short || strings.HasPrefix(arg, long+"=") || strings.HasPrefix(arg, short+"=") {
			return true
		}
	}
	return false
}

func interactiveTerminal() bool {
	if strings.EqualFold(os.Getenv("TERM"), "dumb") {
		return false
	}
	for _, file := range []*os.File{os.Stdin, os.Stdout} {
		info, err := file.Stat()
		if err != nil || info.Mode()&os.ModeCharDevice == 0 {
			return false
		}
	}
	return true
}
