package session

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReplayMatchesLiveSnapshotAndJSONRoundTrip(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC))
	sink := mustSink(t, Options{RunID: "replay-1", Clock: clock})
	var events []Event
	publish := func(event Event, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
		clock.Advance(250 * time.Millisecond)
	}

	publish(sink.Start(RunInfo{Model: "qwen3:8b", Quant: "Q4_K_M", GPU: "test GPU", Level: "full", NumCtx: 8192, Repeats: 2}))
	publish(sink.PhaseStarted("speed", "two repeats", 2))
	publish(sink.PhaseProgress("speed", 1, 2, "repeat 1"))
	publish(sink.Observe(MetricSample{Phase: "speed", Metric: MetricDecodeTPS, Value: 22.5, Sample: 1, Total: 2, Source: SourceServer}))
	publish(sink.PhaseProgress("speed", 2, 2, "repeat 2"))
	publish(sink.Observe(MetricSample{Phase: "speed", Metric: MetricDecodeTPS, Value: 23.0, Sample: 2, Total: 2, Source: SourceServer}))
	publish(sink.PhaseCompleted("speed"))
	publish(sink.Notify(Notice{Level: NoticeWarning, Code: "first_run_slow", Message: "first repeat was slower", Remedy: "measure on a quiet system"}))
	publish(sink.Complete(Completion{Passes: 5, Fails: 1, Unproven: 2, Saved: true}))

	wire, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []Event
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	replayed, err := Replay(decoded)
	if err != nil {
		t.Fatal(err)
	}
	live := sink.Snapshot()
	if !reflect.DeepEqual(replayed, live) {
		t.Fatalf("replayed snapshot differs\nreplayed: %#v\nlive: %#v", replayed, live)
	}
	if replayed.State != StateCompleted || replayed.Completion.ExitCode != 3 {
		t.Fatalf("terminal snapshot = state %q, exit %d", replayed.State, replayed.Completion.ExitCode)
	}
	if len(replayed.Metrics) != 1 || len(replayed.Metrics[0].Samples) != 2 {
		t.Fatalf("metric history = %#v", replayed.Metrics)
	}
}

func TestApplyEventRejectsSequenceTimeSchemaAndPayloadDrift(t *testing.T) {
	base := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	start := Event{
		Schema: EventSchemaV1, RunID: "run-1", Sequence: 1, At: base,
		Kind: KindRunStarted, Run: &RunInfo{Model: "m"},
	}
	snapshot, err := ApplyEvent(NewSnapshot(), start)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		event Event
		want  error
	}{
		{"gap", Event{Schema: EventSchemaV1, RunID: "run-1", Sequence: 3, At: base, Kind: KindNotice, Notice: &Notice{Level: NoticeInfo, Message: "x"}}, ErrSequence},
		{"backward time", Event{Schema: EventSchemaV1, RunID: "run-1", Sequence: 2, At: base.Add(-time.Second), Kind: KindNotice, Notice: &Notice{Level: NoticeInfo, Message: "x"}}, ErrInvalidEvent},
		{"schema", Event{Schema: "future", RunID: "run-1", Sequence: 2, At: base, Kind: KindNotice, Notice: &Notice{Level: NoticeInfo, Message: "x"}}, ErrInvalidEvent},
		{"run id", Event{Schema: EventSchemaV1, RunID: "other", Sequence: 2, At: base, Kind: KindNotice, Notice: &Notice{Level: NoticeInfo, Message: "x"}}, ErrInvalidEvent},
		{"two payloads", Event{Schema: EventSchemaV1, RunID: "run-1", Sequence: 2, At: base, Kind: KindNotice, Notice: &Notice{Level: NoticeInfo, Message: "x"}, Phase: &PhaseEvent{Name: "p"}}, ErrInvalidEvent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ApplyEvent(snapshot, tt.event)
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestReplayCanonicalizesUntrustedText(t *testing.T) {
	base := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{Schema: EventSchemaV1, RunID: "safe-1", Sequence: 1, At: base, Kind: KindRunStarted,
			Run: &RunInfo{Model: "bad\x1b[2J\nmodel", GPU: "gpu\x1b]0;title\a"}},
		{Schema: EventSchemaV1, RunID: "safe-1", Sequence: 2, At: base, Kind: KindRunFailed,
			Failure: &Failure{Code: "backend_error", Summary: "failed\r\nforged", Remedy: "retry\x1b[31m now", ExitCode: 1}},
	}
	snapshot, err := Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Run.Model != "bad model" || snapshot.Run.GPU != "gpu" {
		t.Fatalf("unsafe run info survived: %#v", snapshot.Run)
	}
	if snapshot.Failure.Summary != "failed forged" || snapshot.Failure.Remedy != "retry now" {
		t.Fatalf("unsafe failure survived: %#v", snapshot.Failure)
	}
}

func TestInvalidMetricsAreRejected(t *testing.T) {
	clock := newFakeClock(time.Now().UTC())
	sink := mustSink(t, Options{RunID: "metric-validation", Clock: clock})
	mustStart(t, sink, RunInfo{Model: "m"})
	mustPhaseStart(t, sink, "speed", "", 0)
	bad := []MetricSample{
		{Phase: "speed", Metric: "unknown", Value: 1, Source: SourceServer},
		{Phase: "speed", Metric: MetricDecodeTPS, Value: math.NaN(), Source: SourceServer},
		{Phase: "speed", Metric: MetricDecodeTPS, Value: -1, Source: SourceServer},
		{Phase: "speed", Metric: MetricDecodeTPS, Value: 1, Sample: 1, Source: SourceServer},
		{Phase: "speed", Metric: MetricDecodeTPS, Value: 1, Source: "guess"},
	}
	for i, sample := range bad {
		if _, err := sink.Observe(sample); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("sample %d error = %v", i, err)
		}
	}
	if got := sink.Snapshot().LastSequence; got != 2 {
		t.Fatalf("invalid events consumed sequence numbers: %d", got)
	}
}

func TestEventSchemaDoesNotExposeSensitiveFields(t *testing.T) {
	event := Event{Run: &RunInfo{Model: "m"}}
	b, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	wire := strings.ToLower(string(b))
	for _, forbidden := range []string{"prompt", "response", "hostname", "endpoint", "environment", "result_path", "local_path", "answer"} {
		if strings.Contains(wire, forbidden) {
			t.Fatalf("wire schema contains forbidden field %q: %s", forbidden, wire)
		}
	}
}
