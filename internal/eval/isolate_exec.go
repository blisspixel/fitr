package eval

import (
	"context"
	"time"

	"github.com/blisspixel/fitr/internal/isolate"
)

// isolationStatus is the sandbox probe. Tests replace it so a machine with
// bubblewrap does not change cases that are about the unisolated flag.
var isolationStatus = isolate.Probe

type isolatedExecutionKey struct{}

// WithIsolatedExecution opts one run into the bubblewrap worker. Other
// protocols, including auto, tailor, and role confirmation, leave the value
// unset and keep generated code disabled.
func WithIsolatedExecution(ctx context.Context) context.Context {
	return context.WithValue(ctx, isolatedExecutionKey{}, true)
}

// IsolatedExecutionEnabled reports whether this context opted into the worker.
func IsolatedExecutionEnabled(ctx context.Context) bool {
	enabled, _ := ctx.Value(isolatedExecutionKey{}).(bool)
	return enabled
}

func executionKind(ctx context.Context) string {
	if unsafeExecutionEnabled(ctx) {
		return "unsafe"
	}
	if IsolatedExecutionEnabled(ctx) && isolationStatus().Available {
		return "isolated"
	}
	return "unavailable"
}

// isolatedOutcome scores a verifier that ran in the sandbox. A process that
// never started, or a limit that stopped the probe, is an error. It is not
// evidence about the model.
func isolatedOutcome(pass bool, receipt VerificationReceipt) Outcome {
	if receipt.Failure != nil {
		switch receipt.Failure.Kind {
		case FailureExecutorPreflight, FailureExecutorLaunch, FailureExecutorTimeout:
			return OutcomeError
		}
	}
	if !receipt.ProcessStarted {
		return OutcomeError
	}
	return outcomeFor(pass)
}

func isolationSkipDetail() string {
	reason := isolationStatus().Reason
	if reason == "" {
		return "isolated execution is unavailable"
	}
	return reason
}

func verifyIsolated(ctx context.Context, dir string, runner resolvedTaskRunner, marker string) VerificationReceipt {
	r := VerificationReceipt{
		Protocol: "fitr.verifier.v2", ExitCode: -1,
		InterpreterPath: runner.path, InterpreterVer: runner.version, InterpreterHash: runner.sha256,
	}
	if err := runner.validateIdentity(); err != nil {
		r.Failure = failure(FailureExecutorPreflight, "verify", err)
		return r
	}
	cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	cmd, err := isolate.Command(cctx, dir, runner.path, runner.args)
	if err != nil {
		r.Failure = failure(FailureExecutorLaunch, "verify", err)
		return r
	}
	out, err := cmd.CombinedOutput()
	started, exitCode := false, -1
	if cmd.ProcessState != nil {
		started, exitCode = true, cmd.ProcessState.ExitCode()
	}
	r = verifierReceiptFromProcess(out, started, exitCode, err, cctx.Err(), marker)
	r.InterpreterPath, r.InterpreterVer, r.InterpreterHash = runner.path, runner.version, runner.sha256
	return r
}

func verifyForKind(ctx context.Context, dir string, runner resolvedTaskRunner, marker string) VerificationReceipt {
	if executionKind(ctx) == "isolated" {
		return verifyIsolated(ctx, dir, runner, marker)
	}
	return verifyIn(ctx, dir, runner, marker)
}
