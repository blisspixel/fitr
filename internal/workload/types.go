// Package workload runs bounded, independently verified workflow experiments.
// The first workflow uses a harness-owned virtual filesystem and fixed tools;
// it never grants a model shell access or authority over its verifier.
package workload

import "github.com/blisspixel/fitr/internal/record"

const (
	PlanSchema      = "fitr.workload.plan.v1"
	TrialSchema     = "fitr.workload.trial.v1"
	ReportSchema    = "fitr.workload.analysis.v1"
	BundleSchema    = "fitr.workload.bundle.v1"
	WorkflowID      = "policy-repair"
	WorkflowVersion = 1
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
	Retention        RetentionPolicy      `json:"retention"`
}

type EventType string

const (
	EventScenarioReleased  EventType = "scenario_released"
	EventWorkerStarted     EventType = "worker_started"
	EventModelStarted      EventType = "model_request_started"
	EventModelCompleted    EventType = "model_request_completed"
	EventToolStarted       EventType = "tool_started"
	EventToolCompleted     EventType = "tool_completed"
	EventWorkerCompleted   EventType = "worker_completed"
	EventVerifierQueued    EventType = "verifier_queued"
	EventVerifierStarted   EventType = "verifier_started"
	EventVerifierCompleted EventType = "verifier_completed"
	EventAccepted          EventType = "accepted"
	EventRejected          EventType = "rejected"
	EventTimedOut          EventType = "timed_out"
	EventInfrastructure    EventType = "infrastructure_fault"
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
	EvidenceClass        string              `json:"evidence_class"`
	PolicySHA256         string              `json:"policy_sha256"`
	ProtectedStateSHA256 string              `json:"protected_state_sha256"`
	Checks               []VerificationCheck `json:"checks"`
	Accepted             bool                `json:"accepted"`
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
}

type Bundle struct {
	Schema string  `json:"schema"`
	Plan   Plan    `json:"plan"`
	Trials []Trial `json:"trials"`
	Report Report  `json:"report"`
}
