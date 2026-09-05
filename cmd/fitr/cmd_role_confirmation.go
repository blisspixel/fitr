package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/role"
)

type roleConfirmationCommand struct {
	action, mode, backend string
	args                  []string
}

func roleLifecycleAction(action string) bool {
	switch action {
	case "confirm", "adopt", "rollback", "status":
		return true
	default:
		return false
	}
}

func parseRoleConfirmation(args []string) (roleConfirmationCommand, int, bool) {
	command := roleConfirmationCommand{action: args[0]}
	fs := flag.NewFlagSet("role "+command.action, flag.ContinueOnError)
	fs.StringVar(&command.mode, "display", "auto", "auto|rich|plain|json|none")
	if command.action == "confirm" {
		fs.StringVar(&command.backend, "backend", "auto", "auto|ollama|llama-server|openai")
	}
	if code, ok := parseCommandFlags(fs, args[1:]); !ok {
		return command, code, false
	}
	count := 1
	if command.action == "adopt" {
		count = 2
	}
	if fs.NArg() != count || !render.ValidMode(command.mode) {
		errPrint("invalid role lifecycle arguments", "", "fitr role --help")
		return command, exitUsage, false
	}
	command.args = append([]string(nil), fs.Args()...)
	return command, exitOK, true
}

func cmdRoleLifecycle(ctx context.Context, args []string) int {
	command, code, ok := parseRoleConfirmation(args)
	if !ok {
		return code
	}
	if command.action == "confirm" && strings.HasSuffix(strings.ToLower(command.args[0]), ".json") {
		return reopenRoleConfirmation(command)
	}
	store := role.Store{Dir: filepath.Join(resultsDir(), ".roles")}
	records := record.Store{Dir: resultsDir()}
	switch command.action {
	case "confirm":
		return startRoleConfirmation(ctx, store, records, command)
	case "status":
		return showRoleSelection(store, records, command.args[0], command.mode)
	default:
		return changeRoleSelection(store, records, command)
	}
}

func reopenRoleConfirmation(command roleConfirmationCommand) int {
	if command.backend != "auto" {
		errPrint("a saved role bundle does not accept live backend flags", "", "remove --backend")
		return exitUsage
	}
	bundle, err := role.LoadConfirmationBundle(command.args[0])
	if err != nil {
		return roleFailure(err)
	}
	return writeRoleConfirmation(bundle, command.mode, command.args[0])
}

func startRoleConfirmation(ctx context.Context, store role.Store, records record.Store, command roleConfirmationCommand) int {
	library, err := store.Load(command.args[0])
	if err != nil {
		return roleFailure(err)
	}
	plan, err := planRoleConfirmation(library, records, time.Now())
	if err != nil {
		return roleFailure(err)
	}
	life, err := store.LoadLifecycle(library.Name)
	if err != nil {
		return roleFailure(err)
	}
	// Select the runtime without the ordinary model-check path, which can
	// automatically pull a missing HF alias. Confirmation only measures the
	// already installed identity; execute resolves it before loading or inference.
	backend, code := newBackendWithDisplay(ctx, "", command.backend, false, nil)
	if code != exitOK {
		return code
	}
	life, err = store.IssueConfirmation(plan, life.Digest, time.Now())
	if err != nil {
		return roleFailure(err)
	}
	life, err = store.BeginConfirmation(library.Name, plan.PlanSHA256, life.Digest, time.Now())
	if err != nil {
		return roleFailure(err)
	}
	return executeRoleConfirmation(ctx, store, records, backend, plan, life, command.mode)
}

func planRoleConfirmation(library role.Library, records record.Store, now time.Time) (role.ConfirmationPlan, error) {
	review, err := role.Review(library, records, now)
	if err != nil {
		return role.ConfirmationPlan{}, err
	}
	if review.State != "exploration-lead" && review.State != "single-qualified" {
		return role.ConfirmationPlan{}, errors.New("resolve the role's exploratory choice before confirmation")
	}
	spec, err := library.CurrentSpec()
	if err != nil {
		return role.ConfirmationPlan{}, err
	}
	chosen := review.Lead
	if chosen == "" {
		for _, candidate := range review.Candidates {
			if candidate.State == "eligible" {
				chosen = candidate.ID
			}
		}
	}
	points := make([]*record.Record, 0, len(library.Candidates))
	for _, attached := range library.Candidates {
		current, err := role.AttachRecord(attached.Path, records)
		if err != nil || current != attached {
			return role.ConfirmationPlan{}, errors.New("exploration evidence changed; reattach current canonical evidence")
		}
		point, err := records.Read(current.Path)
		if err != nil {
			return role.ConfirmationPlan{}, err
		}
		if point.Completion == nil || point.Completion.EvidenceSHA256 != attached.EvidenceSHA256 || point.StableRunID() != attached.RunID {
			return role.ConfirmationPlan{}, errors.New("exploration evidence changed while planning")
		}
		points = append(points, point)
	}
	return role.NewConfirmationPlan(spec, points, chosen, now)
}

func executeRoleConfirmation(ctx context.Context, store role.Store, records record.Store, backend llm.Backend,
	plan role.ConfirmationPlan, life role.Lifecycle, mode string) int {
	points, err := collectRoleConfirmation(ctx, backend, plan)
	if err != nil {
		status, code := "failed", exitError
		if ctx.Err() != nil {
			status, code = "cancelled", exitInterrupt
		}
		if _, finishErr := store.FinishConfirmation(plan.Spec.Name, plan.PlanSHA256, status, nil, records, life.Digest, time.Now()); finishErr != nil {
			errPrint("could not record terminal confirmation state: "+finishErr.Error(), "the attempt cannot resume or adopt", "fitr role status "+plan.Spec.Name)
		}
		errPrint("role confirmation stopped: "+err.Error(), "completed point records remain saved; incumbent unchanged", "review the cause, then issue a new confirmation")
		return code
	}
	bundle, err := role.NewConfirmationBundle(plan, points, time.Now())
	if err != nil {
		return failRoleConfirmation(store, records, plan, life, err)
	}
	path, err := store.SaveConfirmationBundle(bundle)
	if err != nil {
		return failRoleConfirmation(store, records, plan, life, err)
	}
	if _, err := store.FinishConfirmation(plan.Spec.Name, plan.PlanSHA256, "completed", &bundle, records, life.Digest, time.Now()); err != nil {
		return roleFailure(err)
	}
	fmt.Fprintf(os.Stderr, "  bundle   %s\n", terminalText(path))
	return writeRoleConfirmation(bundle, mode, path)
}

func failRoleConfirmation(store role.Store, records record.Store, plan role.ConfirmationPlan, life role.Lifecycle, cause error) int {
	if _, err := store.FinishConfirmation(plan.Spec.Name, plan.PlanSHA256, "failed", nil, records, life.Digest, time.Now()); err != nil {
		errPrint("confirmation failure could not be recorded: "+err.Error(), "the started attempt remains ineligible for adoption", "fitr role status "+plan.Spec.Name)
	}
	return roleFailure(cause)
}

func collectRoleConfirmation(ctx context.Context, backend llm.Backend, plan role.ConfirmationPlan) ([]*record.Record, error) {
	display := render.New("none")
	defer display.Close()
	points := make([]*record.Record, 0, len(plan.Candidates))
	for index, candidate := range plan.Candidates {
		fmt.Fprintf(os.Stderr, "  confirm  %d/%d  %s\n", index+1, len(plan.Candidates), terminalText(candidate.Model.Resolved))
		binding := role.ConfirmationPlanBinding(plan, index+1)
		opts := roleConfirmationRunOptions(plan, index+1, &binding)
		point, err := execute(ctx, backend, candidate.Model.Resolved, opts, display)
		if err != nil {
			return points, err
		}
		if _, err := save(point); err != nil {
			return points, err
		}
		points = append(points, point)
	}
	return points, nil
}

func roleConfirmationRunOptions(plan role.ConfirmationPlan, point int, binding *record.ExperimentBinding) runOpts {
	protocol := plan.Protocol
	allocation := plan.Candidates[point-1].Capacity
	return runOpts{
		level: protocol.Level, profile: protocol.Profile, seedSet: plan.SeedSet,
		reps: protocol.Repeats, checksReps: protocol.Repeats,
		numCtx: protocol.RequestedContext, memoryCtx: protocol.RequestedContext, experiment: binding,
		capacityBudgetGB: bytesAsGiB(allocation.OperatorBudgetBytes), capacityReserveGB: bytesAsGiB(allocation.OperatorReserveBytes),
		validatePrepared: func(run *runExecution) error {
			return role.ValidatePreparedConfirmationPoint(plan, point, run.result, run.resolved.Identity, run.provenance)
		},
		validateCapacity: func(run *runExecution) error { return validateRoleCapacityBeforeLoad(plan, point, run) },
		validateContext:  func(run *runExecution) error { return role.ValidateConfirmationContextPoint(plan, run.result) },
	}
}

func bytesAsGiB(value *int64) *float64 {
	if value == nil {
		return nil
	}
	amount := float64(*value) / float64(1<<30)
	return &amount
}

func changeRoleSelection(store role.Store, records record.Store, command roleConfirmationCommand) int {
	name := command.args[0]
	life, err := store.LoadLifecycle(name)
	if err != nil {
		return roleFailure(err)
	}
	if command.action == "adopt" {
		bundle, loadErr := role.LoadConfirmationBundle(command.args[1])
		if loadErr != nil {
			return roleFailure(loadErr)
		}
		_, err = store.AdoptConfirmation(name, bundle.Plan.PlanSHA256, bundle, records, life.Digest, time.Now())
	} else {
		_, err = store.RollbackSelection(name, life.PreviousSHA256, records, life.Digest, time.Now())
	}
	if err != nil {
		return roleFailure(err)
	}
	return showRoleSelection(store, records, name, command.mode)
}

func showRoleSelection(store role.Store, records record.Store, name, mode string) int {
	status, err := store.ReviewSelection(name, records, time.Now())
	if err != nil {
		return roleFailure(err)
	}
	if render.Resolve(mode) == "json" {
		if code := writeRoleJSON(status); code != exitOK {
			return code
		}
	} else if render.Resolve(mode) != "none" {
		render.WriteRoleSelection(os.Stdout, status, mode)
	}
	if status.State != "qualified" {
		return exitUnresolved
	}
	return exitOK
}

func writeRoleConfirmation(bundle role.ConfirmationBundle, mode, path string) int {
	report, err := bundle.Validate()
	if err != nil {
		return roleFailure(err)
	}
	if render.Resolve(mode) == "json" {
		if code := writeRoleJSON(report); code != exitOK {
			return code
		}
	} else if render.Resolve(mode) != "none" {
		next := "Keep the incumbent. Resolve the evidence gap before issuing another fresh confirmation."
		if report.State == "confirmed" {
			next = "Store this choice: fitr role adopt " + bundle.Plan.Spec.Name + " " + shellCommandArg(path, runtime.GOOS)
		}
		render.WriteRoleReview(os.Stdout, role.ReviewReport{
			Role: bundle.Plan.Spec.Name, State: report.State, Scope: report.Scope,
			Candidates: report.Candidates, Gaps: report.Gaps, Next: next,
		}, mode)
	}
	switch report.State {
	case "confirmed":
		return exitOK
	case "no-qualified-candidate":
		return exitGates
	default:
		return exitUnresolved
	}
}
