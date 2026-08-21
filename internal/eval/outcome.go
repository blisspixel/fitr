package eval

import (
	"context"
	"errors"
	"fmt"
)

// Outcome separates measured model behavior from unavailable evidence.
// Only pass and fail may enter a score numerator or denominator.
type Outcome string

const (
	OutcomePass         Outcome = "pass"
	OutcomeFail         Outcome = "fail"
	OutcomeInconclusive Outcome = "inconclusive"
	OutcomeError        Outcome = "error"
	OutcomeSkipped      Outcome = "skipped"
)

// FailureKind identifies why an observation could not become model evidence.
// These values are persisted and stable. Human-readable error text is detail,
// never a scoring input.
type FailureKind string

const (
	FailureTransport         FailureKind = "transport"
	FailureExecutorPreflight FailureKind = "executor_preflight"
	FailureExecutorLaunch    FailureKind = "executor_launch"
	FailureExecutorTimeout   FailureKind = "executor_timeout"
	FailureExecutorExit      FailureKind = "executor_exit"
	FailureVerifierProtocol  FailureKind = "verifier_protocol"
	FailureFixtureIO         FailureKind = "fixture_io"
	FailureModelIdentity     FailureKind = "model_identity"
	FailurePersistence       FailureKind = "persistence"
	FailureCancelled         FailureKind = "cancelled"
	FailureInvalidSpec       FailureKind = "invalid_spec"
	FailureUnsafeTask        FailureKind = "unsafe_task"
)

// Failure is a typed evidence failure. Operation is a stable, short label;
// Message is diagnostic text and must not be parsed to make decisions.
type Failure struct {
	Kind      FailureKind `json:"kind"`
	Operation string      `json:"operation,omitempty"`
	Message   string      `json:"message"`
	cause     error
}

// OutcomeCounts makes denominator integrity explicit. Expected comes from the
// immutable run plan; Observed must equal Expected before a phase is known.
type OutcomeCounts struct {
	Expected     int `json:"expected"`
	Observed     int `json:"observed"`
	Attempted    int `json:"attempted"`
	Scorable     int `json:"scorable"`
	Passed       int `json:"passed"`
	Failed       int `json:"failed"`
	Inconclusive int `json:"inconclusive"`
	Errors       int `json:"errors"`
	Skipped      int `json:"skipped"`
}

func CountOutcomes(expected int, outcomes ...Outcome) OutcomeCounts {
	c := OutcomeCounts{Expected: expected}
	for _, outcome := range outcomes {
		c.Observed++
		switch outcome {
		case OutcomePass:
			c.Passed++
		case OutcomeFail:
			c.Failed++
		case OutcomeInconclusive:
			c.Inconclusive++
		case OutcomeError:
			c.Errors++
		case OutcomeSkipped:
			c.Skipped++
		default:
			// Empty is legacy binary evidence and must be translated by the
			// record adapter before it reaches a schema-5 count.
			c.Errors++
		}
	}
	c.Scorable = c.Passed + c.Failed
	c.Attempted = c.Observed - c.Skipped
	return c
}

func (c OutcomeCounts) Complete() bool {
	return c.Expected >= 0 && c.Observed == c.Expected &&
		c.Observed == c.Passed+c.Failed+c.Inconclusive+c.Errors+c.Skipped &&
		c.Scorable == c.Passed+c.Failed && c.Attempted == c.Observed-c.Skipped
}

func (f *Failure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.cause
}

func (f *Failure) Error() string {
	if f == nil {
		return ""
	}
	if f.Operation == "" {
		return fmt.Sprintf("%s: %s", f.Kind, f.Message)
	}
	return fmt.Sprintf("%s %s: %s", f.Kind, f.Operation, f.Message)
}

func failure(kind FailureKind, operation string, err error) *Failure {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		kind = FailureCancelled
	}
	return &Failure{Kind: kind, Operation: operation, Message: err.Error(), cause: err}
}

// MeasuredOutcome translates a result into a binary trial. The empty outcome
// is the schema-4 legacy form, where Pass was the only stored state.
func MeasuredOutcome(outcome Outcome, legacyPass bool) (pass bool, measured bool) {
	switch outcome {
	case OutcomePass:
		return true, true
	case OutcomeFail:
		return false, true
	case "":
		return legacyPass, true
	default:
		return false, false
	}
}

func outcomeFor(pass bool) Outcome {
	if pass {
		return OutcomePass
	}
	return OutcomeFail
}
