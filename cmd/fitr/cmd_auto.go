package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/blisspixel/fitr/internal/automation"
	"github.com/blisspixel/fitr/internal/autoruntime"
	"github.com/blisspixel/fitr/internal/boundedio"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/strictjson"
)

type autoCandidates []string

func (v *autoCandidates) String() string { return fmt.Sprint([]string(*v)) }
func (v *autoCandidates) Set(value string) error {
	if len(*v) >= 4 {
		return errors.New("auto accepts at most four explicit installed candidates")
	}
	if err := validateModelRefs(value); err != nil {
		return err
	}
	*v = append(*v, normalizeModelRef(value))
	return nil
}

type autoCommand struct {
	action, role, sessionID, runtimePath, mode, adoption, profile, display string
	candidates                                                             autoCandidates
	repeats                                                                int
	wall, confirmationWall                                                 time.Duration
	limits                                                                 automation.Limits
}

func cmdAuto(ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(os.Stdout, `fitr auto: bounded local role experiments

  fitr auto runtime <ollama.exe> --models <directory> --out runtime.json
  fitr auto start <role> --mode establish|improve --runtime runtime.json
    --candidate <installed-model> --candidate <installed-model>
    [--adoption manual|confirmed-only] [--max-wall 2h] [-k 3]
  fitr auto status <session-id>
  fitr auto resume <session-id>
  fitr auto adopt <session-id>

Start runs the sealed schedule. Quality floors precede preferences. Confirmation
has protected request, output-token and time allowances. The first runtime owner
supports Windows Ollama, existing local models and execution-disabled batteries.
Adoption changes fitr's role selection. Interrupted confirmation ends that
session's adoption path.`)
		return exitOK
	}
	if args[0] == "runtime" {
		return cmdAutoRuntime(ctx, args[1:])
	}
	command, code, ok := parseAutoCommand(args)
	if !ok {
		return code
	}
	if command.action == "start" {
		return startAuto(ctx, command)
	}
	if command.action == "status" {
		return showAuto(command.sessionID, command.display)
	}
	session, err := (automation.Store{Results: resultsDir()}).Open(command.sessionID)
	if err != nil {
		return autoFailure(err)
	}
	defer func() { _ = session.Close() }()
	if command.action == "adopt" {
		err = adoptAuto(session)
	} else {
		err = resumeAuto(ctx, session, command.display)
	}
	if err != nil {
		return autoFailure(err)
	}
	return showAuto(command.sessionID, command.display)
}

func parseAutoCommand(args []string) (autoCommand, int, bool) {
	c := autoCommand{action: args[0]}
	if c.action != "start" && c.action != "status" && c.action != "resume" && c.action != "adopt" {
		errPrint("unknown auto action", c.action, "fitr auto --help")
		return c, exitUsage, false
	}
	fs := flag.NewFlagSet("auto "+c.action, flag.ContinueOnError)
	fs.StringVar(&c.display, "display", "auto", "auto|rich|plain|json|none")
	if c.action == "start" {
		fs.StringVar(&c.mode, "mode", "", "establish an unselected role, or improve its current selection")
		fs.StringVar(&c.runtimePath, "runtime", "", "owned-runtime JSON from auto runtime")
		fs.StringVar(&c.adoption, "adoption", "manual", "manual|confirmed-only")
		fs.StringVar(&c.profile, "profile", "", "device profile (default auto-match)")
		fs.Var(&c.candidates, "candidate", "installed model; repeat two to four times")
		fs.IntVar(&c.repeats, "k", 3, "fixed repeats per noisy task and generated check")
		fs.DurationVar(&c.wall, "max-wall", 2*time.Hour, "whole-session wall allowance, including downtime")
		fs.DurationVar(&c.confirmationWall, "confirmation-wall", time.Hour, "time protected from exploration")
		fs.Int64Var(&c.limits.MaxRequests, "max-requests", 600, "actual inference attempts, including retries")
		fs.Int64Var(&c.limits.MaxRequestedOutputTokens, "max-requested-output-tokens", 250000, "sum of reserved output caps")
		fs.IntVar(&c.limits.MaxPoints, "max-points", 8, "candidate point attempt ceiling")
	}
	if code, ok := parseCommandFlags(fs, args[1:]); !ok {
		return c, code, false
	}
	if fs.NArg() != 1 || !render.ValidMode(c.display) {
		errPrint("invalid auto arguments", "", "fitr auto --help")
		return c, exitUsage, false
	}
	if c.action == "start" {
		c.role = fs.Arg(0)
		if c.runtimePath == "" || len(c.candidates) < 2 || (c.mode != "establish" && c.mode != "improve") || (c.adoption != "manual" && c.adoption != "confirmed-only") || c.repeats < 3 || c.repeats > 20 || c.wall%time.Second != 0 || c.confirmationWall%time.Second != 0 {
			errPrint("auto start needs an explicit mode, runtime, two to four candidates and bounded fixed repeats", "", "fitr auto --help")
			return c, exitUsage, false
		}
		c.limits.WallSeconds = int64(c.wall / time.Second)
		c.limits.ConfirmationWallSeconds = int64(c.confirmationWall / time.Second)
	} else {
		c.sessionID = fs.Arg(0)
	}
	return c, exitOK, true
}

func cmdAutoRuntime(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("auto runtime", flag.ContinueOnError)
	models := fs.String("models", "", "existing Ollama model-store directory")
	out := fs.String("out", "", "new runtime specification path")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 || *models == "" || *out == "" {
		errPrint("runtime inspection needs an executable, model directory and new output path", "", "fitr auto --help")
		return exitUsage
	}
	inspection, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	spec, err := autoruntime.Inspect(inspection, fs.Arg(0), *models)
	if err != nil {
		return autoFailure(err)
	}
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return autoFailure(err)
	}
	file, err := os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return autoFailure(err)
	}
	_, writeErr := file.Write(append(data, '\n'))
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return autoFailure(err)
	}
	fmt.Fprintf(os.Stdout, "runtime specification saved: %s\nReview context, KV precision and memory reserve before auto start.\n", terminalText(*out))
	return exitOK
}

func loadAutoRuntime(path string) (autoruntime.Spec, error) {
	data, err := boundedio.ReadFile(path, 1<<20)
	if err != nil {
		return autoruntime.Spec{}, err
	}
	if err := strictjson.Validate(data); err != nil {
		return autoruntime.Spec{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var spec autoruntime.Spec
	if err := decoder.Decode(&spec); err != nil {
		return spec, err
	}
	return spec, spec.Validate()
}

func showAuto(id, mode string) int {
	journal, err := (automation.Store{Results: resultsDir()}).Load(id)
	if err != nil {
		return autoFailure(err)
	}
	state, err := journal.Replay()
	if err != nil {
		return autoFailure(err)
	}
	now := time.Now()
	view, code := describeAuto(journal.Plan, state, now)
	autoSelectedStatus(&view, journal.Plan)
	_, records := autoStores()
	autoExplorationStatus(&view, journal.Plan, state, records, now)
	if render.Resolve(mode) == "json" {
		if written := writeRoleJSON(struct {
			Schema string            `json:"schema"`
			Plan   automation.Plan   `json:"plan"`
			State  automation.State  `json:"state"`
			Status render.AutoStatus `json:"status"`
		}{"fitr.auto.status.v1", journal.Plan, state, view}); written != exitOK {
			return written
		}
	} else if render.Resolve(mode) != "none" {
		render.WriteAutoStatus(os.Stdout, view, mode)
	}
	return code
}

func autoFailure(err error) int {
	errPrint("auto stopped: "+err.Error(), "", "fitr auto --help")
	if errors.Is(err, context.Canceled) {
		return exitInterrupt
	}
	return exitError
}
