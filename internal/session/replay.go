package session

import (
	"errors"
	"fmt"
	"math"
	"regexp"
)

var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

var (
	ErrNotStarted       = errors.New("session has not started")
	ErrAlreadyStarted   = errors.New("session has already started")
	ErrTerminal         = errors.New("session is already terminal")
	ErrPhaseActive      = errors.New("another phase is active")
	ErrPhaseNotActive   = errors.New("phase is not active")
	ErrInvalidEvent     = errors.New("invalid session event")
	ErrSequence         = errors.New("non-contiguous session sequence")
	ErrSubscriptionSize = errors.New("invalid subscription capacity")
)

func NewSnapshot() Snapshot {
	return Snapshot{
		Schema:      SnapshotSchemaV1,
		EventSchema: EventSchemaV1,
		State:       StatePending,
	}
}

// Clone returns a deep copy safe for use by another goroutine.
func (s Snapshot) Clone() Snapshot {
	out := s
	if s.Run != nil {
		v := *s.Run
		out.Run = &v
	}
	out.Phases = append([]PhaseSnapshot(nil), s.Phases...)
	out.Metrics = make([]MetricSeries, len(s.Metrics))
	for i, series := range s.Metrics {
		out.Metrics[i] = series
		out.Metrics[i].Samples = append([]MetricObservation(nil), series.Samples...)
	}
	out.Notices = append([]NoticeRecord(nil), s.Notices...)
	if s.Completion != nil {
		v := *s.Completion
		out.Completion = &v
	}
	if s.Failure != nil {
		v := *s.Failure
		out.Failure = &v
	}
	if s.Cancellation != nil {
		v := *s.Cancellation
		out.Cancellation = &v
	}
	return out
}

// Replay validates and reduces a complete contiguous event sequence.
func Replay(events []Event) (Snapshot, error) {
	snapshot := NewSnapshot()
	var err error
	for i, event := range events {
		snapshot, err = ApplyEvent(snapshot, event)
		if err != nil {
			return NewSnapshot(), fmt.Errorf("event %d: %w", i+1, err)
		}
	}
	return snapshot, nil
}

// ApplyEvent returns a new snapshot and never mutates current.
func ApplyEvent(current Snapshot, event Event) (Snapshot, error) {
	if current.Schema == "" {
		current = NewSnapshot()
	}
	if current.Schema != SnapshotSchemaV1 || current.EventSchema != EventSchemaV1 {
		return current, fmt.Errorf("%w: unsupported snapshot schema", ErrInvalidEvent)
	}
	if event.Schema != EventSchemaV1 {
		return current, fmt.Errorf("%w: unsupported event schema %q", ErrInvalidEvent, event.Schema)
	}
	if !tokenPattern.MatchString(event.RunID) {
		return current, fmt.Errorf("%w: invalid run id", ErrInvalidEvent)
	}
	if event.Sequence != current.LastSequence+1 {
		return current, fmt.Errorf("%w: got %d, want %d", ErrSequence, event.Sequence, current.LastSequence+1)
	}
	if event.At.IsZero() || event.ElapsedMillis < 0 {
		return current, fmt.Errorf("%w: invalid event time", ErrInvalidEvent)
	}
	if current.LastSequence > 0 {
		if event.RunID != current.RunID {
			return current, fmt.Errorf("%w: run id changed", ErrInvalidEvent)
		}
		if event.At.Before(current.UpdatedAt) || event.ElapsedMillis < current.ElapsedMillis {
			return current, fmt.Errorf("%w: event time moved backwards", ErrInvalidEvent)
		}
	}
	if isTerminal(current.State) {
		return current, ErrTerminal
	}

	event = canonicalEvent(event)
	if err := validatePayload(event); err != nil {
		return current, err
	}
	next := current.Clone()
	if event.Kind != KindRunStarted && next.State == StatePending {
		return current, ErrNotStarted
	}

	switch event.Kind {
	case KindRunStarted:
		if next.State != StatePending || next.LastSequence != 0 {
			return current, ErrAlreadyStarted
		}
		if event.ElapsedMillis != 0 || event.Run.Model == "" || event.Run.NumCtx < 0 || event.Run.Repeats < 0 {
			return current, fmt.Errorf("%w: invalid run start", ErrInvalidEvent)
		}
		next.RunID = event.RunID
		next.State = StateRunning
		next.StartedAt = event.At
		next.Run = cloneRun(event.Run)
	case KindPhaseStarted:
		if activePhase(next.Phases) >= 0 {
			return current, ErrPhaseActive
		}
		if event.Phase.Name == "" || event.Phase.Total < 0 || phaseIndex(next.Phases, event.Phase.Name) >= 0 {
			return current, fmt.Errorf("%w: invalid or duplicate phase", ErrInvalidEvent)
		}
		next.Phases = append(next.Phases, PhaseSnapshot{
			Name: event.Phase.Name, Detail: event.Phase.Detail, State: PhaseRunning,
			Total: event.Phase.Total, StartedSequence: event.Sequence,
			StartedElapsedMillis: event.ElapsedMillis,
		})
	case KindPhaseProgress:
		i := activePhase(next.Phases)
		if i < 0 || next.Phases[i].Name != event.Progress.Phase {
			return current, ErrPhaseNotActive
		}
		phase := &next.Phases[i]
		if event.Progress.Total <= 0 || event.Progress.Completed < phase.Completed ||
			event.Progress.Completed > event.Progress.Total || (phase.Total > 0 && phase.Total != event.Progress.Total) {
			return current, fmt.Errorf("%w: invalid phase progress", ErrInvalidEvent)
		}
		phase.Completed = event.Progress.Completed
		phase.Total = event.Progress.Total
		if event.Progress.Detail != "" {
			phase.Detail = event.Progress.Detail
		}
	case KindMetricSample:
		i := activePhase(next.Phases)
		if i < 0 || next.Phases[i].Name != event.Metric.Phase {
			return current, ErrPhaseNotActive
		}
		if err := validateMetric(*event.Metric); err != nil {
			return current, err
		}
		appendMetric(&next, event)
	case KindNotice:
		if event.Notice.Level != NoticeInfo && event.Notice.Level != NoticeWarning {
			return current, fmt.Errorf("%w: invalid notice level", ErrInvalidEvent)
		}
		if event.Notice.Code != "" && !tokenPattern.MatchString(event.Notice.Code) {
			return current, fmt.Errorf("%w: invalid notice code", ErrInvalidEvent)
		}
		if event.Notice.Message == "" {
			return current, fmt.Errorf("%w: empty notice", ErrInvalidEvent)
		}
		next.Notices = append(next.Notices, NoticeRecord{
			Sequence: event.Sequence, ElapsedMillis: event.ElapsedMillis, Notice: *event.Notice,
		})
		if len(next.Notices) > MaxNotices {
			drop := len(next.Notices) - MaxNotices
			next.Notices = append([]NoticeRecord(nil), next.Notices[drop:]...)
			next.NoticesDropped += uint64(drop)
		}
	case KindPhaseCompleted:
		i := activePhase(next.Phases)
		if i < 0 || next.Phases[i].Name != event.Phase.Name {
			return current, ErrPhaseNotActive
		}
		phase := &next.Phases[i]
		phase.State = PhaseCompleted
		phase.EndedSequence = event.Sequence
		phase.EndedElapsedMillis = event.ElapsedMillis
		if phase.Total > 0 {
			phase.Completed = phase.Total
		}
	case KindRunCompleted:
		if activePhase(next.Phases) >= 0 {
			return current, ErrPhaseActive
		}
		if err := validateCompletion(*event.Completion); err != nil {
			return current, err
		}
		next.State = StateCompleted
		next.FinishedAt = event.At
		v := *event.Completion
		next.Completion = &v
	case KindRunFailed:
		if event.Failure.Code == "" || !tokenPattern.MatchString(event.Failure.Code) ||
			event.Failure.Summary == "" || event.Failure.ExitCode != 1 {
			return current, fmt.Errorf("%w: invalid failure", ErrInvalidEvent)
		}
		finishActivePhase(&next, event, PhaseFailed)
		next.State = StateFailed
		next.FinishedAt = event.At
		v := *event.Failure
		next.Failure = &v
	case KindRunCancelled:
		if !validCancelReason(event.Cancellation.Reason) || event.Cancellation.ExitCode != 130 {
			return current, fmt.Errorf("%w: invalid cancellation", ErrInvalidEvent)
		}
		finishActivePhase(&next, event, PhaseCancelled)
		next.State = StateCancelled
		next.FinishedAt = event.At
		v := *event.Cancellation
		next.Cancellation = &v
	default:
		return current, fmt.Errorf("%w: unknown kind %q", ErrInvalidEvent, event.Kind)
	}

	next.LastSequence = event.Sequence
	next.UpdatedAt = event.At
	next.ElapsedMillis = event.ElapsedMillis
	return next, nil
}

func canonicalEvent(event Event) Event {
	if event.Run != nil {
		v := *event.Run
		v.Model = safeText(v.Model, 256)
		v.ParamSize = safeText(v.ParamSize, 64)
		v.Quant = safeText(v.Quant, 64)
		v.Family = safeText(v.Family, 128)
		v.GPU = safeText(v.GPU, 256)
		v.Driver = safeText(v.Driver, 128)
		v.Backend = safeText(v.Backend, 64)
		v.Runtime = safeText(v.Runtime, 128)
		v.Profile = safeText(v.Profile, 128)
		v.Level = safeText(v.Level, 64)
		event.Run = &v
	}
	if event.Phase != nil {
		v := *event.Phase
		v.Name = safeText(v.Name, 64)
		v.Detail = safeText(v.Detail, 512)
		event.Phase = &v
	}
	if event.Progress != nil {
		v := *event.Progress
		v.Phase = safeText(v.Phase, 64)
		v.Detail = safeText(v.Detail, 512)
		event.Progress = &v
	}
	if event.Metric != nil {
		v := *event.Metric
		v.Phase = safeText(v.Phase, 64)
		event.Metric = &v
	}
	if event.Notice != nil {
		v := *event.Notice
		v.Code = safeText(v.Code, 64)
		v.Message = safeText(v.Message, 1024)
		v.Remedy = safeText(v.Remedy, 1024)
		event.Notice = &v
	}
	if event.Failure != nil {
		v := *event.Failure
		v.Code = safeText(v.Code, 64)
		v.Summary = safeText(v.Summary, 1024)
		v.Remedy = safeText(v.Remedy, 1024)
		event.Failure = &v
	}
	if event.Cancellation != nil {
		v := *event.Cancellation
		v.Message = safeText(v.Message, 1024)
		event.Cancellation = &v
	}
	return event
}

func validatePayload(event Event) error {
	count := 0
	for _, present := range []bool{
		event.Run != nil, event.Phase != nil, event.Progress != nil, event.Metric != nil,
		event.Notice != nil, event.Completion != nil, event.Failure != nil, event.Cancellation != nil,
	} {
		if present {
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("%w: event must carry exactly one payload", ErrInvalidEvent)
	}
	want := map[Kind]bool{
		KindRunStarted:   event.Run != nil,
		KindPhaseStarted: event.Phase != nil, KindPhaseCompleted: event.Phase != nil,
		KindPhaseProgress: event.Progress != nil, KindMetricSample: event.Metric != nil,
		KindNotice: event.Notice != nil, KindRunCompleted: event.Completion != nil,
		KindRunFailed: event.Failure != nil, KindRunCancelled: event.Cancellation != nil,
	}
	if !want[event.Kind] {
		return fmt.Errorf("%w: payload does not match kind %q", ErrInvalidEvent, event.Kind)
	}
	return nil
}

func validateMetric(sample MetricSample) error {
	if sample.Phase == "" || sample.Metric.Unit() == "" ||
		math.IsNaN(sample.Value) || math.IsInf(sample.Value, 0) || sample.Value < 0 ||
		sample.Sample < 0 || sample.Total < 0 || sample.Sample > sample.Total && sample.Total > 0 ||
		(sample.Sample == 0) != (sample.Total == 0) {
		return fmt.Errorf("%w: invalid metric sample", ErrInvalidEvent)
	}
	if sample.Source != SourceServer && sample.Source != SourceClient && sample.Source != SourceDevice {
		return fmt.Errorf("%w: invalid metric source", ErrInvalidEvent)
	}
	return nil
}

func validateCompletion(completion Completion) error {
	if completion.Passes < 0 || completion.Fails < 0 || completion.Unproven < 0 {
		return fmt.Errorf("%w: invalid completion counts", ErrInvalidEvent)
	}
	want := 0
	if completion.Fails > 0 {
		want = 3
	}
	if completion.ExitCode != want {
		return fmt.Errorf("%w: completion exit code must be %d", ErrInvalidEvent, want)
	}
	return nil
}

func appendMetric(snapshot *Snapshot, event Event) {
	i := -1
	for j := range snapshot.Metrics {
		if snapshot.Metrics[j].Metric == event.Metric.Metric {
			i = j
			break
		}
	}
	if i < 0 {
		snapshot.Metrics = append(snapshot.Metrics, MetricSeries{Metric: event.Metric.Metric})
		i = len(snapshot.Metrics) - 1
	}
	series := &snapshot.Metrics[i]
	series.Samples = append(series.Samples, MetricObservation{
		Sequence: event.Sequence, ElapsedMillis: event.ElapsedMillis, Sample: *event.Metric,
	})
	if len(series.Samples) > MaxMetricSamplesPerSeries {
		drop := len(series.Samples) - MaxMetricSamplesPerSeries
		series.Samples = append([]MetricObservation(nil), series.Samples[drop:]...)
		series.Dropped += uint64(drop)
	}
}

func finishActivePhase(snapshot *Snapshot, event Event, state PhaseState) {
	if i := activePhase(snapshot.Phases); i >= 0 {
		snapshot.Phases[i].State = state
		snapshot.Phases[i].EndedSequence = event.Sequence
		snapshot.Phases[i].EndedElapsedMillis = event.ElapsedMillis
	}
}

func activePhase(phases []PhaseSnapshot) int {
	for i := range phases {
		if phases[i].State == PhaseRunning {
			return i
		}
	}
	return -1
}

func phaseIndex(phases []PhaseSnapshot, name string) int {
	for i := range phases {
		if phases[i].Name == name {
			return i
		}
	}
	return -1
}

func validCancelReason(reason CancelReason) bool {
	return reason == CancelUser || reason == CancelInterrupt || reason == CancelContext
}

func isTerminal(state RunState) bool {
	return state == StateCompleted || state == StateFailed || state == StateCancelled
}

func cloneRun(run *RunInfo) *RunInfo {
	if run == nil {
		return nil
	}
	v := *run
	return &v
}
