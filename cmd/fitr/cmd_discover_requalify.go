package main

import (
	"context"
	"fmt"
	"os"

	"github.com/blisspixel/fitr/internal/boundedio"
	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/render"
)

// discoverRequalify prints a derived installation comparison. The file is
// the only input: reading the host here would present this process as the
// installation the report is about.
func discoverRequalify(ctx context.Context, path, mode string) int {
	if ctx.Err() != nil {
		return exitInterrupt
	}
	data, err := boundedio.ReadFile(path, 1<<20)
	if err != nil {
		errPrint("could not read the requalification input", err.Error(), "fitr discover requalify --help")
		return exitError
	}
	report, err := decision.LoadRequalification(data)
	if err != nil {
		errPrint("could not read the requalification input", err.Error(), "fitr discover requalify --help")
		return exitError
	}
	if code := writeRequalification(report, mode); code != exitOK {
		return code
	}
	return requalificationExit(report)
}

func writeRequalification(report decision.RequalificationReport, mode string) int {
	switch render.Resolve(mode) {
	case "none":
		return exitOK
	case "json":
		return writeRoleJSON(report)
	default:
		for _, line := range render.WrapLines(report.Lines(), render.Width()) {
			fmt.Fprintln(os.Stdout, line)
		}
		return exitOK
	}
}

func requalificationExit(report decision.RequalificationReport) int {
	for _, claim := range report.Claims {
		if claim.State != decision.RequalifyValid {
			return exitUnresolved
		}
	}
	return exitOK
}
