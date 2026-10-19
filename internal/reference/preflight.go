package reference

import (
	"errors"
	"math"
)

// Preflight refuses before a verifier file is accepted. A rate, a disk
// figure, or a served context that was not supplied stays a refusal.
// The arithmetic uses ceiling division and rejects an overflowing product
// instead of wrapping it into a budget that appears to fit.
func Preflight(plan Plan, conditions RunConditions) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	if err := refuseLoad(conditions); err != nil {
		return err
	}
	if conditions.ServedContext < plan.ContextCap {
		return errors.New("served context is below the plan cap")
	}
	seconds, err := estimateSeconds(plan)
	if err != nil {
		return err
	}
	if seconds > plan.WallBudgetSeconds {
		return errors.New("subset cannot finish in the declared wall budget")
	}
	if plan.RequiredDiskBytes > plan.DiskBudgetBytes {
		return errors.New("subset cannot finish in the declared disk budget")
	}
	return nil
}

func refuseLoad(conditions RunConditions) error {
	if conditions.GPUBusyDeclared {
		return errors.New("GPU is busy")
	}
	if conditions.OtherModelDeclared {
		return errors.New("another model is resident")
	}
	if !conditions.WorkerProbeAvailable {
		if conditions.WorkerProbeReason == "" {
			return errors.New("confinement probe is unavailable")
		}
		return errors.New("confinement probe is unavailable: " + conditions.WorkerProbeReason)
	}
	return nil
}

func estimateSeconds(plan Plan) (int64, error) {
	repeats, ok := mulInt64(int64(len(plan.Instances)), int64(plan.K))
	if !ok {
		return 0, errors.New("token estimate is not measurable")
	}
	prompt, ok := ceilDiv(plan.PromptTokens, plan.PrefillTokensPerSecond)
	if !ok {
		return 0, errors.New("token estimate is not measurable")
	}
	completion, ok := ceilDiv(plan.CompletionTokens, plan.DecodeTokensPerSecond)
	if !ok {
		return 0, errors.New("token estimate is not measurable")
	}
	perTask, ok := addInt64(prompt, completion)
	if !ok {
		return 0, errors.New("token estimate is not measurable")
	}
	total, ok := mulInt64(repeats, perTask)
	if !ok {
		return 0, errors.New("token estimate is not measurable")
	}
	return total, nil
}

func ceilDiv(n, d int64) (int64, bool) {
	if n < 1 || d < 1 {
		return 0, false
	}
	sum, ok := addInt64(n, d-1)
	if !ok {
		return 0, false
	}
	return sum / d, true
}

func addInt64(a, b int64) (int64, bool) {
	if a < 0 || b < 0 || a > math.MaxInt64-b {
		return 0, false
	}
	return a + b, true
}

func mulInt64(a, b int64) (int64, bool) {
	if a < 0 || b < 0 {
		return 0, false
	}
	if a != 0 && b > math.MaxInt64/a {
		return 0, false
	}
	return a * b, true
}
