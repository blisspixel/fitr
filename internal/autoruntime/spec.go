// Package autoruntime owns a bounded, explicitly configured Ollama child.
// It does not download models, choose candidates, or attest model quality.
package autoruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

const (
	SpecSchema            = "fitr.autoruntime.spec.v1"
	HashTimeout           = 60 * time.Second
	StartupTimeout        = 30 * time.Second
	MaxRuntimeFiles       = 4096
	MaxRuntimeBytes int64 = 8 << 30
	MaxFileBytes    int64 = 2 << 30
)

var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][a-zA-Z0-9.-]+)?$`)

// Spec contains declarations, not a capacity or execution-locality result.
// Paths and hashes are explicit and the library tree is part of the profile.
type Spec struct {
	Schema           string `json:"schema"`
	Executable       string `json:"executable"`
	ExecutableSHA256 string `json:"executable_sha256"`
	LibrariesSHA256  string `json:"libraries_sha256"`
	RuntimeVersion   string `json:"runtime_version"`
	ModelStore       string `json:"model_store"`
	NumCtx           int    `json:"num_ctx"`
	KVCacheType      string `json:"kv_cache_type"`
	FlashAttention   bool   `json:"flash_attention"`
	ReserveBytes     int64  `json:"reserve_bytes"`
}

func (s Spec) Validate() error {
	if s.Schema != SpecSchema || !digestPattern.MatchString(s.ExecutableSHA256) ||
		!digestPattern.MatchString(s.LibrariesSHA256) || len(s.RuntimeVersion) > 128 || !versionPattern.MatchString(s.RuntimeVersion) {
		return errors.New("invalid owned Ollama schema, binary/library digest or runtime version")
	}
	if err := validatePath(s.Executable); err != nil {
		return fmt.Errorf("runtime executable: %w", err)
	}
	if err := validatePath(s.ModelStore); err != nil {
		return fmt.Errorf("model store: %w", err)
	}
	if s.NumCtx < 512 || s.NumCtx > 1<<20 || s.ReserveBytes < 0 || s.ReserveBytes > 1<<40 {
		return errors.New("runtime context must be 512..1048576 tokens and reserve 0..1 TiB")
	}
	if s.KVCacheType != "f16" && s.KVCacheType != "q8_0" && s.KVCacheType != "q4_0" {
		return errors.New("runtime KV cache must be f16, q8_0 or q4_0")
	}
	if s.KVCacheType != "f16" && !s.FlashAttention {
		return errors.New("quantized runtime KV cache requires flash attention")
	}
	return nil
}

// ProfileDigests permits historical plan validation without launching a process.
func (s Spec) ProfileDigests() (profile, launch string, err error) {
	if err := s.Validate(); err != nil {
		return "", "", err
	}
	p := prepared(s)
	return p.ProfileSHA256, p.LaunchConfigurationSHA256, nil
}

// Prepared cannot be constructed or changed into an executable plan by setting
// its exported report fields; Start verifies its private preparation seal.
type Prepared struct {
	Spec                      Spec   `json:"spec"`
	ProfileSHA256             string `json:"profile_sha256"`
	LaunchConfigurationSHA256 string `json:"launch_configuration_sha256"`
	seal                      string
}

// Inspect reads the explicit software installation and returns editable defaults.
// The version-only subprocess uses an isolated home and a fixture HTTP listener.
// It never starts Ollama serving or reads the model store's descendants.
func Inspect(ctx context.Context, executable, modelStore string) (Spec, error) {
	if err := supported(); err != nil {
		return Spec{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, HashTimeout)
	defer cancel()
	executable, err := physicalPath(executable, false)
	if err != nil {
		return Spec{}, err
	}
	modelStore, err = physicalPath(modelStore, true)
	if err != nil {
		return Spec{}, err
	}
	if err := validateModelStore(modelStore); err != nil {
		return Spec{}, err
	}
	lease, exeHash, libraries, err := inspectInstallation(ctx, executable)
	if err != nil {
		return Spec{}, err
	}
	defer lease.close()
	if err := lease.verify(ctx); err != nil {
		return Spec{}, err
	}
	version, err := inspectVersion(ctx, executable)
	if err != nil {
		return Spec{}, err
	}
	if err := lease.verify(ctx); err != nil {
		return Spec{}, err
	}
	spec := Spec{Schema: SpecSchema, Executable: executable, ExecutableSHA256: exeHash,
		LibrariesSHA256: libraries, RuntimeVersion: version, ModelStore: modelStore,
		NumCtx: 8192, KVCacheType: "f16", FlashAttention: true, ReserveBytes: 2 << 30}
	return spec, spec.Validate()
}

// Prepare verifies the declaration before any serving child is created.
func Prepare(ctx context.Context, spec Spec) (Prepared, error) {
	if err := supported(); err != nil {
		return Prepared{}, err
	}
	if err := spec.Validate(); err != nil {
		return Prepared{}, err
	}
	observed, err := Inspect(ctx, spec.Executable, spec.ModelStore)
	if err != nil {
		return Prepared{}, err
	}
	if observed.ExecutableSHA256 != spec.ExecutableSHA256 || observed.LibrariesSHA256 != spec.LibrariesSHA256 || observed.RuntimeVersion != spec.RuntimeVersion {
		return Prepared{}, errors.New("owned runtime executable, libraries or version changed from the specification")
	}
	spec.Executable, spec.ModelStore = observed.Executable, observed.ModelStore
	return prepared(spec), nil
}

func prepared(spec Spec) Prepared {
	launch := sealJSON("fitr.autoruntime.launch.v1", struct {
		Spec   Spec
		Policy string
	}{spec, launchPolicy})
	profile := sealJSON("fitr.autoruntime.profile.v1", struct{ Executable, Libraries, Version, Launch string }{
		spec.ExecutableSHA256, spec.LibrariesSHA256, spec.RuntimeVersion, launch})
	p := Prepared{Spec: spec, ProfileSHA256: profile, LaunchConfigurationSHA256: launch}
	p.seal = sealJSON("fitr.autoruntime.prepared.v1", p)
	return p
}

func sealJSON(domain string, value any) string {
	data, _ := json.Marshal(value) // all callers pass closed, JSON-compatible types
	h := sha256.New()
	_, _ = h.Write([]byte(domain + "\x00"))
	_, _ = h.Write(data)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}
