package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/blisspixel/fitr/internal/mcp"
)

func cmdMCP(ctx context.Context, args []string) int {
	return runMCP(ctx, args, os.Stdin, os.Stdout, os.Stderr)
}

func runMCP(ctx context.Context, args []string, input io.ReadCloser, output, diagnostic io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(diagnostic, "usage: fitr mcp serve")
		fmt.Fprintln(diagnostic, "Read-only MCP 2026-07-28 over stdio. Shares redacted role evidence from FITR_RESULTS. No legacy handshake, HTTP, model execution or mutations.")
		return exitOK
	}
	if len(args) != 1 || args[0] != "serve" {
		fmt.Fprintln(diagnostic, "error: expected fitr mcp serve; no path or execution arguments are accepted")
		return exitUsage
	}
	if err := mcp.Serve(ctx, input, output, resultsDir(), version); err != nil {
		if errors.Is(err, context.Canceled) {
			return exitInterrupt
		}
		fmt.Fprintln(diagnostic, "error: MCP evidence server stopped; check local evidence and stdio framing")
		return exitError
	}
	return exitOK
}
