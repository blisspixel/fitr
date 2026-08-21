// Package session defines the versioned, privacy-safe presentation event
// contract shared by interactive and replayable fitr interfaces.
package session

import "time"

const (
	// EventSchemaV1 is the wire contract for one presentation event.
	EventSchemaV1 = "fitr.presentation.event.v1"
	// SnapshotSchemaV1 is the wire contract for reduced presentation state.
	SnapshotSchemaV1 = "fitr.presentation.session.v1"

	DefaultSubscriptionCapacity = 16
	MaxSubscriptionCapacity     = 1024
	MaxMetricSamplesPerSeries   = 256
	MaxNotices                  = 128
)

// Kind identifies the one payload carried by an Event.
type Kind string

const (
	KindRunStarted     Kind = "run_started"
	KindPhaseStarted   Kind = "phase_started"
	KindPhaseProgress  Kind = "phase_progress"
	KindMetricSample   Kind = "metric_sample"
	KindNotice         Kind = "notice"
	KindPhaseCompleted Kind = "phase_completed"
	KindRunCompleted   Kind = "run_completed"
	KindRunFailed      Kind = "run_failed"
	KindRunCancelled   Kind = "run_cancelled"
)

// RunState is the mutually exclusive lifecycle state of one measurement.
type RunState string

const (
	StatePending   RunState = "pending"
	StateRunning   RunState = "running"
	StateCompleted RunState = "completed"
	StateFailed    RunState = "failed"
	StateCancelled RunState = "cancelled"
)

// PhaseState is the lifecycle state of one sequential measurement phase.
type PhaseState string

const (
	PhasePending   PhaseState = "pending"
	PhaseRunning   PhaseState = "running"
	PhaseCompleted PhaseState = "completed"
	PhaseFailed    PhaseState = "failed"
	PhaseCancelled PhaseState = "cancelled"
)

// NoticeLevel controls semantic emphasis. Color is a renderer concern.
type NoticeLevel string

const (
	NoticeInfo    NoticeLevel = "info"
	NoticeWarning NoticeLevel = "warning"
)

// Metric identifies observations that are safe to present. It deliberately
// has no arbitrary name or unit fields.
type Metric string

const (
	MetricDecodeTPS   Metric = "decode_tps"
	MetricPrefillTPS  Metric = "prefill_tps"
	MetricTTFTSeconds Metric = "ttft_seconds"
	MetricResidentGiB Metric = "resident_gib"
)

func (m Metric) Unit() string {
	switch m {
	case MetricDecodeTPS, MetricPrefillTPS:
		return "tok/s"
	case MetricTTFTSeconds:
		return "s"
	case MetricResidentGiB:
		return "GiB"
	default:
		return ""
	}
}

// MetricSource discloses where an observation came from.
type MetricSource string

const (
	SourceServer MetricSource = "server"
	SourceClient MetricSource = "client"
	SourceDevice MetricSource = "device"
)

// CancelReason distinguishes user intent from context propagation.
type CancelReason string

const (
	CancelUser      CancelReason = "user"
	CancelInterrupt CancelReason = "interrupt"
	CancelContext   CancelReason = "context"
)

// RunInfo is intentionally allowlisted. It contains no hostname, endpoint,
// local path, environment dump, prompt, response, or computed answer.
type RunInfo struct {
	Model     string `json:"model"`
	ParamSize string `json:"param_size,omitempty"`
	Quant     string `json:"quant,omitempty"`
	Family    string `json:"family,omitempty"`
	GPU       string `json:"gpu,omitempty"`
	Driver    string `json:"driver,omitempty"`
	Backend   string `json:"backend,omitempty"`
	Runtime   string `json:"runtime,omitempty"`
	Profile   string `json:"profile,omitempty"`
	Level     string `json:"level,omitempty"`
	NumCtx    int    `json:"num_ctx,omitempty"`
	Repeats   int    `json:"repeats,omitempty"`
}

type PhaseEvent struct {
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
	Total  int    `json:"total,omitempty"`
}

type Progress struct {
	Phase     string `json:"phase"`
	Completed int    `json:"completed"`
	Total     int    `json:"total"`
	Detail    string `json:"detail,omitempty"`
}

type MetricSample struct {
	Phase  string       `json:"phase"`
	Metric Metric       `json:"metric"`
	Value  float64      `json:"value"`
	Sample int          `json:"sample,omitempty"`
	Total  int          `json:"total,omitempty"`
	Source MetricSource `json:"source"`
	Cached bool         `json:"cached,omitempty"`
}

type Notice struct {
	Level   NoticeLevel `json:"level"`
	Code    string      `json:"code,omitempty"`
	Message string      `json:"message"`
	Remedy  string      `json:"remedy,omitempty"`
}

type Completion struct {
	Passes   int  `json:"passes"`
	Fails    int  `json:"fails"`
	Unproven int  `json:"unproven"`
	Saved    bool `json:"saved"`
	ExitCode int  `json:"exit_code"`
}

type Failure struct {
	Code     string `json:"code"`
	Summary  string `json:"summary"`
	Remedy   string `json:"remedy,omitempty"`
	ExitCode int    `json:"exit_code"`
}

type Cancellation struct {
	Reason   CancelReason `json:"reason"`
	Message  string       `json:"message,omitempty"`
	ExitCode int          `json:"exit_code"`
}

// Event has exactly one payload selected by Kind. Free-form model output is
// not part of this schema.
type Event struct {
	Schema        string        `json:"schema"`
	RunID         string        `json:"run_id"`
	Sequence      uint64        `json:"sequence"`
	At            time.Time     `json:"at"`
	ElapsedMillis int64         `json:"elapsed_ms"`
	Kind          Kind          `json:"kind"`
	Run           *RunInfo      `json:"run,omitempty"`
	Phase         *PhaseEvent   `json:"phase,omitempty"`
	Progress      *Progress     `json:"progress,omitempty"`
	Metric        *MetricSample `json:"metric,omitempty"`
	Notice        *Notice       `json:"notice,omitempty"`
	Completion    *Completion   `json:"completion,omitempty"`
	Failure       *Failure      `json:"failure,omitempty"`
	Cancellation  *Cancellation `json:"cancellation,omitempty"`
}

// Clone returns a deep copy safe to hand to another goroutine.
func (e Event) Clone() Event {
	out := e
	if e.Run != nil {
		v := *e.Run
		out.Run = &v
	}
	if e.Phase != nil {
		v := *e.Phase
		out.Phase = &v
	}
	if e.Progress != nil {
		v := *e.Progress
		out.Progress = &v
	}
	if e.Metric != nil {
		v := *e.Metric
		out.Metric = &v
	}
	if e.Notice != nil {
		v := *e.Notice
		out.Notice = &v
	}
	if e.Completion != nil {
		v := *e.Completion
		out.Completion = &v
	}
	if e.Failure != nil {
		v := *e.Failure
		out.Failure = &v
	}
	if e.Cancellation != nil {
		v := *e.Cancellation
		out.Cancellation = &v
	}
	return out
}

type PhaseSnapshot struct {
	Name                 string     `json:"name"`
	Detail               string     `json:"detail,omitempty"`
	State                PhaseState `json:"state"`
	Completed            int        `json:"completed,omitempty"`
	Total                int        `json:"total,omitempty"`
	StartedSequence      uint64     `json:"started_sequence"`
	EndedSequence        uint64     `json:"ended_sequence,omitempty"`
	StartedElapsedMillis int64      `json:"started_elapsed_ms"`
	EndedElapsedMillis   int64      `json:"ended_elapsed_ms,omitempty"`
}

type MetricObservation struct {
	Sequence      uint64       `json:"sequence"`
	ElapsedMillis int64        `json:"elapsed_ms"`
	Sample        MetricSample `json:"sample"`
}

type MetricSeries struct {
	Metric  Metric              `json:"metric"`
	Samples []MetricObservation `json:"samples"`
	Dropped uint64              `json:"dropped,omitempty"`
}

type NoticeRecord struct {
	Sequence      uint64 `json:"sequence"`
	ElapsedMillis int64  `json:"elapsed_ms"`
	Notice        Notice `json:"notice"`
}

// Snapshot is a read-only presentation state. Slices are copied at API
// boundaries so a consumer cannot race with the measurement producer.
type Snapshot struct {
	Schema         string          `json:"schema"`
	EventSchema    string          `json:"event_schema"`
	RunID          string          `json:"run_id,omitempty"`
	State          RunState        `json:"state"`
	LastSequence   uint64          `json:"last_sequence"`
	StartedAt      time.Time       `json:"started_at,omitempty"`
	UpdatedAt      time.Time       `json:"updated_at,omitempty"`
	FinishedAt     time.Time       `json:"finished_at,omitempty"`
	ElapsedMillis  int64           `json:"elapsed_ms"`
	Run            *RunInfo        `json:"run,omitempty"`
	Phases         []PhaseSnapshot `json:"phases,omitempty"`
	Metrics        []MetricSeries  `json:"metrics,omitempty"`
	Notices        []NoticeRecord  `json:"notices,omitempty"`
	NoticesDropped uint64          `json:"notices_dropped,omitempty"`
	Completion     *Completion     `json:"completion,omitempty"`
	Failure        *Failure        `json:"failure,omitempty"`
	Cancellation   *Cancellation   `json:"cancellation,omitempty"`
}

// Update always carries a complete snapshot. A slow subscriber can therefore
// recover immediately even when intermediate events were evicted.
type Update struct {
	Event    *Event   `json:"event,omitempty"`
	Snapshot Snapshot `json:"snapshot"`
	Dropped  uint64   `json:"dropped,omitempty"`
}
