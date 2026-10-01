package llm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeProfileValidation(t *testing.T) {
	valid := &RuntimeSupportProfile{
		Schema:  ProfileSchema,
		Runtime: "llama-server",
		Version: "b4000",
		Architectures: []ArchitectureSupport{
			{Architecture: "llama", Status: "supported", Capabilities: []string{"text", "tools"}},
			{Architecture: "llava", Status: "supported", Capabilities: []string{"text", "vision"}, RequiredCompanions: []string{"projector"}},
			{Architecture: "mpt", Status: "unsupported"},
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid profile failed validation: %v", err)
	}

	// Bad schema
	badSchema := *valid
	badSchema.Schema = "bad.schema"
	if err := badSchema.Validate(); err == nil {
		t.Fatal("expected error for bad schema")
	}

	// Bad runtime name
	badRuntime := *valid
	badRuntime.Runtime = "UPPERCASE"
	if err := badRuntime.Validate(); err == nil {
		t.Fatal("expected error for uppercase runtime name")
	}

	// Empty version
	badVersion := *valid
	badVersion.Version = ""
	if err := badVersion.Validate(); err == nil {
		t.Fatal("expected error for empty version")
	}

	// Unknown status
	badStatus := *valid
	badStatus.Architectures = []ArchitectureSupport{{Architecture: "llama", Status: "maybe"}}
	if err := badStatus.Validate(); err == nil {
		t.Fatal("expected error for unknown status 'maybe'")
	}

	// Duplicate architecture
	dupArch := *valid
	dupArch.Architectures = []ArchitectureSupport{
		{Architecture: "llama", Status: "supported"},
		{Architecture: "llama", Status: "unsupported"},
	}
	if err := dupArch.Validate(); err == nil {
		t.Fatal("expected error for duplicate architecture")
	}

	// Invalid capability
	badCap := *valid
	badCap.Architectures = []ArchitectureSupport{
		{Architecture: "llama", Status: "supported", Capabilities: []string{"INVALID CAP"}},
	}
	if err := badCap.Validate(); err == nil {
		t.Fatal("expected error for invalid capability")
	}
}

func TestRuntimeProfileDigestAndSeal(t *testing.T) {
	profile := &RuntimeSupportProfile{
		Schema:  ProfileSchema,
		Runtime: "llama-server",
		Version: "b4000",
		Architectures: []ArchitectureSupport{
			{Architecture: "qwen2", Status: "supported"},
			{Architecture: "llama", Status: "supported"},
		},
	}
	digest1, err := profile.Digest()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := profile.Seal(); err != nil {
		t.Fatalf("seal failed: %v", err)
	}
	if profile.ProfileSHA256 != digest1 {
		t.Fatalf("seal did not set digest: %s vs %s", profile.ProfileSHA256, digest1)
	}
	// Re-calculating digest with sealed ProfileSHA256 gives same digest
	digest2, err := profile.Digest()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if digest1 != digest2 {
		t.Fatalf("digest changed after seal: %s vs %s", digest1, digest2)
	}
}

func TestCheckArchitectureAndCapabilities(t *testing.T) {
	profile := &RuntimeSupportProfile{
		Schema:  ProfileSchema,
		Runtime: "llama-server",
		Version: "b4000",
		Architectures: []ArchitectureSupport{
			{
				Architecture:       "llava",
				Status:             "supported",
				Capabilities:       []string{"text", "vision"},
				RequiredCompanions: []string{"projector"},
				SupportedQuants:    []string{"Q4_K_M", "Q8_0"},
			},
			{
				Architecture: "mpt",
				Status:       "unsupported",
				Notes:        "upstream backend removed mpt support",
			},
		},
	}

	// Supported architecture
	entry, status := profile.CheckArchitecture("llava")
	if status != ArchitectureSupported || entry.Architecture != "llava" {
		t.Fatalf("expected supported for llava, got %s", status)
	}
	if !profile.HasCapability("llava", "vision") || !profile.HasCapability("llava", "text") {
		t.Fatal("expected llava to have text and vision capabilities")
	}
	if profile.HasCapability("llava", "audio") {
		t.Fatal("did not expect llava to have audio capability")
	}
	companions := profile.RequiredCompanionsFor("llava")
	if len(companions) != 1 || companions[0] != "projector" {
		t.Fatalf("expected projector companion, got %v", companions)
	}

	// Unsupported architecture
	_, status = profile.CheckArchitecture("mpt")
	if status != ArchitectureUnsupported {
		t.Fatalf("expected unsupported for mpt, got %s", status)
	}

	// Unlisted architecture (unresolved)
	_, status = profile.CheckArchitecture("unknown_arch")
	if status != ArchitectureUnresolved {
		t.Fatalf("expected unresolved for unknown_arch, got %s", status)
	}

	// Quantization checks
	ok, reason := profile.CheckQuant("llava", "Q4_K_M")
	if !ok || reason != "supported" {
		t.Fatalf("expected Q4_K_M supported, got %v, %s", ok, reason)
	}
	ok, reason = profile.CheckQuant("llava", "IQ1_S")
	if ok || reason != "unsupported_quant" {
		t.Fatalf("expected IQ1_S unsupported_quant, got %v, %s", ok, reason)
	}
}

func TestDefaultProfiles(t *testing.T) {
	for _, name := range []string{"llama-server", "ollama"} {
		p := DefaultProfile(name)
		if p == nil {
			t.Fatalf("expected default profile for %s", name)
		}
		if err := p.Validate(); err != nil {
			t.Fatalf("default profile for %s failed validation: %v", name, err)
		}
		if p.ProfileSHA256 == "" {
			t.Fatalf("default profile for %s is not sealed", name)
		}
		// Check that llama is supported
		_, status := p.CheckArchitecture("llama")
		if status != ArchitectureSupported {
			t.Fatalf("expected llama supported in %s", name)
		}
	}
	if DefaultProfile("nonexistent") != nil {
		t.Fatal("expected nil for nonexistent default profile")
	}
}

func TestLoadProfile(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "profile.json")

	profile := DefaultProfile("llama-server")
	data := []byte(`{
		"schema": "fitr.runtime.profile.v1",
		"profile_sha256": "` + profile.ProfileSHA256 + `",
		"runtime": "llama-server",
		"version": "b4000+",
		"notes": "Pinned baseline profile for upstream llama.cpp b4000+ builds",
		"architectures": [
			{"architecture": "command-r", "status": "supported", "capabilities": ["text", "tools"]},
			{"architecture": "deepseek2", "status": "supported", "capabilities": ["text", "tools"]},
			{"architecture": "gemma", "status": "supported", "capabilities": ["text"]},
			{"architecture": "gemma2", "status": "supported", "capabilities": ["text"]},
			{"architecture": "llama", "status": "supported", "capabilities": ["text", "tools"]},
			{"architecture": "llava", "status": "supported", "capabilities": ["text", "vision"], "required_companions": ["projector"]},
			{"architecture": "minicpm", "status": "supported", "capabilities": ["text", "vision"], "required_companions": ["projector"]},
			{"architecture": "mistral", "status": "supported", "capabilities": ["text", "tools"]},
			{"architecture": "phi3", "status": "supported", "capabilities": ["text"]},
			{"architecture": "qwen2", "status": "supported", "capabilities": ["text", "tools"]},
			{"architecture": "qwen3", "status": "supported", "capabilities": ["text", "tools"]}
		]
	}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadProfile(path)
	if err != nil {
		t.Fatalf("load profile failed: %v", err)
	}
	if loaded.Runtime != "llama-server" {
		t.Fatalf("unexpected runtime: %s", loaded.Runtime)
	}

	// Extra unexpected field fails strict unmarshal
	strictPath := filepath.Join(tempDir, "strict.json")
	strictData := []byte(`{"schema": "fitr.runtime.profile.v1", "runtime": "llama-server", "version": "1.0", "extra_field": "bad", "architectures": [{"architecture": "llama", "status": "supported"}]}`)
	if err := os.WriteFile(strictPath, strictData, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfile(strictPath); err == nil {
		t.Fatal("expected strict unmarshal failure on extra field")
	}
}
