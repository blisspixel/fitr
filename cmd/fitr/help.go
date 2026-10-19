package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/blisspixel/fitr/internal/render"
)

// usageText is the first help page. It names every command a person can type
// and points at `fitr help <command>` for the flag list. Asked help is wrapped
// to the report width; this constant keeps the words tests and docs search for.
const usageText = ` - evidence for what local AI works on this machine

usage:
  fitr
    installed models, evidence, next command
  fitr <model>
    named advise (same as fitr advise <model>)
  fitr run <model>
    measure one exact configuration
  fitr board
    compare saved results on this machine
  fitr role
    what a model is for, and whether evidence holds

measure:
  fitr advise [model] [--vram-gb N] [--ctx N] [--load] [--fit]
  fitr run <model> [--quick|--full|--checks-only] [-k N] [--ctx N] [--profile P] [--display MODE] [--html]
  fitr run <model> --context-tiers <bytes,bytes[,...]> [--ctx N]
  fitr diag <model>
  fitr doctor <model> [-n N]
  fitr suggest [model] [--size-gb N] [--ceiling-gb N]
  fitr top [--view live|result|board|history|inventory]
  fitr top view [model|result.json]
  fitr top run <model> [run flags]
  fitr top history [path|clear --yes]

compare:
  fitr compare <model-a> <model-b>
  fitr tune [model-a model-b]
  fitr calibrate <model-a> <model-b> [--out PATH] [--lineage PATH]
  fitr calibrate merge <pair.json>... [--out PATH]
  fitr decide [model|result.json] --spec decision.json [--display MODE]
  fitr experiment context <model> --ctx 4096,8192,... [-k N]
  fitr experiment quant <result.json> <result.json>... --spec decision.json [--lineage conversion.json]
  fitr experiment confirm <model-a> <model-b> --spec decision.json [--ctx N] [-k N] [--min-gap F] [--noise-from result.json]
  fitr experiment workload <model> [-n N] [--ctx N] [--workflow pi-workspace] [--backend B]
  fitr experiment serving <model> [--concurrency N] [-n N] [--warmup N] [--ctx N] [--backend B]
  fitr experiment reference plan|run seals a recorded external pack; it does not launch one
  fitr view [model|result.json] [--display MODE]
  fitr export <model> [--out PATH] [--retonr]
  fitr apply [model] [--ctx N]

roles and tailoring:
  fitr role init <name> --quality <need> --memory-gb <limit> [--ctx N]
  fitr role define <role.json> | list | show <name> | review <name>
  fitr role attach <name> <result.json> | detach <name> <evidence-sha256>
  fitr role confirm <name|bundle.json> | adopt <name> <bundle.json> | status <name> | rollback <name>
  fitr auto runtime <ollama.exe> --models <directory> --out runtime.json
  fitr auto start <role> --mode establish|improve --runtime runtime.json --candidate <model> --candidate <model>
  fitr auto status|resume|adopt <session-id>
  fitr tailor plan --role <name> --outcome <need> --candidate <model> --ctx N (--capacity-budget-gb N | --capacity-reserve-gb N --memory-gb N)
  fitr tailor start|status|resume|adopt <session-id>
  fitr discover add <source> --role <role> [--model <reference>] [--harness <name>]
  fitr discover list|plan [--role <role>] [--display MODE]

desktop and integrations:
  fitr desktop status [--role NAME] [--display MODE]
  fitr desktop benchmark [--role NAME] [--confirm] [--display MODE]
  fitr desktop install [--plugins-dir DIR]
  fitr desktop remove [--plugins-dir DIR] --yes
  fitr mcp serve
  fitr source resolve hf --repo <owner/model> --revision <revision> --file <path> --out <receipt.json>
  fitr source show <receipt.json> [--display MODE]

maintenance:
  fitr device
  fitr profiles [new [name]]
  fitr update [--check] [--reinstall]
  fitr artifact bind --source <resolution.json> --mapping <local-files.json> --out <artifact.json>
  fitr artifact show <artifact.json> [--display MODE]
  fitr cleanup plan <directory> [--min-age-days N] [--display MODE]

flags:
  --display auto|rich|plain|json|none
    output mode (default auto)
  -k
    repeats per noisy task (default 3; 1 quick; 5 checks-only)

fitr help <command> prints that command's flags.
fitr help experiment context prints one experiment form.

environment:
  FITR_OPENAI_URL
    OpenAI-compatible endpoint
  FITR_OPENAI_API_KEY
    bearer token; kept out of command history and process arguments
  FITR_OPENAI_MODEL_SHA256
    independently obtained model digest required for measured OpenAI-compatible runs

exit codes:
  0 eligible/ok   1 error   2 usage   3 disproven   4 unresolved   130 interrupted
`

// helpPageWritten is set by a FlagSet Usage that already composed its page.
// parseCommandFlags must not write a second one. The flag package calls Usage
// for -h and also from failf, so a command that owns its page keeps that page
// on both paths.
var helpPageWritten bool

func usage() { writeMainHelp(os.Stdout) }

func writeMainHelp(w io.Writer) {
	writeAskedHelp(w, "fitr "+version+usageText)
}

// writeAskedHelp composes help to the report width. Blank lines stay blank.
// Leading spaces are restored after wrapping, because the shared line fitter
// treats a gutter as disposable whitespace.
func writeAskedHelp(w io.Writer, text string) {
	width := render.Width()
	if width < 1 {
		width = 1
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimRight(text, "\n")
	for _, line := range strings.Split(text, "\n") {
		writeHelpLine(w, line, width)
	}
}

func writeHelpLine(w io.Writer, line string, width int) {
	if strings.TrimSpace(line) == "" {
		fmt.Fprintln(w)
		return
	}
	body := strings.TrimLeft(line, " ")
	indentN := len(line) - len(body)
	if indentN >= width {
		indentN = 0
	}
	bodyWidth := width - indentN
	if bodyWidth < 1 {
		bodyWidth = 1
	}
	parts := render.WrapLines([]string{body}, bodyWidth)
	if len(parts) == 0 {
		fmt.Fprintln(w)
		return
	}
	fmt.Fprintln(w, strings.Repeat(" ", indentN)+parts[0])
	if len(parts) == 1 {
		return
	}
	cont := indentN + 2
	if cont >= width {
		cont = indentN
	}
	restWidth := width - cont
	if restWidth < 1 {
		restWidth = 1
	}
	for _, more := range render.WrapLines([]string{strings.Join(parts[1:], " ")}, restWidth) {
		fmt.Fprintln(w, strings.Repeat(" ", cont)+more)
	}
}

// commandFlags is the shared flag set for a user-facing command. Usage is
// intentionally empty: the flag package also calls it when a flag is unknown
// or a value is missing, and that path must stay a stderr diagnostic rather
// than a second copy of the help page on stdout. parseCommandFlags writes the
// page only for -h and --help.
func commandFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {}
	return fs
}

var flagExamples = map[string]string{
	"run":  "fitr run <model> [--quick|--full|--checks-only]",
	"fitr": "fitr [--display MODE]",
}

func writeFlagHelp(w io.Writer, fs *flag.FlagSet) {
	example := flagExamples[fs.Name()]
	if example == "" {
		example = "fitr " + fs.Name()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "usage: %s\n", example)
	fs.VisitAll(func(f *flag.Flag) {
		line := "--" + f.Name
		if f.Usage != "" {
			line += "  " + f.Usage
		}
		fmt.Fprintf(&b, "  %s\n", line)
	})
	writeAskedHelp(w, b.String())
}

func cmdHelp(ctx context.Context, args []string) int {
	switch len(args) {
	case 0:
		writeMainHelp(os.Stdout)
		return exitOK
	case 1:
		return helpOne(ctx, args[0], nil)
	case 2:
		return helpOne(ctx, args[0], []string{args[1], "--help"})
	default:
		errPrint("too many arguments", "fitr help <command> [form]", "fitr help")
		return exitUsage
	}
}

func helpOne(ctx context.Context, name string, args []string) int {
	if name == "--help" || name == "-h" || name == "help" {
		writeMainHelp(os.Stdout)
		return exitOK
	}
	if name == "version" {
		fmt.Println("fitr", version)
		return exitOK
	}
	handler := commandHandler(name)
	if handler == nil {
		errPrint("unknown command", name, "fitr help")
		return exitUsage
	}
	if args == nil {
		args = []string{"--help"}
	}
	return handler(ctx, args)
}
