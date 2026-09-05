package source

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestSourceDependencyFindings(t *testing.T) {
	first := "q4/model-00001-of-00003.gguf"
	siblings := []hfSibling{sourceSibling(first), sourceSibling("q4/model-00002-of-00003.gguf"),
		sourceSibling("q8/model-00001-of-00002.gguf"), sourceSibling("mmproj-f16.gguf"),
		sourceSibling("mmproj-q8.gguf"), sourceSibling("text_encoder/model.safetensors")}
	body := sourceBody(t, siblings...)
	resolver, _ := sourceResolver(t, body, body)
	request := sourceRequest()
	request.Files = []string{first}
	result, err := resolver.ResolveHF(t.Context(), request)
	if err != nil || result.State != "resolved" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	want := []DependencyFinding{
		{Kind: "shard", SourceFile: first, TargetFile: "q4/model-00002-of-00003.gguf", Status: "unselected", Basis: "numbered_filename"},
		{Kind: "shard", SourceFile: first, TargetFile: "q4/model-00003-of-00003.gguf", Status: "missing", Basis: "numbered_filename"},
		{Kind: "projector", TargetFile: "mmproj-f16.gguf", Status: "candidate", Basis: "filename_only"},
		{Kind: "projector", TargetFile: "mmproj-q8.gguf", Status: "candidate", Basis: "filename_only"},
		{Kind: "encoder", TargetFile: "text_encoder/model.safetensors", Status: "candidate", Basis: "filename_only"},
	}
	for _, finding := range want {
		if !slices.Contains(result.Dependencies, finding) {
			t.Fatalf("missing finding %+v in %+v", finding, result.Dependencies)
		}
	}
	for _, finding := range result.Dependencies {
		if strings.HasPrefix(finding.TargetFile, "q8/") {
			t.Fatal("unrelated shard group selected")
		}
	}
	altered := sourceClone(t, result)
	altered.Dependencies = altered.Dependencies[:len(altered.Dependencies)-1]
	if _, err := altered.Digest(); err == nil {
		t.Fatal("omitted dependency could be resealed")
	}
}

func TestSourceShardGroupBounds(t *testing.T) {
	cases := []struct {
		selected []string
		paths    []string
	}{
		{[]string{"model-00000-of-00002.gguf"}, nil},
		{[]string{"model-00001-of-00000.gguf"}, nil},
		{[]string{"model-00003-of-00002.gguf"}, nil},
		{[]string{"model-00001-of-99999.gguf"}, nil},
		{[]string{"model-00001-of-00002.gguf"}, []string{"model-00002-of-00003.gguf"}},
	}
	for _, test := range cases {
		inventory := make(map[string]FileMetadata)
		for _, path := range test.paths {
			inventory[path] = FileMetadata{Path: path, State: "present"}
		}
		if _, err := findDependencies(test.selected, inventory); err == nil {
			t.Fatalf("accepted group %v", test.selected)
		}
	}
	inventory := make(map[string]FileMetadata)
	for index := range MaxDependencies {
		path := fmt.Sprintf("encoder/%03d.bin", index)
		inventory[path] = FileMetadata{Path: path, State: "present"}
	}
	if _, err := findDependencies([]string{"model.gguf"}, inventory); err == nil {
		t.Fatal("candidate limit ignored")
	}
}

func TestSourceSharedShardGroups(t *testing.T) {
	selected := []string{"model-00001-of-00003.safetensors", "model-00002-of-00003.safetensors"}
	findings, err := findDependencies(selected, map[string]FileMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, finding := range findings {
		if finding.Kind == "shard" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("duplicate shard findings: %+v", findings)
	}
}
