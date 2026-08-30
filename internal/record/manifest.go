package record

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/eval"
)

const (
	LegacyRunManifestSchema = "fitr.run.manifest.v1"
	RunManifestSchema       = "fitr.run.manifest.v2"
	ExecutionDisabled       = "generated_code_disabled"
	ExecutionUnsafe         = "unsafe_unisolated_diagnostics"

	IdentityRuntimeDigest   = "runtime_digest"
	IdentityLocalFile       = "local_file_sha256"
	IdentityBindingRuntime  = "runtime_bound"
	IdentityBindingObserved = "observed_only"

	BackendProtocolOllama            = "fitr.backend.ollama.v1"
	BackendProtocolLlamaServerNative = "fitr.backend.llama-server-native.v1"
	BackendProtocolOpenAICompatible  = "fitr.backend.openai-compatible.v1"
)

var (
	sha256Digest = regexp.MustCompile(`(?i)^sha256:[0-9a-f]{64}$`)
	splitGGUF    = regexp.MustCompile(`(?i)^(.*)-([0-9]{5})-of-([0-9]{5})(\.gguf)$`)
)

const maxSplitGGUFShards = 4096

// ModelIdentity records what the serving runtime actually resolved. Requested
// is kept for auditability, but Resolved is authoritative for the result.
// ContentAddressed is true only when Value names the artifact bytes rather
// than a mutable file or runtime model registration.
type ModelIdentity struct {
	Requested        string `json:"requested"`
	Resolved         string `json:"resolved"`
	Backend          string `json:"backend"`
	Runtime          string `json:"runtime"`
	Kind             string `json:"kind"`
	Value            string `json:"value"`
	ContentAddressed bool   `json:"content_addressed"`
	Binding          string `json:"binding"`
	SizeBytes        int64  `json:"size_bytes,omitempty"`
}

// TaskPlan is the immutable denominator plan selected before measurement.
// It records what was scheduled so missing observations cannot silently
// shrink a denominator.
type TaskPlan struct {
	SpeedSamples     int  `json:"speed_samples"`
	Memory           bool `json:"memory"`
	CodeTrials       int  `json:"code_trials"`
	CheckTrialsLimit int  `json:"check_trials_limit"`
	// CheckPlanSHA256 seals the ordered task/family/need/origin/seed schedule
	// before inference begins. A count alone cannot detect replacing a hard
	// check with a duplicate easy one.
	CheckPlanSHA256 string `json:"check_plan_sha256,omitempty"`
	AdaptiveChecks  bool   `json:"adaptive_checks,omitempty"`
	Plumbing        bool   `json:"plumbing"`
	ToolTrials      int    `json:"tool_trials"`
	Withdrawal      bool   `json:"withdrawal"`
	RefusalTrials   int    `json:"refusal_trials"`
	// RefusalPlanSHA256 seals the exact canonical prompt-ID plan before
	// inference. A count alone cannot detect replacing a difficult prompt.
	RefusalPlanSHA256 string `json:"refusal_plan_sha256,omitempty"`
	AgenticTrials     int    `json:"agentic_trials"`
}

// RunProvenance identifies every mutable definition that can alter a verdict
// while the model and machine stay the same. The task and spec hashes come
// from eval.BuiltinHashes. Profile and scoring policy hashes are canonical
// JSON digests created by NewRunProvenance.
type RunProvenance struct {
	TaskSetSHA256       string `json:"task_set_sha256"`
	ProfileSHA256       string `json:"profile_sha256"`
	SpecSHA256          string `json:"spec_sha256"`
	ScoringPolicySHA256 string `json:"scoring_policy_sha256"`
	FitrVersion         string `json:"fitr_version"`
	SoftwareBuildSHA256 string `json:"software_build_sha256"`
	BackendProtocol     string `json:"backend_protocol"`
}

type SoftwareReceipt struct {
	FitrVersion         string
	SoftwareBuildSHA256 string
	BackendProtocol     string
}

// NewRunProvenance binds the embedded definition hashes to the exact selected
// profile and the caller's explicit scoring policy descriptor.
func NewRunProvenance(taskSetSHA256, specSHA256 string, profile any, scoringPolicy ScoringPolicy,
	software SoftwareReceipt) (RunProvenance, error) {
	profileHash, err := canonicalJSONDigest("fitr.profile.v1", profile)
	if err != nil {
		return RunProvenance{}, fmt.Errorf("hash profile: %w", err)
	}
	if err := scoringPolicy.Validate(); err != nil {
		return RunProvenance{}, fmt.Errorf("scoring policy: %w", err)
	}
	policyHash, err := canonicalJSONDigest("fitr.scoring-policy.v1", scoringPolicy)
	if err != nil {
		return RunProvenance{}, fmt.Errorf("hash scoring policy: %w", err)
	}
	p := RunProvenance{
		TaskSetSHA256: strings.ToLower(strings.TrimSpace(taskSetSHA256)),
		ProfileSHA256: profileHash, SpecSHA256: strings.ToLower(strings.TrimSpace(specSHA256)),
		ScoringPolicySHA256: policyHash,
		FitrVersion:         strings.TrimSpace(software.FitrVersion),
		SoftwareBuildSHA256: strings.ToLower(strings.TrimSpace(software.SoftwareBuildSHA256)),
		BackendProtocol:     strings.TrimSpace(software.BackendProtocol),
	}
	if err := p.Validate(); err != nil {
		return RunProvenance{}, err
	}
	return p, nil
}

func canonicalJSONDigest(domain string, value any) (string, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if string(b) == "null" {
		return "", errors.New("value is null")
	}
	h := sha256.New()
	_, _ = io.WriteString(h, domain+"\x00")
	_, _ = h.Write(b)
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func (p RunProvenance) Validate() error {
	for _, field := range []struct {
		name, value string
	}{
		{"task_set_sha256", p.TaskSetSHA256},
		{"profile_sha256", p.ProfileSHA256},
		{"spec_sha256", p.SpecSHA256},
		{"scoring_policy_sha256", p.ScoringPolicySHA256},
	} {
		if !sha256Digest.MatchString(field.value) {
			return fmt.Errorf("run provenance %s is not a SHA-256 token", field.name)
		}
	}
	if strings.TrimSpace(p.FitrVersion) == "" {
		return errors.New("run provenance is missing the fitr version")
	}
	if !sha256Digest.MatchString(p.SoftwareBuildSHA256) {
		return errors.New("run provenance software_build_sha256 is not a SHA-256 token")
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,127}$`).MatchString(p.BackendProtocol) {
		return errors.New("run provenance has an invalid backend protocol version")
	}
	return nil
}

func (p TaskPlan) Validate() error {
	if p.SpeedSamples < 0 || p.CodeTrials < 0 || p.CheckTrialsLimit < 0 ||
		p.ToolTrials < 0 || p.RefusalTrials < 0 || p.AgenticTrials < 0 {
		return errors.New("task plan counts cannot be negative")
	}
	if p.AdaptiveChecks && p.CheckTrialsLimit < 1 {
		return errors.New("adaptive checks require a positive trial limit")
	}
	if p.SpeedSamples+p.CodeTrials+p.CheckTrialsLimit+p.ToolTrials+
		p.RefusalTrials+p.AgenticTrials == 0 && !p.Memory && !p.Plumbing && !p.Withdrawal {
		return errors.New("task plan is empty")
	}
	return nil
}

// validateCurrentSeals applies only to the current result schema. Manifest v2
// was already published with schema-5 records before these plan digests
// existed, so putting this rule in RunManifest.validateFields would make those
// signed records unreadable.
func (p TaskPlan) validateCurrentSeals() error {
	if p.CheckTrialsLimit > 0 && !sha256Digest.MatchString(p.CheckPlanSHA256) {
		return errors.New("current run manifest fixed checks require a sealed check plan")
	}
	if p.CheckTrialsLimit == 0 && p.CheckPlanSHA256 != "" {
		return errors.New("current run manifest empty check plan cannot carry a check-plan digest")
	}
	if p.RefusalTrials > 0 && !sha256Digest.MatchString(p.RefusalPlanSHA256) {
		return errors.New("current run manifest refusal battery requires a sealed refusal plan")
	}
	if p.RefusalTrials == 0 && p.RefusalPlanSHA256 != "" {
		return errors.New("current run manifest empty refusal plan cannot carry a refusal-plan digest")
	}
	return nil
}

type checkPlanEntry struct {
	TaskID string `json:"task"`
	Family string `json:"family"`
	Need   string `json:"need"`
	Origin string `json:"origin"`
	Seed   uint64 `json:"seed"`
}

// FixedCheckPlanSHA256 seals the exact generated-check schedule selected
// before measurement. Order and multiplicity are intentional parts of the
// contract because both affect pairing and clustered denominators.
func FixedCheckPlanSHA256(checks []eval.CheckSpec, rounds int, seedSet string) (string, error) {
	if rounds < 0 {
		return "", errors.New("check-plan rounds cannot be negative")
	}
	entries := make([]checkPlanEntry, 0, len(checks)*rounds)
	for round := range rounds {
		for _, check := range checks {
			entries = append(entries, checkPlanEntry{
				TaskID: check.ID, Family: check.Family, Need: check.Need, Origin: check.Origin,
				Seed: eval.InstanceSeed(seedSet, check.ID, round),
			})
		}
	}
	return checkPlanDigest(entries)
}

// ObservedCheckPlanSHA256 hashes the plan identity carried by terminal check
// observations. It is exported for current-schema fixture builders; normal
// runs seal their plan from CheckSpec values before inference.
func ObservedCheckPlanSHA256(checks []eval.CheckOutcome) (string, error) {
	entries := make([]checkPlanEntry, 0, len(checks))
	for i, check := range checks {
		if strings.TrimSpace(check.TaskID) == "" || strings.TrimSpace(check.Family) == "" ||
			strings.TrimSpace(check.Need) == "" || strings.TrimSpace(check.Origin) == "" {
			return "", fmt.Errorf("check observation %d has incomplete plan identity", i)
		}
		entries = append(entries, checkPlanEntry{
			TaskID: check.TaskID, Family: check.Family, Need: check.Need, Origin: check.Origin, Seed: check.Seed,
		})
	}
	return checkPlanDigest(entries)
}

func checkPlanDigest(entries []checkPlanEntry) (string, error) {
	return canonicalJSONDigest("fitr.check-plan.v1", entries)
}

// FixedRefusalPlanSHA256 seals the predeclared refusal prompt-ID plan before
// inference. IDs must be unique because every prompt is scheduled exactly once.
func FixedRefusalPlanSHA256(ids []string) (string, error) {
	seen := make(map[string]bool, len(ids))
	ordered := make([]string, 0, len(ids))
	for i, id := range ids {
		if strings.TrimSpace(id) == "" {
			return "", fmt.Errorf("refusal plan ID %d is empty", i)
		}
		if seen[id] {
			return "", fmt.Errorf("refusal plan ID %q is duplicated", id)
		}
		seen[id] = true
		ordered = append(ordered, id)
	}
	return canonicalJSONDigest("fitr.refusal-plan.v1", ordered)
}

// ObservedRefusalPlanSHA256 reconstructs the protocol order from terminal
// refusal observations. It is exported for current-schema fixture builders;
// normal runs seal the plan from RefusalSpec before inference.
func ObservedRefusalPlanSHA256(results map[string]eval.RefusalVerdict) (string, error) {
	ids := make([]string, 0, len(results))
	for id := range results {
		if strings.TrimSpace(id) == "" {
			return "", errors.New("refusal observation has an empty prompt ID")
		}
		ids = append(ids, id)
	}
	return FixedRefusalPlanSHA256(eval.OrderedRefusalIDs(ids))
}

// ScoringPolicy is the stable, hashable description of verdict semantics that
// are not supplied by the selected profile. Any change to these semantics must
// change Schema so otherwise identical runs do not claim reproducibility.
type ScoringPolicy struct {
	Schema              string `json:"schema"`
	RateInterval        string `json:"rate_interval"`
	BoundaryVerdict     string `json:"boundary_verdict"`
	ScorableOutcomes    string `json:"scorable_outcomes"`
	UnisolatedExecution string `json:"unisolated_execution"`
	Contamination       string `json:"contamination"`
	MissingGate         string `json:"missing_gate"`
}

func CurrentScoringPolicy() ScoringPolicy {
	return ScoringPolicy{
		Schema:          "fitr.scoring.policy.v3",
		RateInterval:    "clustered_wilson_95_fixed",
		BoundaryVerdict: "inconclusive", ScorableOutcomes: "pass_fail_only",
		UnisolatedExecution: "excluded", Contamination: "exclude_measured_claims",
		MissingGate: "skip",
	}
}

func (p ScoringPolicy) Validate() error {
	for _, field := range []struct {
		name, value string
	}{
		{"schema", p.Schema}, {"rate_interval", p.RateInterval},
		{"boundary_verdict", p.BoundaryVerdict}, {"scorable_outcomes", p.ScorableOutcomes},
		{"unisolated_execution", p.UnisolatedExecution}, {"contamination", p.Contamination},
		{"missing_gate", p.MissingGate},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("scoring policy is missing %s", field.name)
		}
	}
	return nil
}

// NewModelIdentity creates the strongest identity available from a runtime
// inspection. Runtime digests win. A local path is represented by a private
// content hash so the manifest does not disclose it. A mutable runtime model
// name alone is rejected because it cannot identify the evaluated artifact.
func NewModelIdentity(requested, resolved, backend, runtimeVersion, digest, localPath string, sizeBytes int64) (ModelIdentity, error) {
	i := ModelIdentity{
		Requested: strings.TrimSpace(requested),
		Resolved:  strings.TrimSpace(resolved),
		Backend:   strings.TrimSpace(backend),
		Runtime:   strings.TrimSpace(runtimeVersion),
		SizeBytes: sizeBytes,
	}
	if digest = strings.ToLower(strings.TrimSpace(digest)); digest != "" {
		if !strings.Contains(digest, ":") && len(digest) == 64 {
			digest = "sha256:" + digest
		}
		if !sha256Digest.MatchString(digest) {
			return ModelIdentity{}, fmt.Errorf("unsupported model digest %q", digest)
		}
		i.Kind, i.Value, i.ContentAddressed = IdentityRuntimeDigest, digest, true
		i.Binding = IdentityBindingRuntime
	} else if strings.TrimSpace(localPath) != "" {
		value, observedSize, err := localFileDigest(localPath)
		if err != nil {
			return ModelIdentity{}, fmt.Errorf("hash local model artifact: %w", err)
		}
		i.Kind, i.Value, i.ContentAddressed = IdentityLocalFile, value, true
		i.Binding = IdentityBindingObserved
		i.SizeBytes = observedSize
	} else {
		return ModelIdentity{}, errors.New("runtime did not provide an immutable model digest or readable local artifact")
	}
	if err := i.Validate(); err != nil {
		return ModelIdentity{}, err
	}
	return i, nil
}

func localFileDigest(path string) (string, int64, error) {
	paths, split, err := modelArtifactPaths(path)
	if err != nil {
		return "", 0, err
	}
	before := make([]os.FileInfo, len(paths))
	var total int64
	for i, artifactPath := range paths {
		info, statErr := os.Stat(artifactPath)
		if statErr != nil {
			return "", 0, fmt.Errorf("artifact shard %d of %d: %w", i+1, len(paths), statErr)
		}
		if !info.Mode().IsRegular() {
			return "", 0, fmt.Errorf("artifact shard %d of %d is not a regular file", i+1, len(paths))
		}
		if info.Size() < 0 || total > math.MaxInt64-info.Size() {
			return "", 0, errors.New("model artifact size overflows int64")
		}
		before[i] = info
		total += info.Size()
	}

	h := sha256.New()
	if split {
		_, _ = io.WriteString(h, "fitr.split.gguf.v1\x00")
		_ = binary.Write(h, binary.BigEndian, uint32(len(paths)))
	}
	for i, artifactPath := range paths {
		if split {
			_ = binary.Write(h, binary.BigEndian, uint32(i+1))
			_ = binary.Write(h, binary.BigEndian, uint64(before[i].Size()))
		}
		f, openErr := os.Open(artifactPath)
		if openErr != nil {
			return "", 0, fmt.Errorf("open artifact shard %d of %d: %w", i+1, len(paths), openErr)
		}
		n, copyErr := io.Copy(h, f)
		closeErr := f.Close()
		if copyErr != nil {
			return "", 0, fmt.Errorf("hash artifact shard %d of %d: %w", i+1, len(paths), copyErr)
		}
		if closeErr != nil {
			return "", 0, fmt.Errorf("close artifact shard %d of %d: %w", i+1, len(paths), closeErr)
		}
		if n != before[i].Size() {
			return "", 0, fmt.Errorf("artifact shard %d of %d changed while it was being hashed", i+1, len(paths))
		}
	}
	for i, artifactPath := range paths {
		after, statErr := os.Stat(artifactPath)
		if statErr != nil {
			return "", 0, fmt.Errorf("recheck artifact shard %d of %d: %w", i+1, len(paths), statErr)
		}
		if before[i].Size() != after.Size() || !before[i].ModTime().Equal(after.ModTime()) {
			return "", 0, fmt.Errorf("artifact shard %d of %d changed while the model was being hashed", i+1, len(paths))
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), total, nil
}

func modelArtifactPaths(path string) ([]string, bool, error) {
	clean := filepath.Clean(path)
	match := splitGGUF.FindStringSubmatch(filepath.Base(clean))
	if match == nil {
		return []string{clean}, false, nil
	}
	part, err := strconv.Atoi(match[2])
	if err != nil || part < 1 {
		return nil, false, errors.New("split GGUF has an invalid shard number")
	}
	total, err := strconv.Atoi(match[3])
	if err != nil || total < 1 || part > total {
		return nil, false, errors.New("split GGUF has an invalid shard count")
	}
	if total > maxSplitGGUFShards {
		return nil, false, fmt.Errorf("split GGUF declares %d shards; limit is %d", total, maxSplitGGUFShards)
	}
	dir := filepath.Dir(clean)
	paths := make([]string, total)
	for i := 1; i <= total; i++ {
		name := fmt.Sprintf("%s-%05d-of-%05d%s", match[1], i, total, match[4])
		paths[i-1] = filepath.Join(dir, name)
	}
	return paths, true, nil
}

func (i ModelIdentity) Validate() error {
	switch {
	case i.Requested == "":
		return errors.New("model identity is missing the requested model")
	case i.Resolved == "":
		return errors.New("model identity is missing the resolved model")
	case i.Backend == "":
		return errors.New("model identity is missing the backend")
	case i.Runtime == "":
		return errors.New("model identity is missing the runtime version")
	case !sha256Digest.MatchString(i.Value):
		return errors.New("model identity value is not a SHA-256 token")
	case i.SizeBytes < 0:
		return errors.New("model identity has a negative size")
	}
	switch i.Kind {
	case IdentityRuntimeDigest:
		if !i.ContentAddressed {
			return errors.New("model artifact identity must be content-addressed")
		}
		if i.Binding != "" && i.Binding != IdentityBindingRuntime {
			return errors.New("runtime digest identity is not runtime-bound")
		}
	case IdentityLocalFile:
		if !i.ContentAddressed {
			return errors.New("model artifact identity must be content-addressed")
		}
		if i.Binding != "" && i.Binding != IdentityBindingObserved {
			return errors.New("local file identity must be marked observed-only")
		}
	default:
		return fmt.Errorf("unknown model identity kind %q", i.Kind)
	}
	return nil
}

// RankingIssue reports whether the identity proves which artifact the runtime
// served. A local file hash is useful provenance, but observing bytes at a path
// does not bind those bytes to an already-running server process.
func (i ModelIdentity) RankingIssue() string {
	if err := i.Validate(); err != nil {
		return err.Error()
	}
	if i.Binding != IdentityBindingRuntime {
		return "model artifact was observed locally but was not bound to the serving runtime"
	}
	return ""
}

// RuntimeBoundDigest is the content digest the serving runtime bound to this
// measurement. Observed-only local file hashes are not returned: they do not
// prove which bytes the process loaded.
func (i ModelIdentity) RuntimeBoundDigest() string {
	if i.Kind != IdentityRuntimeDigest || i.RankingIssue() != "" {
		return ""
	}
	return i.Value
}

// RunManifest is the sealed, run-defining state captured before evaluation.
// It intentionally excludes measurements and verdicts. ManifestSHA256 detects
// any later mutation of the identity or configuration.
type RunManifest struct {
	Schema                  string                `json:"schema"`
	ManifestSHA256          string                `json:"manifest_sha256"`
	RunID                   string                `json:"run_id"`
	StartedAt               string                `json:"started_at"`
	Model                   ModelIdentity         `json:"model"`
	DeviceKey               string                `json:"device_key"`
	DeviceFingerprintSHA256 string                `json:"device_fingerprint_sha256,omitempty"`
	CompletionPublicKey     string                `json:"completion_public_key,omitempty"`
	Profile                 string                `json:"profile"`
	Level                   string                `json:"level"`
	ExecutionPolicy         string                `json:"execution_policy"`
	Executor                *eval.ExecutorReceipt `json:"executor,omitempty"`
	TaskPlan                TaskPlan              `json:"task_plan"`
	SeedSet                 string                `json:"seedset"`
	Repeats                 int                   `json:"repeats"`
	NumCtx                  int                   `json:"num_ctx"`
	Provenance              *RunProvenance        `json:"provenance,omitempty"`
}

func NewRunID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate run ID: %w", err)
	}
	return "run_" + hex.EncodeToString(raw[:]), nil
}

// AttachManifest seals the record's current run-defining state. Calling it a
// second time is rejected so an evaluation cannot silently replace its start
// receipt after measurements have begun.
func (r *Record) AttachManifest(identity ModelIdentity, provenance ...RunProvenance) error {
	return r.attachManifest(identity, nil, provenance...)
}

// AttachManifestWithExecutor seals an unsafe run with the exact interpreter
// preflight receipt. The receipt identifies unisolated execution and does not
// claim that the interpreter or generated code was sandboxed.
func (r *Record) AttachManifestWithExecutor(identity ModelIdentity, executor eval.ExecutorReceipt,
	provenance ...RunProvenance) error {
	dup := executor
	return r.attachManifest(identity, &dup, provenance...)
}

func (r *Record) attachManifest(identity ModelIdentity, executor *eval.ExecutorReceipt,
	provenance ...RunProvenance) error {
	if r == nil {
		return errors.New("cannot attach a manifest to a nil record")
	}
	if r.Manifest != nil {
		return errors.New("run manifest is already attached")
	}
	if len(provenance) > 1 {
		return errors.New("run manifest accepts at most one provenance receipt")
	}
	if r.SchemaVersion >= EvidenceSchemaVersion {
		if err := r.TaskPlan.validateCurrentSeals(); err != nil {
			return err
		}
	}
	if !validRunID.MatchString(r.RunID) {
		id, err := NewRunID()
		if err != nil {
			return err
		}
		r.RunID = id
	}
	schema := LegacyRunManifestSchema
	var receipt *RunProvenance
	if len(provenance) == 1 {
		schema = RunManifestSchema
		dup := provenance[0]
		receipt = &dup
	}
	m := &RunManifest{
		Schema: schema, RunID: r.RunID, StartedAt: r.StartedAt,
		Model: identity, DeviceKey: r.DeviceKey, Profile: r.Profile,
		Level: r.Level, ExecutionPolicy: r.ExecutionPolicy, TaskPlan: r.TaskPlan,
		Executor: executor,
		SeedSet:  r.SeedSet, Repeats: r.Repeats,
		NumCtx: r.ContextSize(), Provenance: receipt,
	}
	if schema == RunManifestSchema && r.DeviceV2 != nil {
		key, err := r.DeviceV2.EvidenceKey()
		if err != nil {
			return fmt.Errorf("fingerprint v2: %w", err)
		}
		m.DeviceFingerprintSHA256 = key
	}
	if schema == RunManifestSchema {
		publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return fmt.Errorf("generate completion signing key: %w", err)
		}
		m.CompletionPublicKey = base64.RawStdEncoding.EncodeToString(publicKey)
		r.completionPrivateKey = privateKey
	}
	if err := m.Seal(); err != nil {
		return err
	}
	r.Manifest = m
	return nil
}

func (m *RunManifest) Seal() error {
	if m == nil {
		return errors.New("nil run manifest")
	}
	m.ManifestSHA256 = ""
	if err := m.validateFields(); err != nil {
		return err
	}
	sum, err := m.digest()
	if err != nil {
		return err
	}
	m.ManifestSHA256 = sum
	return nil
}

func (m RunManifest) Verify() error {
	if err := m.validateFields(); err != nil {
		return err
	}
	want := strings.ToLower(strings.TrimSpace(m.ManifestSHA256))
	if !sha256Digest.MatchString(want) {
		return errors.New("run manifest is not sealed with SHA-256")
	}
	m.ManifestSHA256 = ""
	got, err := m.digest()
	if err != nil {
		return err
	}
	if got != want {
		return errors.New("run manifest does not match its sealed content")
	}
	return nil
}

func (m RunManifest) validateFields() error {
	if err := m.validateSchemaFields(); err != nil {
		return err
	}
	if err := m.validateRequiredFields(); err != nil {
		return err
	}
	if err := m.validateExecutionFields(); err != nil {
		return err
	}
	if err := m.validatePlanFields(); err != nil {
		return err
	}
	if err := m.validateOptionalReceipts(); err != nil {
		return err
	}
	return m.Model.Validate()
}

func (m RunManifest) validateSchemaFields() error {
	switch {
	case m.Schema != LegacyRunManifestSchema && m.Schema != RunManifestSchema:
		return fmt.Errorf("unsupported run manifest schema %q", m.Schema)
	case m.Schema == LegacyRunManifestSchema && m.Provenance != nil:
		return errors.New("legacy run manifest cannot contain release-B provenance")
	case m.Schema == RunManifestSchema && m.Provenance == nil:
		return errors.New("run manifest is missing reproducibility provenance")
	case m.Schema == RunManifestSchema && !sha256Digest.MatchString(m.DeviceFingerprintSHA256):
		return errors.New("run manifest is missing a sealed fingerprint v2")
	case m.Schema == RunManifestSchema && !validCompletionPublicKey(m.CompletionPublicKey):
		return errors.New("run manifest is missing a valid completion public key")
	case m.Schema == RunManifestSchema && m.Model.Binding == "":
		return errors.New("run manifest model identity is missing runtime binding provenance")
	case m.Schema == LegacyRunManifestSchema && m.CompletionPublicKey != "":
		return errors.New("legacy run manifest cannot contain a completion public key")
	case m.Schema == LegacyRunManifestSchema && m.Executor != nil:
		return errors.New("legacy run manifest cannot contain an executor receipt")
	}
	return nil
}

func (m RunManifest) validateRequiredFields() error {
	switch {
	case !validRunID.MatchString(m.RunID):
		return errors.New("run manifest has an invalid run ID")
	case strings.TrimSpace(m.StartedAt) == "":
		return errors.New("run manifest is missing started_at")
	case strings.TrimSpace(m.DeviceKey) == "":
		return errors.New("run manifest is missing the device key")
	case strings.TrimSpace(m.Profile) == "":
		return errors.New("run manifest is missing the profile")
	case strings.TrimSpace(m.Level) == "":
		return errors.New("run manifest is missing the level")
	}
	return nil
}

func (m RunManifest) validateExecutionFields() error {
	switch {
	case m.ExecutionPolicy != ExecutionDisabled && m.ExecutionPolicy != ExecutionUnsafe:
		return fmt.Errorf("unknown execution policy %q", m.ExecutionPolicy)
	case m.Schema == RunManifestSchema && m.ExecutionPolicy == ExecutionUnsafe && m.Executor == nil:
		return errors.New("unsafe run manifest is missing its executor receipt")
	case m.ExecutionPolicy == ExecutionDisabled && m.Executor != nil:
		return errors.New("disabled execution manifest cannot contain an executor receipt")
	}
	return nil
}

func (m RunManifest) validatePlanFields() error {
	switch {
	case strings.TrimSpace(m.SeedSet) == "":
		return errors.New("run manifest is missing the seed set")
	case m.Repeats < 1:
		return errors.New("run manifest repeats must be at least 1")
	case m.NumCtx < 1:
		return errors.New("run manifest context must be at least 1")
	}
	if _, err := time.Parse(time.RFC3339, m.StartedAt); err != nil {
		return fmt.Errorf("run manifest started_at: %w", err)
	}
	if err := m.TaskPlan.Validate(); err != nil {
		return fmt.Errorf("run manifest task plan: %w", err)
	}
	return nil
}

func (m RunManifest) validateOptionalReceipts() error {
	if m.Provenance != nil {
		if err := m.Provenance.Validate(); err != nil {
			return err
		}
		if !protocolMatchesBackend(m.Provenance.BackendProtocol, m.Model.Backend) {
			return fmt.Errorf("backend protocol %q does not match resolved backend %q",
				m.Provenance.BackendProtocol, m.Model.Backend)
		}
	}
	if m.Executor != nil {
		if err := m.Executor.Validate(); err != nil {
			return fmt.Errorf("run manifest executor: %w", err)
		}
	}
	return nil
}

func validCompletionPublicKey(encoded string) bool {
	key, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(encoded))
	return err == nil && len(key) == ed25519.PublicKeySize
}

// CompatibilityError reports drift in the definitions that determine a
// verdict or measurement semantics. The exact fitr software build remains
// available for replay without making every rebuild incomparable.
func (p RunProvenance) CompatibilityError(other RunProvenance) error {
	var mismatches []string
	for _, field := range []struct {
		name  string
		left  string
		right string
	}{
		{"task set", p.TaskSetSHA256, other.TaskSetSHA256},
		{"effective spec", p.SpecSHA256, other.SpecSHA256},
		{"profile", p.ProfileSHA256, other.ProfileSHA256},
		{"scoring policy", p.ScoringPolicySHA256, other.ScoringPolicySHA256},
		{"backend protocol", p.BackendProtocol, other.BackendProtocol},
	} {
		if field.left != field.right {
			mismatches = append(mismatches, field.name)
		}
	}
	if len(mismatches) != 0 {
		return fmt.Errorf("incompatible run provenance: %s differ", strings.Join(mismatches, ", "))
	}
	return nil
}

// BackendProtocol is the versioned wire-protocol receipt for a serving
// backend. llama-server's native completion protocol is tagged
// llama-server-native; a substring match on "llama-server" would reject it.
func BackendProtocol(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "ollama":
		return BackendProtocolOllama
	case "llama-server", "llamaserver":
		return BackendProtocolLlamaServerNative
	case "openai", "openai-compatible":
		return BackendProtocolOpenAICompatible
	default:
		name = strings.ToLower(strings.TrimSpace(name))
		name = strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
				return r
			}
			return '-'
		}, name)
		name = strings.Trim(name, "._-")
		if name == "" {
			name = "unknown"
		}
		return "fitr.backend." + name + ".v1"
	}
}

func protocolMatchesBackend(protocol, backend string) bool {
	if strings.TrimSpace(protocol) == "" || strings.TrimSpace(backend) == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(protocol), BackendProtocol(backend))
}

func (m RunManifest) digest() (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("encode run manifest: %w", err)
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// ValidateManifest verifies the seal and the duplicated record fields. These
// checks make the manifest immutable within a persisted result while leaving
// legacy records without a manifest readable.
func (r *Record) ValidateManifest() error {
	if r == nil || r.Manifest == nil {
		return nil
	}
	if err := r.Manifest.Verify(); err != nil {
		return err
	}
	if r.SchemaVersion >= EvidenceSchemaVersion {
		if err := r.TaskPlan.validateCurrentSeals(); err != nil {
			return err
		}
	}
	m := r.Manifest
	switch {
	case r.RunID != m.RunID:
		return errors.New("record run ID differs from its manifest")
	case r.StartedAt != m.StartedAt:
		return errors.New("record start time differs from its manifest")
	case r.Model != m.Model.Resolved:
		return errors.New("record model differs from its resolved manifest identity")
	case r.DeviceKey != m.DeviceKey:
		return errors.New("record device key differs from its manifest")
	case r.Profile != m.Profile:
		return errors.New("record profile differs from its manifest")
	case r.Level != m.Level:
		return errors.New("record level differs from its manifest")
	case r.ExecutionPolicy != m.ExecutionPolicy:
		return errors.New("record execution policy differs from its manifest")
	case !reflect.DeepEqual(r.TaskPlan, m.TaskPlan):
		return errors.New("record task plan differs from its manifest")
	case r.SeedSet != m.SeedSet:
		return errors.New("record seed set differs from its manifest")
	case r.Repeats != m.Repeats:
		return errors.New("record repeats differ from its manifest")
	case r.ContextSize() != m.NumCtx:
		return errors.New("record context differs from its manifest")
	}
	if m.Schema == RunManifestSchema {
		if r.DeviceV2 == nil {
			return errors.New("record is missing fingerprint v2")
		}
		if !reflect.DeepEqual(r.Device, r.DeviceV2.Device) {
			return errors.New("record legacy device fields differ from fingerprint v2")
		}
		key, err := r.DeviceV2.EvidenceKey()
		if err != nil {
			return fmt.Errorf("record fingerprint v2: %w", err)
		}
		if key != m.DeviceFingerprintSHA256 {
			return errors.New("record fingerprint v2 differs from its manifest")
		}
	}
	return nil
}
