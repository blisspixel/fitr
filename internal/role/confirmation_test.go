package role

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/capacity"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/score"
	"github.com/blisspixel/fitr/internal/stats"
)

// roleConfirmationFixture also serves lifecycle tests. Points have fresh
// signed evidence but are not persisted; the caller owns its record store.
func roleConfirmationFixture(t *testing.T) (ConfirmationPlan, []*record.Record, time.Time) {
	t.Helper()
	now := roleReviewNow
	sources := []*record.Record{
		roleConfirmationRecord(t, "fast", 80, 8, now.Add(-time.Hour)),
		roleConfirmationRecord(t, "slow", 20, 8, now.Add(-time.Hour)),
	}
	plan, err := NewConfirmationPlan(roleReviewSpec(), sources, sources[0].Completion.EvidenceSHA256, now)
	if err != nil {
		t.Fatal(err)
	}
	points := make([]*record.Record, len(sources))
	for index, source := range sources {
		result := roleConfirmationClone(t, source)
		result.StartedAt, result.SeedSet = now.Add(time.Minute).Format(time.RFC3339), plan.SeedSet
		binding := ConfirmationPlanBinding(plan, index+1)
		result.Experiment, result.TaskPlan = &binding, plan.Protocol.TaskPlan
		for trial, check := range plan.Protocol.Checks {
			result.Checks[trial].Seed = check.Seed
		}
		result.CapacityPlan = roleConfirmationCapacityPlan(t, result, plan.Candidates[index].Capacity)
		points[index] = roleConfirmationReseal(t, result)
	}
	return plan, points, now.Add(2 * time.Minute)
}

func roleConfirmationRecord(t *testing.T, model string, speed float64, passes int, started time.Time) *record.Record {
	t.Helper()
	result := roleReviewRecord(t, model, speed, passes, 8192, "runtime-1", started)
	result.Level, result.SeedSet = "full", "role-exploration"
	result.Checks = nil
	for round := range result.Repeats {
		for index := range 8 {
			id := fmt.Sprintf("check-%d", index)
			outcome := eval.OutcomeFail
			if index < passes {
				outcome = eval.OutcomePass
			}
			result.Checks = append(result.Checks, eval.CheckOutcome{TaskID: id, Family: fmt.Sprintf("family-%d", index),
				Need: "structured_output", Origin: "builtin", Seed: eval.InstanceSeed(result.SeedSet, id, round), Pass: index < passes, Outcome: outcome})
		}
	}
	result.TaskPlan.CheckTrialsLimit = len(result.Checks)
	var err error
	result.TaskPlan.CheckPlanSHA256, err = record.ObservedCheckPlanSHA256(result.Checks)
	if err != nil {
		t.Fatal(err)
	}
	return roleConfirmationReseal(t, result)
}

func roleConfirmationReseal(t *testing.T, result *record.Record) *record.Record {
	t.Helper()
	result = roleConfirmationClone(t, result)
	profile, identity, provenance := result.Completion.Profile, result.Manifest.Model, *result.Manifest.Provenance
	result.Manifest, result.Completion, result.RunID = nil, nil, ""
	var err error
	result.EvidenceCounts, err = result.DeriveEvidenceCounts()
	if err != nil {
		t.Fatal(err)
	}
	result.Scorecard = score.Score(result.Measured(), profile)
	if err := result.AttachManifest(identity, provenance); err != nil {
		t.Fatal(err)
	}
	if err := result.CompleteEvidence(profile); err != nil {
		t.Fatal(err)
	}
	if issue := result.EvidenceIntegrityIssue(); issue != "" {
		t.Fatal(issue)
	}
	return result
}

func roleConfirmationCapacityPlan(t *testing.T, result *record.Record, control ConfirmationCapacity) *capacity.Plan {
	t.Helper()
	domain := control.ResourceDomain
	if domain == "" {
		domain = capacity.DomainAccelerator
	}
	policy, err := capacity.BuildPolicy(capacity.PolicyInput{ResourceDomain: domain,
		OperatorBudgetBytes: control.OperatorBudgetBytes, OperatorReserveBytes: control.OperatorReserveBytes})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := policy.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return &capacity.Plan{Schema: capacity.PlanSchema, Policy: policy, Prediction: capacity.Prediction{
		Schema: capacity.PredictionSchema, CreatedAt: result.StartedAt, ArtifactSHA256: result.Manifest.Model.Value,
		ResourceDomain: domain, RequestedContext: result.ContextSize(), PlacementAssumption: "runtime placement",
		State: capacity.PredictionUnavailable, Missing: []string{"architecture"}, Excluded: []string{"runtime buffers"}, PolicySHA256: digest,
	}}
}

func roleConfirmationClone[T any](t *testing.T, value T) T {
	t.Helper()
	cloned, err := cloneConfirmation(value)
	if err != nil {
		t.Fatal(err)
	}
	return cloned
}

func TestRoleConfirmationRoundTripAndCopies(t *testing.T) {
	plan, points, now := roleConfirmationFixture(t)
	bundle, err := NewConfirmationBundle(plan, points, now)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Report.State != "confirmed" || bundle.Report.Scope != ConfirmationScope || bundle.Report.WinnerEvidenceSHA256 != plan.ChosenEvidenceSHA256 {
		t.Fatalf("unexpected report: %+v", bundle.Report)
	}
	points[0].Checks[0].Pass = false
	*plan.Spec.Decision.Requirements[0].Behavior.MinimumRate = 0.99
	if _, err := bundle.Validate(); err != nil {
		t.Fatalf("input mutation changed bundle: %v", err)
	}
	data, err := bundle.JSON()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "confirmation.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfirmationBundle(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Report.State = "unexpected-winner"
	if _, err := loaded.Validate(); err == nil {
		t.Fatal("modified report accepted")
	}
}

func TestRoleConfirmationPlanRejectsPolicyAndCandidateMutation(t *testing.T) {
	plan, _, _ := roleConfirmationFixture(t)
	mutations := map[string]func(*ConfirmationPlan){
		"schema":      func(p *ConfirmationPlan) { p.Schema = "other" },
		"weight":      func(p *ConfirmationPlan) { p.Spec.Preferences[0].Weight = 2 },
		"anchor":      func(p *ConfirmationPlan) { p.Spec.Preferences[0].Best = 200 },
		"sensitivity": func(p *ConfirmationPlan) { p.RelativeWeightSensitivity = 0.1 },
		"policy":      func(p *ConfirmationPlan) { p.PreferencePolicy = "other" },
		"choice":      func(p *ConfirmationPlan) { p.ChosenEvidenceSHA256 = p.Candidates[1].EvidenceSHA256 },
		"artifact":    func(p *ConfirmationPlan) { p.Candidates[0].Model.Value = p.Candidates[1].Model.Value },
		"order":       func(p *ConfirmationPlan) { p.Candidates[0], p.Candidates[1] = p.Candidates[1], p.Candidates[0] },
		"build":       func(p *ConfirmationPlan) { p.Protocol.Provenance.FitrVersion = "other" },
		"seed":        func(p *ConfirmationPlan) { p.SeedSet = p.Candidates[0].SeedSet },
		"schedule":    func(p *ConfirmationPlan) { p.Protocol.Checks[0].Seed++ },
		"denominator": func(p *ConfirmationPlan) { p.Protocol.TaskPlan.CheckTrialsLimit-- },
		"created":     func(p *ConfirmationPlan) { p.CreatedAt = "bad" },
		"expiry":      func(p *ConfirmationPlan) { p.ExpiresAt = p.CreatedAt },
		"capacity":    func(p *ConfirmationPlan) { *p.Candidates[0].Capacity.OperatorBudgetBytes++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := roleConfirmationClone(t, plan)
			mutate(&changed)
			if changed.Validate() == nil {
				t.Fatal("changed plan accepted")
			}
		})
	}
}

func TestRoleConfirmationPreparedPointRejectsChangedProtocol(t *testing.T) {
	plan, points, _ := roleConfirmationFixture(t)
	point := points[0]
	if err := ValidatePreparedConfirmationPoint(plan, 1, point, point.Manifest.Model, *point.Manifest.Provenance); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*record.Record){
		"binding":   func(r *record.Record) { r.Experiment.PointIndex = 2 },
		"context":   func(r *record.Record) { r.NumCtx++ },
		"profile":   func(r *record.Record) { r.Profile = "other" },
		"seed":      func(r *record.Record) { r.SeedSet = "old" },
		"repeats":   func(r *record.Record) { r.Repeats++ },
		"level":     func(r *record.Record) { r.Level = "quick" },
		"device":    func(r *record.Record) { r.Device.Host = "other" },
		"execution": func(r *record.Record) { r.ExecutionPolicy = record.ExecutionUnsafe },
		"task plan": func(r *record.Record) { r.TaskPlan.CheckTrialsLimit-- },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := roleConfirmationClone(t, point)
			mutate(changed)
			if ValidatePreparedConfirmationPoint(plan, 1, changed, point.Manifest.Model, *point.Manifest.Provenance) == nil {
				t.Fatal("changed preparation accepted")
			}
		})
	}
	if ValidatePreparedConfirmationPoint(plan, 0, point, point.Manifest.Model, *point.Manifest.Provenance) == nil {
		t.Fatal("zero point index accepted")
	}
	if ValidatePreparedConfirmationPoint(plan, 1, point, points[1].Manifest.Model, *point.Manifest.Provenance) == nil {
		t.Fatal("different artifact accepted")
	}
	provenance := *point.Manifest.Provenance
	provenance.SoftwareBuildSHA256 = plan.Candidates[0].EvidenceSHA256
	if ValidatePreparedConfirmationPoint(plan, 1, point, point.Manifest.Model, provenance) == nil {
		t.Fatal("new build accepted")
	}
}

func TestRoleConfirmationFreshnessAndIncomplete(t *testing.T) {
	plan, points, now := roleConfirmationFixture(t)
	for _, partial := range [][]*record.Record{nil, points[:1], {nil, points[1]}} {
		report, err := AnalyzeConfirmation(plan, partial, now)
		if err != nil || report.State != "incomplete" || report.WinnerEvidenceSHA256 != "" {
			t.Fatalf("partial report: %+v, %v", report, err)
		}
	}
	for _, moment := range []time.Time{{}, now.Add(-time.Hour), now.Add(25 * time.Hour)} {
		if _, err := AnalyzeConfirmation(plan, points, moment); err == nil {
			t.Fatal("invalid analysis time accepted")
		}
	}
	for _, moment := range []time.Time{now.Add(-time.Hour), now.Add(time.Hour)} {
		changed := roleConfirmationClone(t, points)
		changed[0].StartedAt = moment.Format(time.RFC3339)
		changed[0] = roleConfirmationReseal(t, changed[0])
		if _, err := AnalyzeConfirmation(plan, changed, now); err == nil {
			t.Fatal("invalid run time accepted")
		}
	}
	if _, err := AnalyzeConfirmation(plan, append(points, points[0]), now); err == nil {
		t.Fatal("extra point accepted")
	}
}

func TestRoleConfirmationDecisionOutcomes(t *testing.T) {
	for _, scenario := range []struct {
		name, want string
		speeds     [2]float64
		passes     [2]int
	}{
		{"confirmed", "confirmed", [2]float64{80, 20}, [2]int{8, 8}},
		{"overlap", "overlap", [2]float64{50, 50}, [2]int{8, 8}},
		{"unexpected", "unexpected-winner", [2]float64{20, 80}, [2]int{8, 8}},
		{"fast fails quality", "unexpected-winner", [2]float64{100, 20}, [2]int{0, 8}},
		{"none qualified", "no-qualified-candidate", [2]float64{80, 20}, [2]int{0, 0}},
		{"uncertain quality", "unresolved", [2]float64{80, 20}, [2]int{4, 8}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			plan, points, now := roleConfirmationFixture(t)
			for index, point := range points {
				roleConfirmationMeasurements(point, scenario.speeds[index], scenario.passes[index])
				points[index] = roleConfirmationReseal(t, point)
			}
			report, err := AnalyzeConfirmation(plan, points, now)
			if err != nil || report.State != scenario.want {
				t.Fatalf("report = %+v, %v", report, err)
			}
		})
	}
}

func roleConfirmationMeasurements(point *record.Record, speed float64, passes int) {
	for index := range point.Speed {
		point.Speed[index].DecodeTPS = speed
	}
	point.DecodeSum = stats.MeanSD([]float64{speed, speed, speed})
	for index := range point.Checks {
		point.Checks[index].Pass = index%8 < passes
		point.Checks[index].Outcome = eval.OutcomeFail
		if index%8 < passes {
			point.Checks[index].Outcome = eval.OutcomePass
		}
	}
}

func TestRoleConfirmationNextCycleAndPlanDeepCopy(t *testing.T) {
	plan, points, now := roleConfirmationFixture(t)
	next, err := NewConfirmationPlan(plan.Spec, points, points[0].Completion.EvidenceSHA256, now)
	if err != nil {
		t.Fatal(err)
	}
	if next.SeedSet == plan.SeedSet || next.Candidates[0].Experiment.PlanSHA256 != plan.PlanSHA256 {
		t.Fatal("next cycle lost fresh lineage")
	}
	points[0].Experiment.PlanSHA256 = "changed"
	plan.Spec.Preferences[0].Weight = 7
	if err := next.Validate(); err != nil {
		t.Fatalf("caller mutation affected new plan: %v", err)
	}
	if _, err := AnalyzeConfirmation(next, points, now); err == nil {
		t.Fatal("prior confirmation reused as fresh evidence")
	}
}

func TestRoleConfirmationStrictBundleInput(t *testing.T) {
	plan, points, now := roleConfirmationFixture(t)
	bundle, err := NewConfirmationBundle(plan, points, now)
	if err != nil {
		t.Fatal(err)
	}
	data, err := bundle.JSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range [][]byte{
		[]byte(`{"schema":"x","schema":"y"}`), []byte(`{"unknown":true}`),
		append(append([]byte{}, data...), []byte(` {}`)...), []byte(`{`),
		[]byte(strings.Replace(string(data), `"scope": "battery_screening"`, `"scope": "workflow_competence"`, 1)),
	} {
		if _, err := decodeConfirmationBundle(invalid); err == nil {
			t.Fatal("invalid bundle accepted")
		}
	}
	if _, err := LoadConfirmationBundle(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing file accepted")
	}
	if _, err := confirmationDigest("test", math.NaN()); err == nil {
		t.Fatal("nonfinite digest accepted")
	}
	if _, err := cloneConfirmation(math.NaN()); err == nil {
		t.Fatal("nonfinite clone accepted")
	}
	if confirmationEqual(math.NaN(), math.NaN()) {
		t.Fatal("nonfinite values compare equal")
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
}

func TestRoleConfirmationRejectsInvalidExploration(t *testing.T) {
	valid := func(t *testing.T) []*record.Record {
		t.Helper()
		return []*record.Record{
			roleConfirmationRecord(t, "first", 80, 8, roleReviewNow.Add(-time.Hour)),
			roleConfirmationRecord(t, "second", 20, 8, roleReviewNow.Add(-time.Hour)),
		}
	}
	for _, scenario := range []string{"nil", "one", "duplicate", "stale", "future", "unbound", "new runtime", "quick", "legacy schedule", "not lead", "different software"} {
		t.Run(scenario, func(t *testing.T) {
			points := valid(t)
			chosen := points[0].Completion.EvidenceSHA256
			switch scenario {
			case "nil":
				points[1] = nil
			case "one":
				points = points[:1]
			case "duplicate":
				points[1] = points[0]
			case "stale":
				points[0].StartedAt = roleReviewNow.Add(-31 * 24 * time.Hour).Format(time.RFC3339)
			case "future":
				points[0].StartedAt = roleReviewNow.Add(time.Hour).Format(time.RFC3339)
			case "unbound":
				points[0].Manifest.Model.Binding = "declared"
			case "new runtime":
				points[1].Device.Runtime = "other"
			case "quick":
				points[0].Level = "quick"
			case "legacy schedule":
				points[0].Checks[0].Seed++
			case "not lead":
				chosen = points[1].Completion.EvidenceSHA256
			case "different software":
				points[1].Manifest.Provenance.FitrVersion = "other"
			}
			if _, err := NewConfirmationPlan(roleReviewSpec(), points, chosen, roleReviewNow); err == nil {
				t.Fatal("invalid exploration accepted")
			}
		})
	}
	points := valid(t)
	spec := roleReviewSpec()
	spec.Preferences[0].Weight = math.NaN()
	if _, err := NewConfirmationPlan(spec, points, points[0].Completion.EvidenceSHA256, roleReviewNow); err == nil {
		t.Fatal("invalid spec accepted")
	}
}

func TestRoleConfirmationProtocolValidationBranches(t *testing.T) {
	plan, _, _ := roleConfirmationFixture(t)
	for name, mutate := range map[string]func(*ConfirmationProtocol){
		"empty device":      func(p *ConfirmationProtocol) { p.DeviceKey = "" },
		"empty provenance":  func(p *ConfirmationProtocol) { p.Provenance.TaskSetSHA256 = "" },
		"adaptive":          func(p *ConfirmationProtocol) { p.TaskPlan.AdaptiveChecks = true },
		"no memory":         func(p *ConfirmationProtocol) { p.TaskPlan.Memory = false },
		"negative count":    func(p *ConfirmationProtocol) { p.TaskPlan.CodeTrials = -1 },
		"few repeats":       func(p *ConfirmationProtocol) { p.Repeats = 2 },
		"oversized context": func(p *ConfirmationProtocol) { p.RequestedContext = 16*1024*1024 + 1 },
		"check identity":    func(p *ConfirmationProtocol) { p.Checks[0].TaskID = "" },
		"duplicate check":   func(p *ConfirmationProtocol) { p.Checks[1] = p.Checks[0] },
		"unsealed refusal":  func(p *ConfirmationProtocol) { p.TaskPlan.RefusalTrials = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			protocol := roleConfirmationClone(t, plan.Protocol)
			mutate(&protocol)
			if protocol.validate(plan.SeedSet) == nil {
				t.Fatal("invalid protocol accepted")
			}
		})
	}
}

func TestRoleConfirmationCapacityControlsAndFreshObservations(t *testing.T) {
	plan, points, _ := roleConfirmationFixture(t)
	if err := ValidateConfirmationCapacityPoint(plan, 1, points[0]); err != nil {
		t.Fatal(err)
	}
	if err := ValidateConfirmationCapacityPoint(plan, 1, nil); err == nil {
		t.Fatal("missing policy accepted")
	}
	for _, scenario := range []string{"budget", "domain", "old available", "old container", "invalid policy"} {
		t.Run(scenario, func(t *testing.T) {
			plan := roleConfirmationClone(t, plan)
			changed := roleConfirmationClone(t, points[0])
			policy := &changed.CapacityPlan.Policy
			switch scenario {
			case "budget":
				*policy.OperatorBudgetBytes--
			case "domain":
				plan.Candidates[0].Capacity.ResourceDomain = capacity.DomainHost
				plan.PlanSHA256 = ""
				plan.PlanSHA256, _ = confirmationDigest(ConfirmationPlanSchema, plan)
			case "old available":
				policy.CurrentAvailable = &capacity.MemoryObservation{Kind: capacity.ObservationCurrentAvailable,
					ResourceDomain: capacity.DomainAccelerator, Bytes: 16 << 30, Source: "probe", ObservedAt: roleReviewNow.Add(-time.Hour).Format(time.RFC3339)}
			case "old container":
				policy.Container = &capacity.ContainerReceipt{ResourceDomain: capacity.DomainAccelerator,
					LimitBytes: 16 << 30, CurrentBytes: 0, HeadroomBytes: 16 << 30, Source: "probe", ObservedAt: roleReviewNow.Add(-time.Hour).Format(time.RFC3339)}
			case "invalid policy":
				policy.Schema = "other"
			}
			if scenario == "old available" || scenario == "old container" {
				digest, err := policy.Digest()
				if err != nil {
					t.Fatal(err)
				}
				changed.CapacityPlan.Prediction.PolicySHA256 = digest
			}
			if ValidateConfirmationCapacityPoint(plan, 1, changed) == nil {
				t.Fatal("changed or stale policy accepted")
			}
		})
	}
}

func TestRoleConfirmationExplicitFailureAndMissingBounds(t *testing.T) {
	plan, points, now := roleConfirmationFixture(t)
	points[0].Checks[0].Pass = false
	points[0].Checks[0].Outcome = eval.OutcomeError
	points[0].Checks[0].Detail = "transport failed"
	report, err := AnalyzeConfirmation(plan, points, now)
	if err == nil || report.WinnerEvidenceSHA256 != "" {
		t.Fatalf("infrastructure failure became confirmation: %+v, %v", report, err)
	}
	// The substrate rejects infrastructure errors as ranking evidence. The
	// attempt owner records failure; the analyzer never repairs the receipt.
	points[0].EvidenceCounts["checks"] = eval.OutcomeCounts{Expected: 24, Errors: 1}
	if confirmationObservationState(points) != "failed" {
		t.Fatal("error counts lost failure state")
	}
	plan, points, now = roleConfirmationFixture(t)
	report, err = AnalyzeConfirmation(plan, points, now)
	if err != nil {
		t.Fatal(err)
	}
	report.Candidates[0].State, report.Candidates[0].Preference = "eligible", nil
	selectConfirmationWinner(&report, plan)
	if report.State != "unresolved" {
		t.Fatalf("missing preference bounds: %+v", report)
	}
}

func TestRoleConfirmationAllocationDomains(t *testing.T) {
	for _, scenario := range []struct {
		name                   string
		domain                 capacity.ResourceDomain
		accelerator, remainder int64
	}{
		{"accelerator", capacity.DomainAccelerator, 6 << 30, 0},
		{"partial", capacity.DomainAccelerator, 3 << 30, 3 << 30},
		{"host", capacity.DomainHost, 0, 0},
		{"unified", capacity.DomainUnified, 6 << 30, 0},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			point := roleConfirmationRecord(t, "model", 80, 8, roleReviewNow.Add(-time.Hour))
			point.Memory.AcceleratorBytes = scenario.accelerator
			point.Memory.PctOnGPU = int(scenario.accelerator * 100 / point.Memory.ResidentBytes)
			if scenario.domain == capacity.DomainHost {
				point.Device.GPU, point.Device.InferenceDevice = "", "CPU"
			}
			if scenario.domain == capacity.DomainUnified {
				point.Device.GPU = "Apple M3 Pro"
			}
			allocation, err := confirmationCapacityFrom(roleReviewSpec(), point)
			if err != nil {
				t.Fatal(err)
			}
			if allocation.ResourceDomain != scenario.domain || allocation.HostRemainderBytes != scenario.remainder ||
				allocation.ObservedDomainBytes+allocation.HostRemainderBytes != allocation.ObservedResidentBytes {
				t.Fatalf("allocation = %+v", allocation)
			}
		})
	}
	point := roleConfirmationRecord(t, "model", 80, 8, roleReviewNow.Add(-time.Hour))
	point.Memory.EffectiveCtx = nil
	if _, err := confirmationAllocationFrom(point); err == nil {
		t.Fatal("unverified allocation accepted")
	}
	point = roleConfirmationRecord(t, "model", 80, 8, roleReviewNow.Add(-time.Hour))
	point.Device.GPU, point.Device.InferenceDevice = "", "CPU"
	if _, err := confirmationAllocationFrom(point); err == nil {
		t.Fatal("contradictory host attribution accepted")
	}
}

func TestRoleConfirmationRejectsFutureCapacityAndWrongPrediction(t *testing.T) {
	for _, scenario := range []string{"artifact", "context", "future observation", "old prediction"} {
		t.Run(scenario, func(t *testing.T) {
			plan, points, now := roleConfirmationFixture(t)
			point := points[0]
			switch scenario {
			case "artifact":
				point.CapacityPlan.Prediction.ArtifactSHA256 = plan.Candidates[1].Model.Value
			case "context":
				point.CapacityPlan.Prediction.RequestedContext++
			case "old prediction":
				point.CapacityPlan.Prediction.CreatedAt = roleReviewNow.Add(-time.Hour).Format(time.RFC3339)
			case "future observation":
				point.CapacityPlan.Policy.CurrentAvailable = &capacity.MemoryObservation{Kind: capacity.ObservationCurrentAvailable,
					ResourceDomain: capacity.DomainAccelerator, Bytes: 16 << 30, Source: "probe", ObservedAt: now.Add(time.Hour).Format(time.RFC3339)}
				point.CapacityPlan.Prediction.PolicySHA256, _ = point.CapacityPlan.Policy.Digest()
			}
			if scenario == "context" {
				if ValidateConfirmationCapacityPoint(plan, 1, point) == nil {
					t.Fatal("wrong prediction context accepted")
				}
				return
			}
			points[0] = roleConfirmationReseal(t, point)
			if _, err := AnalyzeConfirmation(plan, points, now); err == nil {
				t.Fatal("unbound or mistimed capacity receipt accepted")
			}
		})
	}
}
