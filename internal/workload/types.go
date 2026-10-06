// Package workload runs bounded, independently verified workflow experiments.
// The first workflow uses a harness-owned virtual filesystem and fixed tools;
// it never grants a model shell access or authority over its verifier.
package workload

import "github.com/blisspixel/fitr/internal/record"

const (
	PlanSchema         = "fitr.workload.plan.v2"
	LegacyPlanSchema   = "fitr.workload.plan.v1"
	TrialSchema        = "fitr.workload.trial.v1"
	ReportSchema       = "fitr.workload.analysis.v2"
	LegacyReportSchema = "fitr.workload.analysis.v1"
	BundleSchema       = "fitr.workload.bundle.v1"
	WorkflowID         = "policy-repair"
	PiWorkflowID       = "pi-workspace"
	WorkflowVersion    = 1

	// Checked against packages/coding-agent/package.json and
	// docs/compaction.md at commit d981de1229ef899957bbe968bc8dcda02a21f477,
	// published as @earendil-works/pi-coding-agent 0.85.1. A split turn
	// generates two summaries. fitr seals that schedule; it does not launch Pi.
	PiDriverPackage = "@earendil-works/pi-coding-agent"
	PiDriverCommit  = "d981de1229ef899957bbe968bc8dcda02a21f477"
	PiDriverRelease = "0.85.1"
	PiDriverAdapter = "fitr.pi-session.v1"
	PiProviderLocal = "local"
	PiProviderFake  = "fake"
)

type RetentionPolicy string

const RetainHashesAndVerifier RetentionPolicy = "hashes_and_verifier"

type Plan struct {
	Schema           string               `json:"schema"`
	PlanSHA256       string               `json:"plan_sha256"`
	CompletionKey    string               `json:"completion_public_key"`
	Workflow         string               `json:"workflow"`
	WorkflowVersion  int                  `json:"workflow_version"`
	Model            record.ModelIdentity `json:"model"`
	DeviceKey        string               `json:"device_key"`
	Trials           int                  `json:"trials"`
	MaxTurns         int                  `json:"max_turns"`
	TimeoutSeconds   int                  `json:"timeout_seconds"`
	RequestedContext int                  `json:"requested_context"`
	MaxAttempts      int                  `json:"max_attempts,omitempty"`
	Retention        RetentionPolicy      `json:"retention"`
	Contract         *WorkflowContract    `json:"contract,omitempty"`
	// Pi-workspace fields stay empty on every other workflow so older plans
	// keep their exact bytes.
	Driver        *SessionDriver   `json:"driver,omitempty"`
	Provider      string           `json:"provider,omitempty"`
	RequestBudget int              `json:"request_budget,omitempty"`
	PiSchedule    *SessionSchedule `json:"pi_schedule,omitempty"`
}

// SessionDriver is the harness identity a pi-workspace plan seals before any
// model request. Adapter is fitr's session, not a claim that the Pi process ran.
type SessionDriver struct {
	Package string `json:"package"`
	Commit  string `json:"commit"`
	Release string `json:"release"`
	Adapter string `json:"adapter"`
}

// SessionSchedule is the forced split compaction. OrdinaryCap is a ceiling.
// Summary calls and the reopen turn are spent on every accepted trial.
type SessionSchedule struct {
	OrdinaryCap  int `json:"ordinary_cap"`
	SummaryCalls int `json:"summary_calls"`
	ReopenTurns  int `json:"reopen_turns"`
}

type EventType string

const (
	EventScenarioReleased    EventType = "scenario_released"
	EventWorkerStarted       EventType = "worker_started"
	EventModelStarted        EventType = "model_request_started"
	EventModelCompleted      EventType = "model_request_completed"
	EventToolStarted         EventType = "tool_started"
	EventToolCompleted       EventType = "tool_completed"
	EventApprovalRequested   EventType = "approval_requested"
	EventApprovalGranted     EventType = "approval_granted"
	EventApprovalDenied      EventType = "approval_denied"
	EventEscalationRequested EventType = "escalation_requested"
	EventEscalationCompleted EventType = "escalation_completed"
	EventHumanWaitStarted    EventType = "human_wait_started"
	EventHumanWaitCompleted  EventType = "human_wait_completed"
	EventCompactionStarted   EventType = "compaction_started"
	EventCompactionCompleted EventType = "compaction_completed"
	EventCheckpointResumed   EventType = "checkpoint_resumed"
	EventRetry               EventType = "retry"
	EventWorkerCompleted     EventType = "worker_completed"
	EventVerifierQueued      EventType = "verifier_queued"
	EventVerifierStarted     EventType = "verifier_started"
	EventVerifierCompleted   EventType = "verifier_completed"
	EventAccepted            EventType = "accepted"
	EventRejected            EventType = "rejected"
	EventTimedOut            EventType = "timed_out"
	EventInfrastructure      EventType = "infrastructure_fault"
)

type Event struct {
	Sequence       int       `json:"sequence"`
	ElapsedMillis  int64     `json:"elapsed_ms"`
	Type           EventType `json:"type"`
	Actor          string    `json:"actor"`
	Attempt        int       `json:"attempt"`
	Tool           string    `json:"tool,omitempty"`
	Status         string    `json:"status,omitempty"`
	EvidenceSHA256 string    `json:"evidence_sha256,omitempty"`
	// Class distinguishes a split-compaction summary from an ordinary turn.
	// Empty on every existing event, so signed trials keep their bytes.
	Class string `json:"class,omitempty"`
}

type Outcome string

const (
	OutcomeAccepted       Outcome = "accepted"
	OutcomeRejected       Outcome = "rejected"
	OutcomeTimedOut       Outcome = "timed_out"
	OutcomeInfrastructure Outcome = "infrastructure_fault"
)

type VerificationCheck struct {
	Code   string `json:"code"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

type VerifierReceipt struct {
	EvidenceClass        EvidenceClass       `json:"evidence_class"`
	PolicySHA256         string              `json:"policy_sha256"`
	ProtectedStateSHA256 string              `json:"protected_state_sha256"`
	Checks               []VerificationCheck `json:"checks"`
	Accepted             bool                `json:"accepted"`
	CheckpointSHA256     string              `json:"checkpoint_sha256,omitempty"`
	// Summary digests are the observation behind the summaries check.
	// Empty on policy-repair receipts, so those signed bytes stay stable.
	SummarySHA256 []string `json:"summary_sha256,omitempty"`
}

type Trial struct {
	Schema              string          `json:"schema"`
	PlanSHA256          string          `json:"plan_sha256"`
	TrialID             string          `json:"trial_id"`
	Index               int             `json:"index"`
	Events              []Event         `json:"events"`
	Outcome             Outcome         `json:"outcome"`
	ElapsedMillis       int64           `json:"elapsed_ms"`
	Attempts            int             `json:"attempts"`
	Turns               int             `json:"turns"`
	ToolCalls           int             `json:"tool_calls"`
	DuplicateCalls      int             `json:"duplicate_calls"`
	AuthorityViolations int             `json:"authority_violations"`
	Verifier            VerifierReceipt `json:"verifier"`
	SummaryCalls        int             `json:"summary_calls,omitempty"`
	Effects             int             `json:"effects,omitempty"`
	EvidenceSHA256      string          `json:"evidence_sha256"`
	Signature           string          `json:"signature"`
}

type OutcomeCounts struct {
	Planned             int `json:"planned"`
	Accepted            int `json:"accepted"`
	Rejected            int `json:"rejected"`
	TimedOut            int `json:"timed_out"`
	InfrastructureFault int `json:"infrastructure_fault"`
}

type RateObservation struct {
	Estimate *float64 `json:"estimate,omitempty"`
	Unit     string   `json:"unit"`
	Status   string   `json:"status"`
	Reason   string   `json:"reason"`
}

type Report struct {
	Schema                  string          `json:"schema"`
	PlanSHA256              string          `json:"plan_sha256"`
	Workflow                string          `json:"workflow"`
	Counts                  OutcomeCounts   `json:"counts"`
	MedianAcceptedMillis    *float64        `json:"median_accepted_ms,omitempty"`
	AcceptedOutcomesPerHour RateObservation `json:"accepted_outcomes_per_hour"`
	Coverage                string          `json:"coverage"`
	Gaps                    []string        `json:"gaps,omitempty"`
	TrialAnalysis           []TrialAnalysis `json:"trial_analysis,omitempty"`
}

type Bundle struct {
	Schema string  `json:"schema"`
	Plan   Plan    `json:"plan"`
	Trials []Trial `json:"trials"`
	Report Report  `json:"report"`
}
