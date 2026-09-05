package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/blisspixel/fitr/internal/cleanup"
	"github.com/blisspixel/fitr/internal/render"
)

func cmdCleanup(ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(os.Stderr, "usage: fitr cleanup plan <directory> [--min-age-days 7] [--display MODE]")
		fmt.Fprintln(os.Stderr, "Read-only storage inventory. REVIEW candidates require usage and dependency checks.")
		return exitOK
	}
	if args[0] != "plan" {
		errPrint("unknown cleanup action", render.SingleLine(args[0]), "use fitr cleanup plan <directory>")
		return exitUsage
	}
	fs := flag.NewFlagSet("cleanup plan", flag.ContinueOnError)
	mode := fs.String("display", "auto", "auto|rich|plain|json|none")
	minimumAge := fs.Int("min-age-days", 7, "review incomplete downloads last modified at least this many days ago")
	if code, ok := parseCommandFlags(fs, args[1:]); !ok {
		return code
	}
	if fs.NArg() != 1 || !render.ValidMode(*mode) || *minimumAge < 0 || *minimumAge > 36500 {
		errPrint("cleanup plan needs one directory, a valid display mode and min-age-days from 0 to 36500", "", "fitr cleanup plan <directory> --min-age-days 7")
		return exitUsage
	}
	plan, err := cleanup.Scan(ctx, fs.Arg(0), cleanup.Options{AsOf: time.Now(), MinAgeDays: *minimumAge})
	if err != nil {
		errPrint(err.Error(), "", "check that the directory exists and is readable")
		return exitError
	}
	if render.Resolve(*mode) == "json" {
		if err := json.NewEncoder(os.Stdout).Encode(plan); err != nil {
			return exitError
		}
	} else if render.Resolve(*mode) != "none" {
		writeCleanupPlan(os.Stdout, plan, *mode)
	}
	if !plan.Complete {
		return exitError
	}
	return exitOK
}

func writeCleanupPlan(w io.Writer, plan cleanup.Plan, mode string) {
	width := render.Width()
	field := func(label, value string) { render.Field(w, "  "+label, 13, value, width) }
	heading, reset := "", ""
	if render.Resolve(mode) == "rich" && os.Getenv("NO_COLOR") == "" {
		heading, reset = "\x1b[36m", "\x1b[0m"
	}
	fmt.Fprintf(w, "  %sfitr / cleanup plan%s\n", heading, reset)
	state := "COMPLETE"
	if !plan.Complete {
		state = "INCOMPLETE"
	}
	field("scan", fmt.Sprintf("%s | %d files | %d directories", state, plan.Files, plan.Directories))
	field("apparent", cleanup.Bytes(plan.ApparentBytes)+"; not recoverable disk space")
	field("review", fmt.Sprintf("%d aged incomplete downloads | %s apparent", plan.ReviewCandidateCount, cleanup.Bytes(plan.ReviewApparentBytes)))
	field("cutoff", plan.Cutoff.Format(time.RFC3339))
	if plan.StopReason != "" {
		field("stopped", plan.StopReason)
	}
	writeCleanupInventory(w, plan, field, width)
	fmt.Fprintln(w)
	for _, note := range plan.Notes {
		field("", note)
	}
}

func writeCleanupInventory(w io.Writer, plan cleanup.Plan, field func(string, string), width int) {
	heading := func(value string) {
		fmt.Fprintln(w)
		render.Field(w, "  ", 2, value, width)
	}
	heading("Top-level storage")
	for _, item := range plan.TopLevel {
		field(cleanup.Bytes(item.ApparentBytes), item.Name)
	}
	if plan.TopLevelOmitted > 0 {
		field("more", fmt.Sprintf("%d groups omitted", plan.TopLevelOmitted))
	}
	heading("Categories (filename and directory hints)")
	for _, item := range plan.Categories {
		field(cleanup.Bytes(item.ApparentBytes), item.Name)
	}
	heading("Largest files | usage unresolved")
	for _, item := range plan.LargestFiles {
		field(cleanup.Bytes(item.ApparentBytes), item.Path)
	}
	heading("REVIEW | age alone does not establish inactivity")
	if len(plan.ReviewCandidates) == 0 {
		field("", "No incomplete-download names met the age threshold in the scanned entries.")
	}
	for _, item := range plan.ReviewCandidates {
		field(cleanup.Bytes(item.ApparentBytes), item.Path)
	}
	if plan.ReviewCandidateCount > len(plan.ReviewCandidates) {
		field("more", fmt.Sprintf("%d review candidates omitted", plan.ReviewCandidateCount-len(plan.ReviewCandidates)))
	}
	for _, issue := range plan.Issues {
		field("scan issue", issue.Code+": "+issue.Path)
	}
	if plan.IssueCount > len(plan.Issues) {
		field("more", fmt.Sprintf("%d scan issues omitted", plan.IssueCount-len(plan.Issues)))
	}
}
