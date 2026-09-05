// Package automation persists bounded local experiments before requests leave
// the process. Its seals detect inconsistent edits, not an authorized writer.
package automation

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/blisspixel/fitr/internal/autoruntime"
	"github.com/blisspixel/fitr/internal/modelref"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/role"
)

const (
	PlanSchema          = "fitr.auto.plan.v1"
	JournalSchema       = "fitr.auto.journal.v1"
	MaximumEvents       = 8192
	MaximumJournalBytes = 16 << 20
)

var (
	ErrBudget = errors.New("auto session budget exhausted")
	ErrStale  = errors.New("auto session changed or expired")
	idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,95}$`)
)

type Candidate struct {
	ID                       string `json:"id"`
	Model                    string `json:"model"`
	ArtifactDigest           string `json:"artifact_digest"`
	ModelConfigurationSHA256 string `json:"model_configuration_sha256"`
}

type Limits struct {
	MaxRequests              int64 `json:"max_requests"`
	MaxRequestedOutputTokens int64 `json:"max_requested_output_tokens"`
	MaxPoints                int   `json:"max_points"`
	WallSeconds              int64 `json:"wall_seconds"`
	ConfirmationWallSeconds  int64 `json:"confirmation_wall_seconds"`
}

type Plan struct {
	Schema                     string               `json:"schema"`
	ID                         string               `json:"id"`
	SHA256                     string               `json:"sha256"`
	Mode                       string               `json:"mode"`
	Adoption                   string               `json:"adoption"`
	Spec                       role.Spec            `json:"role"`
	RoleRevision               string               `json:"role_revision"`
	IncumbentSHA256            string               `json:"incumbent_sha256,omitempty"`
	LifecycleSHA256            string               `json:"lifecycle_sha256"`
	Candidates                 []Candidate          `json:"candidates"`
	Runtime                    autoruntime.Spec     `json:"runtime"`
	SoftwareSHA256             string               `json:"software_sha256"`
	TaskSetSHA256              string               `json:"task_set_sha256"`
	SpecSHA256                 string               `json:"spec_sha256"`
	Profile                    string               `json:"profile"`
	Provenance                 record.RunProvenance `json:"provenance"`
	DeviceSHA256               string               `json:"device_sha256"`
	Repeats                    int                  `json:"repeats"`
	SeedSet                    string               `json:"seedset"`
	EnvelopeSHA256             string               `json:"envelope_sha256"`
	PointRequests              int64                `json:"point_requests"`
	PointRequestedOutputTokens int64                `json:"point_requested_output_tokens"`
	Limits                     Limits               `json:"limits"`
	CreatedAt                  string               `json:"created_at"`
	ExpiresAt                  string               `json:"expires_at"`
}

func NewID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return "auto-" + hex.EncodeToString(data[:]), nil
}

func digest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", hash), nil
}

func validDigest(value string) bool {
	if len(value) != 71 || value[:7] != "sha256:" {
		return false
	}
	decoded, err := hex.DecodeString(value[7:])
	return err == nil && hex.EncodeToString(decoded) == value[7:]
}

func (plan *Plan) Seal(now time.Time) error {
	if now.IsZero() {
		return errors.New("auto plan needs a current time")
	}
	plan.Schema = PlanSchema
	plan.CreatedAt = now.UTC().Format(time.RFC3339Nano)
	plan.ExpiresAt = now.Add(time.Duration(plan.Limits.WallSeconds) * time.Second).UTC().Format(time.RFC3339Nano)
	plan.SHA256 = ""
	var err error
	plan.SHA256, err = digest(*plan)
	if err != nil {
		return err
	}
	return plan.Validate()
}

func (plan Plan) Validate() error {
	if plan.Schema != PlanSchema || !idPattern.MatchString(plan.ID) || len(plan.ID) > 48 || !idPattern.MatchString(plan.SeedSet) {
		return errors.New("invalid auto plan identity")
	}
	if plan.Mode != "establish" && plan.Mode != "improve" {
		return errors.New("auto mode must explicitly establish or improve a role")
	}
	if (plan.Mode == "establish" && plan.IncumbentSHA256 != "") || (plan.Mode == "improve" && !validDigest(plan.IncumbentSHA256)) {
		return errors.New("auto mode does not match the incumbent")
	}
	if plan.Adoption != "manual" && plan.Adoption != "confirmed-only" {
		return errors.New("invalid auto adoption policy")
	}
	if err := plan.Runtime.Validate(); err != nil {
		return err
	}
	revision, err := plan.Spec.Digest()
	if err != nil {
		return err
	}
	if plan.RoleRevision != revision {
		return errors.New("auto role revision changed")
	}
	for _, value := range []string{plan.LifecycleSHA256, plan.SoftwareSHA256, plan.TaskSetSHA256, plan.SpecSHA256, plan.EnvelopeSHA256, plan.DeviceSHA256} {
		if !validDigest(value) {
			return errors.New("auto plan is missing a definition digest")
		}
	}
	if err := plan.Provenance.Validate(); err != nil {
		return err
	}
	if plan.Provenance.TaskSetSHA256 != plan.TaskSetSHA256 || plan.Provenance.SpecSHA256 != plan.SpecSHA256 ||
		plan.Provenance.SoftwareBuildSHA256 != plan.SoftwareSHA256 || plan.Provenance.BackendProtocol != record.BackendProtocolOllama {
		return errors.New("auto provenance disagrees with the fixed definitions or native Ollama protocol")
	}
	if err := plan.validateCandidates(); err != nil {
		return err
	}
	if err := plan.validateLimits(); err != nil {
		return err
	}
	expected := plan.SHA256
	plan.SHA256 = ""
	actual, err := digest(plan)
	if err != nil || expected != actual {
		return errors.New("auto plan seal does not match")
	}
	return nil
}

// Event contains no prompts, responses, credentials, or arbitrary file paths.
// Request events reserve the full requested cap before dispatch; no event can
// refund an interrupted request. Complete points reference original evidence.
type Event struct {
	Sequence              int                     `json:"sequence"`
	Previous              string                  `json:"previous"`
	SHA256                string                  `json:"sha256"`
	At                    string                  `json:"at"`
	Action                string                  `json:"action"`
	Phase                 string                  `json:"phase,omitempty"`
	Point                 int                     `json:"point,omitempty"`
	RunID                 string                  `json:"run_id,omitempty"`
	Kind                  string                  `json:"kind,omitempty"`
	RequestedOutputTokens int64                   `json:"requested_output_tokens,omitempty"`
	EvidenceSHA256        string                  `json:"evidence_sha256,omitempty"`
	StoreRef              *record.ManagedStoreRef `json:"store_ref,omitempty"`
	Confirmation          *role.ConfirmationPlan  `json:"confirmation,omitempty"`
	Outcome               string                  `json:"outcome,omitempty"`
}

type Journal struct {
	Schema string  `json:"schema"`
	Plan   Plan    `json:"plan"`
	Events []Event `json:"events"`
}

type State struct {
	Digest                            string                  `json:"digest"`
	Phase                             string                  `json:"phase"`
	Outcome                           string                  `json:"outcome,omitempty"`
	LastObservedAt                    time.Time               `json:"last_observed_at"`
	Requests                          int64                   `json:"requests"`
	RequestedOutputTokens             int64                   `json:"requested_output_tokens"`
	ExplorationRequests               int64                   `json:"exploration_requests"`
	ExplorationRequestedOutputTokens  int64                   `json:"exploration_requested_output_tokens"`
	ConfirmationRequests              int64                   `json:"confirmation_requests"`
	ConfirmationRequestedOutputTokens int64                   `json:"confirmation_requested_output_tokens"`
	Points                            int                     `json:"points"`
	ActivePoint                       int                     `json:"active_point,omitempty"`
	ActiveRunID                       string                  `json:"active_run_id,omitempty"`
	PointRequests                     int64                   `json:"point_requests"`
	PointRequestedOutputTokens        int64                   `json:"point_requested_output_tokens"`
	CompletedExploration              []Event                 `json:"completed_exploration"`
	CompletedConfirmation             []Event                 `json:"completed_confirmation"`
	ExplorationStore                  *record.ManagedStoreRef `json:"exploration_store,omitempty"`
	ConfirmationStore                 *record.ManagedStoreRef `json:"confirmation_store,omitempty"`
	Confirmation                      *role.ConfirmationPlan  `json:"confirmation,omitempty"`
}

func (journal Journal) Replay() (State, error) {
	if journal.Schema != JournalSchema || len(journal.Events) > MaximumEvents {
		return State{}, errors.New("invalid auto journal schema or event bound")
	}
	if err := journal.Plan.Validate(); err != nil {
		return State{}, err
	}
	created, _ := time.Parse(time.RFC3339Nano, journal.Plan.CreatedAt)
	state := State{Digest: journal.Plan.SHA256, Phase: "exploration", LastObservedAt: created}
	for index, event := range journal.Events {
		if event.Sequence != index+1 || event.Previous != state.Digest {
			return State{}, errors.New("auto journal sequence or previous digest changed")
		}
		expected := event.SHA256
		event.SHA256 = ""
		actual, err := digest(event)
		if err != nil || expected != actual {
			return State{}, errors.New("auto event seal does not match")
		}
		event.SHA256 = expected
		if err := state.apply(journal.Plan, event); err != nil {
			return State{}, fmt.Errorf("auto event %d: %w", index+1, err)
		}
		state.Digest = expected
	}
	return state, nil
}

func (state *State) apply(plan Plan, event Event) error {
	at, err := time.Parse(time.RFC3339Nano, event.At)
	if err != nil || at.Before(state.LastObservedAt) {
		return errors.New("auto clock moved backwards")
	}
	state.LastObservedAt = at
	if state.Outcome != "" {
		return errors.New("terminal auto sessions cannot dispatch or restart work")
	}
	if err := validateEventShape(event); err != nil {
		return err
	}
	switch event.Action {
	case "point_started":
		return state.startPoint(plan, event, at)
	case "request_reserved":
		return state.reserveRequest(plan, event, at)
	case "point_completed":
		return state.completePoint(plan, event, at)
	case "exploration_closed":
		return state.closeExploration(plan, event, at)
	case "confirmation_started":
		return state.startConfirmation(plan, event, at)
	case "confirmation_closed":
		return state.closeConfirmation(plan, event, at)
	case "finished":
		return state.finish(plan, event, at)
	default:
		return errors.New("unknown auto event")
	}
}

func (state *State) startPoint(plan Plan, event Event, at time.Time) error {
	if err := state.live(plan, at); err != nil {
		return err
	}
	completed := state.CompletedExploration
	if state.Phase == "confirmation" {
		completed = state.CompletedConfirmation
	}
	if state.Phase != "exploration" && state.Phase != "confirmation" {
		return errors.New("point cannot start in this phase")
	}
	if state.ActivePoint != 0 || event.Phase != state.Phase || event.Point != len(completed)+1 || event.Point > len(plan.Candidates) || state.Points >= plan.Limits.MaxPoints || !record.ValidRunID(event.RunID) {
		return errors.New("auto point does not match the fixed schedule")
	}
	for _, prior := range append(append([]Event{}, state.CompletedExploration...), state.CompletedConfirmation...) {
		if prior.RunID == event.RunID {
			return errors.New("auto points cannot reuse run identities")
		}
	}
	state.ActivePoint, state.ActiveRunID = event.Point, event.RunID
	state.PointRequests, state.PointRequestedOutputTokens = 0, 0
	state.Points++
	return nil
}

func (state *State) reserveRequest(plan Plan, event Event, at time.Time) error {
	if err := state.live(plan, at); err != nil {
		return err
	}
	if state.ActivePoint == 0 || event.Point != state.ActivePoint || event.Phase != state.Phase || event.RunID != state.ActiveRunID || (event.Kind != "generate" && event.Kind != "chat") || event.RequestedOutputTokens < 1 {
		return errors.New("request has no matching active point or finite output limit")
	}
	tokens := event.RequestedOutputTokens
	if tokens > plan.PointRequestedOutputTokens || state.PointRequests >= plan.PointRequests || state.PointRequestedOutputTokens > plan.PointRequestedOutputTokens-tokens || state.Requests >= plan.Limits.MaxRequests || state.RequestedOutputTokens > plan.Limits.MaxRequestedOutputTokens-tokens {
		return ErrBudget
	}
	if state.Phase == "exploration" {
		reserveRequests := int64(len(plan.Candidates)) * plan.PointRequests
		reserveTokens := int64(len(plan.Candidates)) * plan.PointRequestedOutputTokens
		if state.ExplorationRequests >= plan.Limits.MaxRequests-reserveRequests || state.ExplorationRequestedOutputTokens > plan.Limits.MaxRequestedOutputTokens-reserveTokens-tokens {
			return ErrBudget
		}
		state.ExplorationRequests++
		state.ExplorationRequestedOutputTokens += tokens
	} else {
		state.ConfirmationRequests++
		state.ConfirmationRequestedOutputTokens += tokens
	}
	state.Requests++
	state.RequestedOutputTokens += tokens
	state.PointRequests++
	state.PointRequestedOutputTokens += tokens
	return nil
}

func (state *State) completePoint(plan Plan, event Event, at time.Time) error {
	if state.ActivePoint == 0 || state.PointRequests == 0 || event.Point != state.ActivePoint || event.Phase != state.Phase || event.RunID != state.ActiveRunID || !validDigest(event.EvidenceSHA256) {
		return errors.New("completion does not match an active measured point")
	}
	if state.Phase == "exploration" {
		state.CompletedExploration = append(state.CompletedExploration, event)
	} else {
		state.CompletedConfirmation = append(state.CompletedConfirmation, event)
	}
	state.ActivePoint, state.ActiveRunID = 0, ""
	return nil
}

func (state *State) closeExploration(plan Plan, event Event, at time.Time) error {
	if err := state.live(plan, at); err != nil {
		return err
	}
	if state.Phase != "exploration" || state.ActivePoint != 0 || len(state.CompletedExploration) != len(plan.Candidates) || !storeRefValid(event.StoreRef) {
		return errors.New("exploration cannot close without its complete sealed store")
	}
	state.ExplorationStore = event.StoreRef
	state.Phase = "comparing"
	return nil
}

func (state *State) startConfirmation(plan Plan, event Event, at time.Time) error {
	if err := state.live(plan, at); err != nil {
		return err
	}
	if state.Phase != "comparing" || state.Confirmation != nil || event.Confirmation == nil {
		return errors.New("auto permits only one confirmation attempt")
	}
	if err := event.Confirmation.Validate(); err != nil {
		return err
	}
	if err := state.validateConfirmation(plan, *event.Confirmation); err != nil {
		return err
	}
	state.Confirmation = event.Confirmation
	state.Phase = "confirmation"
	return nil
}

func (state *State) closeConfirmation(plan Plan, event Event, at time.Time) error {
	if state.Phase != "confirmation" || state.ActivePoint != 0 || len(state.CompletedConfirmation) != len(plan.Candidates) || !storeRefValid(event.StoreRef) {
		return errors.New("confirmation cannot close without its complete sealed store")
	}
	state.ConfirmationStore = event.StoreRef
	state.Phase = "awaiting_adoption"
	return nil
}

func (state *State) finish(plan Plan, event Event, at time.Time) error {
	switch event.Outcome {
	case "adopted":
		if state.Phase != "awaiting_adoption" {
			return errors.New("adoption requires completed confirmation")
		}
	case "no_qualified", "overlap", "incumbent_retained", "unresolved":
		if state.Phase != "comparing" && state.Phase != "awaiting_adoption" {
			return errors.New("comparison outcome requires completed exploration")
		}
	case "blocked", "budget_exhausted", "cancelled", "failed", "stale":
	default:
		return errors.New("unknown auto terminal outcome")
	}
	state.Outcome = event.Outcome
	return nil
}

func (state State) validateConfirmation(plan Plan, confirmation role.ConfirmationPlan) error {
	profile, launch, err := plan.Runtime.ProfileDigests()
	if err != nil {
		return err
	}
	protocol := confirmation.Protocol
	if confirmation.SpecSHA256 != plan.RoleRevision || len(confirmation.Candidates) != len(plan.Candidates) ||
		protocol.Profile != plan.Profile || protocol.RequestedContext != plan.Runtime.NumCtx || protocol.EffectiveContext != plan.Runtime.NumCtx ||
		protocol.Repeats != plan.Repeats || protocol.Provenance != plan.Provenance {
		return errors.New("confirmation changed the fixed role, runtime or collection protocol")
	}
	for index, candidate := range confirmation.Candidates {
		want, completed := plan.Candidates[index], state.CompletedExploration[index]
		if candidate.EvidenceSHA256 != completed.EvidenceSHA256 || candidate.RunID != completed.RunID || candidate.SeedSet != plan.SeedSet ||
			candidate.Model.RuntimeBoundDigest() != want.ArtifactDigest || !modelref.SameServed(candidate.Model.Resolved, want.Model) {
			return errors.New("confirmation replaced exploration evidence or candidate identity")
		}
		binding := candidate.RuntimeBinding
		if binding == nil || binding.ProfileSHA256 != profile || binding.LaunchConfigurationSHA256 != launch ||
			binding.ExecutableSHA256 != plan.Runtime.ExecutableSHA256 || binding.RuntimeVersion != plan.Runtime.RuntimeVersion ||
			binding.ModelConfigurationSHA256 != want.ModelConfigurationSHA256 {
			return errors.New("confirmation changed the owned runtime or model configuration")
		}
	}
	return nil
}

func storeRefValid(ref *record.ManagedStoreRef) bool { return ref != nil && ref.Validate() == nil }

func validateEventShape(event Event) error {
	clean := Event{Sequence: event.Sequence, Previous: event.Previous, SHA256: event.SHA256, At: event.At, Action: event.Action}
	switch event.Action {
	case "point_started":
		clean.Phase, clean.Point, clean.RunID = event.Phase, event.Point, event.RunID
	case "request_reserved":
		clean.Phase, clean.Point, clean.RunID, clean.Kind, clean.RequestedOutputTokens = event.Phase, event.Point, event.RunID, event.Kind, event.RequestedOutputTokens
	case "point_completed":
		clean.Phase, clean.Point, clean.RunID, clean.EvidenceSHA256 = event.Phase, event.Point, event.RunID, event.EvidenceSHA256
	case "exploration_closed", "confirmation_closed":
		clean.StoreRef = event.StoreRef
	case "confirmation_started":
		clean.Confirmation = event.Confirmation
	case "finished":
		clean.Outcome = event.Outcome
	default:
		return errors.New("unknown auto event action")
	}
	a, _ := json.Marshal(clean)
	b, _ := json.Marshal(event)
	if !bytes.Equal(a, b) {
		return errors.New("auto event contains fields outside its action")
	}
	return nil
}

func (state State) Deadline(plan Plan) time.Time {
	expires, _ := time.Parse(time.RFC3339Nano, plan.ExpiresAt)
	if state.Phase == "exploration" || state.Phase == "comparing" {
		return expires.Add(-time.Duration(plan.Limits.ConfirmationWallSeconds) * time.Second)
	}
	return expires
}

func (state State) live(plan Plan, now time.Time) error {
	if now.Before(state.LastObservedAt) {
		return ErrStale
	}
	if !now.Before(state.Deadline(plan)) {
		return ErrBudget
	}
	return nil
}

// Reserve implements the admission hook inside each actual Ollama attempt.
// It returns only after the reservation has been synced into the journal.
func (session *Session) Reserve(ctx context.Context, request ollama.InferenceRequest) (ollama.InferencePermit, error) {
	if err := ctx.Err(); err != nil {
		return ollama.InferencePermit{}, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	state, err := session.journal.Replay()
	if err != nil {
		return ollama.InferencePermit{}, err
	}
	if state.ActivePoint < 1 || state.ActivePoint > len(session.journal.Plan.Candidates) || !modelref.SameServed(request.Model, session.journal.Plan.Candidates[state.ActivePoint-1].Model) {
		return ollama.InferencePermit{}, errors.New("auto request does not match the active candidate")
	}
	event := Event{Action: "request_reserved", Phase: state.Phase, Point: state.ActivePoint, RunID: state.ActiveRunID, Kind: request.Kind, RequestedOutputTokens: int64(request.MaxOutputTokens)}
	if err := session.appendLocked(event, session.now()); err != nil {
		return ollama.InferencePermit{}, err
	}
	return ollama.InferencePermit{Deadline: state.Deadline(session.journal.Plan)}, nil
}

func (plan Plan) validateCandidates() error {
	if len(plan.Candidates) < 2 || len(plan.Candidates) > 4 || plan.Repeats < 3 || plan.Repeats > 20 || len(plan.Profile) > 128 {
		return errors.New("auto requires two to four candidates and three to twenty fixed repeats")
	}
	seen := map[string]bool{}
	for _, candidate := range plan.Candidates {
		if !idPattern.MatchString(candidate.ID) || !validDigest(candidate.ArtifactDigest) || !validDigest(candidate.ModelConfigurationSHA256) || len(candidate.Model) > 512 || candidate.Model == "" {
			return errors.New("invalid auto candidate identity")
		}
		for _, key := range []string{"id:" + candidate.ID, "artifact:" + candidate.ArtifactDigest, "model:" + modelref.ServedKey(candidate.Model)} {
			if seen[key] {
				return errors.New("auto candidates must have distinct IDs, served names and artifacts")
			}
			seen[key] = true
		}
	}
	return nil
}

func (plan Plan) validateLimits() error {
	n := int64(len(plan.Candidates))
	limits := plan.Limits
	if plan.PointRequests < 1 || plan.PointRequests > 4096 || plan.PointRequestedOutputTokens < 1 || plan.PointRequestedOutputTokens > 1e9 || limits.MaxRequests < 1 || limits.MaxRequests > 4096 || limits.MaxRequestedOutputTokens < 1 || limits.MaxRequestedOutputTokens > 1e9 {
		return errors.New("auto request limits exceed supported bounds")
	}
	if limits.MaxPoints < 2*int(n) || limits.MaxPoints > 16 || limits.MaxRequests < 2*n*plan.PointRequests || limits.MaxRequestedOutputTokens < 2*n*plan.PointRequestedOutputTokens {
		return errors.New("auto limits must fund the complete exploration and confirmation schedules before inference")
	}
	if limits.WallSeconds < 2 || limits.WallSeconds > 86400 || limits.ConfirmationWallSeconds < 1 || limits.ConfirmationWallSeconds >= limits.WallSeconds {
		return errors.New("auto needs a wall limit up to 24 hours and a protected confirmation time allowance")
	}
	created, err := time.Parse(time.RFC3339Nano, plan.CreatedAt)
	expires, expiryErr := time.Parse(time.RFC3339Nano, plan.ExpiresAt)
	if err != nil || expiryErr != nil || expires.Sub(created) != time.Duration(limits.WallSeconds)*time.Second {
		return errors.New("invalid auto session expiry")
	}
	return nil
}
