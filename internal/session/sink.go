package session

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

type Clock interface {
	Now() time.Time
}

type ClockFunc func() time.Time

func (f ClockFunc) Now() time.Time { return f() }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type Options struct {
	// RunID is optional. When supplied it must be an opaque token, not a model,
	// hostname, path, or other identifying value.
	RunID string
	Clock Clock
}

type Sink struct {
	mu       sync.Mutex
	runID    string
	clock    Clock
	lastAt   time.Time
	snapshot Snapshot
	subs     map[uint64]*subscriber
	nextSub  uint64
}

type subscriber struct {
	ch      chan Update
	dropped uint64
}

type Subscription struct {
	C      <-chan Update
	cancel func()
	once   sync.Once
}

func (s *Subscription) Close() {
	if s == nil {
		return
	}
	s.once.Do(s.cancel)
}

func NewSink(options Options) (*Sink, error) {
	runID := options.RunID
	if runID == "" {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return nil, fmt.Errorf("create session id: %w", err)
		}
		runID = hex.EncodeToString(raw[:])
	}
	if !tokenPattern.MatchString(runID) {
		return nil, errors.New("invalid session run id")
	}
	clock := options.Clock
	if clock == nil {
		clock = systemClock{}
	}
	return &Sink{
		runID: runID, clock: clock, snapshot: NewSnapshot(),
		subs: make(map[uint64]*subscriber),
	}, nil
}

func (s *Sink) RunID() string { return s.runID }

func (s *Sink) Start(info RunInfo) (Event, error) {
	return s.publish(Event{Kind: KindRunStarted, Run: &info})
}

func (s *Sink) PhaseStarted(name, detail string, total int) (Event, error) {
	return s.publish(Event{Kind: KindPhaseStarted, Phase: &PhaseEvent{Name: name, Detail: detail, Total: total}})
}

func (s *Sink) PhaseProgress(phase string, completed, total int, detail string) (Event, error) {
	return s.publish(Event{Kind: KindPhaseProgress, Progress: &Progress{
		Phase: phase, Completed: completed, Total: total, Detail: detail,
	}})
}

func (s *Sink) Observe(sample MetricSample) (Event, error) {
	return s.publish(Event{Kind: KindMetricSample, Metric: &sample})
}

func (s *Sink) Notify(notice Notice) (Event, error) {
	return s.publish(Event{Kind: KindNotice, Notice: &notice})
}

func (s *Sink) PhaseCompleted(name string) (Event, error) {
	return s.publish(Event{Kind: KindPhaseCompleted, Phase: &PhaseEvent{Name: name}})
}

func (s *Sink) Complete(completion Completion) (Event, error) {
	completion.ExitCode = 0
	if completion.Fails > 0 {
		completion.ExitCode = 3
	}
	return s.publish(Event{Kind: KindRunCompleted, Completion: &completion})
}

func (s *Sink) Fail(failure Failure) (Event, error) {
	failure.ExitCode = 1
	return s.publish(Event{Kind: KindRunFailed, Failure: &failure})
}

func (s *Sink) Cancel(cancellation Cancellation) (Event, error) {
	cancellation.ExitCode = 130
	return s.publish(Event{Kind: KindRunCancelled, Cancellation: &cancellation})
}

func (s *Sink) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot.Clone()
}

func (s *Sink) Subscribe(capacity int) (*Subscription, error) {
	if capacity == 0 {
		capacity = DefaultSubscriptionCapacity
	}
	if capacity < 1 || capacity > MaxSubscriptionCapacity {
		return nil, ErrSubscriptionSize
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	ch := make(chan Update, capacity)
	ch <- Update{Snapshot: s.snapshot.Clone()}
	if isTerminal(s.snapshot.State) {
		close(ch)
		return &Subscription{C: ch, cancel: func() {}}, nil
	}
	s.nextSub++
	id := s.nextSub
	s.subs[id] = &subscriber{ch: ch}
	return &Subscription{
		C: ch,
		cancel: func() {
			s.unsubscribe(id)
		},
	}, nil
}

func (s *Sink) unsubscribe(id uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sub, ok := s.subs[id]; ok {
		delete(s.subs, id)
		close(sub.ch)
	}
}

func (s *Sink) publish(draft Event) (Event, error) {
	now := s.clock.Now().Round(0).UTC()
	if now.IsZero() {
		return Event{}, errors.New("session clock returned zero time")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.lastAt.IsZero() && now.Before(s.lastAt) {
		now = s.lastAt
	}
	draft.Schema = EventSchemaV1
	draft.RunID = s.runID
	draft.Sequence = s.snapshot.LastSequence + 1
	draft.At = now
	if s.snapshot.State == StatePending {
		draft.ElapsedMillis = 0
	} else {
		draft.ElapsedMillis = now.Sub(s.snapshot.StartedAt).Milliseconds()
		if draft.ElapsedMillis < s.snapshot.ElapsedMillis {
			draft.ElapsedMillis = s.snapshot.ElapsedMillis
		}
	}
	draft = canonicalEvent(draft)
	next, err := ApplyEvent(s.snapshot, draft)
	if err != nil {
		return Event{}, err
	}
	s.snapshot = next
	s.lastAt = now
	s.broadcastLocked(draft)
	return draft, nil
}

func (s *Sink) broadcastLocked(event Event) {
	terminal := isTerminal(s.snapshot.State)
	for id, sub := range s.subs {
		copyEvent := event.Clone()
		update := Update{Event: &copyEvent, Snapshot: s.snapshot.Clone(), Dropped: sub.dropped}
		select {
		case sub.ch <- update:
		default:
			select {
			case <-sub.ch:
				sub.dropped++
			default:
			}
			update.Dropped = sub.dropped
			select {
			case sub.ch <- update:
			default:
			}
		}
		if terminal {
			delete(s.subs, id)
			close(sub.ch)
		}
	}
}
