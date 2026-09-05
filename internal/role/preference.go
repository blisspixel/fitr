package role

import (
	"math"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/decision"
)

// These bounds propagate per-metric intervals through fixed utility anchors.
// They are not a joint confidence interval or a universal model quality score.
type PreferenceResult struct {
	Estimate    float64            `json:"estimate"`
	Low         float64            `json:"low"`
	High        float64            `json:"high"`
	Sensitivity float64            `json:"relative_weight_sensitivity"`
	Metrics     []PreferenceMetric `json:"metrics"`
}

type PreferenceMetric struct {
	Requirement string        `json:"requirement"`
	Observed    float64       `json:"observed"`
	Unit        analysis.Unit `json:"unit"`
	Estimate    float64       `json:"normalized_estimate"`
	Low         float64       `json:"normalized_low"`
	High        float64       `json:"normalized_high"`
}

func preferenceResult(spec Spec, requirements []decision.RequirementResult) (*PreferenceResult, []string) {
	result := &PreferenceResult{Sensitivity: 0.2}
	var gaps []string
	totalWeight := preferenceWeightTotal(spec.Preferences)
	if totalWeight <= 0 {
		return nil, gaps
	}
	for _, preference := range spec.Preferences {
		metric, ok := preferenceMetric(preference, spec.Decision.Requirements, requirements)
		if !ok {
			gaps = append(gaps, preference.Requirement+": preference measurement or uncertainty bounds unavailable")
			continue
		}
		result.Metrics = append(result.Metrics, metric)
		weight := preference.Weight / totalWeight
		result.Estimate += weight * metric.Estimate
		result.Low += weight * metric.Low
		result.High += weight * metric.High
	}
	if len(gaps) > 0 {
		return nil, gaps
	}
	return result, nil
}

func preferenceWeightTotal(preferences []Preference) float64 {
	var total float64
	for _, preference := range preferences {
		total += preference.Weight
	}
	return total
}

func preferenceMetric(preference Preference, definitions []decision.Requirement, requirements []decision.RequirementResult) (PreferenceMetric, bool) {
	for _, requirement := range requirements {
		if requirement.ID != preference.Requirement || requirement.State != decision.RequirementEstablished || requirement.Observed == nil {
			continue
		}
		low, high := requirement.IntervalLow, requirement.IntervalHigh
		// Resident bytes are an exact-context observation, not a sample mean.
		// A single throughput sample must not acquire zero uncertainty this way.
		if low == nil || high == nil {
			for _, definition := range definitions {
				if definition.ID == preference.Requirement && definition.Capacity != nil {
					low, high = requirement.Observed, requirement.Observed
				}
			}
		}
		if low == nil || high == nil || !finiteMetric(*requirement.Observed) ||
			!finiteMetric(*low) || !finiteMetric(*high) || *low > *high ||
			*requirement.Observed < *low || *requirement.Observed > *high {
			return PreferenceMetric{}, false
		}
		normalizedLow, normalizedHigh := normalize(*low, preference), normalize(*high, preference)
		return PreferenceMetric{
			Requirement: preference.Requirement, Observed: *requirement.Observed, Unit: requirement.Unit,
			Estimate: normalize(*requirement.Observed, preference),
			Low:      math.Min(normalizedLow, normalizedHigh), High: math.Max(normalizedLow, normalizedHigh),
		}, true
	}
	return PreferenceMetric{}, false
}

func normalize(value float64, preference Preference) float64 {
	return math.Max(0, math.Min(1, (value-preference.Worst)/(preference.Best-preference.Worst)))
}

func finiteMetric(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

// Pairwise margin checks every combination of independently perturbed weights
// within +/-20%, including coupled changes; checking one slider at a time
// would miss changes that jointly reverse the recommendation.
func robustlyLeads(candidate, other PreferenceResult, preferences []Preference) bool {
	if len(candidate.Metrics) != len(preferences) || len(other.Metrics) != len(preferences) {
		return false
	}
	totalWeight := preferenceWeightTotal(preferences)
	if totalWeight <= 0 {
		return false
	}
	var margin float64
	for index, preference := range preferences {
		if candidate.Metrics[index].Requirement != preference.Requirement || other.Metrics[index].Requirement != preference.Requirement {
			return false
		}
		difference := candidate.Metrics[index].Low - other.Metrics[index].High
		weight := (preference.Weight / totalWeight) * 0.8
		if difference < 0 {
			weight = (preference.Weight / totalWeight) * 1.2
		}
		margin += weight * difference
	}
	return margin > 1e-12
}
