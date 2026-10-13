package main

import (
	"context"
	"errors"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/ollama"
)

// confirmBoundary asks the runtime to refuse a prompt past the declared
// window. A transport error that is not the typed overflow refusal is not a
// window and does not abort the run. Cancellation does.
func (run *runExecution) confirmBoundary(receipt device.ContextVerification) (device.ContextVerification, string, error) {
	prompt, ok := device.BoundaryPrompt(receipt.RequestedTokens)
	if !ok {
		updated, note := device.ResolveBoundary(receipt, device.BoundaryObservation{})
		return updated, note, nil
	}
	_, metrics, err := run.backend.Generate(run.ctx, run.model, prompt,
		ollama.Deterministic(8, receipt.RequestedTokens))
	obs := device.BoundaryObservation{Sent: true}
	switch {
	case err == nil:
		obs.Accepted = true
		obs.PromptTokens = metrics.PromptTokens
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return receipt, "", err
	default:
		if overflow, ok := ollama.AsContextOverflow(err); ok {
			obs.Overflow = true
			obs.OverflowNCtx = overflow.NCtx
			obs.PromptTokens = overflow.PromptTokens
		} else {
			obs.Unrecognized = true
		}
	}
	updated, note := device.ResolveBoundary(receipt, obs)
	return updated, note, nil
}
