package record

import (
	"encoding/json"
	"strings"
	"testing"
)

func runtimeFixtureBinding(identity ModelIdentity) *RuntimeBinding {
	return &RuntimeBinding{Schema: RuntimeBindingSchema, Kind: "owned_ollama",
		ProfileSHA256: "sha256:" + strings.Repeat("1", 64), ExecutableSHA256: "sha256:" + strings.Repeat("2", 64),
		LaunchConfigurationSHA256: "sha256:" + strings.Repeat("3", 64), ModelConfigurationSHA256: "sha256:" + strings.Repeat("4", 64),
		ArtifactDigest: identity.RuntimeBoundDigest(), RuntimeVersion: identity.Runtime, OwnershipSHA256: "sha256:" + strings.Repeat("5", 64)}
}

func runtimeBoundRecord(t *testing.T) *Record {
	t.Helper()
	r := derivedEvidenceBase(t)
	profile, identity, provenance := r.Completion.Profile, r.Manifest.Model, *r.Manifest.Provenance
	r.Manifest, r.Completion, r.RunID = nil, nil, ""
	r.RuntimeBinding = runtimeFixtureBinding(identity)
	if err := r.AttachManifest(identity, provenance); err != nil {
		t.Fatal(err)
	}
	if err := r.CompleteEvidence(profile); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRuntimeBindingPreservesSignedOwnedConfiguration(t *testing.T) {
	r := runtimeBoundRecord(t)
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	read, err := decodeRecord(data)
	if err != nil || read.EvidenceIntegrityIssue() != "" || *read.RuntimeBinding != *r.RuntimeBinding {
		t.Fatal("binding lost", err)
	}
	if read.RuntimeBinding == read.Manifest.RuntimeBinding {
		t.Fatal("manifest aliases mutable binding")
	}
	read.RuntimeBinding.OwnershipSHA256 = "sha256:" + strings.Repeat("6", 64)
	if read.ValidateManifest() == nil {
		t.Fatal("binding differs from signed manifest")
	}
	read.Manifest.RuntimeBinding = CloneRuntimeBinding(read.RuntimeBinding)
	if err := read.Manifest.Seal(); err != nil {
		t.Fatal(err)
	}
	if read.ValidateEvidenceContract() == nil {
		t.Fatal("resealed ownership forged completed evidence")
	}
	legacy := derivedEvidenceBase(t)
	plain, err := json.Marshal(legacy)
	if err != nil || strings.Contains(string(plain), "runtime_binding") {
		t.Fatal("absent binding changed legacy JSON", err)
	}
}

func TestRuntimeBindingComparisonSeparatesProfileArtifactAndLaunch(t *testing.T) {
	a := runtimeBoundRecord(t)
	b := CloneRuntimeBinding(a.RuntimeBinding)
	b.OwnershipSHA256 = "sha256:" + strings.Repeat("6", 64)
	if !SameRuntimeConfiguration(a.RuntimeBinding, b) || !SameRuntimeProfile(a.RuntimeBinding, b) {
		t.Fatal("new launch rejected")
	}
	b.ModelConfigurationSHA256 = "sha256:" + strings.Repeat("7", 64)
	if SameRuntimeConfiguration(a.RuntimeBinding, b) || !SameRuntimeProfile(a.RuntimeBinding, b) {
		t.Fatal("model configuration boundary lost")
	}
	b.LaunchConfigurationSHA256 = "sha256:" + strings.Repeat("8", 64)
	if SameRuntimeProfile(a.RuntimeBinding, b) {
		t.Fatal("launch drift comparable")
	}
	baseKey, err := a.DeviceV2.ComparabilityKey()
	if err != nil {
		t.Fatal(err)
	}
	key, err := a.ComparableDeviceKey()
	if err != nil || key == baseKey {
		t.Fatal("owned profile absent from comparison", err)
	}
	if SameRuntimeConfiguration(a.RuntimeBinding, nil) || SameRuntimeProfile(nil, b) || !SameRuntimeConfiguration(nil, nil) || !SameRuntimeProfile(nil, nil) {
		t.Fatal("legacy profile mixed with owned")
	}
}

func TestRuntimeBindingValidation(t *testing.T) {
	r := runtimeBoundRecord(t)
	for _, mutate := range []func(*RuntimeBinding){
		func(b *RuntimeBinding) { b.Schema = "other" }, func(b *RuntimeBinding) { b.Kind = "shared" },
		func(b *RuntimeBinding) { b.OwnershipSHA256 = "bad" }, func(b *RuntimeBinding) { b.RuntimeVersion = "\nversion" },
		func(b *RuntimeBinding) { b.RuntimeVersion = "version\x00" }, func(b *RuntimeBinding) { b.RuntimeVersion = strings.Repeat("x", 129) },
	} {
		b := CloneRuntimeBinding(r.RuntimeBinding)
		mutate(b)
		if b.Validate() == nil || SameRuntimeConfiguration(b, r.RuntimeBinding) || SameRuntimeProfile(b, r.RuntimeBinding) {
			t.Fatal("invalid binding accepted", b)
		}
	}
	identity := r.Manifest.Model
	identity.Runtime = "different"
	if r.RuntimeBinding.ValidateFor(identity) == nil {
		t.Fatal("runtime identity changed")
	}
	orphan := &Record{RuntimeBinding: CloneRuntimeBinding(r.RuntimeBinding)}
	if orphan.ValidateManifest() == nil {
		t.Fatal("binding without manifest accepted")
	}
	manifest := *r.Manifest
	manifest.Schema = LegacyRunManifestSchema
	if manifest.Seal() == nil {
		t.Fatal("legacy runtime binding accepted")
	}
}
