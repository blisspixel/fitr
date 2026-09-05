package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestMCPCommandRejectsExecutionAndPathArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"run"}, {"serve", "--path", "private"}, {"serve", "--backend", "openai"}} {
		var output, diagnostic bytes.Buffer
		code := runMCP(t.Context(), args, io.NopCloser(strings.NewReader("")), &output, &diagnostic)
		if code != exitUsage || output.Len() != 0 || strings.Contains(diagnostic.String(), "private") {
			t.Fatalf("code=%d output=%s diagnostic=%s", code, output.String(), diagnostic.String())
		}
	}
}

func TestMCPCommandHelpAndTransport(t *testing.T) {
	t.Setenv("FITR_RESULTS", t.TempDir())
	var output, diagnostic bytes.Buffer
	if code := runMCP(t.Context(), []string{"--help"}, io.NopCloser(strings.NewReader("")), &output, &diagnostic); code != exitOK || output.Len() != 0 || !strings.Contains(diagnostic.String(), "2026-07-28") {
		t.Fatalf("help code=%d", code)
	}
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"fitr_roles_list","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}` + "\n"
	diagnostic.Reset()
	if code := runMCP(t.Context(), []string{"serve"}, io.NopCloser(strings.NewReader(input)), &output, &diagnostic); code != exitOK || diagnostic.Len() != 0 || !strings.Contains(output.String(), `"structuredContent"`) {
		t.Fatalf("serve code=%d out=%s diagnostic=%s", code, output.String(), diagnostic.String())
	}
}

func TestMCPCommandTransportErrorAndCancellation(t *testing.T) {
	t.Setenv("FITR_RESULTS", t.TempDir())
	var output, diagnostic bytes.Buffer
	if code := runMCP(t.Context(), []string{"serve"}, io.NopCloser(strings.NewReader(strings.Repeat("x", 70000))), &output, &diagnostic); code != exitError || output.Len() != 0 {
		t.Fatalf("error code=%d output=%s", code, output.String())
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if code := runMCP(ctx, []string{"serve"}, io.NopCloser(strings.NewReader("")), &output, &diagnostic); code != exitInterrupt {
		t.Fatalf("cancel code=%d", code)
	}
}
