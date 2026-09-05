package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/blisspixel/fitr/internal/artifact"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/source"
)

const artifactHelp = `usage: fitr artifact bind --source <resolution.json> --mapping <local-files.json> --out <artifact.json> [--max-bytes N] [--timeout 10m] [--display MODE]
       fitr artifact show <artifact.json> [--display MODE]

Hash explicitly mapped absolute local file paths within a byte and time budget.
No downloads or model execution. A local byte match does not establish runtime
use, complete dependencies, capacity or quality. Output must be a new file.`

type artifactBinder func(context.Context, source.Resolution, artifact.Spec, artifact.Options) (artifact.Binding, error)

type artifactBindCommand struct {
	sourcePath, mappingPath, outputPath, mode string
	options                                   artifact.Options
}

func cmdArtifact(ctx context.Context, args []string) int {
	return cmdArtifactWithBinder(ctx, args, artifact.Bind)
}

func cmdArtifactWithBinder(ctx context.Context, args []string, bind artifactBinder) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(os.Stderr, artifactHelp)
		return exitOK
	}
	switch args[0] {
	case "bind":
		return bindArtifact(ctx, args[1:], bind)
	case "show":
		return showArtifact(args[1:])
	default:
		errPrint("unknown artifact action", "", "fitr artifact --help")
		return exitUsage
	}
}

func parseArtifactBind(args []string) (artifactBindCommand, int, bool) {
	command := artifactBindCommand{}
	fs := flag.NewFlagSet("artifact bind", flag.ContinueOnError)
	fs.StringVar(&command.sourcePath, "source", "", "saved source resolution receipt")
	fs.StringVar(&command.mappingPath, "mapping", "", "explicit local file mapping specification")
	fs.StringVar(&command.outputPath, "out", "", "new private binding receipt in an existing directory")
	fs.StringVar(&command.mode, "display", "auto", "auto|rich|plain|json|none")
	fs.Int64Var(&command.options.MaxBytes, "max-bytes", artifact.DefaultMaxBytes, "maximum total local bytes to read")
	fs.DurationVar(&command.options.Timeout, "timeout", artifact.DefaultTimeout, "whole millisecond time budget, up to 1h")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return command, code, false
	}
	if fs.NArg() != 0 || command.sourcePath == "" || command.mappingPath == "" || command.outputPath == "" || !render.ValidMode(command.mode) {
		errPrint("artifact bind needs source, mapping, output and a valid display mode", "", "fitr artifact --help")
		return command, exitUsage, false
	}
	if err := command.options.Validate(); err != nil || command.options.MaxBytes == 0 || command.options.Timeout == 0 {
		errPrint("artifact limits require 1 byte to 1 TiB and whole milliseconds from 1 ms to 1 hour", "", "fitr artifact --help")
		return command, exitUsage, false
	}
	return command, exitOK, true
}

func bindArtifact(ctx context.Context, args []string, bind artifactBinder) int {
	command, code, ok := parseArtifactBind(args)
	if !ok {
		return code
	}
	resolution, spec, err := loadArtifactBindInputs(command)
	if err != nil {
		return artifactFailure(err)
	}
	if ctx.Err() != nil {
		return exitInterrupt
	}
	binding, err := bind(ctx, resolution, spec, command.options)
	if err != nil {
		if ctx.Err() != nil {
			return exitInterrupt
		}
		return artifactFailure(err)
	}
	if err := artifact.WriteBinding(command.outputPath, binding); err != nil {
		return artifactFailure(err)
	}
	fmt.Fprintf(os.Stderr, "  receipt  %s\n", terminalText(command.outputPath))
	code = writeArtifactBinding(binding, command.mode)
	if ctx.Err() != nil && code != exitError {
		return exitInterrupt
	}
	return code
}

func loadArtifactBindInputs(command artifactBindCommand) (source.Resolution, artifact.Spec, error) {
	var resolution source.Resolution
	var spec artifact.Spec
	if err := artifact.ValidateOutputPath(command.outputPath); err != nil {
		return resolution, spec, err
	}
	spec, err := artifact.LoadSpec(command.mappingPath)
	if err != nil {
		return resolution, spec, err
	}
	if err := artifact.ValidateBindingOutputPath(command.outputPath, spec); err != nil {
		return resolution, spec, err
	}
	resolution, err = source.LoadResolution(command.sourcePath)
	if err != nil {
		return resolution, spec, err
	}
	if spec.ResolutionSHA256 != resolution.ResolutionSHA256 {
		return resolution, spec, errors.New("mapping source digest does not match the supplied resolution receipt")
	}
	return resolution, spec, nil
}

func showArtifact(args []string) int {
	fs := flag.NewFlagSet("artifact show", flag.ContinueOnError)
	mode := fs.String("display", "auto", "auto|rich|plain|json|none")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 || !render.ValidMode(*mode) {
		errPrint("artifact show needs one binding receipt and a valid display mode", "", "fitr artifact show <artifact.json>")
		return exitUsage
	}
	binding, err := artifact.LoadBinding(fs.Arg(0))
	if err != nil {
		return artifactFailure(err)
	}
	return writeArtifactBinding(binding, *mode)
}

func writeArtifactBinding(binding artifact.Binding, mode string) int {
	if err := binding.Validate(); err != nil {
		return artifactFailure(err)
	}
	switch render.Resolve(mode) {
	case "json":
		data, err := binding.JSON()
		if err != nil {
			return artifactFailure(err)
		}
		if _, err := fmt.Fprintln(os.Stdout, string(data)); err != nil {
			return exitError
		}
	case "none":
	default:
		var report bytes.Buffer
		render.WriteArtifactBinding(&report, binding, mode)
		if _, err := os.Stdout.Write(report.Bytes()); err != nil {
			return exitError
		}
	}
	if binding.State != "matched" {
		return exitUnresolved
	}
	return exitOK
}

func artifactFailure(err error) int {
	errPrint("artifact binding: "+err.Error(), "", "fitr artifact --help")
	return exitError
}
