package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/blisspixel/fitr/internal/calibration"
	"github.com/blisspixel/fitr/internal/eval"
)

// cmdCalibrate reports which check items discriminated between two saved
// runs (typically two quants of the same model on a shared seedset). It
// does not rewrite the spec: dropping an item is a human decision after
// more than one box has spoken.
func cmdCalibrate(ctx context.Context, args []string) int {
	if len(args) > 0 && args[0] == "merge" {
		return cmdCalibrateMerge(args[1:])
	}
	command, code, ok := parseCalibrateCommand(args)
	if !ok {
		return code
	}
	a, b, stats, report, code, ok := buildCalibrationPair(command)
	if !ok {
		return code
	}
	if err := attachCalibrateLineage(&report, command.lineagePath); err != nil {
		errPrint("could not verify same-base lineage", err.Error(),
			"pass a fitr.lineage.conversion.v1 manifest with --lineage, or keep the pair exploratory")
		return exitError
	}
	renderCalibrationPair(a, b, stats, report)
	if command.out != "" {
		if err := calibration.WriteJSON(command.out, report); err != nil {
			errPrint("could not write calibration report", err.Error(), "")
			return exitError
		}
		fmt.Printf("\n  wrote      %s (pseudonymous device, seedset, and local-model IDs; no prompts or raw output)\n",
			terminalText(command.out))
	}
	fmt.Println("\n  this command does not rewrite spec/tasks. One pair is a lead, not a cull.")
	return exitOK
}

type calibrateCommand struct {
	a, b        string
	out         string
	lineagePath string
}

func parseCalibrateCommand(args []string) (calibrateCommand, int, bool) {
	fs := flag.NewFlagSet("calibrate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	out := fs.String("out", "", "write a privacy-safe calibration pair as JSON")
	lineagePath := fs.String("lineage", "", "publisher conversion manifest binding both artifacts to one base revision")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return calibrateCommand{}, code, false
	}
	if fs.NArg() != 2 {
		errPrint("need two saved results", "",
			"fitr run a --checks-only --seedset night && fitr run b --checks-only --seedset night && fitr calibrate a b")
		return calibrateCommand{}, exitUsage, false
	}
	return calibrateCommand{a: fs.Arg(0), b: fs.Arg(1), out: *out, lineagePath: *lineagePath}, exitOK, true
}

func buildCalibrationPair(command calibrateCommand) (
	*Result, *Result, []eval.ItemStat, calibration.PairReport, int, bool,
) {
	results, err := loadResults()
	if err != nil || len(results) == 0 {
		errPrint("no saved results", "", "fitr run both models with the same --seedset first")
		return nil, nil, nil, calibration.PairReport{}, exitError, false
	}
	a := latestNamed(results, command.a)
	b := latestNamed(results, command.b)
	if a == nil || b == nil {
		errPrint("need two saved results", "", "fitr board lists what is on disk")
		return nil, nil, nil, calibration.PairReport{}, exitUsage, false
	}
	if a.SeedSet == "" || a.SeedSet != b.SeedSet {
		errPrint("runs did not share a seedset",
			fmt.Sprintf("%s seedset=%q, %s seedset=%q", a.Model, a.SeedSet, b.Model, b.SeedSet),
			"re-run both with the same --seedset so instances pair")
		return nil, nil, nil, calibration.PairReport{}, exitUsage, false
	}
	stats := eval.ItemStats(a.Checks, b.Checks)
	if len(stats) == 0 {
		errPrint("no shared check instances", "", "both runs need --checks-only (or the default/full level), the same seedset, and the same -k")
		return nil, nil, nil, calibration.PairReport{}, exitError, false
	}
	report, err := calibrationPair(a, b, stats)
	if err != nil {
		errPrint("invalid calibration pair", err.Error(), "pair the same model family and size on one unchanged device/config")
		return nil, nil, nil, calibration.PairReport{}, exitUsage, false
	}
	return a, b, stats, report, exitOK, true
}

func renderCalibrationPair(a, b *Result, stats []eval.ItemStat, report calibration.PairReport) {
	qa, qb := a.ModelMeta.Details.QuantizationLevel, b.ModelMeta.Details.QuantizationLevel
	fmt.Printf("  calibrate  %s (%s)  vs  %s (%s)\n", terminalText(a.Model), terminalText(qa), terminalText(b.Model), terminalText(qb))
	fmt.Printf("  seedset    %s\n", terminalText(a.SeedSet))
	assessment := calibration.AssessPair(report)
	writeCalibrationAssessment(report, assessment)
	kept, drop := partitionCalibrationStats(stats)
	fmt.Printf("  %d items shared, %d discriminated, %d never flipped\n",
		len(stats), len(kept), len(drop))
	writeCalibrationDirectionNote(qa, qb, assessment.SameBaseLineageVerified)
	writeCalibrationItems(kept, drop)
}

func writeCalibrationAssessment(report calibration.PairReport, assessment calibration.PairAssessment) {
	if assessment.SameBaseLineageVerified {
		fmt.Printf("  lineage    verified (%s)\n", terminalText(report.Lineage.Method))
		fmt.Printf("  evidence   unsigned: %s\n", terminalText(strings.Join(assessment.Reasons, "; ")))
		return
	}
	fmt.Printf("  evidence   exploratory only: %s\n", terminalText(strings.Join(assessment.Reasons, "; ")))
}

func partitionCalibrationStats(stats []eval.ItemStat) (kept, drop []eval.ItemStat) {
	for _, stat := range stats {
		if stat.Discriminated() {
			kept = append(kept, stat)
		} else {
			drop = append(drop, stat)
		}
	}
	return kept, drop
}

func writeCalibrationDirectionNote(qa, qb string, lineageVerified bool) {
	ra, rb := eval.QuantRank(qa), eval.QuantRank(qb)
	switch {
	case ra == 0 || rb == 0 || ra == rb:
		fmt.Println("  note       dtypes are not a ranked pair; this is discrimination, not directional quant damage")
	case lineageVerified:
		fmt.Println("  note       same-base lineage is verified; unsigned pairs still cannot create campaign readiness")
	default:
		fmt.Println("  note       same-base revision lineage is unverified; flips are discrimination only, not directional quant damage")
	}
}

func writeCalibrationItems(kept, drop []eval.ItemStat) {
	if len(kept) > 0 {
		fmt.Println("\n  discriminated (these separate the two runs):")
		for _, s := range kept {
			fmt.Printf("    %-22s  %d/%d flipped  %s\n", terminalText(s.TaskID), s.Flips, s.Shared, terminalText(s.Need))
		}
	}
	if len(drop) > 0 {
		fmt.Println("\n  never flipped (not evidence to drop until more devices and model pairs agree):")
		for _, s := range drop {
			fmt.Printf("    %-22s  %d/%d agree    %s\n", terminalText(s.TaskID), s.Shared, s.Shared, terminalText(s.Need))
		}
	}
}

func cmdCalibrateMerge(args []string) int {
	reports, out, code, ok := loadCalibrationReports(args)
	if !ok {
		return code
	}
	summary, err := calibration.Aggregate(reports)
	if err != nil {
		errPrint("could not aggregate calibration reports", err.Error(), "")
		return exitError
	}
	writeCalibrationSummary(reports, summary)
	if out != "" {
		if err := calibration.WriteJSON(out, summary); err != nil {
			errPrint("could not write calibration summary", err.Error(), "")
			return exitError
		}
		fmt.Printf("\n  wrote  %s\n", terminalText(out))
	}
	fmt.Println("\n  evidence only: aggregation never deletes or rewrites a task.")
	return exitOK
}

func loadCalibrationReports(args []string) ([]calibration.PairReport, string, int, bool) {
	fs := flag.NewFlagSet("calibrate merge", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	out := fs.String("out", "", "write the aggregate calibration summary as JSON")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return nil, "", code, false
	}
	if fs.NArg() < 1 {
		errPrint("need calibration pair reports", "", "fitr calibrate merge pair-a.json pair-b.json --out summary.json")
		return nil, "", exitUsage, false
	}
	reports := make([]calibration.PairReport, 0, fs.NArg())
	for _, path := range fs.Args() {
		r, err := calibration.ReadPair(path)
		if err != nil {
			errPrint("could not read calibration report", err.Error(), "")
			return nil, "", exitError, false
		}
		reports = append(reports, r)
	}
	return reports, *out, exitOK, true
}

func writeCalibrationSummary(reports []calibration.PairReport, summary calibration.Summary) {
	fmt.Printf("  calibration evidence  %d report(s), %d device(s), %d model pair(s), spec v%d\n",
		summary.Reports, summary.Devices, summary.ModelPairs, summary.SpecVersion)
	readiness := summary.Readiness
	fmt.Printf("  authenticated          %d/%d report(s), %d/%d device(s), %d/%d model families\n",
		readiness.DecisionGradeReports, summary.Reports,
		readiness.Devices, readiness.MinimumDevices,
		readiness.ModelFamilies, readiness.MinimumModelFamilies)
	fmt.Printf("  readiness              UNVERIFIED: %s\n", terminalText(strings.Join(readiness.Missing, "; ")))
	writeExploratoryReports(reports)
	observed, unseen := partitionSummaryItems(summary.Items)
	writeSummaryItems(observed, unseen)
}

func writeExploratoryReports(reports []calibration.PairReport) {
	var exploratory []string
	for _, report := range reports {
		assessment := calibration.AssessPair(report)
		if !assessment.DecisionGrade {
			exploratory = append(exploratory, fmt.Sprintf("%s / %s: %s",
				report.Reference.Model, report.Candidate.Model, strings.Join(assessment.Reasons, "; ")))
		}
	}
	if len(exploratory) > 0 {
		fmt.Println("\n  exploratory reports excluded from readiness:")
		for _, reason := range exploratory {
			fmt.Printf("    %s\n", terminalText(reason))
		}
	}
}

func partitionSummaryItems(items []calibration.SummaryItem) (observed, unseen []calibration.SummaryItem) {
	for _, item := range items {
		if item.Status == "observed" {
			observed = append(observed, item)
		} else {
			unseen = append(unseen, item)
		}
	}
	return observed, unseen
}

func writeSummaryItems(observed, unseen []calibration.SummaryItem) {
	if len(observed) > 0 {
		fmt.Println("\n  discrimination observed:")
		for _, item := range observed {
			fmt.Printf("    %-22s  %d flip(s)/%d shared  on %d/%d device(s)\n",
				terminalText(item.TaskID), item.Flips, item.Shared, item.DiscriminatedDevices, item.Devices)
		}
	}
	if len(unseen) > 0 {
		fmt.Println("\n  discrimination not yet observed:")
		for _, item := range unseen {
			fmt.Printf("    %-22s  0/%d shared  across %d device(s)\n", terminalText(item.TaskID), item.Shared, item.Devices)
		}
	}
}
