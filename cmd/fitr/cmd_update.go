package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/updater"
)

type updateEvent struct {
	Event       string `json:"event"`
	Schema      string `json:"schema"`
	Status      string `json:"status"`
	Current     string `json:"current"`
	Target      string `json:"target"`
	Asset       string `json:"asset"`
	SHA256      string `json:"sha256,omitempty"`
	Release     string `json:"release"`
	Replacement string `json:"replacement,omitempty"`
}

type updateOptions struct {
	check     bool
	reinstall bool
	mode      string
}

func cmdUpdate(ctx context.Context, args []string) int {
	options, code, ok := parseUpdateOptions(args)
	if !ok {
		return code
	}
	client := updater.NewClient(version)
	plan, err := client.Lookup(ctx, version)
	if err != nil {
		return updateFailure(ctx, "could not resolve the latest fitr release", err)
	}
	if options.check {
		if err := client.Validate(ctx, plan); err != nil {
			return updateFailure(ctx, "latest fitr release is incomplete", err)
		}
		emitUpdate(options.mode, eventForPlan(plan, string(plan.State), "", ""))
		return exitOK
	}
	if plan.State == updater.StateAhead || plan.State == updater.StateCurrent && !options.reinstall {
		emitUpdate(options.mode, eventForPlan(plan, string(plan.State), "", ""))
		return exitOK
	}
	return installUpdate(ctx, client, plan, options.mode)
}

func parseUpdateOptions(args []string) (updateOptions, int, bool) {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	check := fs.Bool("check", false, "check the latest stable release without changing this binary")
	reinstall := fs.Bool("reinstall", false, "reinstall the latest release when versions match")
	mode := fs.String("display", "auto", "auto|rich|plain|json|none")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return updateOptions{}, code, false
	}
	if !render.ValidMode(*mode) {
		errPrint("invalid display mode", *mode, "use auto, rich, plain, json, or none")
		return updateOptions{}, exitUsage, false
	}
	if fs.NArg() != 0 {
		errPrint("unexpected argument", fs.Arg(0), "fitr update [--check] [--reinstall] [--display MODE]")
		return updateOptions{}, exitUsage, false
	}
	if *check && *reinstall {
		errPrint("incompatible update flags", "--check never changes the executable", "remove --check or --reinstall")
		return updateOptions{}, exitUsage, false
	}
	return updateOptions{check: *check, reinstall: *reinstall, mode: *mode}, exitOK, true
}

func installUpdate(ctx context.Context, client *updater.Client, plan updater.Plan, mode string) int {
	targetPath, err := updater.TargetPath()
	if err != nil {
		return updateFailure(ctx, "could not identify the executable to update", err)
	}
	currentDigest, err := updater.HashFile(targetPath)
	if err != nil {
		return updateFailure(ctx, "could not identify the running executable", err)
	}
	stagedPath, digest, err := client.Download(ctx, plan, filepath.Dir(targetPath))
	if err != nil {
		return updateFailure(ctx, "could not stage the fitr update", err)
	}
	keepStaged := false
	defer func() {
		if !keepStaged {
			_ = os.Remove(stagedPath)
		}
	}()
	if err := updater.VerifyVersion(ctx, stagedPath, plan.LatestVersion); err != nil {
		return updateFailure(ctx, "downloaded fitr binary failed its version check", err)
	}
	deferred, err := updater.Install(stagedPath, targetPath, currentDigest)
	if err != nil {
		return updateFailure(ctx, "could not replace the fitr executable", err)
	}
	keepStaged = true
	replacement := "complete"
	status := "updated"
	if deferred {
		replacement = "after_exit"
		status = "staged"
	}
	emitUpdate(mode, eventForPlan(plan, status, digest, replacement))
	return exitOK
}

func eventForPlan(plan updater.Plan, status, digest, replacement string) updateEvent {
	return updateEvent{
		Event:       "update",
		Schema:      "fitr.update.v1",
		Status:      status,
		Current:     plan.CurrentVersion,
		Target:      plan.LatestVersion,
		Asset:       plan.AssetName,
		SHA256:      digest,
		Release:     plan.ReleaseURL,
		Replacement: replacement,
	}
}

func emitUpdate(mode string, event updateEvent) {
	switch render.Resolve(mode) {
	case "none":
		return
	case "json":
		_ = json.NewEncoder(os.Stdout).Encode(event)
		return
	}
	switch event.Status {
	case string(updater.StateCurrent):
		fmt.Printf("fitr %s is current\n", terminalText(event.Current))
	case string(updater.StateAhead):
		fmt.Printf("fitr %s is newer than the latest stable release %s\n", terminalText(event.Current), terminalText(event.Target))
	case string(updater.StateUpdateAvailable):
		fmt.Printf("update available  %s -> %s\n", terminalText(event.Current), terminalText(event.Target))
	case "staged":
		fmt.Printf("fitr %s verified and staged\n", terminalText(event.Target))
		fmt.Println("replacement will be attempted after this process exits; verify with fitr version")
	case "updated":
		fmt.Printf("fitr updated to %s\n", terminalText(event.Target))
	}
	if event.SHA256 != "" {
		fmt.Printf("checksum         %s\n", terminalText(event.SHA256))
	}
	fmt.Printf("release          %s\n", terminalText(event.Release))
}

func updateFailure(ctx context.Context, message string, err error) int {
	if ctx.Err() != nil {
		errPrint("update interrupted", ctx.Err().Error(), "the installed executable was not changed")
		return exitInterrupt
	}
	errPrint(message, err.Error(), "retry, or use the installer at "+updater.RepositoryURL+"#install")
	return exitError
}
