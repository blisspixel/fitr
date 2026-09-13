package source

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// SelectedGGUFGroup identifies one complete filename-defined model selection.
// A companion or another model cannot be added to one weight total: their
// compatibility and concurrent allocation have not been established.
func SelectedGGUFGroup(files []FileMetadata) ([]FileMetadata, string) {
	if len(files) == 0 || len(files) > MaxFiles {
		return nil, "one explicitly selected GGUF or complete shard group is required"
	}
	ordered := slices.Clone(files)
	slices.SortFunc(ordered, func(a, b FileMetadata) int { return strings.Compare(a.Path, b.Path) })
	for i, file := range ordered {
		if !strings.HasSuffix(strings.ToLower(file.Path), ".gguf") || candidateKind(file.Path) != "" {
			return nil, "selected companions or non-GGUF files require a separate dependency and allocation plan"
		}
		if file.State != "present" || file.SizeBytes == nil || *file.SizeBytes <= 0 || file.DeclaredSHA256 == "" {
			return nil, "every selected GGUF needs a declared positive size and content hash"
		}
		if i > 0 && file.Path == ordered[i-1].Path {
			return nil, "the selected GGUF list contains a duplicate file"
		}
	}
	parts := shardPattern.FindStringSubmatch(ordered[0].Path)
	if parts == nil {
		if len(ordered) == 1 {
			return ordered, ""
		}
		return nil, "multiple independent models require separate projections"
	}
	total, err := strconv.Atoi(parts[3])
	if err != nil || total < 1 || total > MaxFiles || len(ordered) != total {
		return nil, "the selected GGUF shard group is incomplete"
	}
	for i, file := range ordered {
		expected := fmt.Sprintf("%s-%05d-of-%05d%s", parts[1], i+1, total, parts[4])
		if file.Path != expected {
			return nil, "the selected files do not form one complete GGUF shard group"
		}
	}
	return ordered, ""
}
