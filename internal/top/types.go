// Package top contains the renderer-neutral state machine and terminal adapter
// for fitr's full-screen data interface.
package top

import "time"

// View identifies one of the four top-level screens.
type View uint8

const (
	ViewLive View = iota
	ViewResult
	ViewBoard
	ViewHistory
	ViewInventory
	viewCount
)

func (v View) String() string {
	switch v {
	case ViewLive:
		return "live"
	case ViewResult:
		return "result"
	case ViewBoard:
		return "board"
	case ViewHistory:
		return "history"
	case ViewInventory:
		return "inventory"
	default:
		return "unknown"
	}
}

// Verdict is one user-facing need result. State is expected to be PASS, FAIL,
// SKIP, n/a, BLKD, or INCONCLUSIVE, but remains a string so presentation is
// not coupled to the scoring package.
type Verdict struct {
	Need  string `json:"need"`
	Label string `json:"label"`
	State string `json:"state"`
	Why   string `json:"why"`
}

// Run is the compact, UI-neutral representation of one saved measurement.
// ID must be stable across reloads. The full saved artifact stays outside this
// package and can be loaded only when a detail view needs it.
type Run struct {
	ID           string        `json:"id"`
	Model        string        `json:"model"`
	Family       string        `json:"family"`
	ParamSize    string        `json:"param_size"`
	Quant        string        `json:"quant"`
	DeviceID     string        `json:"device_id"`
	HardwareID   string        `json:"hardware_id"`
	Device       string        `json:"device"`
	Driver       string        `json:"driver"`
	Runtime      string        `json:"runtime"`
	Config       string        `json:"config"`
	Profile      string        `json:"profile"`
	Level        string        `json:"level"`
	UseFor       string        `json:"use_for"`
	StartedAt    time.Time     `json:"started_at"`
	Duration     time.Duration `json:"duration"`
	Context      int           `json:"context"`
	Repeats      int           `json:"repeats"`
	DecodeMean   float64       `json:"decode_mean"`
	DecodeSD     float64       `json:"decode_sd"`
	PrefillMean  float64       `json:"prefill_mean"`
	TTFTMean     float64       `json:"ttft_mean"`
	MemoryGB     float64       `json:"memory_gb"`
	DecodeSeries []float64     `json:"decode_series"`
	Serves       []string      `json:"serves"`
	Warnings     []string      `json:"warnings"`
	Verdicts     []Verdict     `json:"verdicts"`
	NextCommand  string        `json:"next_command"`
}

// Comparison is an exact-value preview between two saved runs. It never
// claims a statistical winner. The ordinary compare command remains the
// source for confidence intervals and paired tests.
type Comparison struct {
	BaselineID, SelectedID       string
	BaselineModel, SelectedModel string
	Compatible                   bool
	Reason                       string
	DecodeA, DecodeB, DecodeDiff float64
	PrefillA, PrefillB           float64
	MemoryA, MemoryB             float64
}

// BoardGroup contains only mutually comparable runs. The application never
// sorts rows across group boundaries.
type BoardGroup struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Note       string `json:"note"`
	Comparable bool   `json:"comparable"`
	Runs       []Run  `json:"runs"`
}

// LivePhase is one completed or active measurement phase. State is a semantic
// token such as running, completed, failed, or cancelled.
type LivePhase struct {
	Name      string `json:"name"`
	Detail    string `json:"detail"`
	State     string `json:"state"`
	Completed int    `json:"completed"`
	Total     int    `json:"total"`
}

// Live is the latest structured state of a running evaluation.
type Live struct {
	Active         bool        `json:"active"`
	Completed      bool        `json:"completed"`
	Cancelled      bool        `json:"cancelled"`
	Saved          bool        `json:"saved"`
	RunID          string      `json:"run_id"`
	Sequence       uint64      `json:"sequence"`
	Model          string      `json:"model"`
	Phase          string      `json:"phase"`
	Detail         string      `json:"detail"`
	Placement      string      `json:"placement"`
	Error          string      `json:"error"`
	StartedAt      time.Time   `json:"started_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
	Decode         float64     `json:"decode"`
	Prefill        float64     `json:"prefill"`
	TTFT           float64     `json:"ttft"`
	MemoryGB       float64     `json:"memory_gb"`
	DecodeSeries   []float64   `json:"decode_series"`
	PrefillSeries  []float64   `json:"prefill_series"`
	TTFTSeries     []float64   `json:"ttft_series"`
	Warnings       []string    `json:"warnings"`
	Phases         []LivePhase `json:"phases"`
	CompletedSteps int         `json:"completed_steps"`
	TotalSteps     int         `json:"total_steps"`
	Repeats        int         `json:"repeats"`
}

// InventoryItem is one installed model on the TUI inventory view.
// State and Fit are plain text. Color never carries them alone.
type InventoryItem struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	State   string `json:"state"`
	Fit     string `json:"fit,omitempty"`
	SizeB   int64  `json:"size_bytes,omitempty"`
	Loaded  bool   `json:"loaded,omitempty"`
	Next    string `json:"next"`
	Note    string `json:"note,omitempty"`
	Ctx     string `json:"ctx,omitempty"`
	Windows string `json:"windows,omitempty"`
}

// Snapshot is a complete point-in-time presentation snapshot.
type Snapshot struct {
	Generation uint64          `json:"generation"`
	UpdatedAt  time.Time       `json:"updated_at"`
	Live       Live            `json:"live"`
	Board      []BoardGroup    `json:"board"`
	History    []Run           `json:"history"`
	Inventory  []InventoryItem `json:"inventory,omitempty"`
}

// Sort selects the ordering used inside each board group and in history.
type Sort uint8

const (
	SortRecent Sort = iota
	SortModel
	SortDecode
	SortPrefill
	SortMemory
	SortRepeats
	sortCount
)

func (s Sort) String() string {
	switch s {
	case SortRecent:
		return "recent"
	case SortModel:
		return "model"
	case SortDecode:
		return "decode"
	case SortPrefill:
		return "prefill"
	case SortMemory:
		return "memory"
	case SortRepeats:
		return "repeats"
	default:
		return "unknown"
	}
}

// Action is a semantic input. Terminal-specific keys are mapped to these
// actions by the adapter, keeping the reducer deterministic and testable.
type Action uint8

const (
	ActionNone Action = iota
	ActionViewLive
	ActionViewResult
	ActionViewBoard
	ActionViewHistory
	ActionViewInventory
	ActionNextView
	ActionPrevView
	ActionUp
	ActionDown
	ActionPageUp
	ActionPageDown
	ActionHome
	ActionEnd
	ActionOpen
	ActionBack
	ActionFilter
	ActionText
	ActionBackspace
	ActionClearFilter
	ActionSort
	ActionPause
	ActionCompare
	ActionReload
	ActionHelp
	ActionQuit
	ActionConfirm
	ActionReject
	ActionInterrupt
	ActionRedraw
)

// Event is consumed by Update.
type Event interface{ topEvent() }

type SnapshotEvent struct{ Snapshot Snapshot }
type LiveEvent struct{ Live Live }
type TickEvent struct{ Now time.Time }
type ResizeEvent struct{ Width, Height int }
type InputEvent struct {
	Action Action
	Text   string
}
type ErrorEvent struct {
	Err        error
	Generation uint64
}

func (SnapshotEvent) topEvent() {}
func (LiveEvent) topEvent()     {}
func (TickEvent) topEvent()     {}
func (ResizeEvent) topEvent()   {}
func (InputEvent) topEvent()    {}
func (ErrorEvent) topEvent()    {}

// EffectKind identifies work owned by the application boundary.
type EffectKind uint8

const (
	EffectQuit EffectKind = iota + 1
	EffectCancelRun
	EffectReload
	EffectRedraw
)

// Effect is emitted by Update and performed outside the pure state machine.
type Effect struct{ Kind EffectKind }

// State contains all interaction state. Selected and Offset are indexed by
// View so a reload or tab switch does not discard the user's position.
type State struct {
	View             View
	Snapshot         Snapshot
	PendingSnapshot  *Snapshot
	PendingLive      *Live
	Selected         [viewCount]string
	Offset           [viewCount]int
	BoardSort        Sort
	HistorySort      Sort
	Filter           string
	FilterBeforeEdit string
	EditingFilter    bool
	Paused, Help     bool
	BaselineID       string
	Comparison       *Comparison
	ConfirmQuit      bool
	Interrupted      bool
	Width, Height    int
	Now              time.Time
	Error            string
	Revision         uint64
}

// NewState returns a state with practical terminal defaults.
func NewState(snapshot Snapshot) State {
	now := snapshot.UpdatedAt
	if now.IsZero() {
		now = time.Now()
	}
	s := State{View: ViewLive, Snapshot: cloneSnapshot(snapshot), BoardSort: SortDecode, HistorySort: SortRecent,
		Width: 80, Height: 24, Now: now, Revision: 1}
	normalizeSelection(&s)
	return s
}
