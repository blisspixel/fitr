package main

import (
	"testing"

	"github.com/blisspixel/fitr/internal/ollama"
)

func TestInventoryRemoteAliasesDoNotReuseLocalArtifactFacts(t *testing.T) {
	backend := &artifactVerifierTestBackend{verify: func(model, _ string) (string, error) {
		if model != "local" {
			t.Fatalf("remote candidate reached local artifact verifier: %s", model)
		}
		return artifactDigestA, nil
	}}
	tags := []ollama.ModelInfo{
		{Name: "cloud", ReportedDigest: artifactDigestB, Size: 1 << 40, Path: "/must-not-inspect.gguf"},
		{Name: "cloud:Latest", RemoteHost: "remote.invalid", Digest: artifactDigestB},
		{Name: "local", ReportedDigest: artifactDigestA, Size: 1024},
	}
	items, warnings := installedInventoryModels(backend, tags)
	if len(items) != 3 || len(warnings) != 0 || len(backend.calls) != 1 {
		t.Fatalf("mixed inventory lost entries or verified remote bytes: %+v / %v", items, warnings)
	}
	for _, item := range items[:2] {
		if !item.Remote || item.ArtifactDigest != "" || item.Path != "" || item.Size != 0 {
			t.Fatalf("remote inventory retained local artifact facts: %+v", item)
		}
	}
	if items[2].Remote || items[2].ArtifactDigest != artifactDigestA || items[2].Size != 1024 {
		t.Fatalf("unrelated local candidate changed: %+v", items[2])
	}
	if digest, _ := inventoryArtifactDigest(backend, tags[1]); digest != "" {
		t.Fatal("remote manifest digest was promoted as a local artifact")
	}
}
