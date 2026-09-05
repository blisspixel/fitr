package role

import (
	"math"
	"testing"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/device"
)

func TestPreferenceUsesFixedAnchorsAndCorrectDirection(t *testing.T) {
	for _, test := range []struct {
		name       string
		preference Preference
		value      float64
		want       float64
	}{
		{"maximize midpoint", Preference{Worst: 20, Best: 100}, 60, 0.5},
		{"maximize below floor", Preference{Worst: 20, Best: 100}, 10, 0},
		{"maximize above ceiling", Preference{Worst: 20, Best: 100}, 110, 1},
		{"minimize midpoint", Preference{Worst: 8, Best: 4}, 6, 0.5},
		{"minimize above worst", Preference{Worst: 8, Best: 4}, 10, 0},
		{"minimize below best", Preference{Worst: 8, Best: 4}, 2, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := normalize(test.value, test.preference); math.Abs(got-test.want) > 1e-12 {
				t.Fatalf("normalized utility = %v, want %v", got, test.want)
			}
		})
	}
	// Utilities do not depend on which other candidates are in the library.
	spec := roleReviewSpec()
	result, gaps := preferenceResult(spec, []decision.RequirementResult{rolePreferenceObservation("speed", 60, 50, 70)})
	if result == nil || len(gaps) != 0 || math.Abs(result.Estimate-0.6) > 1e-12 ||
		math.Abs(result.Low-0.5) > 1e-12 || math.Abs(result.High-0.7) > 1e-12 {
		t.Fatalf("fixed anchors changed: result=%+v gaps=%v", result, gaps)
	}
}

func TestPreferencePropagatesWeightedBoundsAndMinimization(t *testing.T) {
	spec := roleReviewSpec()
	spec.Preferences = []Preference{
		{Requirement: "speed", Weight: 3, Worst: 0, Best: 100},
		{Requirement: "memory", Weight: 1, Worst: 8 * device.GB, Best: 4 * device.GB},
	}
	observedMemory := float64(5 * device.GB)
	requirements := []decision.RequirementResult{
		rolePreferenceObservation("speed", 60, 50, 70),
		{ID: "memory", State: decision.RequirementEstablished, Observed: &observedMemory, Unit: analysis.UnitBytes},
	}
	result, gaps := preferenceResult(spec, requirements)
	if result == nil || len(gaps) != 0 {
		t.Fatalf("exact capacity and bounded speed were unavailable: result=%+v gaps=%v", result, gaps)
	}
	if math.Abs(result.Estimate-0.6375) > 1e-12 || math.Abs(result.Low-0.5625) > 1e-12 ||
		math.Abs(result.High-0.7125) > 1e-12 || result.Sensitivity != 0.2 {
		t.Fatalf("weighted utility bounds = %+v", result)
	}
	metric := result.Metrics[1]
	if metric.Low != 0.75 || metric.High != 0.75 || metric.Unit != analysis.UnitBytes {
		t.Fatalf("exact resident observation did not retain its units and reversed anchors: %+v", metric)
	}
	boundedCapacity := rolePreferenceObservation("memory", 6*device.GB, 5*device.GB, 7*device.GB)
	metric, ok := preferenceMetric(spec.Preferences[1], spec.Decision.Requirements, []decision.RequirementResult{boundedCapacity})
	if !ok || metric.Low != 0.25 || metric.High != 0.75 {
		t.Fatalf("minimization did not reverse interval endpoints: %+v", metric)
	}
}

func TestPreferenceMissingOrInvalidBoundsCannotManufactureCertainty(t *testing.T) {
	for _, scenario := range []string{"missing", "unestablished", "no observation", "no lower", "no upper", "nan", "infinite", "reversed", "outside"} {
		t.Run(scenario, func(t *testing.T) {
			spec := roleReviewSpec()
			observation := rolePreferenceObservation("speed", 60, 50, 70)
			switch scenario {
			case "missing":
				observation.ID = "other"
			case "unestablished":
				observation.State = decision.RequirementUnresolved
			case "no observation":
				observation.Observed = nil
			case "no lower":
				observation.IntervalLow = nil
			case "no upper":
				observation.IntervalHigh = nil
			case "nan":
				*observation.Observed = math.NaN()
			case "infinite":
				*observation.IntervalHigh = math.Inf(1)
			case "reversed":
				*observation.IntervalLow = 80
			case "outside":
				*observation.Observed = 80
			}
			result, gaps := preferenceResult(spec, []decision.RequirementResult{observation})
			if result != nil || len(gaps) != 1 {
				t.Fatalf("invalid measurement became a preference score: result=%+v gaps=%v", result, gaps)
			}
		})
	}
}

func TestPreferenceSensitivityChecksCombinedWeightChanges(t *testing.T) {
	preferences := []Preference{{Requirement: "speed", Weight: 1}, {Requirement: "memory", Weight: 1}}
	candidate := rolePreferenceMetrics(0.8, 0.25)
	other := rolePreferenceMetrics(0.2, 0.7)
	// The nominal advantage is +0.15. Either adverse weight change alone
	// preserves it (+0.03 or +0.06), while both together reverse it (-0.06).
	if robustlyLeads(candidate, other, preferences) {
		t.Fatal("a choice reversed by simultaneous weight changes became robust")
	}
	candidate.Metrics[1].Low, candidate.Metrics[1].High = 0.6, 0.6
	if !robustlyLeads(candidate, other, preferences) {
		t.Fatal("a positive worst-case margin did not establish a robust lead")
	}
	if robustlyLeads(other, candidate, preferences) || robustlyLeads(candidate, candidate, preferences) {
		t.Fatal("a losing or tied candidate acquired a robust lead")
	}
	candidate.Metrics[0].Requirement = "wrong"
	if robustlyLeads(candidate, other, preferences) || robustlyLeads(PreferenceResult{}, other, preferences) {
		t.Fatal("missing or mismatched metric identity was ignored")
	}
}

func TestPreferenceUsesIntervalsInsteadOfPointEstimateWinner(t *testing.T) {
	preferences := []Preference{{Requirement: "speed", Weight: 1}}
	candidate := PreferenceResult{Estimate: 0.9, Metrics: []PreferenceMetric{{Requirement: "speed", Low: 0.4, High: 1}}}
	other := PreferenceResult{Estimate: 0.5, Metrics: []PreferenceMetric{{Requirement: "speed", Low: 0.45, High: 0.55}}}
	if robustlyLeads(candidate, other, preferences) || robustlyLeads(other, candidate, preferences) {
		t.Fatal("overlapping bounds became a preference winner")
	}
}

func TestPreferenceIsInvariantToCommonPositiveWeightScale(t *testing.T) {
	for _, weight := range []float64{1, 1e-20, math.SmallestNonzeroFloat64, 1e6} {
		spec := roleReviewSpec()
		spec.Preferences[0].Weight = weight
		if err := spec.Validate(); err != nil {
			t.Fatal(err)
		}
		result, gaps := preferenceResult(spec, []decision.RequirementResult{rolePreferenceObservation("speed", 80, 70, 90)})
		if result == nil || len(gaps) != 0 || math.Abs(result.Estimate-0.8) > 1e-12 ||
			math.Abs(result.Low-0.7) > 1e-12 || math.Abs(result.High-0.9) > 1e-12 {
			t.Fatalf("weight %g changed normalized preference: result=%+v gaps=%v", weight, result, gaps)
		}
		other := PreferenceResult{Metrics: []PreferenceMetric{{Requirement: "speed", Low: 0.1, High: 0.2}}}
		if !robustlyLeads(*result, other, spec.Preferences) {
			t.Fatalf("weight %g erased an unchanged relative preference lead", weight)
		}
	}
}

func rolePreferenceObservation(id string, observed, low, high float64) decision.RequirementResult {
	return decision.RequirementResult{ID: id, State: decision.RequirementEstablished, Observed: &observed,
		IntervalLow: &low, IntervalHigh: &high, Unit: analysis.UnitTokensPerSecond}
}

func rolePreferenceMetrics(speed, memory float64) PreferenceResult {
	return PreferenceResult{Metrics: []PreferenceMetric{
		{Requirement: "speed", Low: speed, High: speed},
		{Requirement: "memory", Low: memory, High: memory},
	}}
}
