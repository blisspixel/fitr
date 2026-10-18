package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/experiment"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/stats"
)

func objectiveMetric(spec decision.DecisionSpec) string {
	if spec.Objective == nil {
		return ""
	}
	return spec.Objective.Metric
}

// loadConfirmationNoise checks a timed confirmation before any runtime
// connection. A missing declared gap or an unclaimable noise file is
// unresolved. Flags on an exact byte or behavior objective are a usage error.
func loadConfirmationNoise(spec decision.DecisionSpec, command confirmationCommand) (float64, *record.Record, int, bool) {
	metric := objectiveMetric(spec)
	if !experiment.TimedObjective(metric) {
		if command.minGap != "" || command.noiseFrom != "" {
			errPrint("min-gap and noise-from apply only to a timed confirmation objective", metric,
				"remove them, or set the objective to decode, prefill, or TTFT")
			return 0, nil, exitUsage, false
		}
		return 0, nil, exitOK, true
	}
	if command.minGap == "" || command.noiseFrom == "" {
		errPrint("this confirmation budget is unresolved",
			"a timed objective needs the relative gap to resolve and a sealed noise observation from this device and context",
			"pass --min-gap 0.08 --noise-from result.json")
		return 0, nil, exitUnresolved, false
	}
	gap, err := parseRelativeGap(command.minGap)
	if err != nil {
		errPrint("invalid relative gap", err.Error(), "use 0.08 for an eight percent gap")
		return 0, nil, exitUsage, false
	}
	noise, err := record.NewStore(resultsDir()).Read(command.noiseFrom)
	if err != nil {
		errPrint("could not read noise observation: "+err.Error(), command.noiseFrom,
			"pass a sealed fitr result from this device and context")
		return 0, nil, exitError, false
	}
	if _, err = experiment.ClaimableVariation(noise, metric); err != nil {
		errPrint("noise observation cannot set the confirmation budget", err.Error(),
			"pass a claimable result for this metric from this device and context")
		return 0, nil, exitUnresolved, false
	}
	if err = experiment.NoiseContext(noise, command.ctx); err != nil {
		errPrint("noise observation is not this confirmation context", err.Error(),
			"measure the variation at the context this confirmation will request")
		return 0, nil, exitUnresolved, false
	}
	return gap, noise, exitOK, true
}

func applyConfirmationBudget(metric string, gap float64, noise *record.Record, live device.Fingerprint, repeats int) (int, bool) {
	if noise == nil {
		return exitOK, true
	}
	if err := experiment.NoiseDevice(live, noise); err != nil {
		errPrint("noise observation is not this confirmation device", err.Error(),
			"measure the variation on the device that will run the confirmation")
		return exitUnresolved, false
	}
	cv, err := experiment.ClaimableVariation(noise, metric)
	if err != nil {
		errPrint("noise observation cannot set the confirmation budget", err.Error(),
			"pass a claimable result for this metric from this device and context")
		return exitUnresolved, false
	}
	return refuseInseparableBudget(cv, gap, repeats)
}

func refuseInseparableBudget(cv, gap float64, repeats int) (int, bool) {
	separation, err := stats.PredictMeanSeparation(cv, 1+gap, repeats)
	if err != nil {
		errPrint("confirmation budget is unresolved", err.Error(),
			"declare a finite gap above zero and a measured variation")
		return exitUnresolved, false
	}
	if separation.Separable {
		return exitOK, true
	}
	note, hint := budgetRefusal(separation, cv, gap)
	errPrint("this repeat budget cannot separate the declared gap", note, hint)
	return exitUnresolved, false
}

func budgetRefusal(separation stats.MeanSeparation, cv, gap float64) (string, string) {
	if separation.RequiredRepeats == 0 {
		return fmt.Sprintf("measured variation is %.2f%%; no repeat count from %d to %d separates a %.2f%% gap",
				cv*100, stats.MinimumComparisonRepeats, stats.MaximumComparisonRepeats, gap*100),
			"declare a larger gap or leave the comparison unresolved"
	}
	return fmt.Sprintf("-k %d at %.2f%% variation has a %.2f%% relative 95%% half-width; a %.2f%% gap needs -k %d",
			separation.Repeats, cv*100, separation.HalfWidth*100, gap*100, separation.RequiredRepeats),
		fmt.Sprintf("rerun with -k %d, or declare a larger gap", separation.RequiredRepeats)
}

func parseRelativeGap(text string) (float64, error) {
	gap, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(gap) || math.IsInf(gap, 0) || gap <= 0 {
		return 0, errors.New("--min-gap must be a finite number greater than 0")
	}
	return gap, nil
}
