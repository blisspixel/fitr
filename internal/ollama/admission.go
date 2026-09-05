package ollama

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

const (
	InferenceGenerate = "generate"
	InferenceChat     = "chat"
	// ChatMaxAttempts includes the original request and the one retry allowed
	// for an empty, non-terminal response. Generate has no application retry.
	ChatMaxAttempts = 2
)

// ErrInferenceAdmission marks a refusal to dispatch. Callers must stop the
// point rather than downgrade this error into a task outcome and continue.
var ErrInferenceAdmission = errors.New("inference admission refused")

// InferenceRequest contains only admission facts, never prompt or reply text.
// MaxOutputTokens is the positive requested output cap, not measured usage.
type InferenceRequest struct {
	Kind            string
	Model           string
	MaxOutputTokens int
}

// InferencePermit supplies a finite deadline after a durable reservation. The
// client preserves any earlier caller deadline and caller cancellation.
type InferencePermit struct {
	Deadline time.Time
}

// InferenceAdmission reserves one actual inference attempt before dispatch.
// Errors after admission do not imply that a reservation can be refunded.
// A configured hook must return a nonzero future deadline. Client.HTTP must
// not contain a custom RoundTripper that retries requests internally.
type InferenceAdmission func(context.Context, InferenceRequest) (InferencePermit, error)

// InferenceAttempt contains post-attempt validation facts, never prompts,
// generated text, tool calls or raw response diagnostics.
type InferenceAttempt struct {
	Kind            string
	Model           string
	NumCtx          int
	MaxOutputTokens int
}

// InferenceObservation validates execution after each admitted attempt, before
// returning its output or retrying an empty Chat response. Configure before use.
// It shares the attempt's deadline and must honor context cancellation.
// Client.HTTP must not contain a RoundTripper that retries internally.
type InferenceObservation func(context.Context, InferenceAttempt) error

func (c *Client) finishInference(ctx context.Context, attempt InferenceAttempt, started time.Time, metrics Metrics) (Metrics, error) {
	if c.Admission == nil && c.ObserveInference == nil {
		return metrics, nil
	}
	// Capture the entire HTTP/parsing/body-close interval before validation.
	// Neither durable admission nor post-attempt checks are inference time.
	metrics.InferenceElapsed = time.Since(started)
	metrics.InferenceElapsedKnown = true
	if c.ObserveInference == nil {
		return metrics, nil
	}
	if err := ctx.Err(); err != nil {
		return Metrics{}, fmt.Errorf("%w: %w", ErrUnverifiedLocalExecution, err)
	}
	if err := c.ObserveInference(ctx, attempt); err != nil {
		return Metrics{}, fmt.Errorf("%w: %w", ErrUnverifiedLocalExecution, err)
	}
	if err := ctx.Err(); err != nil {
		return Metrics{}, fmt.Errorf("%w: %w", ErrUnverifiedLocalExecution, err)
	}
	return metrics, nil
}

func (c *Client) admitInference(ctx context.Context, request InferenceRequest) (context.Context, context.CancelFunc, error) {
	if c.Admission == nil {
		return ctx, func() {}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrInferenceAdmission, err)
	}
	if request.MaxOutputTokens <= 0 {
		return nil, nil, fmt.Errorf("%w: output cap must be positive", ErrInferenceAdmission)
	}
	permit, err := c.Admission(ctx, request)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrInferenceAdmission, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrInferenceAdmission, err)
	}
	if permit.Deadline.IsZero() {
		return nil, nil, fmt.Errorf("%w: permit requires a deadline", ErrInferenceAdmission)
	}
	if !permit.Deadline.After(time.Now()) {
		return nil, nil, fmt.Errorf("%w: %w", ErrInferenceAdmission, context.DeadlineExceeded)
	}
	admitted, cancel := context.WithDeadline(ctx, permit.Deadline)
	return admitted, cancel, nil
}

// DisableRedirects prevents a redirect from dispatching an unadmitted request
// or sending model inputs to a different endpoint. Configure before use. The
// shallow copy preserves the caller's transport without changing a shared
// http.Client. Redirect responses remain ordinary HTTP errors to the caller.
func (c *Client) DisableRedirects() {
	client := http.DefaultClient
	if c.HTTP != nil {
		client = c.HTTP
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.HTTP = &copyClient
}
