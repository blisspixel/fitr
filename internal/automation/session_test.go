package automation

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/autoruntime"
	"github.com/blisspixel/fitr/internal/capacity"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/role"
)

func sessionTime() time.Time          { return time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC) }
func sessionHash(value string) string { return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(value))) }

// These are typed state-machine fixtures, not claims of measured quality. The
// role package independently verifies real canonical evidence before adoption.
func sessionPlan(t *testing.T) Plan {
	t.Helper()
	id, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	spec := feasibleRole()
	revision, _ := spec.Digest()
	directory := t.TempDir()
	plan := Plan{ID: id, Mode: "establish", Adoption: "manual", Spec: spec, RoleRevision: revision,
		LifecycleSHA256: sessionHash("lifecycle"), Runtime: autoruntime.Spec{Schema: autoruntime.SpecSchema,
			Executable: filepath.Join(directory, "ollama"), ModelStore: filepath.Join(directory, "models"),
			ExecutableSHA256: sessionHash("runtime"), LibrariesSHA256: sessionHash("libraries"), RuntimeVersion: "0.17.0", NumCtx: 4096, KVCacheType: "f16"},
		SoftwareSHA256: sessionHash("software"), TaskSetSHA256: sessionHash("tasks"), SpecSHA256: sessionHash("spec"),
		Profile: "test-profile", DeviceSHA256: sessionHash("device"), Repeats: 3, SeedSet: "auto-exploration", EnvelopeSHA256: sessionHash("envelope"),
		PointRequests: 4, PointRequestedOutputTokens: 40, Limits: Limits{MaxRequests: 16, MaxRequestedOutputTokens: 160,
			MaxPoints: 4, WallSeconds: 3600, ConfirmationWallSeconds: 1200}}
	plan.Provenance = record.RunProvenance{TaskSetSHA256: plan.TaskSetSHA256, SpecSHA256: plan.SpecSHA256,
		ProfileSHA256: sessionHash("profile"), ScoringPolicySHA256: sessionHash("scoring"), FitrVersion: "test",
		SoftwareBuildSHA256: plan.SoftwareSHA256, BackendProtocol: record.BackendProtocolOllama}
	for _, name := range []string{"first", "second"} {
		plan.Candidates = append(plan.Candidates, Candidate{ID: name, Model: name + ":latest", ArtifactDigest: sessionHash(name), ModelConfigurationSHA256: sessionHash(name + "-configuration")})
	}
	if err := plan.Seal(sessionTime()); err != nil {
		t.Fatal(err)
	}
	return plan
}

func resealJournal(t *testing.T, journal *Journal) {
	t.Helper()
	previous := journal.Plan.SHA256
	for index := range journal.Events {
		event := &journal.Events[index]
		event.Sequence, event.Previous, event.SHA256 = index+1, previous, ""
		if event.At == "" {
			event.At = sessionTime().Add(time.Second).Format(time.RFC3339Nano)
		}
		var err error
		event.SHA256, err = digest(*event)
		if err != nil {
			t.Fatal(err)
		}
		previous = event.SHA256
	}
}

func sessionPointEvents(phase string, point int) []Event {
	runID := fmt.Sprintf("%s-run-%d", phase, point)
	return []Event{
		{Action: "point_started", Phase: phase, Point: point, RunID: runID},
		{Action: "request_reserved", Phase: phase, Point: point, RunID: runID, Kind: "generate", RequestedOutputTokens: 10},
		{Action: "point_completed", Phase: phase, Point: point, RunID: runID, EvidenceSHA256: sessionHash(runID)},
	}
}

func sessionStoreRef(purpose string) *record.ManagedStoreRef {
	return &record.ManagedStoreRef{Schema: record.ManagedStoreRefSchema, ID: purpose, SealSHA256: sessionHash(purpose)}
}

func explorationJournal(t *testing.T) Journal {
	t.Helper()
	journal := Journal{Schema: JournalSchema, Plan: sessionPlan(t)}
	for point := 1; point <= len(journal.Plan.Candidates); point++ {
		journal.Events = append(journal.Events, sessionPointEvents("exploration", point)...)
	}
	journal.Events = append(journal.Events, Event{Action: "exploration_closed", StoreRef: sessionStoreRef("exploration")})
	resealJournal(t, &journal)
	if _, err := journal.Replay(); err != nil {
		t.Fatal(err)
	}
	return journal
}

func syntheticConfirmation(t *testing.T, plan Plan) role.ConfirmationPlan {
	t.Helper()
	seed := "role-confirm-" + strings.Repeat("1", 32)
	checks := []eval.CheckSpec{{ID: "json", Family: "json_object", Need: "structured_output", Origin: "builtin"},
		{ID: "csv", Family: "csv_strict", Need: "structured_output", Origin: "builtin"}}
	checkDigest, err := record.FixedCheckPlanSHA256(checks, plan.Repeats, seed)
	if err != nil {
		t.Fatal(err)
	}
	confirmation := role.ConfirmationPlan{Schema: role.ConfirmationPlanSchema, Spec: plan.Spec, SpecSHA256: plan.RoleRevision,
		SeedSet: seed, CreatedAt: sessionTime().Add(time.Second).Format(time.RFC3339Nano), ExpiresAt: sessionTime().Add(24*time.Hour + time.Second).Format(time.RFC3339Nano),
		PreferencePolicy: role.ConfirmationPreferencePolicy, RelativeWeightSensitivity: 0.2,
		Protocol: role.ConfirmationProtocol{DeviceKey: "device", ComparabilityKey: "comparison", Profile: plan.Profile,
			Level: "full", ExecutionPolicy: record.ExecutionDisabled, RequestedContext: plan.Runtime.NumCtx, EffectiveContext: plan.Runtime.NumCtx,
			Repeats: plan.Repeats, Provenance: plan.Provenance, TaskPlan: record.TaskPlan{SpeedSamples: plan.Repeats, Memory: true,
				CheckTrialsLimit: len(checks) * plan.Repeats, CheckPlanSHA256: checkDigest}}}
	for round := range plan.Repeats {
		for _, check := range checks {
			confirmation.Protocol.Checks = append(confirmation.Protocol.Checks, role.ConfirmationCheck{
				TaskID: check.ID, Family: check.Family, Need: check.Need, Origin: check.Origin, Seed: eval.InstanceSeed(seed, check.ID, round)})
		}
	}
	confirmation.Candidates = syntheticCandidates(t, plan)
	confirmation.ChosenEvidenceSHA256 = confirmation.Candidates[0].EvidenceSHA256
	resealConfirmation(t, &confirmation)
	return confirmation
}

func syntheticCandidates(t *testing.T, plan Plan) []role.ConfirmationCandidate {
	t.Helper()
	profile, launch, err := plan.Runtime.ProfileDigests()
	if err != nil {
		t.Fatal(err)
	}
	var candidates []role.ConfirmationCandidate
	budget := int64(16 << 30)
	for index, candidate := range plan.Candidates {
		identity, err := record.NewModelIdentity(candidate.Model, candidate.Model, "ollama", plan.Runtime.RuntimeVersion, candidate.ArtifactDigest, "", 1024)
		if err != nil {
			t.Fatal(err)
		}
		runID := fmt.Sprintf("exploration-run-%d", index+1)
		candidates = append(candidates, role.ConfirmationCandidate{Model: identity, RunID: runID, EvidenceSHA256: sessionHash(runID),
			SeedSet: plan.SeedSet, StartedAt: plan.CreatedAt, Capacity: role.ConfirmationCapacity{ResourceDomain: capacity.DomainHost,
				OperatorBudgetBytes: &budget, ObservedResidentBytes: 1024, ObservedDomainBytes: 1024,
				AttributionSource: "memory.verified_allocation_at_requested_context", Source: "role_capacity_floor"},
			RuntimeBinding: &record.RuntimeBinding{Schema: record.RuntimeBindingSchema, Kind: "owned_ollama", ProfileSHA256: profile,
				ExecutableSHA256: plan.Runtime.ExecutableSHA256, LaunchConfigurationSHA256: launch, ModelConfigurationSHA256: candidate.ModelConfigurationSHA256,
				ArtifactDigest: candidate.ArtifactDigest, RuntimeVersion: plan.Runtime.RuntimeVersion, OwnershipSHA256: sessionHash("owned-child")}})
	}
	return candidates
}

func resealConfirmation(t *testing.T, plan *role.ConfirmationPlan) {
	t.Helper()
	plan.PlanSHA256 = ""
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	plan.PlanSHA256 = sessionHash(role.ConfirmationPlanSchema + "\x00" + string(data))
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
}

func confirmationJournal(t *testing.T, complete bool) Journal {
	t.Helper()
	journal := explorationJournal(t)
	confirmation := syntheticConfirmation(t, journal.Plan)
	journal.Events = append(journal.Events, Event{Action: "confirmation_started", Confirmation: &confirmation})
	if complete {
		for point := 1; point <= len(journal.Plan.Candidates); point++ {
			journal.Events = append(journal.Events, sessionPointEvents("confirmation", point)...)
		}
		journal.Events = append(journal.Events, Event{Action: "confirmation_closed", StoreRef: sessionStoreRef("confirmation")})
	}
	resealJournal(t, &journal)
	if _, err := journal.Replay(); err != nil {
		t.Fatal(err)
	}
	return journal
}

func TestSessionProtectsConfirmationAcrossRequestTokenAndTimeLimits(t *testing.T) {
	for _, phase := range []string{"exploration", "confirmation"} {
		for _, limit := range []string{"requests", "tokens", "time"} {
			t.Run(phase+"/"+limit, func(t *testing.T) { checkSessionBudget(t, phase, limit) })
		}
	}
}

func TestSessionExplorationClosureAndComparisonCannotSpendProtectedTime(t *testing.T) {
	journal := explorationJournal(t)
	cutoff := (State{Phase: "exploration"}).Deadline(journal.Plan)
	journal.Events[len(journal.Events)-1].At = cutoff.Format(time.RFC3339Nano)
	resealJournal(t, &journal)
	if _, err := journal.Replay(); !errors.Is(err, ErrBudget) {
		t.Fatalf("late exploration closed into reserved confirmation time: %v", err)
	}
	journal = confirmationJournal(t, false)
	journal.Events[len(journal.Events)-1].At = cutoff.Format(time.RFC3339Nano)
	resealJournal(t, &journal)
	if _, err := journal.Replay(); !errors.Is(err, ErrBudget) {
		t.Fatalf("late comparison spent protected confirmation time: %v", err)
	}
}

func checkSessionBudget(t *testing.T, phase, limit string) {
	t.Helper()
	plan := sessionPlan(t)
	state := State{Phase: phase, LastObservedAt: sessionTime(), ActivePoint: 1, ActiveRunID: "test-run"}
	reserveRequests := int64(len(plan.Candidates)) * plan.PointRequests
	reserveTokens := int64(len(plan.Candidates)) * plan.PointRequestedOutputTokens
	if limit == "requests" {
		state.Requests = plan.Limits.MaxRequests
		if phase == "exploration" {
			state.Requests = plan.Limits.MaxRequests - reserveRequests
			state.ExplorationRequests = state.Requests
		}
	}
	if limit == "tokens" {
		state.RequestedOutputTokens = plan.Limits.MaxRequestedOutputTokens
		if phase == "exploration" {
			state.RequestedOutputTokens -= reserveTokens
			state.ExplorationRequestedOutputTokens = state.RequestedOutputTokens
		}
	}
	at := sessionTime().Add(time.Second)
	if limit == "time" {
		at = state.Deadline(plan)
	}
	err := state.apply(plan, Event{Action: "request_reserved", Phase: phase, Point: 1, RunID: "test-run", Kind: "chat", RequestedOutputTokens: 1, At: at.Format(time.RFC3339Nano)})
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("protected %s budget error = %v", limit, err)
	}
}

func TestSessionReplayRejectsRehashedInvalidTransitions(t *testing.T) {
	cases := map[string][]Event{
		"point out of order":         {{Action: "point_started", Phase: "exploration", Point: 2, RunID: "test-run"}},
		"request without point":      {{Action: "request_reserved", Phase: "exploration", Point: 1, RunID: "test-run", Kind: "generate", RequestedOutputTokens: 1}},
		"adopt without confirmation": {{Action: "finished", Outcome: "adopted"}},
		"finish before comparison":   {{Action: "finished", Outcome: "no_qualified"}},
		"restart terminal":           {{Action: "finished", Outcome: "cancelled"}, {Action: "point_started", Phase: "exploration", Point: 1, RunID: "test-run"}},
		"field smuggling":            {{Action: "finished", Outcome: "failed", RequestedOutputTokens: 5}},
		"no inference completion":    {{Action: "point_started", Phase: "exploration", Point: 1, RunID: "test-run"}, {Action: "point_completed", Phase: "exploration", Point: 1, RunID: "test-run", EvidenceSHA256: sessionHash("fake")}},
		"unsupported outcome":        {{Action: "finished", Outcome: "qualified"}},
	}
	for name, events := range cases {
		t.Run(name, func(t *testing.T) {
			journal := Journal{Schema: JournalSchema, Plan: sessionPlan(t), Events: events}
			resealJournal(t, &journal)
			if _, err := journal.Replay(); err == nil {
				t.Fatal("invalid rehashed transition accepted")
			}
		})
	}
}

func TestSessionConfirmationIdentityCannotTransferBetweenPlans(t *testing.T) {
	for _, field := range []string{"artifact", "profile", "context", "provenance", "configuration", "seed", "unowned", "owned-profile", "launch"} {
		t.Run(field, func(t *testing.T) {
			journal := confirmationJournal(t, false)
			confirmation := journal.Events[len(journal.Events)-1].Confirmation
			switch field {
			case "artifact":
				confirmation.Candidates[0].Model.Value = sessionHash("other")
				confirmation.Candidates[0].RuntimeBinding.ArtifactDigest = sessionHash("other")
			case "profile":
				confirmation.Protocol.Profile = "other-profile"
			case "context":
				confirmation.Protocol.RequestedContext, confirmation.Protocol.EffectiveContext = 8192, 8192
			case "provenance":
				confirmation.Protocol.Provenance.TaskSetSHA256 = sessionHash("other")
			case "configuration":
				confirmation.Candidates[0].RuntimeBinding.ModelConfigurationSHA256 = sessionHash("other")
			case "seed":
				confirmation.Candidates[0].SeedSet = "other-exploration"
			case "unowned":
				for index := range confirmation.Candidates {
					confirmation.Candidates[index].RuntimeBinding = nil
				}
			case "owned-profile", "launch":
				for index := range confirmation.Candidates {
					binding := confirmation.Candidates[index].RuntimeBinding
					if field == "launch" {
						binding.LaunchConfigurationSHA256 = sessionHash("other-launch")
					} else {
						binding.ProfileSHA256 = sessionHash("other-profile")
					}
				}
			}
			resealConfirmation(t, confirmation)
			resealJournal(t, &journal)
			if _, err := journal.Replay(); err == nil {
				t.Fatal("different valid confirmation identity entered the session")
			}
		})
	}
}

func TestSessionConfirmationCannotRestartAndAdoptionNeedsCompletedSchedule(t *testing.T) {
	journal := confirmationJournal(t, false)
	journal.Events = append(journal.Events, journal.Events[len(journal.Events)-1])
	resealJournal(t, &journal)
	if _, err := journal.Replay(); err == nil {
		t.Fatal("second confirmation attempt accepted")
	}
	journal = confirmationJournal(t, true)
	journal.Events = append(journal.Events, Event{Action: "finished", Outcome: "adopted"})
	resealJournal(t, &journal)
	state, err := journal.Replay()
	if err != nil || state.Outcome != "adopted" || len(state.CompletedConfirmation) != 2 {
		t.Fatalf("state=%+v error=%v", state, err)
	}
}

func TestSessionReserveRejectsCancellationAndAnotherModel(t *testing.T) {
	session := createdSession(t)
	if err := session.Append(Event{Action: "point_started", Phase: "exploration", Point: 1, RunID: "test-run"}, sessionTime().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	request := ollama.InferenceRequest{Kind: "generate", Model: session.journal.Plan.Candidates[0].Model, MaxOutputTokens: 10}
	if _, err := session.Reserve(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	request.Model = "another:latest"
	if _, err := session.Reserve(t.Context(), request); err == nil {
		t.Fatal("another model admitted")
	}
	_, state, err := session.Snapshot()
	if err != nil || state.Requests != 0 {
		t.Fatalf("rejected request consumed state: %+v, %v", state, err)
	}
}
