package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/render"
)

func cmdDecide(_ context.Context, args []string) int {
	fs := flag.NewFlagSet("decide", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	specPath := fs.String("spec", "", "decision specification JSON")
	mode := fs.String("display", "auto", "auto|rich|plain|json|none")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if !render.ValidMode(*mode) {
		errPrint("invalid display mode", *mode, "use auto, rich, plain, json, or none")
		return exitUsage
	}
	if strings.TrimSpace(*specPath) == "" {
		errPrint("missing decision specification", "--spec is required",
			"fitr decide [model|result.json] --spec decision.json")
		return exitUsage
	}
	if fs.NArg() > 1 {
		errPrint("too many arguments", "decide accepts one model or result path",
			"fitr decide [model|result.json] --spec decision.json")
		return exitUsage
	}

	spec, err := decision.LoadSpec(*specPath)
	if err != nil {
		errPrint("could not load decision specification: "+err.Error(), "", "fix the spec and run fitr decide again")
		return exitError
	}
	selected, code, ok := selectViewResult(fs.Arg(0), fs.NArg() == 1)
	if !ok {
		return code
	}
	evaluation, err := decision.Evaluate(selected, spec)
	if err != nil {
		errPrint("could not evaluate decision: "+err.Error(), "", "inspect the sealed result with fitr view --full")
		return exitError
	}
	if render.Resolve(*mode) != "none" {
		if render.Resolve(*mode) == "json" {
			if err := writeDecisionJSON(evaluation); err != nil {
				errPrint("could not render decision: "+err.Error(), "", "")
				return exitError
			}
		} else {
			writeDecisionText(evaluation)
		}
	}
	return decisionExitCode(evaluation.State)
}

func writeDecisionJSON(evaluation decision.Evaluation) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(evaluation)
}

func writeDecisionText(evaluation decision.Evaluation) {
	fmt.Fprintf(os.Stdout, "DECISION  %s\n", strings.ToUpper(string(evaluation.State)))
	fmt.Fprintf(os.Stdout, "  %-10s %s\n", "spec", terminalText(evaluation.SpecName))
	fmt.Fprintf(os.Stdout, "  %-10s %s\n", "evidence", terminalText(string(evaluation.Evidence)))
	fmt.Fprintf(os.Stdout, "  %-10s %s\n", "subject", terminalText(evaluation.Subject.ResolvedModel))
	fmt.Fprintf(os.Stdout, "  %-10s %s\n", "config", terminalText(evaluation.Subject.ID))
	fmt.Fprintln(os.Stdout, "\nREQUIREMENTS")
	for _, requirement := range evaluation.Requirements {
		fmt.Fprintf(os.Stdout, "  %-12s %-22s %s\n",
			strings.ToUpper(string(requirement.State)), terminalText(requirement.ID), terminalText(requirement.Reason))
		if observed := decisionObservation(requirement); observed != "" {
			fmt.Fprintf(os.Stdout, "  %-12s %-22s %s\n", "", "observed", observed)
		}
		if len(requirement.Missing) > 0 {
			fmt.Fprintf(os.Stdout, "  %-12s %-22s %s\n", "", "missing",
				terminalText(strings.Join(requirement.Missing, "; ")))
		}
	}
	if evaluation.Objective != nil {
		fmt.Fprintln(os.Stdout, "\nOBJECTIVE")
		fmt.Fprintf(os.Stdout, "  %-12s %-22s %s\n", strings.ToUpper(string(evaluation.Objective.State)),
			terminalText(evaluation.Objective.Metric), terminalText(evaluation.Objective.Reason))
	}
	if evaluation.NextAction != nil {
		fmt.Fprintln(os.Stdout, "\nNEXT")
		if len(evaluation.NextAction.Argv) > 0 {
			argv := make([]string, 0, len(evaluation.NextAction.Argv))
			for _, argument := range evaluation.NextAction.Argv {
				argv = append(argv, terminalText(argument))
			}
			fmt.Fprintln(os.Stdout, "  "+strings.Join(argv, " "))
		} else {
			fmt.Fprintln(os.Stdout, "  experiment "+terminalText(evaluation.NextAction.Experiment))
		}
		fmt.Fprintln(os.Stdout, "  "+terminalText(evaluation.NextAction.Reason))
	}
}

func decisionObservation(requirement decision.RequirementResult) string {
	if requirement.Observed == nil {
		return ""
	}
	value := fmt.Sprintf("%.4g %s", *requirement.Observed, requirement.Unit)
	if requirement.IntervalLow != nil && requirement.IntervalHigh != nil {
		value += fmt.Sprintf("; 95%% interval [%.4g, %.4g]", *requirement.IntervalLow, *requirement.IntervalHigh)
	}
	if requirement.IndependentUnits > 0 {
		value += fmt.Sprintf("; %d independent families", requirement.IndependentUnits)
	}
	return terminalText(value)
}

func decisionExitCode(state decision.EvaluationState) int {
	switch state {
	case decision.DecisionEligible:
		return exitOK
	case decision.DecisionIneligible:
		return exitGates
	default:
		return exitUnresolved
	}
}
