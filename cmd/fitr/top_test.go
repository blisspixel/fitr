package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/score"
	"github.com/blisspixel/fitr/internal/session"
	"github.com/blisspixel/fitr/internal/top"
)

func captureTopStdout(t *testing.T, fn func() int) (string, int) {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	result := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(reader)
		result <- data
	}()
	code := fn()
	_ = writer.Close()
	os.Stdout = original
	data := <-result
	_ = reader.Close()
	return string(data), code
}

func captureTopStderr(t *testing.T, fn func() int) (string, int) {
	t.Helper()
	original := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	result := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(reader)
		result <- data
	}()
	code := fn()
	_ = writer.Close()
	os.Stderr = original
	data := <-result
	_ = reader.Close()
	return string(data), code
}

func TestPreviewTopRunMatchesTrustCriticalValidation(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		ok   bool
	}{
		{"default", []string{"model"}, true},
		{"quick", []string{"model", "--quick", "-k", "1"}, true},
		{"quiet repeated", []string{"model", "-q", "-q"}, true},
		{"quiet explicit", []string{"model", "-q=2"}, true},
		{"missing model", nil, false},
		{"levels", []string{"model", "--quick", "--full"}, false},
		{"checks seed", []string{"model", "--checks-only"}, false},
		{"adaptive removed", []string{"model", "--adaptive"}, false},
		{"checks html", []string{"model", "--checks-only", "--seedset", "pair", "--html"}, false},
		{"bad repeats", []string{"model", "-k", "-1"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := previewTopRun(test.args)
			if (err == nil) != test.ok {
				t.Fatalf("error=%v, want ok=%v", err, test.ok)
			}
		})
	}
}

func TestSelectTopRunNeverGuessesAcrossSavedModelNames(t *testing.T) {
	snapshot := top.Snapshot{History: []top.Run{
		{ID: "coder", Model: "qwen-coder:latest"},
		{ID: "base", Model: "qwen:latest"},
	}}
	got, err := selectTopRun(snapshot, "qwen")
	if err != nil || got.ID != "base" {
		t.Fatalf("exact :latest alias resolved to %+v, %v", got, err)
	}
	if got, err := selectTopRun(snapshot, "qwe"); err == nil {
		t.Fatalf("partial saved-model name selected %+v", got)
	}
}

func TestTopSnapshotIsVersionedAndPrivacySafe(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	r := mockResult(filepath.Join(dir, "private", "model.gguf"), 12, .2, 100, 2, 1, 1, 1, 1)
	r.Device.Host = "secret-hostname"
	r.DeviceKey = "secret-hostname|gpu|driver|runtime"
	r.CodeWrite = []eval.ExecResult{{Raw: "private model response", Pass: true}}
	sealCurrentResult(t, r)
	if _, err := save(r); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "corrupt-private-name.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	output, code := captureTopStdout(t, func() int {
		return cmdTopBrowse(context.Background(), []string{"--snapshot"})
	})
	if code != exitOK {
		t.Fatalf("snapshot exit=%d", code)
	}
	for _, want := range []string{presentationSnapshotSchema, "model.gguf", "1 saved result could not be read"} {
		if !strings.Contains(output, want) {
			t.Errorf("snapshot missing %q", want)
		}
	}
	for _, secret := range []string{dir, "secret-hostname", "private model response", "corrupt-private-name.json"} {
		if strings.Contains(output, secret) {
			t.Errorf("snapshot leaked %q", secret)
		}
	}
}

func TestTopViewSnapshotRejectsMissingModel(t *testing.T) {
	t.Setenv("FITR_RESULTS", t.TempDir())
	if _, err := save(mockResult("present", 12, .2, 100, 2, 1, 1, 1, 1)); err != nil {
		t.Fatal(err)
	}
	output, code := captureTopStdout(t, func() int {
		return cmdTopView(context.Background(), []string{"missing", "--snapshot"})
	})
	if code != exitError || output != "" {
		t.Fatalf("exit=%d output=%q", code, output)
	}
}

func TestTopViewSnapshotIdentifiesSelectedRun(t *testing.T) {
	t.Setenv("FITR_RESULTS", t.TempDir())
	r := mockResult("present", 12, .2, 100, 2, 1, 1, 1, 1)
	if _, err := save(r); err != nil {
		t.Fatal(err)
	}
	output, code := captureTopStdout(t, func() int {
		return cmdTopView(context.Background(), []string{"present", "--snapshot"})
	})
	if code != exitOK || !strings.Contains(output, `"selected_run_id": "`+r.RunID+`"`) {
		t.Fatalf("exit=%d snapshot=%s", code, output)
	}
}

func TestTopFullScreenCommandsRejectRedirectedOutputWithoutControls(t *testing.T) {
	t.Setenv("FITR_RESULTS", t.TempDir())
	t.Setenv("TERM", "xterm-256color")
	if _, err := save(mockResult("present", 12, .2, 100, 2, 1, 1, 1, 1)); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		call func() int
	}{
		{"browse", func() int { return cmdTopBrowse(context.Background(), nil) }},
		{"view", func() int { return cmdTopView(context.Background(), []string{"present"}) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout string
			stderr, code := captureTopStderr(t, func() int {
				var innerCode int
				stdout, innerCode = captureTopStdout(t, test.call)
				return innerCode
			})
			if code != exitUsage || stdout != "" || !strings.Contains(stderr, "interactive terminal") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if strings.Contains(stdout+stderr, "\x1b[") {
				t.Fatalf("redirected command emitted terminal controls: %q", stdout+stderr)
			}
		})
	}
}

func TestTopDisplayReportsUnsaveableCompletion(t *testing.T) {
	t.Setenv("FITR_RESULTS", t.TempDir())
	sink, err := session.NewSink(session.Options{RunID: "display_run_1234"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Start(session.RunInfo{Model: "model"}); err != nil {
		t.Fatal(err)
	}
	events := make(chan top.Event, 8)
	sequence := &atomic.Uint64{}
	display := &topDisplay{ctx: context.Background(), sink: sink, events: events, sequence: sequence, saved: true}
	display.RunSaveStatus(false, os.ErrPermission)
	display.Result(score.Scorecard{Model: "model"}, structMeta())
	snapshot := sink.Snapshot()
	if snapshot.Completion == nil || snapshot.Completion.Saved {
		t.Fatalf("completion = %+v", snapshot.Completion)
	}
	if len(snapshot.Notices) != 1 || snapshot.Notices[0].Notice.Code != "save_failed" {
		t.Fatalf("notices = %+v", snapshot.Notices)
	}
}

func TestTopDisplayPublishesGeneratedTaskProgress(t *testing.T) {
	sink, err := session.NewSink(session.Options{RunID: "progress_run_1234"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Start(session.RunInfo{Model: "model"}); err != nil {
		t.Fatal(err)
	}
	display := &topDisplay{
		ctx: context.Background(), sink: sink, events: make(chan top.Event, 4),
		sequence: &atomic.Uint64{}, saved: true,
	}
	display.Phase("checks", "160 generated tasks")
	display.LiveProgress(37, 160, "37 of 160 generated tasks")
	live := liveFromSession(sink.Snapshot())
	if live.Phase != "checks" || live.CompletedSteps != 37 || live.TotalSteps != 160 ||
		live.Detail != "37 of 160 generated tasks" {
		t.Fatalf("live progress = %+v", live)
	}
}

func TestTopDisplayPublishesMetricsAndLifecycle(t *testing.T) {
	sink, err := session.NewSink(session.Options{RunID: "metrics_run_1234"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Start(session.RunInfo{Model: "model"}); err != nil {
		t.Fatal(err)
	}
	display := &topDisplay{
		ctx: context.Background(), sink: sink, events: make(chan top.Event, 4),
		sequence: &atomic.Uint64{}, saved: true,
	}
	if got := display.RunID(); got != "metrics_run_1234" {
		t.Fatalf("RunID() = %q", got)
	}
	display.Emit(nil)
	display.Close()
	display.LiveProgress(1, 2, "ignored without a phase")
	display.LiveMemory(1)
	display.Phase("speed", "measuring")
	display.LiveProgress(1, 0, "ignored without a total")
	display.LiveSpeed(eval.SpeedResult{
		DecodeTPS: 12, PrefillTPS: 120, TTFT: 0.5,
		GatedCachedTok: 8, GatedPromptTok: 10,
	}, 1, 2)
	display.LiveSpeed(eval.SpeedResult{DecodeTPS: 14, ClientDerived: true}, 2, 2)
	display.LiveMemory(6)
	display.LiveMemory(0)
	display.Note("all good", "info")
	display.Note("watch this", "warn")
	display.Done("other", 0)
	display.Done("speed", 0)

	snapshot := sink.Snapshot()
	if len(snapshot.Metrics) != 4 {
		t.Fatalf("metrics = %+v", snapshot.Metrics)
	}
	if len(snapshot.Notices) != 2 || snapshot.Notices[1].Notice.Level != session.NoticeWarning {
		t.Fatalf("notices = %+v", snapshot.Notices)
	}
	if display.currentPhase != "" {
		t.Fatalf("current phase = %q", display.currentPhase)
	}
	if metricSource(eval.SpeedResult{}) != session.SourceServer ||
		metricSource(eval.SpeedResult{ClientDerived: true}) != session.SourceClient {
		t.Fatal("metric sources were not classified")
	}
}

func TestTopDisplayPublishesFailuresAndFinishStates(t *testing.T) {
	newDisplay := func(t *testing.T, id string) (*topDisplay, *session.Sink, chan top.Event) {
		t.Helper()
		sink, err := session.NewSink(session.Options{RunID: id})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sink.Start(session.RunInfo{Model: "model"}); err != nil {
			t.Fatal(err)
		}
		events := make(chan top.Event, 4)
		return &topDisplay{ctx: context.Background(), sink: sink, events: events, sequence: &atomic.Uint64{}}, sink, events
	}

	display, sink, events := newDisplay(t, "failed_run_1234")
	display.RunFailed(os.ErrPermission)
	if snapshot := sink.Snapshot(); snapshot.State != session.StateFailed || snapshot.Failure == nil {
		t.Fatalf("failed snapshot = %+v", snapshot)
	}
	if _, ok := (<-events).(top.LiveEvent); !ok {
		t.Fatal("run failure did not emit a live event")
	}

	display, sink, events = newDisplay(t, "cancelled_run_1234")
	display.finish(exitInterrupt, nil)
	if snapshot := sink.Snapshot(); snapshot.State != session.StateCancelled {
		t.Fatalf("cancelled snapshot = %+v", snapshot)
	}
	<-events

	display, sink, events = newDisplay(t, "finish_failed_run_1234")
	display.finish(exitError, nil)
	if snapshot := sink.Snapshot(); snapshot.State != session.StateFailed {
		t.Fatalf("finish failure snapshot = %+v", snapshot)
	}
	<-events
}

func TestRelaySessionEmitsLatestLiveSnapshotAndStops(t *testing.T) {
	sink, err := session.NewSink(session.Options{RunID: "relay_run_1234"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Start(session.RunInfo{Model: "model"}); err != nil {
		t.Fatal(err)
	}
	subscription, err := sink.Subscribe(4)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan top.Event, 4)
	done := make(chan struct{})
	go func() {
		relaySession(ctx, subscription, events)
		close(done)
	}()
	select {
	case event := <-events:
		if live, ok := event.(top.LiveEvent); !ok || live.Live.RunID != "relay_run_1234" {
			t.Fatalf("relay event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("relay did not emit the initial snapshot")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("relay did not stop after cancellation")
	}
}

func TestTopDisplayRedactsLocalPathsFromLiveMessages(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	t.Setenv("FITR_TASKS", filepath.Join(dir, "private tasks"))
	sink, err := session.NewSink(session.Options{RunID: "redaction_run_1234"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Start(session.RunInfo{Model: "model"}); err != nil {
		t.Fatal(err)
	}
	display := &topDisplay{ctx: context.Background(), sink: sink, events: make(chan top.Event, 4), sequence: &atomic.Uint64{}, saved: true}
	display.Phase("checks", "loaded from "+filepath.Join(dir, "private tasks"))
	display.Note("could not open "+filepath.Join(dir, "private tasks", "task.json"), "warn")
	snapshot := sink.Snapshot()
	encoded := snapshot.Phases[0].Detail + " " + snapshot.Notices[0].Notice.Message
	if strings.Contains(encoded, dir) || strings.Contains(encoded, "private tasks") {
		t.Fatalf("live presentation leaked a local path: %q", encoded)
	}
	if !strings.Contains(encoded, "<") {
		t.Fatalf("live presentation did not retain a redaction marker: %q", encoded)
	}
	for _, secret := range []string{
		`Z:\private models\model.gguf`,
		`/opt/private models/model.gguf`,
		`../private/model.gguf`,
		`https://localhost:11434/private`,
		`ssh://private-user@private-host.internal/models`,
		`\\private-fileserver\models\secret.gguf`,
	} {
		if got := presentationMessage("could not open " + secret); strings.Contains(got, "private") || strings.Contains(got, "localhost") {
			t.Errorf("presentationMessage(%q) = %q", secret, got)
		}
	}
}

func TestSuppliedFullScreenDisplayOwnsRunDiagnostics(t *testing.T) {
	sink, err := session.NewSink(session.Options{RunID: "diagnostic_run_1234"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Start(session.RunInfo{Model: "model"}); err != nil {
		t.Fatal(err)
	}
	display := &topDisplay{ctx: context.Background(), sink: sink, events: make(chan top.Event, 4), sequence: &atomic.Uint64{}, saved: true}
	stderr, code := captureTopStderr(t, func() int {
		return cmdRunWithDisplay(context.Background(), []string{"model", "--checks-only"}, display)
	})
	if code != exitUsage || stderr != "" {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if sink.Snapshot().Failure == nil {
		t.Fatal("structured failure was not published")
	}
}

func TestTopSnapshotKeepsUnknownDevicesUnrankedAndShowsConfig(t *testing.T) {
	a := mockResult("a", 10, .1, 100, 1, 1, 1, 1, 1)
	b := mockResult("b", 20, .1, 100, 1, 1, 1, 1, 1)
	a.DeviceKey, b.DeviceKey = "", ""
	b.Device.Config["OLLAMA_KV_CACHE_TYPE"] = "q8_0"
	b.Device.Config["OLLAMA_FLASH_ATTENTION"] = "1"
	snapshot := buildTopSnapshot([]*Result{a, b})
	if len(snapshot.Board) != 0 {
		t.Fatalf("unknown unverified results entered the board: %+v", snapshot.Board)
	}
	if !strings.Contains(snapshot.History[1].Config, "kv_cache_type=q8_0") || !strings.Contains(snapshot.History[1].Config, "flash_attention=1") {
		t.Fatalf("config = %q", snapshot.History[1].Config)
	}
}

func TestTopSnapshotDisclosesUnavailableMemoryProbe(t *testing.T) {
	result := mockResult("memory-unavailable", 10, .1, 100, 1, 1, 1, 1, 1)
	run := presentTopRun(result)
	if !slices.Contains(run.Warnings,
		"the runtime did not provide an exact resident allocation") {
		t.Fatalf("memory warning missing from %+v", run.Warnings)
	}
}

func TestTopSnapshotUsesCentralSemanticNextAction(t *testing.T) {
	run := presentTopRun(mockResult("measured", 10, .1, 100, 1, 1, 1, 1, 1))
	if run.NextCommand != "fitr board" {
		t.Fatalf("next command = %q, want central comparison action", run.NextCommand)
	}
}

func TestTopSnapshotKeepsContaminatedRunInHistoryButOutOfBoard(t *testing.T) {
	clean := mockResult("clean", 10, .1, 100, 1, 1, 1, 1, 1)
	contaminated := mockResult("contaminated", 100, .1, 100, 1, 1, 1, 1, 1)
	contaminated.Contamination = []string{"leftover:7b"}

	snapshot := buildTopSnapshot([]*Result{contaminated, clean})
	if len(snapshot.History) != 2 {
		t.Fatalf("history dropped the integrity record: %+v", snapshot.History)
	}
	if len(snapshot.Board) != 1 || len(snapshot.Board[0].Runs) != 1 || snapshot.Board[0].Runs[0].Model != "clean" {
		t.Fatalf("board retained a contaminated ranking row: %+v", snapshot.Board)
	}
	var found bool
	for _, verdict := range snapshot.History[0].Verdicts {
		if verdict.State == string(score.Inconclusive) {
			found = true
			break
		}
	}
	if !found || !strings.Contains(snapshot.History[0].UseFor, "INCONCLUSIVE") {
		t.Fatalf("history did not expose explicit inconclusive state: %+v", snapshot.History[0])
	}
}

func TestTopBoardUsesOnlyReconciledCurrentRecords(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	trusted := mockResult("trusted", 10, .1, 100, 1, 1, 1, 1, 1)
	if _, err := save(trusted); err != nil {
		t.Fatal(err)
	}

	forged := mockResult("forged", 999, .01, 999, 1, 1, 1, 1, 1)
	raw, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(record.NewStore(dir).HistoryDir(), "forged.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	snapshot, _, err := loadTopSnapshot(nil)
	if err != nil {
		t.Fatal(err)
	}
	assertOnlyTopBoardModel(t, snapshot, "trusted")
	var forgedHistory *top.Run
	for i := range snapshot.History {
		if snapshot.History[i].Model == "forged" {
			forgedHistory = &snapshot.History[i]
		}
	}
	if forgedHistory == nil || !strings.Contains(forgedHistory.UseFor, "INCONCLUSIVE") {
		t.Fatalf("injected history was not disclosed as display-only: %+v", snapshot.History)
	}
}

func TestTopExternalCandidateCannotEnterBoard(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	trusted := mockResult("trusted", 10, .1, 100, 1, 1, 1, 1, 1)
	if _, err := save(trusted); err != nil {
		t.Fatal(err)
	}
	external := mockResult("external", 999, .01, 999, 1, 1, 1, 1, 1)
	raw, err := json.Marshal(external)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "external.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	snapshot, _, err := loadTopSnapshot(&path)
	if err != nil {
		t.Fatal(err)
	}
	assertOnlyTopBoardModel(t, snapshot, "trusted")
	if len(snapshot.History) < 2 || snapshot.History[0].Model != "external" ||
		!strings.Contains(snapshot.History[0].UseFor, "INCONCLUSIVE") {
		t.Fatalf("external candidate was not display-only: %+v", snapshot.History)
	}
}

func assertOnlyTopBoardModel(t *testing.T, snapshot top.Snapshot, model string) {
	t.Helper()
	var models []string
	for _, group := range snapshot.Board {
		for _, run := range group.Runs {
			models = append(models, run.Model)
		}
	}
	if len(models) != 1 || models[0] != model {
		t.Fatalf("board models = %v, want only %q", models, model)
	}
}

func TestTopHistoryClearRequiresConfirmationAndKeepsCanonical(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	if _, err := save(mockResult("model", 10, .1, 100, 1, 1, 1, 1, 1)); err != nil {
		t.Fatal(err)
	}
	if code := cmdTopHistory(context.Background(), []string{"clear"}); code != exitUsage {
		t.Fatalf("unconfirmed clear exit=%d", code)
	}
	output, code := captureTopStdout(t, func() int {
		return cmdTopHistory(context.Background(), []string{"clear", "--yes"})
	})
	if code != exitOK || !strings.Contains(output, "cleared 1 archived result") {
		t.Fatalf("exit=%d output=%q", code, output)
	}
	current, err := record.NewStore(dir).LoadCurrent()
	if err != nil || len(current.Records) != 1 {
		t.Fatalf("current=%d err=%v", len(current.Records), err)
	}
}

func TestWaitForRunCompletionIsBoundedAndPreservesExitCode(t *testing.T) {
	done := make(chan int, 1)
	done <- exitInterrupt
	if !waitForRunCompletion(done, time.Second) {
		t.Fatal("ready runner timed out")
	}
	if code := <-done; code != exitInterrupt {
		t.Fatalf("preserved exit=%d", code)
	}
	if waitForRunCompletion(make(chan int, 1), 5*time.Millisecond) {
		t.Fatal("stalled runner reported completion")
	}
}

// structMeta avoids coupling this test to result presentation details.
func structMeta() render.Meta { return render.Meta{} }
