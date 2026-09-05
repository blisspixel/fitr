package record

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode"
)

const RuntimeBindingSchema = "fitr.runtime.binding.v1"

// RuntimeBinding records the owned configuration observed for a run. Ownership
// identifies that launch, not a guarantee that the process is still running.
type RuntimeBinding struct {
	Schema                    string `json:"schema"`
	Kind                      string `json:"kind"`
	ProfileSHA256             string `json:"profile_sha256"`
	ExecutableSHA256          string `json:"executable_sha256"`
	LaunchConfigurationSHA256 string `json:"launch_configuration_sha256"`
	ModelConfigurationSHA256  string `json:"model_configuration_sha256"`
	ArtifactDigest            string `json:"artifact_digest"`
	RuntimeVersion            string `json:"runtime_version"`
	OwnershipSHA256           string `json:"ownership_sha256"`
}

func (binding RuntimeBinding) Validate() error {
	if binding.Schema != RuntimeBindingSchema || binding.Kind != "owned_ollama" ||
		len(binding.RuntimeVersion) == 0 || len(binding.RuntimeVersion) > 128 || strings.TrimSpace(binding.RuntimeVersion) != binding.RuntimeVersion {
		return errors.New("invalid owned runtime binding")
	}
	for _, r := range binding.RuntimeVersion {
		if unicode.IsControl(r) {
			return errors.New("runtime version contains control characters")
		}
	}
	for _, digest := range []string{binding.ProfileSHA256, binding.ExecutableSHA256, binding.LaunchConfigurationSHA256,
		binding.ModelConfigurationSHA256, binding.ArtifactDigest, binding.OwnershipSHA256} {
		if !sha256Digest.MatchString(digest) {
			return errors.New("runtime binding contains an invalid digest")
		}
	}
	return nil
}

func (binding RuntimeBinding) ValidateFor(identity ModelIdentity) error {
	if err := binding.Validate(); err != nil {
		return err
	}
	if identity.Backend != "ollama" || identity.RuntimeBoundDigest() != binding.ArtifactDigest || identity.Runtime != binding.RuntimeVersion {
		return errors.New("owned runtime binding differs from the measured model identity")
	}
	return nil
}

func CloneRuntimeBinding(binding *RuntimeBinding) *RuntimeBinding {
	if binding == nil {
		return nil
	}
	cloned := *binding
	return &cloned
}

// SameRuntimeConfiguration allows a new child launch while requiring the exact
// stable runtime, launch, model configuration and artifact from the plan.
func SameRuntimeConfiguration(a, b *RuntimeBinding) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if a.Validate() != nil || b.Validate() != nil {
		return false
	}
	left, right := *a, *b
	left.OwnershipSHA256, right.OwnershipSHA256 = "", ""
	return left == right
}

// SameRuntimeProfile compares the shared execution policy across candidates.
// Each candidate's artifact and model configuration remain separately bound.
func SameRuntimeProfile(a, b *RuntimeBinding) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	left, err := a.comparisonKey()
	if err != nil {
		return false
	}
	right, err := b.comparisonKey()
	return err == nil && left == right
}

func (binding RuntimeBinding) comparisonKey() (string, error) {
	if err := binding.Validate(); err != nil {
		return "", err
	}
	// Artifact and model template differences belong to candidate identity;
	// shared execution policy must still agree before comparing candidates.
	binding.ArtifactDigest, binding.ModelConfigurationSHA256, binding.OwnershipSHA256 = "", "", ""
	data, err := json.Marshal(binding)
	if err != nil {
		return "", err
	}
	return digestBytes("fitr.runtime.profile-comparison.v1", data), nil
}

func (r *Record) validateRuntimeBinding() error {
	if r.Manifest == nil {
		if r.RuntimeBinding != nil {
			return errors.New("runtime binding requires a sealed manifest")
		}
		return nil
	}
	if (r.RuntimeBinding == nil) != (r.Manifest.RuntimeBinding == nil) ||
		(r.RuntimeBinding != nil && *r.RuntimeBinding != *r.Manifest.RuntimeBinding) {
		return errors.New("record runtime binding differs from its manifest")
	}
	if r.RuntimeBinding != nil {
		return r.RuntimeBinding.ValidateFor(r.Manifest.Model)
	}
	return nil
}
