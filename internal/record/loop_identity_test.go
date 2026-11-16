package record

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestServingLoopIsOptionalOnTheManifestDigest(t *testing.T) {
	base := currentManifestRecord(t)
	raw, err := json.Marshal(base.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "serving_loop") || strings.Contains(string(raw), "recurrent_kv") {
		t.Fatalf("unobserved manifest grew loop fields: %s", raw)
	}
	changed := cloneManifest(t, base.Manifest)
	before := changed.ManifestSHA256
	n := 4
	changed.ServingLoopCount = &n
	changed.RecurrentKVCacheMode = RecurrentKVCachePerLoop
	if err := changed.Seal(); err != nil {
		t.Fatal(err)
	}
	if changed.ManifestSHA256 == before {
		t.Fatal("a serving loop observation did not change the manifest seal")
	}
	if err := changed.Verify(); err != nil {
		t.Fatal(err)
	}
	if got := ServingLoopText(changed.ServingLoopCount, changed.RecurrentKVCacheMode); !strings.Contains(got, "serving loop count 4") {
		t.Fatalf("text = %q", got)
	}
}

func TestObserveServingLoopRejectsAnEmptyOrRepeatedObservation(t *testing.T) {
	r := manifestRecord("model", "2026-08-21T12:00:00Z")
	r.SchemaVersion = EvidenceSchemaVersion
	addTestFingerprintV2(t, r)
	if err := r.ObserveServingLoop(nil, ""); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty observation error = %v", err)
	}
	if err := r.ObserveServingLoop(nil, "both"); err == nil || !strings.Contains(err.Error(), "unmeasured") {
		t.Fatalf("bad mode error = %v", err)
	}
	n := 0
	if err := r.ObserveServingLoop(&n, RecurrentKVCachePerLoop); err == nil {
		t.Fatal("zero loop count was sealed as a measurement")
	}
	n = 4
	if err := r.ObserveServingLoop(&n, RecurrentKVCacheLastLoop); err != nil {
		t.Fatal(err)
	}
	if err := r.ObserveServingLoop(&n, RecurrentKVCachePerLoop); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("repeat error = %v", err)
	}
	if err := r.AttachManifest(digestIdentity(t, "model", "model")); err == nil || !strings.Contains(err.Error(), "legacy") {
		t.Fatalf("legacy attach error = %v", err)
	}
}

func TestRunManifestRejectsAServingLoopContract(t *testing.T) {
	base := currentManifestRecord(t).Manifest
	legacy := cloneManifest(t, base)
	legacy.Schema = LegacyRunManifestSchema
	legacy.Provenance = nil
	legacy.DeviceFingerprintSHA256 = ""
	legacy.CompletionPublicKey = ""
	n := 4
	legacy.ServingLoopCount = &n
	if err := legacy.validateFields(); err == nil || !strings.Contains(err.Error(), "legacy") {
		t.Fatalf("legacy error = %v", err)
	}
	current := cloneManifest(t, base)
	current.RecurrentKVCacheMode = "both"
	if err := current.validateFields(); err == nil || !strings.Contains(err.Error(), "unmeasured") {
		t.Fatalf("mode error = %v", err)
	}
	zero := cloneManifest(t, base)
	n = 0
	zero.ServingLoopCount = &n
	if err := zero.validateFields(); err == nil || !strings.Contains(err.Error(), "unmeasured") {
		t.Fatalf("zero error = %v", err)
	}
}

func TestUnobservedLoopTextDoesNotInventAFileDefault(t *testing.T) {
	if got := ServingLoopText(nil, ""); strings.Contains(got, "4") || strings.Contains(got, "2") || !strings.Contains(got, "unobserved") {
		t.Fatalf("text = %q", got)
	}
}
