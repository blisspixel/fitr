package reference

import (
	"errors"
	"math"
	"strconv"
)

func align(plan Plan, rows []Observation) ([]Observation, error) {
	want := len(plan.Instances) * plan.K
	if len(rows) != want {
		return nil, errors.New("verifier observations drift from the sealed plan")
	}
	index := make(map[string]Observation, len(rows))
	for _, row := range rows {
		if err := validateObservation(plan, row); err != nil {
			return nil, err
		}
		key := row.InstanceID + "#" + strconv.Itoa(row.Repeat)
		if _, exists := index[key]; exists {
			return nil, errors.New("verifier observations drift from the sealed plan")
		}
		index[key] = row
	}
	return orderObservations(plan, index)
}

func orderObservations(plan Plan, index map[string]Observation) ([]Observation, error) {
	ordered := make([]Observation, 0, len(index))
	for _, instance := range plan.Instances {
		for repeat := 1; repeat <= plan.K; repeat++ {
			row, ok := index[instance.ID+"#"+strconv.Itoa(repeat)]
			if !ok {
				return nil, errors.New("verifier observations drift from the sealed plan")
			}
			ordered = append(ordered, row)
		}
	}
	return ordered, nil
}

func validateObservation(plan Plan, row Observation) error {
	if !knownVerdict(row.Verdict) || !instanceIDPattern.MatchString(row.InstanceID) {
		return errors.New("verifier observation has an unknown verdict or instance")
	}
	if row.Repeat < 1 || row.Repeat > plan.K || row.Steps < 0 || row.Steps > plan.StepLimit {
		return errors.New("verifier observation is outside the sealed plan")
	}
	if !validCount(row.PromptTokens) || !validCount(row.CompletionTokens) ||
		row.WallMillis < 0 || row.WallMillis > maxWallMillis || !validSHA256(row.VerifierLogSHA256) {
		return errors.New("verifier observation has an unmeasured or unbounded field")
	}
	if row.AgentClaim != "" && !knownVerdict(row.AgentClaim) {
		return errors.New("verifier observation carries text that is not a verdict")
	}
	return validateOptional(row)
}

func validCount(value int64) bool {
	return value >= 0 && value <= maxObserved
}

func validateOptional(row Observation) error {
	if row.CacheHitTokens != nil && (*row.CacheHitTokens < 0 || *row.CacheHitTokens > maxObserved) {
		return errors.New("cache-hit tokens are unbounded")
	}
	if !validOptional(row.APICostUSD) || !validOptional(row.EnergyJoules) {
		return errors.New("cost or energy is not a measured non-negative number")
	}
	return nil
}

func validOptional(value *float64) bool {
	if value == nil {
		return true
	}
	return !math.IsNaN(*value) && !math.IsInf(*value, 0) && *value >= 0
}

func knownVerdict(value string) bool {
	switch value {
	case OutcomeResolved, OutcomeUnresolved, OutcomeHarness, OutcomeEnvironment, OutcomeTimeout:
		return true
	default:
		return false
	}
}

func contamination(created, release string) string {
	if !validDate(created) || !validDate(release) {
		return ContaminationAbsent
	}
	if created < release {
		return ContaminationMaybe
	}
	return contaminationClean
}
