package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
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
		{"missing model", nil, false},
		{"levels", []string{"model", "--quick", "--full"}, false},
		{"checks seed", []string{"model", "--checks-only"}, false},
		{"checks adaptive", []string{"model", "--checks-only", "--seedset", "pair", "--adaptive"}, false},
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

func TestTopSnapshotIsVersionedAndPrivacySafe(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FITR_RESULTS", dir)
	r := mockResult(filepath.Join(dir, "private", "model.gguf"), 12, .2, 100, 2, 1, 1, 1, 1)
	r.Device.Host = "secret-hostname"
	r.DeviceKey = "secret-hostname|gpu|driver|runtime"
	r.CodeWrite = []eval.ExecResult{{Raw: "private model response", Pass: true}}
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
	if len(snapshot.Board) != 2 || snapshot.Board[0].Comparable || snapshot.Board[1].Comparable {
		t.Fatalf("unknown board = %+v", snapshot.Board)
	}
	if !strings.Contains(snapshot.History[1].Config, "kv_cache_type=q8_0") || !strings.Contains(snapshot.History[1].Config, "flash_attention=1") {
		t.Fatalf("config = %q", snapshot.History[1].Config)
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
