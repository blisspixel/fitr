package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/blisspixel/fitr/internal/source"
)

func WriteSourceResolution(w io.Writer, resolution source.Resolution, mode string) {
	p, _ := inventoryStyle(Resolve(mode) == "rich")
	width := Width()
	fmt.Fprintf(w, "  %s\n", p.wrap(p.Head, "fitr / source resolution"))
	Field(w, "  ", 2, resolution.Request.RepoID, width)
	fmt.Fprintf(w, "  %s\n", p.wrap(p.Warn, "METADATA "+strings.ToUpper(SingleLine(resolution.State))))
	fmt.Fprintf(w, "  %s\n", p.wrap(p.Muted, strings.Repeat("-", width-4)))
	Field(w, "  revision", 13, resolution.Request.Revision, width)
	if resolution.ResolvedCommit != "" {
		Field(w, "  commit", 13, resolution.ResolvedCommit, width)
	}
	for _, file := range resolution.Files {
		fmt.Fprintln(w)
		Field(w, "  file", 13, file.Path, width)
		detail := file.State + " | size unknown"
		if file.SizeBytes != nil {
			detail = fmt.Sprintf("%s | %.2f GiB declared", file.State, float64(*file.SizeBytes)/float64(1<<30))
		}
		Field(w, "  metadata", 13, detail, width)
		if file.DeclaredSHA256 == "" {
			Field(w, "  hash", 13, "content SHA-256 unavailable", width)
		} else {
			Field(w, "  hash", 13, "provider-declared SHA-256; local bytes unverified", width)
		}
	}
	fmt.Fprintln(w)
	for _, dependency := range resolution.Dependencies {
		Field(w, "  dependency", 13, dependency.Kind+": "+dependency.Status+" "+dependency.TargetFile, width)
	}
	for _, gap := range resolution.Gaps {
		Field(w, "  gap", 13, strings.ReplaceAll(gap, "_", " "), width)
	}
	fmt.Fprintln(w)
	Field(w, "  next", 13, "Resolve dependencies and runtime compatibility before planning a download or local measurement.", width)
	fmt.Fprintln(w)
	Field(w, "  ", 2, "File metadata only. No weights downloaded; local fit and role quality remain unmeasured.", width)
}
