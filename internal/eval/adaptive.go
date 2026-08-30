package eval

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

const AdaptiveMethodWaldSPRT = "wald_sprt_v1"

type AdaptiveDecisionState string

const (
	AdaptiveAboveGate    AdaptiveDecisionState = "above_gate"
	AdaptiveBelowGate    AdaptiveDecisionState = "below_gate"
	AdaptiveInconclusive AdaptiveDecisionState = "inconclusive"
)

// AdaptiveDecision is the legacy schema-5 receipt for sequentially sampled
// checks. Current runs use fixed denominators and never create one. The type
// remains readable so existing local history does not become undecodable.
type AdaptiveDecision struct {
	Need       string                `json:"need"`
	Method     string                `json:"method"`
	Gate       float64               `json:"gate"`
	NullRate   float64               `json:"null_rate"`
	AltRate    float64               `json:"alternative_rate"`
	Alpha      float64               `json:"alpha"`
	Beta       float64               `json:"beta"`
	MaxTrials  int                   `json:"max_trials"`
	Trials     int                   `json:"trials"`
	Passes     int                   `json:"passes"`
	Failures   int                   `json:"failures"`
	LogRatio   float64               `json:"log_likelihood_ratio"`
	Decision   AdaptiveDecisionState `json:"decision"`
	StopReason string                `json:"stop_reason"`
}

func (d AdaptiveDecision) Validate() error {
	switch {
	case strings.TrimSpace(d.Need) == "":
		return errors.New("adaptive decision is missing its need")
	case d.Method != AdaptiveMethodWaldSPRT:
		return fmt.Errorf("unsupported adaptive method %q", d.Method)
	case !(0 < d.NullRate && d.NullRate < d.Gate && d.Gate < d.AltRate && d.AltRate < 1):
		return errors.New("adaptive rates must satisfy 0 < null < gate < alternative < 1")
	case !(0 < d.Alpha && d.Alpha < 1) || !(0 < d.Beta && d.Beta < 1):
		return errors.New("adaptive error rates must be between zero and one")
	case d.MaxTrials < 1:
		return errors.New("adaptive max_trials must be positive")
	case d.Trials < 1 || d.Trials > d.MaxTrials:
		return fmt.Errorf("adaptive trials %d are outside 1..%d", d.Trials, d.MaxTrials)
	case d.Passes < 0 || d.Failures < 0 || d.Passes+d.Failures != d.Trials:
		return errors.New("adaptive pass and failure counts do not equal trials")
	case math.IsNaN(d.LogRatio) || math.IsInf(d.LogRatio, 0):
		return errors.New("adaptive likelihood ratio is not finite")
	}
	switch d.Decision {
	case AdaptiveAboveGate:
		if d.StopReason != "upper_boundary" {
			return errors.New("above-gate decision requires upper_boundary stop")
		}
	case AdaptiveBelowGate:
		if d.StopReason != "lower_boundary" {
			return errors.New("below-gate decision requires lower_boundary stop")
		}
	case AdaptiveInconclusive:
		if d.StopReason != "trial_cap" || d.Trials != d.MaxTrials {
			return errors.New("inconclusive adaptive decision requires an exhausted trial cap")
		}
	default:
		return fmt.Errorf("unknown adaptive decision %q", d.Decision)
	}
	return nil
}
