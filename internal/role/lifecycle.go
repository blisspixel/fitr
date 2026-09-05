package role

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/blisspixel/fitr/internal/record"
)

const LifecycleSchema = "fitr.role.lifecycle.v1"
const maximumLifecycleEvents = 256
const maximumLifecyclePlans = 32
const maximumLifecycleBytes = 8 << 20

type Lifecycle struct {
	Schema          string           `json:"schema"`
	Name            string           `json:"name"`
	Digest          string           `json:"digest"`
	Events          []LifecycleEvent `json:"events"`
	IncumbentSHA256 string           `json:"incumbent_sha256,omitempty"`
	PreviousSHA256  string           `json:"previous_sha256,omitempty"`
}

type LifecycleEvent struct {
	Sequence        int                         `json:"sequence"`
	Action          string                      `json:"action"`
	At              string                      `json:"at"`
	PreviousDigest  string                      `json:"previous_digest"`
	Digest          string                      `json:"digest"`
	PlanSHA256      string                      `json:"plan_sha256"`
	IncumbentSHA256 string                      `json:"incumbent_sha256,omitempty"`
	Plan            *ConfirmationPlan           `json:"plan,omitempty"`
	Completion      *ConfirmationAttemptReceipt `json:"completion,omitempty"`
	Selection       *SelectionReceipt           `json:"selection,omitempty"`
}

// ConfirmationPoint pins the current canonical evidence observed at completion.
// The completed records themselves remain in the record store and bundle.
type ConfirmationPoint struct {
	Attachment     Attachment              `json:"attachment"`
	Model          record.ModelIdentity    `json:"model"`
	StartedAt      string                  `json:"started_at"`
	StoreRef       *record.ManagedStoreRef `json:"store_ref,omitempty"`
	RuntimeBinding *record.RuntimeBinding  `json:"runtime_binding,omitempty"`
}

type ConfirmationAttemptReceipt struct {
	BundleSHA256         string              `json:"bundle_sha256"`
	State                string              `json:"state"`
	ChosenEvidenceSHA256 string              `json:"chosen_evidence_sha256"`
	Points               []ConfirmationPoint `json:"points"`
	ExpiresAt            string              `json:"expires_at"`
	EvaluatedAt          string              `json:"evaluated_at"`
}

type SelectionReceipt struct {
	SpecSHA256           string              `json:"spec_sha256"`
	PlanSHA256           string              `json:"plan_sha256"`
	BundleSHA256         string              `json:"bundle_sha256"`
	ChosenEvidenceSHA256 string              `json:"chosen_evidence_sha256"`
	Selected             ConfirmationPoint   `json:"selected"`
	Points               []ConfirmationPoint `json:"points"`
	ExpiresAt            string              `json:"expires_at"`
	RollbackOf           string              `json:"rollback_of,omitempty"`
	EvaluatedAt          string              `json:"evaluated_at"`
}

type SelectionStatus struct {
	Schema          string                  `json:"schema"`
	Role            string                  `json:"role"`
	Scope           string                  `json:"scope"`
	State           string                  `json:"state"`
	Reason          string                  `json:"reason,omitempty"`
	LifecycleDigest string                  `json:"lifecycle_digest"`
	ReceiptSHA256   string                  `json:"receipt_sha256,omitempty"`
	PreviousSHA256  string                  `json:"previous_sha256,omitempty"`
	Selection       *SelectionReceipt       `json:"selection,omitempty"`
	EvaluatedAt     string                  `json:"evaluated_at"`
	Attempts        int                     `json:"attempts"`
	LastAttempt     *LifecycleAttemptStatus `json:"last_attempt,omitempty"`
}

type LifecycleAttemptStatus struct {
	PlanSHA256 string `json:"plan_sha256"`
	Action     string `json:"action"`
	At         string `json:"at"`
}

func lifecycleAttempts(life Lifecycle) (int, *LifecycleAttemptStatus) {
	count := 0
	var last *LifecycleAttemptStatus
	for _, event := range life.Events {
		switch event.Action {
		case "started":
			count++
			last = &LifecycleAttemptStatus{event.PlanSHA256, event.Action, event.At}
		case "completed", "failed", "cancelled":
			last = &LifecycleAttemptStatus{event.PlanSHA256, event.Action, event.At}
		}
	}
	return count, last
}

type lifecyclePlanState struct {
	plan             ConfirmationPlan
	state            string
	started          time.Time
	completion       *ConfirmationAttemptReceipt
	adopted          bool
	incumbentAtIssue string
}

func lifecycleDigest(domain string, value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(domain+"\x00"), data...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func emptyLifecycle(name string) Lifecycle {
	life := Lifecycle{Schema: LifecycleSchema, Name: name, Events: []LifecycleEvent{}}
	life.Digest, _ = lifecycleDigest(LifecycleSchema, life)
	return life
}

func (life Lifecycle) Validate() error {
	if life.Schema != LifecycleSchema || !roleNamePattern.MatchString(life.Name) || len(life.Events) > maximumLifecycleEvents {
		return errors.New("invalid role lifecycle identity or event bounds")
	}
	states := map[string]*lifecyclePlanState{}
	seeds := map[string]bool{}
	replayed := emptyLifecycle(life.Name)
	var previousTime time.Time
	for index, event := range life.Events {
		at, err := time.Parse(time.RFC3339Nano, event.At)
		if err != nil || at.Before(previousTime) || event.Sequence != index+1 || event.PreviousDigest != replayed.Digest {
			return errors.New("role lifecycle ordering or previous digest is invalid")
		}
		originalDigest := event.Digest
		event.Digest = ""
		digest, err := lifecycleDigest(LifecycleSchema+".event", event)
		if err != nil || digest != originalDigest {
			return errors.New("role lifecycle event digest does not match")
		}
		event.Digest = digest
		if err := validateLifecycleEvent(event, life.Name, states, seeds, replayed); err != nil {
			return err
		}
		replayed.appendEvent(event)
		previousTime = at
	}
	if len(states) > maximumLifecyclePlans || replayed.Digest != life.Digest || replayed.IncumbentSHA256 != life.IncumbentSHA256 || replayed.PreviousSHA256 != life.PreviousSHA256 {
		return errors.New("role lifecycle summary or digest does not match its receipts")
	}
	return nil
}

func validateLifecycleEvent(event LifecycleEvent, name string, states map[string]*lifecyclePlanState, seeds map[string]bool, prior Lifecycle) error {
	if !roleDigestValid(event.PlanSHA256) {
		return errors.New("invalid lifecycle plan digest")
	}
	if event.Action == "issued" {
		return validateIssuance(event, name, states, seeds, prior)
	}
	state := states[event.PlanSHA256]
	if state == nil || event.Plan != nil || event.IncumbentSHA256 != "" {
		return errors.New("confirmation operation has no issued plan")
	}
	if event.Action == "started" || event.Action == "completed" || event.Action == "adopted" {
		at, _ := time.Parse(time.RFC3339Nano, event.At)
		if err := liveConfirmationPlan(state.plan, at); err != nil {
			return err
		}
	}
	if (event.Action == "started" || event.Action == "adopted") && state.incumbentAtIssue != prior.IncumbentSHA256 {
		return errors.New("incumbent changed after confirmation issuance")
	}
	switch event.Action {
	case "adopted":
		if state.state != "completed" || state.adopted || event.Completion != nil || event.Selection == nil || event.Selection.RollbackOf != "" {
			return errors.New("adoption requires one completed confirmation")
		}
		if err := validateSelectionReceipt(*event.Selection, state.plan, state.completion, event.At); err != nil {
			return err
		}
		state.adopted = true
	case "rolled-back":
		return validateRollbackReceipt(event, prior)
	default:
		return validateAttemptTransition(event, state)
	}
	return nil
}

func validateAttemptTransition(event LifecycleEvent, state *lifecyclePlanState) error {
	switch event.Action {
	case "started":
		if state.state != "issued" || event.Completion != nil || event.Selection != nil {
			return errors.New("confirmation plan permits only one fresh attempt")
		}
		state.state = "started"
		state.started, _ = time.Parse(time.RFC3339Nano, event.At)
	case "cancelled", "failed":
		if state.state != "started" || event.Completion != nil || event.Selection != nil {
			return errors.New("confirmation terminal outcome does not follow an attempt")
		}
		state.state = event.Action
	case "completed":
		if state.state != "started" || event.Completion == nil || event.Selection != nil {
			return errors.New("confirmation completion does not follow one attempt")
		}
		if err := validateAttemptReceipt(*event.Completion, state.plan, state.started, event.At); err != nil {
			return err
		}
		state.state, state.completion = "completed", event.Completion
	default:
		return errors.New("unknown role lifecycle action")
	}
	return nil
}

func validateIssuance(event LifecycleEvent, name string, states map[string]*lifecyclePlanState, seeds map[string]bool, prior Lifecycle) error {
	if event.Plan == nil || event.Completion != nil || event.Selection != nil || states[event.PlanSHA256] != nil {
		return errors.New("confirmation plan was repeated or has invalid issuance fields")
	}
	plan := *event.Plan
	if err := plan.Validate(); err != nil {
		return err
	}
	if plan.PlanSHA256 != event.PlanSHA256 || plan.Spec.Name != name || seeds[plan.SeedSet] {
		return errors.New("confirmation plan identity or seed set was reused")
	}
	if event.IncumbentSHA256 != prior.IncumbentSHA256 {
		return errors.New("issuance incumbent binding does not match prior selection")
	}
	if err := planIncludesIncumbent(plan, prior); err != nil {
		return err
	}
	at, _ := time.Parse(time.RFC3339Nano, event.At)
	if err := liveConfirmationPlan(plan, at); err != nil {
		return err
	}
	seeds[plan.SeedSet] = true
	states[event.PlanSHA256] = &lifecyclePlanState{plan: plan, state: "issued", incumbentAtIssue: event.IncumbentSHA256}
	return nil
}

func planIncludesIncumbent(plan ConfirmationPlan, prior Lifecycle) error {
	if prior.IncumbentSHA256 == "" {
		return nil
	}
	incumbent := prior.findEvent(prior.IncumbentSHA256)
	if incumbent == nil || incumbent.Selection == nil {
		return errors.New("incumbent receipt is missing")
	}
	for _, candidate := range plan.Candidates {
		if candidate.Model.RuntimeBoundDigest() == incumbent.Selection.Selected.Model.RuntimeBoundDigest() &&
			record.SameRuntimeConfiguration(candidate.RuntimeBinding, incumbent.Selection.Selected.RuntimeBinding) {
			return nil
		}
	}
	return errors.New("confirmation shortlist must include the incumbent artifact")
}

func validateAttemptReceipt(receipt ConfirmationAttemptReceipt, plan ConfirmationPlan, started time.Time, at string) error {
	if !roleDigestValid(receipt.BundleSHA256) || !roleTextValid(receipt.State, 64, false) || receipt.ChosenEvidenceSHA256 != plan.ChosenEvidenceSHA256 || len(receipt.Points) != len(plan.Candidates) {
		return errors.New("invalid confirmation attempt receipt")
	}
	expiry, err := time.Parse(time.RFC3339Nano, receipt.ExpiresAt)
	if err != nil {
		return errors.New("invalid confirmation receipt expiry")
	}
	var wantExpiry time.Time
	ended, _ := time.Parse(time.RFC3339Nano, at)
	evaluated, err := time.Parse(time.RFC3339Nano, receipt.EvaluatedAt)
	if err != nil || evaluated.After(ended) || evaluated.Before(started) {
		return errors.New("invalid confirmation evaluation time")
	}
	seen := map[string]bool{}
	for index, point := range receipt.Points {
		if !record.SameRuntimeConfiguration(point.RuntimeBinding, plan.Candidates[index].RuntimeBinding) {
			return errors.New("confirmation receipt changed the planned runtime configuration")
		}
		if point.RuntimeBinding != nil {
			if err := point.RuntimeBinding.ValidateFor(point.Model); err != nil {
				return err
			}
		}
		if point.StoreRef != nil {
			if err := point.StoreRef.Validate(); err != nil {
				return err
			}
		}
		if index > 0 && !sameLifecycleValue(point.StoreRef, receipt.Points[0].StoreRef) {
			return errors.New("confirmation points mix evidence store references")
		}
		if err := validateAttachment(point.Attachment); err != nil {
			return err
		}
		pointTime, err := time.Parse(time.RFC3339Nano, point.StartedAt)
		if err != nil || pointTime.Before(started.Truncate(time.Second)) || pointTime.After(ended) || !sameConfirmationModel(point.Model, plan.Candidates[index].Model) || seen[point.Attachment.EvidenceSHA256] {
			return errors.New("confirmation point does not match its issued attempt")
		}
		seen[point.Attachment.EvidenceSHA256] = true
		pointExpiry := pointTime.Add(time.Duration(plan.Spec.MaxAgeDays) * 24 * time.Hour)
		if wantExpiry.IsZero() || pointExpiry.Before(wantExpiry) {
			wantExpiry = pointExpiry
		}
	}
	if !expiry.Equal(wantExpiry) {
		return errors.New("confirmation expiry does not match its evidence")
	}
	return nil
}

func selectedConfirmationPoint(plan ConfirmationPlan, points []ConfirmationPoint) (ConfirmationPoint, error) {
	for index, candidate := range plan.Candidates {
		if candidate.EvidenceSHA256 == plan.ChosenEvidenceSHA256 && index < len(points) {
			return points[index], nil
		}
	}
	return ConfirmationPoint{}, errors.New("original confirmation choice is missing")
}

func validateSelectionReceipt(selection SelectionReceipt, plan ConfirmationPlan, attempt *ConfirmationAttemptReceipt, at string) error {
	if attempt == nil || attempt.State != "confirmed" || selection.SpecSHA256 != plan.SpecSHA256 || selection.PlanSHA256 != plan.PlanSHA256 || selection.BundleSHA256 != attempt.BundleSHA256 || selection.ChosenEvidenceSHA256 != plan.ChosenEvidenceSHA256 || selection.ExpiresAt != attempt.ExpiresAt || selection.EvaluatedAt != attempt.EvaluatedAt || !sameLifecycleValue(selection.Points, attempt.Points) {
		return errors.New("selection is not backed by the confirmed original choice")
	}
	selected, err := selectedConfirmationPoint(plan, attempt.Points)
	if err != nil || !sameLifecycleValue(selection.Selected, selected) {
		return errors.New("selection changed the original confirmed choice")
	}
	expiry, err := time.Parse(time.RFC3339Nano, selection.ExpiresAt)
	when, parseErr := time.Parse(time.RFC3339Nano, at)
	if err != nil || parseErr != nil || !when.Before(expiry) {
		return errors.New("selection evidence has expired")
	}
	return nil
}

func validateRollbackReceipt(event LifecycleEvent, prior Lifecycle) error {
	if event.Completion != nil || event.Selection == nil || !roleDigestValid(event.Selection.RollbackOf) || event.Selection.RollbackOf == prior.incumbentAdoptionSHA256() {
		return errors.New("rollback requires a different prior adoption receipt")
	}
	original := prior.findEvent(event.Selection.RollbackOf)
	if original == nil || original.Action != "adopted" || original.PlanSHA256 != event.PlanSHA256 {
		return errors.New("rollback target is not a prior adoption")
	}
	copySelection := *event.Selection
	copySelection.RollbackOf = ""
	if !sameLifecycleValue(copySelection, *original.Selection) {
		return errors.New("rollback altered its original selection provenance")
	}
	expiry, _ := time.Parse(time.RFC3339Nano, event.Selection.ExpiresAt)
	at, _ := time.Parse(time.RFC3339Nano, event.At)
	if !at.Before(expiry) {
		return errors.New("rollback evidence has expired")
	}
	return nil
}

func sameLifecycleValue(a, b any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	return err == nil && bytes.Equal(left, right)
}

func (life *Lifecycle) appendEvent(event LifecycleEvent) {
	life.Events = append(life.Events, event)
	if event.Selection != nil {
		life.PreviousSHA256, life.IncumbentSHA256 = life.incumbentAdoptionSHA256(), event.Digest
	}
	life.Digest = ""
	life.Digest, _ = lifecycleDigest(LifecycleSchema, *life)
}

// Rollback receipts preserve the original adoption rather than creating a
// new adoption. The next rollback must target that original receipt, while
// IncumbentSHA256 continues to identify the latest selection event for CAS.
func (life Lifecycle) incumbentAdoptionSHA256() string {
	incumbent := life.findEvent(life.IncumbentSHA256)
	if incumbent != nil && incumbent.Selection != nil && incumbent.Selection.RollbackOf != "" {
		return incumbent.Selection.RollbackOf
	}
	return life.IncumbentSHA256
}

func (life Lifecycle) findEvent(digest string) *LifecycleEvent {
	for index := range life.Events {
		if life.Events[index].Digest == digest {
			return &life.Events[index]
		}
	}
	return nil
}

func (life Lifecycle) issuedPlan(planSHA256 string) (*ConfirmationPlan, error) {
	for _, event := range life.Events {
		if event.Action == "issued" && event.PlanSHA256 == planSHA256 {
			return event.Plan, nil
		}
	}
	return nil, fmt.Errorf("confirmation plan %s was not issued for this role", planSHA256)
}
