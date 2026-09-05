package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/blisspixel/fitr/internal/artifact"
)

func WriteArtifactBinding(w io.Writer, binding artifact.Binding, mode string) {
	p, _ := inventoryStyle(Resolve(mode) == "rich")
	width := Width()
	fmt.Fprintf(w, "  %s\n", p.wrap(p.Head, "fitr / local artifact observation"))
	Field(w, "  ", 2, binding.Source.Request.RepoID, width)
	fmt.Fprintf(w, "  %s\n", p.wrap(p.Warn, "LOCAL BYTES "+strings.ToUpper(strings.ReplaceAll(SingleLine(binding.State), "_", " "))))
	fmt.Fprintf(w, "  %s\n", p.wrap(p.Muted, strings.Repeat("-", width-4)))
	Field(w, "  commit", 14, binding.Source.ResolvedCommit, width)
	Field(w, "  read", 14, artifactBytes(binding.BytesRead)+" observed | "+artifactBytes(binding.Limits.MaxBytes)+" limit", width)
	for _, file := range binding.Files {
		writeArtifactFile(w, file, binding, width)
	}
	for _, path := range binding.UnmappedFiles {
		Field(w, "  unmapped", 14, path, width)
	}
	for _, dependency := range binding.Source.Dependencies {
		Field(w, "  dependency", 14, dependency.Kind+": "+dependency.Status+" ("+dependency.Basis+")", width)
		if dependency.SourceFile != "" {
			Field(w, "    source", 14, dependency.SourceFile, width)
		}
		if dependency.TargetFile != "" {
			Field(w, "    target", 14, dependency.TargetFile, width)
		}
	}
	for _, gap := range binding.Gaps {
		Field(w, "  gap", 14, gap, width)
	}
	fmt.Fprintln(w)
	Field(w, "  dependencies", 16, binding.DependencyState, width)
	Field(w, "  runtime", 16, binding.RuntimeState, width)
	Field(w, "  capacity", 16, binding.CapacityState, width)
	Field(w, "  quality", 16, binding.QualityState, width)
	Field(w, "  next", 16, "Establish dependency compatibility and the exact loaded runtime configuration before fit and quality testing.", width)
	fmt.Fprintln(w)
	Field(w, "  ", 2, "Historical local-byte observation. Files can change afterward; this receipt does not authorize loading or adoption.", width)
}

func writeArtifactFile(w io.Writer, file artifact.FileObservation, binding artifact.Binding, width int) {
	fmt.Fprintln(w)
	Field(w, "  file", 14, file.SourcePath, width)
	Field(w, "  local", 14, file.LocalPath, width)
	Field(w, "  observation", 14, strings.ReplaceAll(file.State, "_", " "), width)
	Field(w, "  component", 14, file.ComponentRole+" (declared)", width)
	if file.Before != nil {
		Field(w, "  size", 14, artifactBytes(file.Before.SizeBytes)+" observed", width)
	}
	if file.ObservedSHA256 != "" {
		fmt.Fprintln(w, "  observed SHA-256")
		Field(w, "    ", 4, file.ObservedSHA256, width)
	}
	for _, expected := range binding.Source.Files {
		if expected.Path != file.SourcePath {
			continue
		}
		if file.State == "size_mismatch" && expected.SizeBytes != nil {
			Field(w, "  expected", 14, artifactBytes(*expected.SizeBytes)+" declared by source", width)
		}
		if file.State == "hash_mismatch" {
			Field(w, "  expected", 14, expected.DeclaredSHA256+" (source declared)", width)
		}
	}
}

func artifactBytes(value int64) string {
	for _, unit := range []struct {
		bytes int64
		name  string
	}{{1 << 30, "GiB"}, {1 << 20, "MiB"}, {1 << 10, "KiB"}} {
		if value >= unit.bytes {
			return fmt.Sprintf("%.2f %s (%d B)", float64(value)/float64(unit.bytes), unit.name, value)
		}
	}
	return fmt.Sprintf("%d B", value)
}
