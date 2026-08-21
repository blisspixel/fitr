package session

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(now time.Time) *fakeClock { return &fakeClock{now: now} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func (c *fakeClock) Set(now time.Time) {
	c.mu.Lock()
	c.now = now
	c.mu.Unlock()
}

func mustSink(t *testing.T, options Options) *Sink {
	t.Helper()
	sink, err := NewSink(options)
	if err != nil {
		t.Fatal(err)
	}
	return sink
}

func mustEvent(t *testing.T, event Event, err error) Event {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func mustStart(t *testing.T, sink *Sink, info RunInfo) Event {
	t.Helper()
	event, err := sink.Start(info)
	return mustEvent(t, event, err)
}

func mustPhaseStart(t *testing.T, sink *Sink, name, detail string, total int) Event {
	t.Helper()
	event, err := sink.PhaseStarted(name, detail, total)
	return mustEvent(t, event, err)
}

func mustNotice(t *testing.T, sink *Sink, notice Notice) Event {
	t.Helper()
	event, err := sink.Notify(notice)
	return mustEvent(t, event, err)
}

func mustObservation(t *testing.T, sink *Sink, sample MetricSample) Event {
	t.Helper()
	event, err := sink.Observe(sample)
	return mustEvent(t, event, err)
}

func mustComplete(t *testing.T, sink *Sink, completion Completion) Event {
	t.Helper()
	event, err := sink.Complete(completion)
	return mustEvent(t, event, err)
}

func mustFail(t *testing.T, sink *Sink, failure Failure) Event {
	t.Helper()
	event, err := sink.Fail(failure)
	return mustEvent(t, event, err)
}

func mustCancel(t *testing.T, sink *Sink, cancellation Cancellation) Event {
	t.Helper()
	event, err := sink.Cancel(cancellation)
	return mustEvent(t, event, err)
}

func TestLifecycleRulesAndTerminalOutcomes(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC))
	sink := mustSink(t, Options{RunID: "lifecycle", Clock: clock})
	if _, err := sink.PhaseStarted("speed", "", 0); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("phase before start error = %v", err)
	}
	mustStart(t, sink, RunInfo{Model: "m"})
	if _, err := sink.Start(RunInfo{Model: "m"}); !errors.Is(err, ErrAlreadyStarted) {
		t.Fatalf("second start error = %v", err)
	}
	mustPhaseStart(t, sink, "speed", "", 2)
	if _, err := sink.PhaseStarted("memory", "", 0); !errors.Is(err, ErrPhaseActive) {
		t.Fatalf("parallel phase error = %v", err)
	}
	if _, err := sink.Complete(Completion{}); !errors.Is(err, ErrPhaseActive) {
		t.Fatalf("completion with active phase error = %v", err)
	}
	if _, err := sink.PhaseCompleted("wrong"); !errors.Is(err, ErrPhaseNotActive) {
		t.Fatalf("wrong phase completion error = %v", err)
	}
	mustFail(t, sink, Failure{Code: "backend_timeout", Summary: "backend timed out", Remedy: "retry the run"})
	snapshot := sink.Snapshot()
	if snapshot.State != StateFailed || snapshot.Failure.ExitCode != 1 || snapshot.Phases[0].State != PhaseFailed {
		t.Fatalf("failed snapshot = %#v", snapshot)
	}
	if _, err := sink.Notify(Notice{Level: NoticeInfo, Message: "late"}); !errors.Is(err, ErrTerminal) {
		t.Fatalf("post-terminal error = %v", err)
	}

	cancelled := mustSink(t, Options{RunID: "cancelled", Clock: clock})
	mustStart(t, cancelled, RunInfo{Model: "m"})
	mustPhaseStart(t, cancelled, "checks", "", 10)
	mustCancel(t, cancelled, Cancellation{Reason: CancelInterrupt, Message: "interrupted"})
	if got := cancelled.Snapshot(); got.State != StateCancelled || got.Cancellation.ExitCode != 130 || got.Phases[0].State != PhaseCancelled {
		t.Fatalf("cancelled snapshot = %#v", got)
	}

	completed := mustSink(t, Options{RunID: "completed", Clock: clock})
	mustStart(t, completed, RunInfo{Model: "m"})
	mustComplete(t, completed, Completion{Passes: 4})
	if got := completed.Snapshot().Completion.ExitCode; got != 0 {
		t.Fatalf("passing completion exit code = %d", got)
	}
}

func TestClockIsClampedAndSequenceIsMonotonic(t *testing.T) {
	base := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	clock := newFakeClock(base)
	sink := mustSink(t, Options{RunID: "clock-1", Clock: clock})
	first := mustStart(t, sink, RunInfo{Model: "m"})
	clock.Set(base.Add(-time.Hour))
	second := mustNotice(t, sink, Notice{Level: NoticeInfo, Message: "wall clock moved"})
	clock.Set(base.Add(1500 * time.Millisecond))
	third := mustComplete(t, sink, Completion{})
	if first.Sequence != 1 || second.Sequence != 2 || third.Sequence != 3 {
		t.Fatalf("sequences = %d, %d, %d", first.Sequence, second.Sequence, third.Sequence)
	}
	if second.At.Before(first.At) || second.ElapsedMillis != 0 || third.ElapsedMillis != 1500 {
		t.Fatalf("times = %#v, %#v, %#v", first, second, third)
	}
}

func TestSlowSubscriberCannotBlockAndReceivesTerminalSnapshot(t *testing.T) {
	clock := newFakeClock(time.Now().UTC())
	sink := mustSink(t, Options{RunID: "slow-consumer", Clock: clock})
	sub, err := sink.Subscribe(1)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	done := make(chan error, 1)
	go func() {
		if _, err := sink.Start(RunInfo{Model: "m"}); err != nil {
			done <- err
			return
		}
		for i := 0; i < 500; i++ {
			if _, err := sink.Notify(Notice{Level: NoticeInfo, Message: fmt.Sprintf("notice %d", i)}); err != nil {
				done <- err
				return
			}
		}
		_, err := sink.Complete(Completion{Passes: 1})
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("publishing blocked on an unread subscriber")
	}

	updates := make([]Update, 0, 1)
	for update := range sub.C {
		updates = append(updates, update)
	}
	if len(updates) != 1 {
		t.Fatalf("capacity-one subscriber retained %d updates", len(updates))
	}
	last := updates[0]
	if last.Event == nil || last.Event.Kind != KindRunCompleted || last.Snapshot.State != StateCompleted {
		t.Fatalf("last update = %#v", last)
	}
	if last.Dropped == 0 || last.Snapshot.LastSequence != 502 {
		t.Fatalf("drop accounting = %d, sequence = %d", last.Dropped, last.Snapshot.LastSequence)
	}
	if len(last.Snapshot.Notices) != MaxNotices || last.Snapshot.NoticesDropped != 500-MaxNotices {
		t.Fatalf("notice retention = %d retained, %d dropped", len(last.Snapshot.Notices), last.Snapshot.NoticesDropped)
	}
}

func TestSubscriptionAndSnapshotCopiesAreIsolated(t *testing.T) {
	clock := newFakeClock(time.Now().UTC())
	sink := mustSink(t, Options{RunID: "copy-isolation", Clock: clock})
	sub, err := sink.Subscribe(3)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	<-sub.C
	mustStart(t, sink, RunInfo{Model: "original"})
	event := mustNotice(t, sink, Notice{Level: NoticeInfo, Message: "original notice"})
	event.Notice.Message = "mutated"
	startUpdate := <-sub.C
	noticeUpdate := <-sub.C
	if startUpdate.Event.Run.Model != "original" || noticeUpdate.Event.Notice.Message != "original notice" {
		t.Fatalf("subscriber event was mutated: %#v %#v", startUpdate.Event, noticeUpdate.Event)
	}

	snapshot := sink.Snapshot()
	snapshot.Run.Model = "changed"
	snapshot.Notices[0].Notice.Message = "changed"
	again := sink.Snapshot()
	if again.Run.Model != "original" || again.Notices[0].Notice.Message != "original notice" {
		t.Fatalf("snapshot mutation escaped clone: %#v", again)
	}
}

func TestSubscriptionCloseIsIdempotent(t *testing.T) {
	sink := mustSink(t, Options{RunID: "close-sub"})
	sub, err := sink.Subscribe(1)
	if err != nil {
		t.Fatal(err)
	}
	sub.Close()
	sub.Close()
	if _, ok := <-sub.C; !ok {
		t.Fatal("initial snapshot should remain drainable")
	}
	if _, ok := <-sub.C; ok {
		t.Fatal("subscription channel remained open")
	}
	mustStart(t, sink, RunInfo{Model: "m"})
}

func TestMetricHistoryIsBounded(t *testing.T) {
	sink := mustSink(t, Options{RunID: "metric-history"})
	mustStart(t, sink, RunInfo{Model: "m"})
	mustPhaseStart(t, sink, "speed", "", 300)
	for i := 1; i <= 300; i++ {
		mustObservation(t, sink, MetricSample{
			Phase: "speed", Metric: MetricDecodeTPS, Value: float64(i),
			Sample: i, Total: 300, Source: SourceServer,
		})
	}
	snapshot := sink.Snapshot()
	series := snapshot.Metrics[0]
	if len(series.Samples) != MaxMetricSamplesPerSeries || series.Dropped != 44 {
		t.Fatalf("metric retention = %d retained, %d dropped", len(series.Samples), series.Dropped)
	}
	if got := series.Samples[0].Sample.Sample; got != 45 {
		t.Fatalf("first retained sample = %d", got)
	}
}

func TestConcurrentPublishSnapshotAndSubscribe(t *testing.T) {
	sink := mustSink(t, Options{RunID: "race-safe"})
	mustStart(t, sink, RunInfo{Model: "m"})

	const writers = 8
	const perWriter = 100
	var writersWG sync.WaitGroup
	for writer := 0; writer < writers; writer++ {
		writersWG.Add(1)
		go func(writer int) {
			defer writersWG.Done()
			for i := 0; i < perWriter; i++ {
				if _, err := sink.Notify(Notice{Level: NoticeInfo, Message: fmt.Sprintf("%d/%d", writer, i)}); err != nil {
					t.Errorf("notify: %v", err)
					return
				}
				_ = sink.Snapshot()
			}
		}(writer)
	}

	const readers = 4
	var readersWG sync.WaitGroup
	for i := 0; i < readers; i++ {
		sub, err := sink.Subscribe(2)
		if err != nil {
			t.Fatal(err)
		}
		readersWG.Add(1)
		go func(sub *Subscription) {
			defer readersWG.Done()
			defer sub.Close()
			for range sub.C {
			}
		}(sub)
	}
	writersWG.Wait()
	mustComplete(t, sink, Completion{Passes: 1})
	readersWG.Wait()
	if got, want := sink.Snapshot().LastSequence, uint64(2+writers*perWriter); got != want {
		t.Fatalf("last sequence = %d, want %d", got, want)
	}
}

func TestSubscriptionCapacityValidation(t *testing.T) {
	sink := mustSink(t, Options{RunID: "capacity"})
	for _, capacity := range []int{-1, MaxSubscriptionCapacity + 1} {
		if _, err := sink.Subscribe(capacity); !errors.Is(err, ErrSubscriptionSize) {
			t.Fatalf("capacity %d error = %v", capacity, err)
		}
	}
}

func TestInvalidRunID(t *testing.T) {
	for _, id := range []string{"has space", "C:/Users/name", strings.Repeat("x", 65)} {
		if _, err := NewSink(Options{RunID: id}); err == nil {
			t.Fatalf("accepted run id %q", id)
		}
	}
}
