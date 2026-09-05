package eval

import (
	"context"
	"errors"
	"fmt"

	"github.com/blisspixel/fitr/internal/contextquality"
	"github.com/blisspixel/fitr/internal/ollama"
)

// ContextBackend is the narrow runtime surface the context task pack needs.
// Only a client that reports ollama.PreserveContextV1 can send the overflow
// controls this pack declares, so the policy is asked for rather than assumed.
// A reported policy describes the request, never the server's treatment of it.
type ContextBackend interface {
	Generate(ctx context.Context, model, prompt string, s ollama.Sampling) (string, ollama.Metrics, error)
	ContextRequestPolicy() ollama.ContextRequestPolicy
}

// ContextTaskRun is one phase of the finite document pack against one model.
// Report is re-derived by the pure analyzer from Observations; this adapter
// grades nothing and no dispatch outcome is copied into a task verdict.
type ContextTaskRun struct {
	Report       contextquality.Report
	Observations []contextquality.Observation
	// Ended is the fault that stopped the phase before its last cell, if any.
	// The cells after it are not_attempted. It is an execution fact, never a
	// task outcome and never evidence about the model's quality.
	Ended error
}

// RunContextTasks submits every sealed cell in plan order at one fixed
// operating window and returns the independently re-verified report beside the
// observations it came from. It never lowers the window, the payload or the
// output reserve, and never retries a cell.
//
// A refused reservation, a cancelled context, an unverified local runtime or
// an invalid request policy ends the phase, because none of them can improve
// on the next cell. A per-cell transport fault or unknown accounting is
// recorded and the phase continues: an incomplete phase already cannot
// qualify, and the remaining cells still carry their own diagnostics.
//
// A non-nil error means no report could be derived at all. An early-ended
// phase returns a usable report with Ended set; that report is diagnostic,
// and its unavailable cells keep it from establishing a verified prefix.
func RunContextTasks(ctx context.Context, c ContextBackend, model string, plan contextquality.Plan) (ContextTaskRun, error) {
	if err := plan.Validate(); err != nil {
		return ContextTaskRun{}, err
	}
	if model == "" {
		return ContextTaskRun{}, errors.New("context task run requires a model")
	}
	if c == nil || c.ContextRequestPolicy() != ollama.PreserveContextV1 {
		return ContextTaskRun{}, fmt.Errorf("%w: backend does not send the declared context controls", ollama.ErrContextRequestPolicy)
	}
	run, err := dispatchContextCells(ctx, c, model, plan)
	if err != nil {
		return ContextTaskRun{}, err
	}
	run.Report, err = contextquality.Analyze(plan, run.Observations)
	if err != nil {
		return ContextTaskRun{}, err
	}
	return run, nil
}

func dispatchContextCells(ctx context.Context, c ContextBackend, model string, plan contextquality.Plan) (ContextTaskRun, error) {
	policy := plan.Policy
	sampling := ollama.Deterministic(policy.OutputReserveTokens, policy.OperatingWindowTokens)
	run := ContextTaskRun{Observations: make([]contextquality.Observation, 0, len(plan.Cells))}
	for index, cell := range plan.Cells {
		if run.Ended != nil {
			run.Observations = append(run.Observations, unavailableCell(cell, "not_attempted"))
			continue
		}
		task, err := contextquality.Generate(plan, index+1)
		if err != nil {
			return ContextTaskRun{}, err
		}
		text, metrics, err := c.Generate(ctx, model, task.Prompt, sampling)
		observation, ended := classifyContextCell(cell, policy, text, metrics, err)
		if ended {
			run.Ended = err
		}
		run.Observations = append(run.Observations, observation)
	}
	return run, nil
}

// classifyContextCell establishes one cell's disposition from the dispatch
// result alone. The returned flag says whether the phase must stop.
func classifyContextCell(cell contextquality.Cell, policy contextquality.Policy,
	text string, metrics ollama.Metrics, err error,
) (contextquality.Observation, bool) {
	if err != nil {
		reason, ended := contextFaultReason(err)
		return unavailableCell(cell, reason), ended
	}
	observation := contextquality.Observation{CellID: cell.ID, PayloadSHA256: cell.PayloadSHA256, PromptSHA256: cell.PromptSHA256}
	// A terminal success still has to prove the entire declared reserve fit
	// beside the accepted prompt. An answer that only fits because the model
	// stopped early has not met the operating policy it was measured under.
	if err := metrics.ContextAccounting.CheckReserve(policy.OperatingWindowTokens, policy.OutputReserveTokens); err != nil {
		if errors.Is(err, ollama.ErrContextReserve) {
			observation.Disposition = contextquality.ContextLimit
			return observation, false
		}
		return unavailableCell(cell, "accounting_unknown"), false
	}
	if metrics.Truncated {
		observation.Disposition = contextquality.OutputLimit
		observation.Answer = boundedAnswer(text, contextquality.MaxAnswerBytes)
		return observation, false
	}
	observation.Disposition = contextquality.Answered
	// One byte past the retained bound still fails as an oversized answer
	// without copying an unbounded reply into the observation.
	observation.Answer = boundedAnswer(text, contextquality.MaxAnswerBytes+1)
	return observation, false
}

// contextFaultReason maps a dispatch error onto a supported unavailable reason.
// None of these are model-quality zeroes. A refused reservation is reported as
// not attempted because the request never reached the runtime.
func contextFaultReason(err error) (string, bool) {
	switch {
	case errors.Is(err, ollama.ErrInferenceAdmission):
		return "not_attempted", true
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "cancelled", true
	case ollama.IsLocalityError(err), errors.Is(err, ollama.ErrContextRequestPolicy):
		return "runtime_unverified", true
	case errors.Is(err, ollama.ErrContextAccounting):
		return "accounting_unknown", false
	default:
		return "transport_error", false
	}
}

func unavailableCell(cell contextquality.Cell, reason string) contextquality.Observation {
	return contextquality.Observation{
		CellID: cell.ID, PayloadSHA256: cell.PayloadSHA256, PromptSHA256: cell.PromptSHA256,
		Disposition: contextquality.NotAvailable, UnavailableReason: reason,
	}
}

func boundedAnswer(text string, limit int) string {
	if len(text) > limit {
		return text[:limit]
	}
	return text
}
