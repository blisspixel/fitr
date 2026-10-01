package llm

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/blisspixel/fitr/internal/strictjson"
)

const (
	ProfileSchema = "fitr.runtime.profile.v1"
	MaxArchCount  = 256
)

var (
	profileIdentifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

type ArchitectureSupportStatus string

const (
	ArchitectureSupported   ArchitectureSupportStatus = "supported"
	ArchitectureUnsupported ArchitectureSupportStatus = "unsupported"
	ArchitectureUnresolved  ArchitectureSupportStatus = "unresolved"
)

// ArchitectureSupport binds upstream GGUF architecture declarations to
// expected runtime support and companion requirements. This records an
// upstream build contract or operator allowlist; it is routing evidence,
// never a behavioral PASS.
type ArchitectureSupport struct {
	Architecture       string   `json:"architecture"`
	Status             string   `json:"status"` // "supported", "unsupported"
	Capabilities       []string `json:"capabilities,omitempty"`
	RequiredCompanions []string `json:"required_companions,omitempty"`
	SupportedQuants    []string `json:"supported_quants,omitempty"`
	Notes              string   `json:"notes,omitempty"`
}

// RuntimeSupportProfile declares architecture and companion contracts for
// an exact runtime build. An unsupported architecture is explicitly refused;
// an unlisted architecture remains unresolved. Capability declarations remain
// separate from measured behavioral evidence.
type RuntimeSupportProfile struct {
	Schema        string                `json:"schema"`
	ProfileSHA256 string                `json:"profile_sha256,omitempty"`
	Runtime       string                `json:"runtime"`
	Version       string                `json:"version"`
	Notes         string                `json:"notes,omitempty"`
	Architectures []ArchitectureSupport `json:"architectures"`
}

func (p *RuntimeSupportProfile) Validate() error {
	if p == nil {
		return errors.New("runtime support profile is nil")
	}
	if p.Schema != ProfileSchema {
		return fmt.Errorf("unexpected runtime profile schema: %q", p.Schema)
	}
	if !profileIdentifierPattern.MatchString(p.Runtime) {
		return fmt.Errorf("runtime name %q must be a lowercase identifier (1-64 chars)", p.Runtime)
	}
	if len(p.Version) < 1 || len(p.Version) > 64 {
		return errors.New("runtime version must be between 1 and 64 characters")
	}
	if len(p.Architectures) < 1 || len(p.Architectures) > MaxArchCount {
		return fmt.Errorf("runtime profile must contain between 1 and %d architectures", MaxArchCount)
	}
	seen := make(map[string]bool, len(p.Architectures))
	for _, arch := range p.Architectures {
		if !profileIdentifierPattern.MatchString(arch.Architecture) {
			return fmt.Errorf("architecture %q must be a lowercase identifier", arch.Architecture)
		}
		if seen[arch.Architecture] {
			return fmt.Errorf("duplicate architecture entry: %q", arch.Architecture)
		}
		seen[arch.Architecture] = true
		if arch.Status != string(ArchitectureSupported) && arch.Status != string(ArchitectureUnsupported) {
			return fmt.Errorf("architecture %q status must be 'supported' or 'unsupported', got %q", arch.Architecture, arch.Status)
		}
		if err := validateIdentifiers(arch.Capabilities, "capability"); err != nil {
			return fmt.Errorf("architecture %q: %w", arch.Architecture, err)
		}
		if err := validateIdentifiers(arch.RequiredCompanions, "required companion"); err != nil {
			return fmt.Errorf("architecture %q: %w", arch.Architecture, err)
		}
	}
	return nil
}

func validateIdentifiers(items []string, label string) error {
	if len(items) > 32 {
		return fmt.Errorf("too many %s entries (max 32)", label)
	}
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if !profileIdentifierPattern.MatchString(item) {
			return fmt.Errorf("invalid %s identifier %q", label, item)
		}
		if seen[item] {
			return fmt.Errorf("duplicate %s entry %q", label, item)
		}
		seen[item] = true
	}
	return nil
}

// Digest returns the deterministic sha256: hash of the canonical JSON bytes,
// omitting ProfileSHA256.
func (p *RuntimeSupportProfile) Digest() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	clone := *p
	clone.ProfileSHA256 = ""
	// Sort architectures deterministically by architecture name for canonical hashing.
	archs := slices.Clone(clone.Architectures)
	slices.SortFunc(archs, func(a, b ArchitectureSupport) int {
		return strings.Compare(a.Architecture, b.Architecture)
	})
	clone.Architectures = archs
	data, err := json.Marshal(clone)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// Seal computes Digest and stores it in ProfileSHA256.
func (p *RuntimeSupportProfile) Seal() error {
	digest, err := p.Digest()
	if err != nil {
		return err
	}
	p.ProfileSHA256 = digest
	return nil
}

// CheckArchitecture looks up the architecture and returns its support status.
// Unlisted architectures remain ArchitectureUnresolved.
func (p *RuntimeSupportProfile) CheckArchitecture(arch string) (ArchitectureSupport, ArchitectureSupportStatus) {
	if p == nil {
		return ArchitectureSupport{}, ArchitectureUnresolved
	}
	normalized := strings.ToLower(strings.TrimSpace(arch))
	for _, entry := range p.Architectures {
		if entry.Architecture == normalized {
			if entry.Status == string(ArchitectureSupported) {
				return entry, ArchitectureSupported
			}
			return entry, ArchitectureUnsupported
		}
	}
	return ArchitectureSupport{}, ArchitectureUnresolved
}

// RequiredCompanionsFor returns any required companion kinds (e.g. "projector")
// if the architecture is supported by this profile.
func (p *RuntimeSupportProfile) RequiredCompanionsFor(arch string) []string {
	entry, status := p.CheckArchitecture(arch)
	if status != ArchitectureSupported {
		return nil
	}
	return slices.Clone(entry.RequiredCompanions)
}

// HasCapability checks whether a supported architecture declares a specific capability.
func (p *RuntimeSupportProfile) HasCapability(arch, capability string) bool {
	entry, status := p.CheckArchitecture(arch)
	if status != ArchitectureSupported {
		return false
	}
	normCap := strings.ToLower(strings.TrimSpace(capability))
	return slices.Contains(entry.Capabilities, normCap)
}

// CheckQuant tests whether a quantization type is compatible with this architecture.
// An empty supported_quants list means unconstrained (clears with unconstrained status).
func (p *RuntimeSupportProfile) CheckQuant(arch, quant string) (bool, string) {
	entry, status := p.CheckArchitecture(arch)
	if status != ArchitectureSupported {
		return false, string(status)
	}
	if len(entry.SupportedQuants) == 0 {
		return true, "unconstrained"
	}
	normQuant := strings.TrimSpace(quant)
	for _, q := range entry.SupportedQuants {
		if strings.EqualFold(q, normQuant) {
			return true, "supported"
		}
	}
	return false, "unsupported_quant"
}

// DefaultProfile returns a versioned baseline profile for standard runtimes.
func DefaultProfile(runtimeName string) *RuntimeSupportProfile {
	norm := strings.ToLower(strings.TrimSpace(runtimeName))
	switch norm {
	case "llama-server":
		p := &RuntimeSupportProfile{
			Schema:  ProfileSchema,
			Runtime: "llama-server",
			Version: "b4000+",
			Notes:   "Pinned baseline profile for upstream llama.cpp b4000+ builds",
			Architectures: []ArchitectureSupport{
				{Architecture: "command-r", Status: "supported", Capabilities: []string{"text", "tools"}},
				{Architecture: "deepseek2", Status: "supported", Capabilities: []string{"text", "tools"}},
				{Architecture: "gemma", Status: "supported", Capabilities: []string{"text"}},
				{Architecture: "gemma2", Status: "supported", Capabilities: []string{"text"}},
				{Architecture: "llama", Status: "supported", Capabilities: []string{"text", "tools"}},
				{Architecture: "llava", Status: "supported", Capabilities: []string{"text", "vision"}, RequiredCompanions: []string{"projector"}},
				{Architecture: "minicpm", Status: "supported", Capabilities: []string{"text", "vision"}, RequiredCompanions: []string{"projector"}},
				{Architecture: "mistral", Status: "supported", Capabilities: []string{"text", "tools"}},
				{Architecture: "phi3", Status: "supported", Capabilities: []string{"text"}},
				{Architecture: "qwen2", Status: "supported", Capabilities: []string{"text", "tools"}},
				{Architecture: "qwen3", Status: "supported", Capabilities: []string{"text", "tools"}},
			},
		}
		_ = p.Seal()
		return p
	case "ollama":
		p := &RuntimeSupportProfile{
			Schema:  ProfileSchema,
			Runtime: "ollama",
			Version: "0.5.0+",
			Notes:   "Pinned baseline profile for Ollama 0.5.0+ builds",
			Architectures: []ArchitectureSupport{
				{Architecture: "command-r", Status: "supported", Capabilities: []string{"text", "tools"}},
				{Architecture: "gemma", Status: "supported", Capabilities: []string{"text"}},
				{Architecture: "gemma2", Status: "supported", Capabilities: []string{"text"}},
				{Architecture: "llama", Status: "supported", Capabilities: []string{"text", "tools"}},
				{Architecture: "llava", Status: "supported", Capabilities: []string{"text", "vision"}, RequiredCompanions: []string{"projector"}},
				{Architecture: "mistral", Status: "supported", Capabilities: []string{"text", "tools"}},
				{Architecture: "phi3", Status: "supported", Capabilities: []string{"text"}},
				{Architecture: "qwen2", Status: "supported", Capabilities: []string{"text", "tools"}},
			},
		}
		_ = p.Seal()
		return p
	default:
		return nil
	}
}

// LoadProfile loads and validates a runtime support profile from a JSON file.
func LoadProfile(path string) (*RuntimeSupportProfile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading runtime profile: %w", err)
	}
	if err := strictjson.Validate(data); err != nil {
		return nil, fmt.Errorf("decoding runtime profile: %w", err)
	}
	var profile RuntimeSupportProfile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&profile); err != nil {
		return nil, fmt.Errorf("decoding runtime profile: %w", err)
	}
	if err := profile.Validate(); err != nil {
		return nil, fmt.Errorf("validating runtime profile: %w", err)
	}
	if profile.ProfileSHA256 != "" {
		digest, err := profile.Digest()
		if err != nil {
			return nil, err
		}
		if digest != profile.ProfileSHA256 {
			return nil, fmt.Errorf("runtime profile digest mismatch: declared %s, computed %s", profile.ProfileSHA256, digest)
		}
	}
	return &profile, nil
}
