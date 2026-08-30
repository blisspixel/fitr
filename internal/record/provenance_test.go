package record

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
)

func testRunProvenance(t *testing.T) RunProvenance {
	t.Helper()
	hashes, err := eval.BuiltinHashes()
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewRunProvenance(hashes.TaskSetSHA256, hashes.SpecSHA256,
		map[string]any{"name": "default", "gates": map[string]any{"quality": 0.75}},
		CurrentScoringPolicy(), testSoftwareReceipt())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func testSoftwareReceipt() SoftwareReceipt {
	return SoftwareReceipt{
		FitrVersion: "0.4.0", SoftwareBuildSHA256: testArtifactDigest,
		BackendProtocol: "fitr.backend.ollama.v1",
	}
}

func addTestFingerprintV2(t *testing.T, r *Record) {
	t.Helper()
	r.Device = device.Fingerprint{
		Host: "host", OS: "linux", CPU: "cpu", GPU: "gpu", Runtime: "runtime",
		InferenceDevice: "GPU 100%", Config: map[string]string{},
	}
	effective := r.ContextSize()
	v2, err := device.NewFingerprintV2(r.Device, device.ContextVerification{
		RequestedTokens: effective, EffectiveTokens: &effective,
		EffectiveSource: device.ContextSourceRuntimeReport,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.DeviceV2 = &v2
	r.DeviceKey, err = v2.ComparabilityKey()
	if err != nil {
		t.Fatal(err)
	}
}

func TestReleaseBManifestSealsAllPolicyHashes(t *testing.T) {
	r := manifestRecord("model", "2026-08-21T12:00:00Z")
	addTestFingerprintV2(t, r)
	provenance := testRunProvenance(t)
	if err := r.AttachManifest(digestIdentity(t, "model", "model"), provenance); err != nil {
		t.Fatal(err)
	}
	if r.Manifest.Schema != RunManifestSchema || r.Manifest.Provenance == nil || *r.Manifest.Provenance != provenance {
		t.Fatalf("release-B manifest = %+v", r.Manifest)
	}
	if provenance.FitrVersion == "" || provenance.SoftwareBuildSHA256 == "" || provenance.BackendProtocol == "" ||
		r.Manifest.DeviceFingerprintSHA256 == "" {
		t.Fatalf("release-B receipts are incomplete: manifest=%+v provenance=%+v", r.Manifest, provenance)
	}
	if err := r.ValidateManifest(); err != nil {
		t.Fatal(err)
	}
	old := r.Manifest.Provenance.ScoringPolicySHA256
	replacement := "0"
	if strings.HasSuffix(old, replacement) {
		replacement = "1"
	}
	r.Manifest.Provenance.ScoringPolicySHA256 = old[:len(old)-1] + replacement
	if err := r.ValidateManifest(); err == nil {
		t.Fatal("a mutated scoring policy hash kept a valid seal")
	}
}

func TestUnsafeReleaseBManifestRequiresAndSealsExecutorReceipt(t *testing.T) {
	executor := eval.ExecutorReceipt{
		Kind: "python", Path: filepath.Join(t.TempDir(), "python"), Version: "Python 3.12.7",
		SHA256: "sha256:" + strings.Repeat("a", 64),
	}
	unsafe := manifestRecord("model", "2026-08-21T12:00:00Z")
	unsafe.ExecutionPolicy = ExecutionUnsafe
	addTestFingerprintV2(t, unsafe)
	if err := unsafe.AttachManifestWithExecutor(digestIdentity(t, "model", "model"), executor,
		testRunProvenance(t)); err != nil {
		t.Fatal(err)
	}
	if unsafe.Manifest.Executor == nil || *unsafe.Manifest.Executor != executor {
		t.Fatalf("unsafe manifest executor = %+v", unsafe.Manifest.Executor)
	}
	if err := unsafe.ValidateManifest(); err != nil {
		t.Fatal(err)
	}

	missing := manifestRecord("model", "2026-08-21T12:00:00Z")
	missing.ExecutionPolicy = ExecutionUnsafe
	addTestFingerprintV2(t, missing)
	if err := missing.AttachManifest(digestIdentity(t, "model", "model"), testRunProvenance(t)); err == nil || !strings.Contains(err.Error(), "executor receipt") {
		t.Fatalf("unsafe manifest without executor error = %v", err)
	}

	disabled := manifestRecord("model", "2026-08-21T12:00:00Z")
	addTestFingerprintV2(t, disabled)
	if err := disabled.AttachManifestWithExecutor(digestIdentity(t, "model", "model"), executor,
		testRunProvenance(t)); err == nil || !strings.Contains(err.Error(), "disabled execution") {
		t.Fatalf("disabled manifest with executor error = %v", err)
	}
}

func TestManifestSchemaHasExplicitLegacyAndReleaseBRules(t *testing.T) {
	legacy := manifestRecord("model", "2026-08-21T12:00:00Z")
	if err := legacy.AttachManifest(digestIdentity(t, "model", "model")); err != nil {
		t.Fatal(err)
	}
	if legacy.Manifest.Schema != LegacyRunManifestSchema || legacy.Manifest.Provenance != nil {
		t.Fatalf("legacy compatibility manifest = %+v", legacy.Manifest)
	}
	if err := legacy.ValidateManifest(); err != nil {
		t.Fatalf("legacy manifest no longer verifies: %v", err)
	}

	missing := *legacy.Manifest
	missing.Schema = RunManifestSchema
	missing.ManifestSHA256 = ""
	if err := missing.Seal(); err == nil || !strings.Contains(err.Error(), "missing reproducibility") {
		t.Fatalf("release-B missing provenance error = %v", err)
	}
	withLegacyTag := *legacy.Manifest
	p := testRunProvenance(t)
	withLegacyTag.Provenance = &p
	withLegacyTag.ManifestSHA256 = ""
	if err := withLegacyTag.Seal(); err == nil || !strings.Contains(err.Error(), "legacy") {
		t.Fatalf("legacy manifest with new fields error = %v", err)
	}
}

func TestCanonicalPolicyHashesIgnoreMapInsertionOrder(t *testing.T) {
	task := "sha256:" + strings.Repeat("1", 64)
	spec := "sha256:" + strings.Repeat("2", 64)
	a, err := NewRunProvenance(task, spec,
		map[string]any{"name": "p", "gates": map[string]any{"a": 1, "b": 2}},
		CurrentScoringPolicy(), testSoftwareReceipt())
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewRunProvenance(task, spec,
		map[string]any{"gates": map[string]any{"b": 2, "a": 1}, "name": "p"},
		CurrentScoringPolicy(), testSoftwareReceipt())
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("canonical hashes differ: %+v vs %+v", a, b)
	}
}

func TestLegacyAdaptiveDecisionPersistsButCurrentSchemaRejectsIt(t *testing.T) {
	decision := eval.AdaptiveDecision{
		Need: "structured_output", Method: eval.AdaptiveMethodWaldSPRT,
		Gate: 0.75, NullRate: 0.65, AltRate: 0.85, Alpha: 0.05, Beta: 0.05,
		MaxTrials: 1, Trials: 1, Passes: 1, Failures: 0, LogRatio: 0.2,
		Decision: eval.AdaptiveInconclusive, StopReason: "trial_cap",
	}
	r := manifestRecord("model", "2026-08-21T12:00:00Z")
	r.SchemaVersion = EvidenceSchemaVersion
	r.AdaptiveDecisions = []eval.AdaptiveDecision{decision}
	addTestFingerprintV2(t, r)
	if err := r.AttachManifest(digestIdentity(t, "model", "model"), testRunProvenance(t)); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Record
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.AdaptiveDecisions) != 1 || decoded.AdaptiveDecisions[0] != decision {
		t.Fatalf("adaptive receipt changed: %+v", decoded.AdaptiveDecisions)
	}
	if err := r.ValidateEvidenceContract(); err == nil || !strings.Contains(err.Error(), "fixed generated-check plan") {
		t.Fatalf("current adaptive rejection = %v", err)
	}
}
