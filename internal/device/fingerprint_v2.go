package device

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

const FingerprintSchemaV2 = "fitr.device.fingerprint.v2"

type ContextEvidenceSource string

const (
	ContextSourceRuntimeReport ContextEvidenceSource = "runtime_report"
	ContextSourceBoundaryProbe ContextEvidenceSource = "boundary_probe"
	ContextSourceGeneration    ContextEvidenceSource = "generation_receipt"
)

type ContextVerificationState string

const (
	ContextUnverified ContextVerificationState = "unverified"
	ContextLowerBound ContextVerificationState = "observed_lower_bound"
	ContextProbeShort ContextVerificationState = "probe_below_minimum"
	ContextVerified   ContextVerificationState = "verified"
	ContextAdjusted   ContextVerificationState = "adjusted"
)

// ContextProbe is a request-specific receipt from a generation probe.
// PromptTokens and CachedTokens are observations reported by the runtime.
// MinimumExpectedTokens is the probe's predeclared acceptance threshold, not
// an estimate of the prompt's token count. A probe proves only a lower bound
// unless a boundary search separately establishes EffectiveTokens.
type ContextProbe struct {
	PromptTokens          int                   `json:"prompt_tokens"`
	CachedTokens          int                   `json:"cached_tokens,omitempty"`
	MinimumExpectedTokens int                   `json:"minimum_expected_tokens"`
	Source                ContextEvidenceSource `json:"source"`
}

func (p ContextProbe) ServedTokens() int {
	return p.PromptTokens + p.CachedTokens
}

func (p ContextProbe) MeetsMinimum() bool {
	return p.ServedTokens() >= p.MinimumExpectedTokens
}

func (p ContextProbe) Validate() error {
	switch {
	case p.Source != ContextSourceGeneration:
		return fmt.Errorf("context probe source must be %q", ContextSourceGeneration)
	case p.PromptTokens < 0:
		return errors.New("context probe prompt tokens cannot be negative")
	case p.CachedTokens < 0:
		return errors.New("context probe cached tokens cannot be negative")
	case p.MinimumExpectedTokens < 1:
		return errors.New("context probe minimum expected tokens must be at least 1")
	case p.PromptTokens > int(^uint(0)>>1)-p.CachedTokens:
		return errors.New("context probe served token count overflows int")
	}
	return nil
}

// ContextVerification distinguishes the context requested by fitr from the
// context the runtime actually allocated for that request. EffectiveTokens is
// nil when the runtime does not expose this fact. Probe evidence is preserved
// separately because one successful prompt receipt does not establish the
// full context-window boundary.
type ContextVerification struct {
	RequestedTokens int                   `json:"requested_tokens"`
	EffectiveTokens *int                  `json:"effective_tokens,omitempty"`
	EffectiveSource ContextEvidenceSource `json:"effective_source,omitempty"`
	Probe           *ContextProbe         `json:"probe,omitempty"`
}

func (c ContextVerification) Validate() error {
	if c.RequestedTokens < 1 {
		return errors.New("requested context must be at least 1 token")
	}
	if c.EffectiveTokens == nil {
		if c.EffectiveSource != "" {
			return errors.New("effective context source is present without an effective context")
		}
	} else {
		if *c.EffectiveTokens < 1 {
			return errors.New("effective context must be at least 1 token")
		}
		switch c.EffectiveSource {
		case ContextSourceRuntimeReport, ContextSourceBoundaryProbe:
		default:
			return errors.New("effective context requires a runtime report or boundary probe")
		}
	}
	if c.Probe != nil {
		if err := c.Probe.Validate(); err != nil {
			return err
		}
		if c.EffectiveTokens != nil && c.Probe.ServedTokens() > *c.EffectiveTokens {
			return errors.New("context probe receipt exceeds the observed effective context")
		}
	}
	return nil
}

func (c ContextVerification) State() ContextVerificationState {
	if c.Probe != nil && !c.Probe.MeetsMinimum() {
		return ContextProbeShort
	}
	if c.EffectiveTokens != nil {
		if *c.EffectiveTokens == c.RequestedTokens {
			return ContextVerified
		}
		return ContextAdjusted
	}
	if c.Probe != nil {
		return ContextLowerBound
	}
	return ContextUnverified
}

func (c ContextVerification) EffectiveKnown() bool {
	return c.EffectiveTokens != nil
}

// FingerprintV2 is an additive envelope around the persisted legacy
// fingerprint. Keeping the original value nested preserves its JSON and Key
// contracts while v2 can state the context evidence required for comparison.
type FingerprintV2 struct {
	Schema  string              `json:"schema"`
	Device  Fingerprint         `json:"device"`
	Context ContextVerification `json:"context"`
}

func NewFingerprintV2(fp Fingerprint, context ContextVerification) (FingerprintV2, error) {
	v2 := FingerprintV2{
		Schema:  FingerprintSchemaV2,
		Device:  cloneFingerprint(fp),
		Context: cloneContextVerification(context),
	}
	if err := v2.Validate(); err != nil {
		return FingerprintV2{}, err
	}
	return v2, nil
}

func (f FingerprintV2) Validate() error {
	if f.Schema != FingerprintSchemaV2 {
		return fmt.Errorf("unsupported fingerprint schema %q", f.Schema)
	}
	switch {
	case strings.TrimSpace(f.Device.Host) == "":
		return errors.New("fingerprint is missing host")
	case strings.TrimSpace(f.Device.OS) == "":
		return errors.New("fingerprint is missing OS")
	case strings.TrimSpace(f.Device.CPU) == "":
		return errors.New("fingerprint is missing CPU")
	case strings.TrimSpace(f.Device.GPU) == "":
		return errors.New("fingerprint is missing GPU")
	case strings.TrimSpace(f.Device.Runtime) == "":
		return errors.New("fingerprint is missing runtime")
	case strings.TrimSpace(f.Device.InferenceDevice) == "":
		return errors.New("fingerprint is missing inference device")
	case f.Device.Config == nil:
		return errors.New("fingerprint is missing resolved configuration")
	case invalidCapacity(f.Device.RAMGb):
		return errors.New("fingerprint RAM must be a finite nonnegative value")
	case invalidCapacity(f.Device.VRAMGb):
		return errors.New("fingerprint VRAM must be a finite nonnegative value")
	case f.Device.VRAMGb <= 0 && f.Device.VRAMSource != "":
		return errors.New("fingerprint VRAM source is present without a measured value")
	case f.Device.VRAMGb > 0 && strings.TrimSpace(f.Device.VRAMSource) == "":
		return errors.New("fingerprint measured VRAM is missing its source")
	}
	return f.Context.Validate()
}

func invalidCapacity(v float64) bool {
	return v < 0 || math.IsNaN(v) || math.IsInf(v, 0)
}

// LegacyKey returns the original delimiter-based key unchanged. It exists for
// loading and grouping schema-v1 results only.
func (f FingerprintV2) LegacyKey() string {
	return f.Device.Key()
}

// EvidenceKey seals the complete v2 observation, including facts that do not
// determine comparability. It is available even when effective context is
// unknown so an unrankable run can still carry an immutable environment
// receipt.
func (f FingerprintV2) EvidenceKey() (string, error) {
	if err := f.Validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(f)
	if err != nil {
		return "", fmt.Errorf("encode fingerprint evidence: %w", err)
	}
	h := sha256.New()
	_, _ = h.Write([]byte("fitr.device.fingerprint.evidence.v2\x00"))
	_, _ = h.Write(b)
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// ComparabilityKey returns a collision-resistant v2 key only when effective
// context is known. Two unknown contexts must not become comparable merely
// because both were unavailable.
func (f FingerprintV2) ComparabilityKey() (string, error) {
	if err := f.Validate(); err != nil {
		return "", err
	}
	if f.Context.EffectiveTokens == nil {
		return "", errors.New("effective context is unverified")
	}
	if f.Context.Probe != nil && !f.Context.Probe.MeetsMinimum() {
		return "", errors.New("context probe did not meet its minimum served-token receipt")
	}
	material := fingerprintKeyMaterial{
		Schema: f.Schema,
		Host:   f.Device.Host, OS: f.Device.OS, CPU: f.Device.CPU,
		RAMGb: f.Device.RAMGb, GPU: f.Device.GPU,
		GPUDriver: f.Device.GPUDriver, GPUDriverDate: f.Device.GPUDriverDate,
		GPUBackend: f.Device.GPUBackend, VRAMGb: f.Device.VRAMGb,
		Runtime: f.Device.Runtime, InferenceDevice: f.Device.InferenceDevice,
		Config:          sortedNonemptyConfig(f.Device.Config),
		RequestedTokens: f.Context.RequestedTokens,
		EffectiveTokens: *f.Context.EffectiveTokens,
		EffectiveSource: f.Context.EffectiveSource,
	}
	b, err := json.Marshal(material)
	if err != nil {
		return "", fmt.Errorf("encode fingerprint key: %w", err)
	}
	sum := sha256.Sum256(b)
	return "fp2:" + hex.EncodeToString(sum[:]), nil
}

type configEntry struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type fingerprintKeyMaterial struct {
	Schema          string                `json:"schema"`
	Host            string                `json:"host"`
	OS              string                `json:"os"`
	CPU             string                `json:"cpu"`
	RAMGb           float64               `json:"ram_gb"`
	GPU             string                `json:"gpu"`
	GPUDriver       string                `json:"gpu_driver"`
	GPUDriverDate   string                `json:"gpu_driver_date"`
	GPUBackend      string                `json:"gpu_backend"`
	VRAMGb          float64               `json:"vram_gb"`
	Runtime         string                `json:"runtime"`
	InferenceDevice string                `json:"inference_device"`
	Config          []configEntry         `json:"config"`
	RequestedTokens int                   `json:"requested_tokens"`
	EffectiveTokens int                   `json:"effective_tokens"`
	EffectiveSource ContextEvidenceSource `json:"effective_source"`
}

func sortedNonemptyConfig(config map[string]string) []configEntry {
	names := make([]string, 0, len(config))
	for name, value := range config {
		if value != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := make([]configEntry, 0, len(names))
	for _, name := range names {
		out = append(out, configEntry{Name: name, Value: config[name]})
	}
	return out
}

func cloneFingerprint(fp Fingerprint) Fingerprint {
	snapshot := fp
	if fp.Config != nil {
		snapshot.Config = make(map[string]string, len(fp.Config))
		for name, value := range fp.Config {
			snapshot.Config[name] = value
		}
	}
	return snapshot
}

func cloneContextVerification(context ContextVerification) ContextVerification {
	snapshot := context
	if context.EffectiveTokens != nil {
		effective := *context.EffectiveTokens
		snapshot.EffectiveTokens = &effective
	}
	if context.Probe != nil {
		probe := *context.Probe
		snapshot.Probe = &probe
	}
	return snapshot
}
