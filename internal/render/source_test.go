package render

import (
	"bytes"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/source"

	"github.com/clipperhouse/displaywidth"
)

func TestSourceResolutionPreservesScopeAndTerminalBounds(t *testing.T) {
	t.Setenv("FITR_WIDTH", "40")
	t.Setenv("NO_COLOR", "1")
	t.Setenv("FORCE_COLOR", "1")
	size := int64(18 << 30)
	var output bytes.Buffer
	WriteSourceResolution(&output, source.Resolution{
		State: "incomplete", Request: source.HFRequest{RepoID: "owner/model", Revision: "main"}, ResolvedCommit: strings.Repeat("a", 40),
		Files:        []source.FileMetadata{{Path: "hostile\x1b[2J" + strings.Repeat("模型", 40), State: "present", SizeBytes: &size, DeclaredSHA256: "sha256:example"}, {Path: "missing.gguf", State: "missing"}},
		Dependencies: []source.DependencyFinding{{Kind: "projector", Status: "unresolved", TargetFile: "mmproj.gguf"}},
		Gaps:         []string{"dependency_closure_unverified"},
	}, "rich")
	text := output.String()
	for _, want := range []string{"INCOMPLETE", "18.00 GiB declared", "File metadata only", "local fit and role quality"} {
		if !strings.Contains(strings.Join(strings.Fields(text), " "), want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "\x1b") {
		t.Fatal("terminal controls survived or NO_COLOR was ignored")
	}
	for _, line := range strings.Split(text, "\n") {
		if displaywidth.String(line) > 40 {
			t.Fatalf("source view overflow: %q", line)
		}
	}
}
