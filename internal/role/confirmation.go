package role

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/record"
)

const (
	ConfirmationPlanSchema       = "fitr.role.confirmation.plan.v1"
	ConfirmationPreferencePolicy = "fitr.role.preference.fixed-anchors.v1"
	ConfirmationScope            = "battery_screening"
	maximumConfirmationChecks    = 4096
)

// ConfirmationCandidate retains exploration identity. Fresh records occupy
// the same ordered position, but have different run and evidence identities.
type ConfirmationCandidate struct {
	Model          record.ModelIdentity      `json:"model"`
	Capacity       ConfirmationCapacity      `json:"capacity"`
	Experiment     *record.ExperimentBinding `json:"experiment,omitempty"`
	RuntimeBinding *record.RuntimeBinding    `json:"runtime_binding,omitempty"`
	EvidenceSHA256 string                    `json:"evidence_sha256"`
	RunID          string                    `json:"run_id"`
	SeedSet        string                    `json:"seedset"`
	StartedAt      string                    `json:"started_at"`
}

type ConfirmationCheck struct {
	TaskID string `json:"task"`
	Family string `json:"family"`
	Need   string `json:"need"`
	Origin string `json:"origin"`
	Seed   uint64 `json:"seed"`
}

// ConfirmationProtocol freezes the full exploration protocol. The CLI must
// compare preparation against it before inference, including software build,
// merged task definitions, profile, scoring policy and generated schedule.
// A software or definition upgrade requires new exploration first.
type ConfirmationProtocol struct {
	DeviceKey        string               `json:"device_key"`
	ComparabilityKey string               `json:"comparability_key"`
	Profile          string               `json:"profile"`
	Level            string               `json:"level"`
	ExecutionPolicy  string               `json:"execution_policy"`
	RequestedContext int                  `json:"requested_context"`
	EffectiveContext int                  `json:"effective_context"`
	Repeats          int                  `json:"repeats"`
	Provenance       record.RunProvenance `json:"provenance"`
	TaskPlan         record.TaskPlan      `json:"task_plan"`
	Checks           []ConfirmationCheck  `json:"checks"`
}

type ConfirmationPlan struct {
	Schema                    string                  `json:"schema"`
	PlanSHA256                string                  `json:"plan_sha256"`
	Spec                      Spec                    `json:"spec"`
	SpecSHA256                string                  `json:"spec_sha256"`
	Candidates                []ConfirmationCandidate `json:"candidates"`
	ChosenEvidenceSHA256      string                  `json:"chosen_evidence_sha256"`
	SeedSet                   string                  `json:"seedset"`
	CreatedAt                 string                  `json:"created_at"`
	ExpiresAt                 string                  `json:"expires_at"`
	Protocol                  ConfirmationProtocol    `json:"protocol"`
	PreferencePolicy          string                  `json:"preference_policy"`
	RelativeWeightSensitivity float64                 `json:"relative_weight_sensitivity"`
}

// NewConfirmationPlan accepts validated canonical exploration records from
// the caller. It freezes their existing full protocol and generates a fresh
// paired seed. The caller must persist issuance before collecting evidence.
// This establishes fresh generated instances, not held-out task families.
func NewConfirmationPlan(spec Spec, exploration []*record.Record, chosenEvidenceSHA256 string, now time.Time) (ConfirmationPlan, error) {
	digest, err := spec.Digest()
	if err != nil {
		return ConfirmationPlan{}, err
	}
	if now.IsZero() || len(exploration) < 2 || len(exploration) > 4 {
		return ConfirmationPlan{}, errors.New("role confirmation requires a current time and two to four exploration records")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return ConfirmationPlan{}, err
	}
	plan := ConfirmationPlan{
		Schema: ConfirmationPlanSchema, Spec: spec, SpecSHA256: digest,
		ChosenEvidenceSHA256: chosenEvidenceSHA256, SeedSet: "role-confirm-" + hex.EncodeToString(random[:]),
		CreatedAt: now.UTC().Format(time.RFC3339Nano), ExpiresAt: now.Add(24 * time.Hour).UTC().Format(time.RFC3339Nano),
		PreferencePolicy: ConfirmationPreferencePolicy, RelativeWeightSensitivity: 0.2,
	}
	if err := plan.setExploration(exploration); err != nil {
		return ConfirmationPlan{}, err
	}
	review, err := evaluateConfirmationRecords(plan, exploration, now)
	if err != nil {
		return ConfirmationPlan{}, err
	}
	if review.State != "confirmed" {
		return ConfirmationPlan{}, errors.New("preselected candidate is not a conclusive exploration lead")
	}
	plan.PlanSHA256, err = confirmationDigest(ConfirmationPlanSchema, plan)
	if err != nil {
		return ConfirmationPlan{}, err
	}
	if err := plan.Validate(); err != nil {
		return ConfirmationPlan{}, err
	}
	return cloneConfirmation(plan)
}

func (plan *ConfirmationPlan) setExploration(results []*record.Record) error {
	for index, result := range results {
		if issue := result.EvidenceIntegrityIssue(); issue != "" {
			return errors.New(issue)
		}
		protocol, err := confirmationProtocolFrom(result, plan.SeedSet)
		if err != nil {
			return err
		}
		allocation, err := confirmationCapacityFrom(plan.Spec, result)
		if err != nil {
			return err
		}
		if index == 0 {
			plan.Protocol = protocol
		} else if !confirmationEqual(plan.Protocol, protocol) {
			return errors.New("exploration protocols differ")
		}
		plan.Candidates = append(plan.Candidates, ConfirmationCandidate{
			Model: result.Manifest.Model, Capacity: allocation, Experiment: result.Experiment, EvidenceSHA256: result.Completion.EvidenceSHA256,
			RuntimeBinding: record.CloneRuntimeBinding(result.RuntimeBinding),
			RunID:          result.StableRunID(), SeedSet: result.SeedSet, StartedAt: result.StartedAt,
		})
	}
	return nil
}

func confirmationProtocolFrom(result *record.Record, seedSet string) (ConfirmationProtocol, error) {
	if result.Manifest.Provenance == nil || result.DeviceV2 == nil || result.DeviceV2.Context.EffectiveTokens == nil {
		return ConfirmationProtocol{}, errors.New("confirmation requires sealed provenance and verified context")
	}
	key, err := result.DeviceV2.ComparabilityKey()
	if err != nil {
		return ConfirmationProtocol{}, err
	}
	protocol := ConfirmationProtocol{
		DeviceKey: result.Device.Key(), ComparabilityKey: key, Profile: result.Profile,
		Level: result.Level, ExecutionPolicy: result.ExecutionPolicy, RequestedContext: result.ContextSize(),
		EffectiveContext: *result.DeviceV2.Context.EffectiveTokens, Repeats: result.Repeats,
		Provenance: *result.Manifest.Provenance, TaskPlan: result.TaskPlan,
	}
	counts := make(map[string]int)
	for _, check := range result.Checks {
		round := counts[check.TaskID]
		if check.Seed != eval.InstanceSeed(result.SeedSet, check.TaskID, round) {
			return ConfirmationProtocol{}, errors.New("exploration checks do not use the fixed generated-instance schedule")
		}
		counts[check.TaskID]++
		protocol.Checks = append(protocol.Checks, ConfirmationCheck{
			TaskID: check.TaskID, Family: check.Family, Need: check.Need, Origin: check.Origin,
			Seed: eval.InstanceSeed(seedSet, check.TaskID, round),
		})
	}
	protocol.TaskPlan.CheckPlanSHA256, err = confirmationCheckDigest(protocol.Checks)
	return protocol, err
}

func (plan ConfirmationPlan) Validate() error {
	if plan.Schema != ConfirmationPlanSchema || plan.PreferencePolicy != ConfirmationPreferencePolicy || plan.RelativeWeightSensitivity != 0.2 {
		return errors.New("unsupported role confirmation schema or preference policy")
	}
	digest, err := plan.Spec.Digest()
	if err != nil || digest != plan.SpecSHA256 {
		return errors.New("confirmation role specification digest differs")
	}
	created, err := confirmationTime(plan.CreatedAt)
	if err != nil {
		return err
	}
	expires, err := confirmationTime(plan.ExpiresAt)
	if err != nil || !expires.Equal(created.Add(24*time.Hour)) {
		return errors.New("confirmation expiry must be 24 hours after issuance")
	}
	if !strings.HasPrefix(plan.SeedSet, "role-confirm-") || len(plan.SeedSet) != len("role-confirm-")+32 {
		return errors.New("invalid confirmation seed set")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(plan.SeedSet, "role-confirm-")); err != nil {
		return err
	}
	if err := plan.validateCandidates(created); err != nil {
		return err
	}
	if err := plan.Protocol.validate(plan.SeedSet); err != nil {
		return err
	}
	value := plan
	value.PlanSHA256 = ""
	digest, err = confirmationDigest(ConfirmationPlanSchema, value)
	if err != nil || digest != plan.PlanSHA256 {
		return errors.New("confirmation plan digest differs")
	}
	return nil
}

func (plan ConfirmationPlan) validateCandidates(created time.Time) error {
	if len(plan.Candidates) < 2 || len(plan.Candidates) > 4 {
		return errors.New("confirmation requires two to four candidates")
	}
	seen := make(map[string]bool)
	chosen := false
	first := plan.Candidates[0].Model
	for _, candidate := range plan.Candidates {
		if err := validateCandidateRuntime(candidate, plan.Candidates[0]); err != nil {
			return err
		}
		if err := candidate.Capacity.validate(); err != nil {
			return err
		}
		if candidate.Experiment != nil {
			if err := candidate.Experiment.Validate(); err != nil {
				return err
			}
		}
		if candidate.Model.RuntimeBoundDigest() == "" || candidate.Model.Backend != first.Backend || candidate.Model.Runtime != first.Runtime {
			return errors.New("confirmation candidates must be runtime-bound on the same backend and runtime")
		}
		if !roleDigestValid(candidate.EvidenceSHA256) || !roleTextValid(candidate.RunID, 128, false) || !roleTextValid(candidate.SeedSet, 128, false) {
			return errors.New("invalid exploration identity")
		}
		for _, key := range []string{"artifact:" + candidate.Model.RuntimeBoundDigest(), "evidence:" + candidate.EvidenceSHA256, "run:" + candidate.RunID} {
			if seen[key] {
				return errors.New("duplicate confirmation candidate identity")
			}
			seen[key] = true
		}
		started, err := confirmationTime(candidate.StartedAt)
		if err != nil || started.After(created) || created.Sub(started) > time.Duration(plan.Spec.MaxAgeDays)*24*time.Hour {
			return errors.New("exploration evidence is stale or has invalid timing")
		}
		if candidate.SeedSet == plan.SeedSet {
			return errors.New("confirmation cannot reuse an exploration seed")
		}
		chosen = chosen || candidate.EvidenceSHA256 == plan.ChosenEvidenceSHA256
	}
	if !chosen {
		return errors.New("preselected evidence is outside the candidate set")
	}
	return nil
}

func (protocol ConfirmationProtocol) validate(seedSet string) error {
	if !roleTextValid(protocol.DeviceKey, 512, false) || !roleTextValid(protocol.ComparabilityKey, 512, false) || !roleTextValid(protocol.Profile, 128, false) {
		return errors.New("confirmation device and profile identities are required")
	}
	if protocol.Level != "full" || protocol.ExecutionPolicy != record.ExecutionDisabled || protocol.Repeats < 3 || protocol.Repeats > 20 ||
		protocol.RequestedContext < 1 || protocol.RequestedContext > 16*1024*1024 || protocol.EffectiveContext < 1 || protocol.EffectiveContext > 16*1024*1024 {
		return errors.New("confirmation requires a bounded full protocol with execution disabled")
	}
	if err := protocol.Provenance.Validate(); err != nil {
		return err
	}
	if err := protocol.TaskPlan.Validate(); err != nil {
		return err
	}
	if protocol.TaskPlan.AdaptiveChecks || protocol.TaskPlan.SpeedSamples != protocol.Repeats || !protocol.TaskPlan.Memory {
		return errors.New("confirmation requires fixed sampling and memory measurement")
	}
	return protocol.validateChecks(seedSet)
}

func (protocol ConfirmationProtocol) validateChecks(seedSet string) error {
	if len(protocol.Checks) < protocol.Repeats || len(protocol.Checks) > maximumConfirmationChecks ||
		len(protocol.Checks)%protocol.Repeats != 0 || protocol.TaskPlan.CheckTrialsLimit != len(protocol.Checks) {
		return errors.New("confirmation check denominator differs from its fixed schedule")
	}
	perRound := len(protocol.Checks) / protocol.Repeats
	seen := make(map[string]bool)
	for index, check := range protocol.Checks {
		if !roleTextValid(check.TaskID, 128, false) || !roleTextValid(check.Family, 128, false) || !roleTextValid(check.Need, 128, false) || !roleTextValid(check.Origin, 128, false) {
			return errors.New("invalid confirmation check identity")
		}
		if index < perRound {
			if seen[check.TaskID] {
				return errors.New("duplicate task in confirmation round")
			}
			seen[check.TaskID] = true
		}
		expected := protocol.Checks[index%perRound]
		expected.Seed = eval.InstanceSeed(seedSet, check.TaskID, index/perRound)
		if check != expected {
			return errors.New("confirmation task schedule differs from its fresh seed and fixed rounds")
		}
	}
	digest, err := confirmationCheckDigest(protocol.Checks)
	if err != nil || digest != protocol.TaskPlan.CheckPlanSHA256 {
		return errors.New("confirmation task plan digest differs")
	}
	if protocol.TaskPlan.RefusalTrials > 0 && !roleDigestValid(protocol.TaskPlan.RefusalPlanSHA256) {
		return errors.New("confirmation refusal schedule is not sealed")
	}
	return nil
}

func ConfirmationPlanBinding(plan ConfirmationPlan, pointIndex int) record.ExperimentBinding {
	return record.ExperimentBinding{Schema: record.ExperimentBindingSchema, Kind: "role", Stage: "confirm",
		PlanSHA256: plan.PlanSHA256, PointIndex: pointIndex, PointCount: len(plan.Candidates)}
}

// ValidatePreparedConfirmationPoint checks non-observational fields before
// inference. pointIndex is one-based. Effective context and device placement
// are checked again against the completed, sealed evidence during analysis.
func ValidatePreparedConfirmationPoint(plan ConfirmationPlan, pointIndex int, result *record.Record,
	identity record.ModelIdentity, provenance record.RunProvenance) error {
	return validatePreparedConfirmationPoint(plan, pointIndex, result, identity, provenance, true)
}

// ValidatePreparedOwnedConfirmationPoint permits the compute backend to remain
// unobserved before an owned runtime's first load. The caller must independently
// bind the physical device and settings before using this check, then call
// ValidateConfirmationContextPoint after loading and before the battery. This
// API cannot validate sealed or already observed evidence; final analysis uses
// ValidatePreparedConfirmationPoint and retains its strict device comparison.
func ValidatePreparedOwnedConfirmationPoint(plan ConfirmationPlan, pointIndex int, result *record.Record,
	identity record.ModelIdentity, provenance record.RunProvenance) error {
	if result == nil || result.RuntimeBinding == nil || result.DeviceV2 != nil || result.Manifest != nil || result.Completion != nil {
		return errors.New("owned confirmation preflight requires an unsealed point before context observation")
	}
	return validatePreparedConfirmationPoint(plan, pointIndex, result, identity, provenance, result.Device.GPUBackend != "")
}

func validatePreparedConfirmationPoint(plan ConfirmationPlan, pointIndex int, result *record.Record,
	identity record.ModelIdentity, provenance record.RunProvenance, checkDevice bool) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	if result == nil || pointIndex < 1 || pointIndex > len(plan.Candidates) {
		return errors.New("invalid confirmation point position")
	}
	want := ConfirmationPlanBinding(plan, pointIndex)
	if !record.SameRuntimeConfiguration(result.RuntimeBinding, plan.Candidates[pointIndex-1].RuntimeBinding) {
		return errors.New("confirmation runtime configuration differs from exploration")
	}
	if result.RuntimeBinding != nil {
		if err := result.RuntimeBinding.ValidateFor(identity); err != nil {
			return err
		}
	}
	if result.Experiment == nil || *result.Experiment != want || !sameConfirmationModel(identity, plan.Candidates[pointIndex-1].Model) {
		return errors.New("confirmation point binding or artifact differs")
	}
	p := plan.Protocol
	if (checkDevice && result.Device.Key() != p.DeviceKey) || result.ContextSize() != p.RequestedContext || result.Profile != p.Profile ||
		result.Level != p.Level || result.ExecutionPolicy != p.ExecutionPolicy || result.Repeats != p.Repeats || result.SeedSet != plan.SeedSet ||
		result.TaskPlan != p.TaskPlan || provenance != p.Provenance {
		return errors.New("confirmation preparation differs from the sealed protocol")
	}
	return nil
}

// Requested is the caller's audit spelling and may be an alias. Collection
// uses Resolved, so only that spelling may differ from exploration. All
// runtime, artifact, binding and size fields still identify the same subject.
func sameConfirmationModel(actual, expected record.ModelIdentity) bool {
	if actual.RuntimeBoundDigest() == "" || expected.RuntimeBoundDigest() == "" {
		return false
	}
	actual.Requested = expected.Requested
	return actual == expected
}

func confirmationCheckDigest(checks []ConfirmationCheck) (string, error) {
	outcomes := make([]eval.CheckOutcome, len(checks))
	for index, check := range checks {
		outcomes[index] = eval.CheckOutcome{TaskID: check.TaskID, Family: check.Family, Need: check.Need, Origin: check.Origin, Seed: check.Seed}
	}
	return record.ObservedCheckPlanSHA256(outcomes)
}

func confirmationTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.IsZero() {
		return time.Time{}, errors.New("invalid role confirmation timestamp")
	}
	return parsed, nil
}

func confirmationDigest(domain string, value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if len(data) > maximumLibraryBytes {
		return "", errors.New("role confirmation plan exceeds one MiB")
	}
	sum := sha256.Sum256(append([]byte(domain+"\x00"), data...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func cloneConfirmation[T any](value T) (T, error) {
	var cloned T
	data, err := json.Marshal(value)
	if err != nil {
		return cloned, err
	}
	if err := json.Unmarshal(data, &cloned); err != nil {
		return cloned, fmt.Errorf("clone role confirmation: %w", err)
	}
	return cloned, nil
}

func confirmationEqual(left, right any) bool {
	first, err := json.Marshal(left)
	if err != nil {
		return false
	}
	second, err := json.Marshal(right)
	return err == nil && bytes.Equal(first, second)
}
