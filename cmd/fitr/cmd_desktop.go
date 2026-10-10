package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/blisspixel/fitr/internal/buildinfo"
	"github.com/blisspixel/fitr/internal/desktop"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/role"
	"github.com/blisspixel/fitr/plugins/omarchy"
)

func cmdDesktop(ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(os.Stderr, desktopUsage)
		return exitOK
	}
	switch args[0] {
	case "status":
		return cmdDesktopStatus(args[1:])
	case "benchmark":
		return cmdDesktopBenchmark(ctx, args[1:])
	case "install":
		return cmdDesktopInstall(args[1:])
	case "remove":
		return cmdDesktopRemove(args[1:])
	default:
		errPrint("unknown desktop action", args[0], "fitr desktop --help")
		return exitUsage
	}
}

const desktopUsage = `usage: fitr desktop status [--role NAME] [--display MODE]
       fitr desktop benchmark [--role NAME] [--confirm] [--display MODE]
       fitr desktop install [--plugins-dir DIR] [--display MODE]
       fitr desktop remove [--plugins-dir DIR] --yes [--display MODE]

status reads sealed role and analysis documents. It does not download models,
reconfigure serving, or call a model. The default display is json.
benchmark starts a run only with --confirm, and only when that run is an
allowlisted local ollama or llama-server command.
install copies the embedded Omarchy bar widget. It does not edit shell.json.
remove deletes only a directory fitr marked as its own.`

func cmdDesktopStatus(args []string) int {
	fs := flag.NewFlagSet("desktop status", flag.ContinueOnError)
	roleName := fs.String("role", "", "role library name")
	mode := fs.String("display", "json", "auto|rich|plain|json|none")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 || !render.ValidMode(*mode) {
		errPrint("desktop status takes no positional arguments", "", "fitr desktop status --role NAME --display json")
		return exitUsage
	}
	status := desktopStatus(*roleName)
	return writeDesktop(*mode, func() error { return encodeDesktop(status) }, func() {
		render.WriteDesktopStatus(os.Stdout, status, *mode)
	})
}

func cmdDesktopBenchmark(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("desktop benchmark", flag.ContinueOnError)
	roleName := fs.String("role", "", "role library name")
	mode := fs.String("display", "json", "auto|rich|plain|json|none")
	confirm := fs.Bool("confirm", false, "start the allowlisted local measurement")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 || !render.ValidMode(*mode) {
		errPrint("desktop benchmark takes no positional arguments", "", "fitr desktop benchmark --role NAME --confirm")
		return exitUsage
	}
	plan := desktop.PlanBenchmark(desktopStatus(*roleName), os.Environ())
	if !plan.Eligible {
		if err := presentBenchmark(plan, *mode); err != nil {
			return exitError
		}
		return exitUnresolved
	}
	if !*confirm {
		if err := presentBenchmark(plan, *mode); err != nil {
			return exitError
		}
		return exitOK
	}
	ran, code := desktop.ExecuteBenchmark(ctx, plan, true, desktopMeasurement)
	if !ran {
		plan.Reason = "The desktop did not start a measurement."
		if err := presentBenchmark(plan, *mode); err != nil {
			return exitError
		}
		return exitUnresolved
	}
	return code
}

func cmdDesktopInstall(args []string) int {
	parent, mode, code, ok := desktopPluginFlags("desktop install", args, false)
	if !ok {
		return code
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		errPrint("the plugins directory is not usable", "", "choose a directory this user can create")
		return exitError
	}
	if err := omarchy.Install(parent, buildinfo.Version()); err != nil {
		errPrint(err.Error(), "", "leave an existing foreign plugin in place, or pass its parent with --plugins-dir")
		return exitError
	}
	return writeDesktopChange("install", mode)
}

func cmdDesktopRemove(args []string) int {
	parent, mode, code, ok := desktopPluginFlags("desktop remove", args, true)
	if !ok {
		return code
	}
	if err := omarchy.Remove(parent); err != nil {
		errPrint(err.Error(), "", "disable the plugin in Omarchy if it is still listed; shell.json was not changed")
		return exitError
	}
	return writeDesktopChange("remove", mode)
}

func desktopPluginFlags(name string, args []string, remove bool) (string, string, int, bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	parentFlag := fs.String("plugins-dir", "", "Omarchy third-party plugins directory")
	mode := fs.String("display", "json", "auto|rich|plain|json|none")
	yes := fs.Bool("yes", false, "remove the fitr-marked plugin directory")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return "", "", code, false
	}
	if fs.NArg() != 0 || !render.ValidMode(*mode) || (remove && !*yes) {
		hint := "fitr desktop install --plugins-dir DIR"
		message := "desktop install takes no positional arguments"
		if remove {
			hint = "fitr desktop remove --plugins-dir DIR --yes"
			message = "desktop remove requires --yes"
		}
		errPrint(message, "", hint)
		return "", "", exitUsage, false
	}
	parent := *parentFlag
	if parent == "" {
		resolved, err := omarchy.DefaultPluginsDir()
		if errors.Is(err, omarchy.ErrUnsupported) {
			errPrint("desktop install is unsupported on this platform", "", "pass --plugins-dir to choose an explicit directory")
			return "", "", exitUnresolved, false
		}
		if err != nil {
			errPrint("the plugins directory is not usable", "", "pass --plugins-dir")
			return "", "", exitError, false
		}
		parent = resolved
	}
	if !filepath.IsAbs(parent) {
		errPrint("the plugins directory is not usable", "", "pass an absolute --plugins-dir")
		return "", "", exitError, false
	}
	return parent, *mode, exitOK, true
}

func desktopStatus(roleName string) desktop.Status {
	status := desktop.Load(role.Store{Dir: filepath.Join(resultsDir(), ".roles")}, record.Store{Dir: resultsDir()}, roleName, time.Now(), os.Environ())
	status.FitrVersion = buildinfo.Version()
	return status
}

func desktopMeasurement(ctx context.Context, argv []string) int {
	if len(argv) < 2 || argv[0] != "fitr" || argv[1] != "run" {
		return exitUsage
	}
	return cmdRun(ctx, argv[2:])
}

func presentBenchmark(plan desktop.BenchmarkPlan, mode string) error {
	return presentDesktop(mode, func() error { return encodeDesktop(plan) }, func() {
		render.WriteDesktopBenchmark(os.Stdout, plan, mode)
	})
}

func writeDesktop(mode string, writeJSON func() error, writeText func()) int {
	if err := presentDesktop(mode, writeJSON, writeText); err != nil {
		return exitError
	}
	return exitOK
}

func presentDesktop(mode string, writeJSON func() error, writeText func()) error {
	switch render.Resolve(mode) {
	case "none":
		return nil
	case "json":
		return writeJSON()
	default:
		writeText()
		return nil
	}
}

type desktopChange struct {
	Schema    string `json:"schema"`
	ID        string `json:"id"`
	Action    string `json:"action"`
	Changed   bool   `json:"changed"`
	ShellJSON string `json:"shell_json"`
}

func writeDesktopChange(action, mode string) int {
	change := desktopChange{
		Schema: "fitr.desktop.install-result.v1", ID: omarchy.PluginID,
		Action: action, Changed: true, ShellJSON: "unchanged",
	}
	return writeDesktop(mode, func() error { return encodeDesktop(change) }, func() {
		fmt.Fprintf(os.Stdout, "fitr desktop %s copied or removed only the marked plugin directory\n", action)
		fmt.Fprintln(os.Stdout, "shell.json was not changed")
	})
}

func encodeDesktop(document any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(document)
}
