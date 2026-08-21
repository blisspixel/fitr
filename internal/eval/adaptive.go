package eval

import (
	"fmt"
	"math"
	"strings"

	"github.com/blisspixel/fitr/internal/stats"
)

const AdaptiveMethodWaldSPRT = "wald_sprt_v1"

type AdaptiveDecisionState string

const (
	AdaptiveAboveGate    AdaptiveDecisionState = "above_gate"
	AdaptiveBelowGate    AdaptiveDecisionState = "below_gate"
	AdaptiveInconclusive AdaptiveDecisionState = "inconclusive"
)

// AdaptiveDecision is the persisted receipt for one sequentially sampled
// capability pool. It records both the decision and every parameter needed to
// reproduce why sampling stopped.
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

// CaptureAdaptiveDecision snapshots the GateSPRT policy used by fitr. Passes
// is explicit because SPRT deliberately exposes only its aggregate trial
// count and likelihood ratio.
func CaptureAdaptiveDecision(need string, gate float64, maxTrials, passes int, test *stats.SPRT) (AdaptiveDecision, error) {
	if test == nil {
		return AdaptiveDecision{}, fmt.Errorf("adaptive decision has no sequential test")
	}
	d := AdaptiveDecision{
		Need: strings.TrimSpace(need), Method: AdaptiveMethodWaldSPRT,
		Gate: gate, NullRate: math.Max(0.02, gate-0.10),
		AltRate: math.Min(0.98, gate+0.10), Alpha: 0.05, Beta: 0.05,
		MaxTrials: maxTrials, Trials: test.N, Passes: passes,
		Failures: test.N - passes, LogRatio: test.LLR,
	}
	switch test.State() {
	case stats.SPRTAcceptH1:
		d.Decision, d.StopReason = AdaptiveAboveGate, "upper_boundary"
	case stats.SPRTAcceptH0:
		d.Decision, d.StopReason = AdaptiveBelowGate, "lower_boundary"
	default:
		d.Decision, d.StopReason = AdaptiveInconclusive, "trial_cap"
	}
	if err := d.Validate(); err != nil {
		return AdaptiveDecision{}, err
	}
	return d, nil
}

func (d AdaptiveDecision) Validate() error {
	switch {
	case strings.TrimSpace(d.Need) == "":
		return fmt.Errorf("adaptive decision is missing its need")
	case d.Method != AdaptiveMethodWaldSPRT:
		return fmt.Errorf("unsupported adaptive method %q", d.Method)
	case !(0 < d.NullRate && d.NullRate < d.Gate && d.Gate < d.AltRate && d.AltRate < 1):
		return fmt.Errorf("adaptive rates must satisfy 0 < null < gate < alternative < 1")
	case !(0 < d.Alpha && d.Alpha < 1) || !(0 < d.Beta && d.Beta < 1):
		return fmt.Errorf("adaptive error rates must be between zero and one")
	case d.MaxTrials < 1:
		return fmt.Errorf("adaptive max_trials must be positive")
	case d.Trials < 1 || d.Trials > d.MaxTrials:
		return fmt.Errorf("adaptive trials %d are outside 1..%d", d.Trials, d.MaxTrials)
	case d.Passes < 0 || d.Failures < 0 || d.Passes+d.Failures != d.Trials:
		return fmt.Errorf("adaptive pass and failure counts do not equal trials")
	case math.IsNaN(d.LogRatio) || math.IsInf(d.LogRatio, 0):
		return fmt.Errorf("adaptive likelihood ratio is not finite")
	}
	switch d.Decision {
	case AdaptiveAboveGate:
		if d.StopReason != "upper_boundary" {
			return fmt.Errorf("above-gate decision requires upper_boundary stop")
		}
	case AdaptiveBelowGate:
		if d.StopReason != "lower_boundary" {
			return fmt.Errorf("below-gate decision requires lower_boundary stop")
		}
	case AdaptiveInconclusive:
		if d.StopReason != "trial_cap" || d.Trials != d.MaxTrials {
			return fmt.Errorf("inconclusive adaptive decision requires an exhausted trial cap")
		}
	default:
		return fmt.Errorf("unknown adaptive decision %q", d.Decision)
	}
	return nil
}
