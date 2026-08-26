package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/render"
)

// ---------------------------------------------------------------- device
func cmdDevice(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("device", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	mode := fs.String("display", "auto", "auto|rich|plain|json|none")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if !render.ValidMode(*mode) {
		errPrint("invalid display mode", *mode, "use auto, rich, plain, json, or none")
		return exitUsage
	}
	if fs.NArg() != 0 {
		errPrint("unexpected argument", fs.Arg(0), "fitr device [--display MODE]")
		return exitUsage
	}
	fp := device.Detect(ctx, probeBackend(ctx))
	prof, err := device.SelectProfile("", fp)
	if err != nil {
		errPrint("could not select device profile", err.Error(),
			"repair or remove invalid files in "+device.UserProfilesDir())
		return exitError
	}
	switch render.Resolve(*mode) {
	case "json":
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(map[string]any{
			"fingerprint": fp, "key": fp.Key(), "profile": prof.Name,
		}); err != nil {
			errPrint("could not render device: "+err.Error(), "", "")
			return exitError
		}
		return exitOK
	case "none":
		return exitOK
	}
	fmt.Printf("  host               %s\n", terminalText(fp.Host))
	fmt.Printf("  os                 %s\n", terminalText(fp.OS))
	fmt.Printf("  cpu                %s\n", terminalText(device.FormatCPU(fp.CPU)))
	fmt.Printf("  ram_gb             %.1f\n", fp.RAMGb)
	fmt.Printf("  vram_gb            %s\n", terminalText(device.FormatVRAM(fp.VRAMGb, fp.VRAMSource)))
	fmt.Printf("  gpu                %s\n", terminalText(fp.GPU))
	fmt.Printf("  gpu_backend        %s\n", terminalText(emptyDash(fp.GPUBackend)))
	fmt.Printf("  gpu_driver         %s  (%s)\n", terminalText(fp.GPUDriver), terminalText(fp.GPUDriverDate))
	fmt.Printf("  runtime            %s\n", terminalText(fp.Runtime))
	fmt.Printf("  inference_device   %s\n", terminalText(fp.InferenceDevice))
	// Not part of the sealed fingerprint, but an acceptance row needs it: a
	// matrix whose finest grain is the operating system cannot see a defect
	// that only appears on one interpreter version.
	if tooling := device.ProbeTooling(ctx); tooling != "" {
		fmt.Printf("  probe_tooling      %s\n", terminalText(tooling))
	}
	for _, conflict := range fp.IdentityConflicts() {
		fmt.Printf("  ! identity         %s\n", terminalText(conflict))
	}
	fmt.Println("  config")
	keys := make([]string, 0, len(fp.Config))
	for k := range fp.Config {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := fp.Config[k]
		if v == "" {
			v = "(unset)"
		}
		fmt.Printf("    %-26s %s\n", terminalText(k), terminalText(v))
	}
	// Both of these run long: a profile description is a sentence, and the
	// comparability key is a pipe-joined record of every field that decides
	// whether two runs may be compared. Wrapping keeps them inside the rule the
	// rest of the block observes.
	render.Field(os.Stdout, "  profile", deviceLabelWidth,
		terminalText(prof.Name)+" - "+terminalText(prof.Description), render.Width())
	// The key is deliberately NOT wrapped. It is one identifier, and the GPU
	// name inside it contains spaces, so a wrap would put a line break where a
	// space is and anyone copying it out would copy something else. Let the
	// terminal own that decision.
	fmt.Println("  key")
	fmt.Printf("    %s\n", terminalText(fp.Key()))
	return exitOK
}

// deviceLabelWidth matches the "  inference_device   " column above.
const deviceLabelWidth = 21

func cmdProfiles(ctx context.Context, args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "new":
			if len(args) > 2 {
				errPrint("too many arguments", "profiles new accepts at most one name", "fitr profiles new [name]")
				return exitUsage
			}
			name := ""
			if len(args) > 1 {
				name = args[1]
			}
			return cmdProfilesNew(ctx, name)
		case "-h", "--help", "help":
			if len(args) > 1 {
				errPrint("unexpected argument", args[1], "fitr profiles --help")
				return exitUsage
			}
			fmt.Fprint(os.Stderr, "usage: fitr profiles [new [name]]\n")
			return exitOK
		default:
			errPrint(fmt.Sprintf("unknown profiles subcommand %q", args[0]), "",
				"fitr profiles    or    fitr profiles new [name]")
			return exitUsage
		}
	}
	fp := device.Detect(ctx, probeBackend(ctx))
	active, _ := device.SelectProfile("", fp)
	profs, err := device.LoadProfiles()
	if err != nil {
		errPrint(err.Error(), "", "")
		return exitError
	}
	for _, p := range profs {
		mark := " "
		if p.Name == active.Name {
			mark = "*"
		}
		fmt.Printf(" %s %-12s %s\n", terminalText(mark), terminalText(p.Name), terminalText(p.Description))
	}
	fmt.Println("\n  * = auto-selected for this machine")
	fmt.Println("  next   fitr profiles new [name]   # UNCALIBRATED local copy; edit the gates")
	return exitOK
}

func cmdProfilesNew(ctx context.Context, name string) int {
	fp := device.Detect(ctx, probeBackend(ctx))
	p, err := device.ScaffoldProfile(name, fp)
	if err != nil {
		errPrint(err.Error(), "", "")
		return exitError
	}
	path, err := device.WriteProfile(device.UserProfilesDir(), p)
	if err != nil {
		errPrint(err.Error(), "", "pick a new name, or edit the existing file")
		return exitError
	}
	fmt.Printf("  wrote  %s\n", terminalText(path))
	fmt.Println("  UNCALIBRATED copy of default. Run models you already have opinions")
	fmt.Println("  about, then edit the gates so the verdicts match lived experience.")
	fmt.Println("  Do not publish these numbers as a calibrated community profile.")
	return exitOK
}

// cmdStatus is what a bare `fitr` prints after install: this box, what is
// already serving, current evidence, and one next command per row.
func cmdStatus(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("fitr", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	mode := fs.String("display", "auto", "auto|rich|plain|json|none")
	backend := fs.String("backend", "auto", "auto|ollama|llama-server|openai")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if !render.ValidMode(*mode) {
		errPrint("invalid display mode", *mode, "use auto, rich, plain, json, or none")
		return exitUsage
	}
	if fs.NArg() > 0 {
		rest := []string{fs.Arg(0), "--display", *mode, "--backend", *backend}
		rest = append(rest, fs.Args()[1:]...)
		return cmdAdvise(ctx, rest)
	}
	return printInventory(ctx, *backend, *mode)
}
