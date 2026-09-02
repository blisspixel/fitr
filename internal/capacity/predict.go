package capacity

import (
	"errors"
	"sort"
	"strings"
	"time"
)

type PredictionInput struct {
	CreatedAt           time.Time
	ArtifactSHA256      string
	ResourceDomain      ResourceDomain
	RequestedContext    int
	Architecture        string
	KVDataType          string
	KVElementBytes      float64
	PlacementAssumption string
	ArtifactBytes       *int64
	KVBytes             *int64
	Missing             []string
	Excluded            []string
}

func BuildPrediction(policy Policy, input PredictionInput) (Prediction, error) {
	policyDigest, err := policy.Digest()
	if err != nil {
		return Prediction{}, err
	}
	if input.CreatedAt.IsZero() {
		return Prediction{}, errors.New("capacity prediction requires a creation time")
	}
	prediction := Prediction{
		Schema: PredictionSchema, CreatedAt: input.CreatedAt.UTC().Format(time.RFC3339Nano),
		ArtifactSHA256: input.ArtifactSHA256, ResourceDomain: input.ResourceDomain,
		RequestedContext: input.RequestedContext, Architecture: strings.TrimSpace(input.Architecture),
		KVDataType: strings.TrimSpace(input.KVDataType), KVElementBytes: input.KVElementBytes,
		PlacementAssumption: strings.TrimSpace(input.PlacementAssumption),
		ArtifactBytes:       cloneInt64(input.ArtifactBytes), KVBytes: cloneInt64(input.KVBytes),
		Missing: normalizedLabels(input.Missing), Excluded: normalizedLabels(input.Excluded),
		PolicySHA256: policyDigest,
	}
	componentSum, known, valid := knownComponentSum(prediction.ArtifactBytes, prediction.KVBytes)
	if !valid {
		return Prediction{}, errors.New("capacity prediction component sum overflows")
	}
	if known {
		prediction.KnownComponentBytes = &componentSum
		prediction.State = PredictionComponentProjection
	} else {
		prediction.State = PredictionUnavailable
	}
	if err := prediction.Validate(); err != nil {
		return Prediction{}, err
	}
	return prediction, nil
}

func normalizedLabels(source []string) []string {
	seen := make(map[string]bool, len(source))
	labels := make([]string, 0, len(source))
	for _, item := range source {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		labels = append(labels, item)
	}
	sort.Strings(labels)
	if len(labels) == 0 {
		return nil
	}
	return labels
}
