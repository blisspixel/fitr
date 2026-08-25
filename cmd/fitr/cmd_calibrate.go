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
	fs := flag.NewFlagSet("calibrate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	out := fs.String("out", "", "write a privacy-safe calibration pair as JSON")
	lineagePath := fs.String("lineage", "", "publisher conversion manifest binding both artifacts to one base revision")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 2 {
		errPrint("need two saved results", "",
			"fitr run a --checks-only --seedset night && fitr run b --checks-only --seedset night && fitr calibrate a b")
		return exitUsage
	}
	results, err := loadResults()
	if err != nil || len(results) == 0 {
		errPrint("no saved results", "", "fitr run both models with the same --seedset first")
		return exitError
	}
	a := latestNamed(results, fs.Arg(0))
	b := latestNamed(results, fs.Arg(1))
	if a == nil || b == nil {
		errPrint("need two saved results", "", "fitr board lists what is on disk")
		return exitUsage
	}
	if a.SeedSet == "" || a.SeedSet != b.SeedSet {
		errPrint("runs did not share a seedset",
			fmt.Sprintf("%s seedset=%q, %s seedset=%q", a.Model, a.SeedSet, b.Model, b.SeedSet),
			"re-run both with the same --seedset so instances pair")
		return exitUsage
	}
	stats := eval.ItemStats(a.Checks, b.Checks)
	if len(stats) == 0 {
		errPrint("no shared check instances", "", "both runs need --checks-only (or the default/full level), the same seedset, and the same -k")
		return exitError
	}
	report, err := calibrationPair(a, b, stats)
	if err != nil {
		errPrint("invalid calibration pair", err.Error(), "pair the same model family and size on one unchanged device/config")
		return exitUsage
	}
	if err := attachCalibrateLineage(&report, *lineagePath); err != nil {
		errPrint("could not verify same-base lineage", err.Error(),
			"pass a fitr.lineage.conversion.v1 manifest with --lineage, or keep the pair exploratory")
		return exitError
	}
	qa, qb := a.ModelMeta.Details.QuantizationLevel, b.ModelMeta.Details.QuantizationLevel
	fmt.Printf("  calibrate  %s (%s)  vs  %s (%s)\n", terminalText(a.Model), terminalText(qa), terminalText(b.Model), terminalText(qb))
	fmt.Printf("  seedset    %s\n", terminalText(a.SeedSet))
	assessment := calibration.AssessPair(report)
	if assessment.SameBaseLineageVerified {
		fmt.Printf("  lineage    verified (%s)\n", terminalText(report.Lineage.Method))
		fmt.Printf("  evidence   unsigned: %s\n", terminalText(strings.Join(assessment.Reasons, "; ")))
	} else {
		fmt.Printf("  evidence   exploratory only: %s\n", terminalText(strings.Join(assessment.Reasons, "; ")))
	}
	var kept, drop []eval.ItemStat
	for _, s := range stats {
		if s.Discriminated() {
			kept = append(kept, s)
		} else {
			drop = append(drop, s)
		}
	}
	fmt.Printf("  %d items shared, %d discriminated, %d never flipped\n",
		len(stats), len(kept), len(drop))
	if ra, rb := eval.QuantRank(qa), eval.QuantRank(qb); ra == 0 || rb == 0 || ra == rb {
		fmt.Println("  note       dtypes are not a ranked pair; this is discrimination, not directional quant damage")
	} else if assessment.SameBaseLineageVerified {
		fmt.Println("  note       same-base lineage is verified; unsigned pairs still cannot create campaign readiness")
	} else {
		fmt.Println("  note       same-base revision lineage is unverified; flips are discrimination only, not directional quant damage")
	}
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
	if *out != "" {
		if err := calibration.WriteJSON(*out, report); err != nil {
			errPrint("could not write calibration report", err.Error(), "")
			return exitError
		}
		fmt.Printf("\n  wrote      %s (pseudonymous device, seedset, and local-model IDs; no prompts or raw output)\n", terminalText(*out))
	}
	fmt.Println("\n  this command does not rewrite spec/tasks. One pair is a lead, not a cull.")
	return exitOK
}

func cmdCalibrateMerge(args []string) int {
	fs := flag.NewFlagSet("calibrate merge", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	out := fs.String("out", "", "write the aggregate calibration summary as JSON")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() < 1 {
		errPrint("need calibration pair reports", "", "fitr calibrate merge pair-a.json pair-b.json --out summary.json")
		return exitUsage
	}
	reports := make([]calibration.PairReport, 0, fs.NArg())
	for _, path := range fs.Args() {
		r, err := calibration.ReadPair(path)
		if err != nil {
			errPrint("could not read calibration report", err.Error(), "")
			return exitError
		}
		reports = append(reports, r)
	}
	summary, err := calibration.Aggregate(reports)
	if err != nil {
		errPrint("could not aggregate calibration reports", err.Error(), "")
		return exitError
	}
	fmt.Printf("  calibration evidence  %d report(s), %d device(s), %d model pair(s), spec v%d\n",
		summary.Reports, summary.Devices, summary.ModelPairs, summary.SpecVersion)
	readiness := summary.Readiness
	fmt.Printf("  authenticated          %d/%d report(s), %d/%d device(s), %d/%d model families\n",
		readiness.DecisionGradeReports, summary.Reports,
		readiness.Devices, readiness.MinimumDevices,
		readiness.ModelFamilies, readiness.MinimumModelFamilies)
	fmt.Printf("  readiness              UNVERIFIED: %s\n", terminalText(strings.Join(readiness.Missing, "; ")))
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
	var observed, unseen []calibration.SummaryItem
	for _, item := range summary.Items {
		if item.Status == "observed" {
			observed = append(observed, item)
		} else {
			unseen = append(unseen, item)
		}
	}
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
	if *out != "" {
		if err := calibration.WriteJSON(*out, summary); err != nil {
			errPrint("could not write calibration summary", err.Error(), "")
			return exitError
		}
		fmt.Printf("\n  wrote  %s\n", terminalText(*out))
	}
	fmt.Println("\n  evidence only: aggregation never deletes or rewrites a task.")
	return exitOK
}
