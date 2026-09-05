package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/source"
)

const sourceHelp = `usage: fitr source resolve hf --repo owner/model --revision <revision> --file <path> [--file <path>...] --out <receipt.json> [--display MODE]
       fitr source show <receipt.json> [--display MODE]

Resolve public file metadata without downloading weights. Exact file metadata
does not establish local fit, complete dependencies or model quality.`

type sourceFiles []string

func (files *sourceFiles) String() string { return fmt.Sprint([]string(*files)) }
func (files *sourceFiles) Set(value string) error {
	if len(*files) >= source.MaxFiles {
		return fmt.Errorf("at most %d explicit files are supported", source.MaxFiles)
	}
	*files = append(*files, value)
	return nil
}

func cmdSource(ctx context.Context, args []string) int {
	return cmdSourceWithResolver(ctx, args, source.ResolveHF)
}

func cmdSourceWithResolver(ctx context.Context, args []string, resolve func(context.Context, source.HFRequest) (source.Resolution, error)) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		fmt.Fprintln(os.Stderr, sourceHelp)
		return exitOK
	}
	switch args[0] {
	case "show":
		return showSource(args[1:])
	case "resolve":
		return resolveSource(ctx, args[1:], resolve)
	default:
		errPrint("unknown source action", "", "fitr source --help")
		return exitUsage
	}
}

func resolveSource(ctx context.Context, args []string, resolve func(context.Context, source.HFRequest) (source.Resolution, error)) int {
	fs := flag.NewFlagSet("source resolve", flag.ContinueOnError)
	repo := fs.String("repo", "", "explicit owner/repository")
	revision := fs.String("revision", "", "explicit branch, tag or full commit")
	output := fs.String("out", "", "new private receipt in an existing directory")
	mode := fs.String("display", "auto", "auto|rich|plain|json|none")
	var files sourceFiles
	fs.Var(&files, "file", "exact relative filename; repeat for each selected file")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 || fs.Arg(0) != "hf" || *output == "" || !render.ValidMode(*mode) {
		errPrint("source resolution needs provider hf, an output path and a valid display mode", "", "fitr source --help")
		return exitUsage
	}
	request := source.HFRequest{RepoID: *repo, Revision: *revision, Files: files}
	if err := request.Validate(); err != nil {
		errPrint(err.Error(), "", "provide an explicit repository, revision and selected files")
		return exitUsage
	}
	if err := source.ValidateOutputPath(*output); err != nil {
		return sourceFailure(err)
	}
	resolution, err := resolve(ctx, request)
	if err != nil {
		if ctx.Err() != nil {
			errPrint("source resolution cancelled", "", "retry with a new receipt path when ready")
			return exitInterrupt
		}
		return sourceFailure(err)
	}
	if err := source.WriteResolution(*output, resolution); err != nil {
		return sourceFailure(err)
	}
	fmt.Fprintf(os.Stderr, "  receipt  %s\n", terminalText(*output))
	code := writeSourceResolution(resolution, *mode)
	if ctx.Err() != nil && code != exitError {
		return exitInterrupt
	}
	return code
}

func showSource(args []string) int {
	fs := flag.NewFlagSet("source show", flag.ContinueOnError)
	mode := fs.String("display", "auto", "auto|rich|plain|json|none")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 || !render.ValidMode(*mode) {
		errPrint("source show needs one receipt and a valid display mode", "", "fitr source show <receipt.json>")
		return exitUsage
	}
	resolution, err := source.LoadResolution(fs.Arg(0))
	if err != nil {
		return sourceFailure(err)
	}
	return writeSourceResolution(resolution, *mode)
}

func writeSourceResolution(resolution source.Resolution, mode string) int {
	switch render.Resolve(mode) {
	case "json":
		data, err := resolution.JSON()
		if err != nil {
			return sourceFailure(err)
		}
		if _, err := fmt.Fprintln(os.Stdout, string(data)); err != nil {
			return exitError
		}
	case "none":
	default:
		var report bytes.Buffer
		render.WriteSourceResolution(&report, resolution, mode)
		if _, err := os.Stdout.Write(report.Bytes()); err != nil {
			return exitError
		}
	}
	if resolution.State != "resolved" {
		return exitUnresolved
	}
	return exitOK
}

func sourceFailure(err error) int {
	errPrint("source resolution: "+err.Error(), "", "fitr source --help")
	return exitError
}
