package record

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/eval"
)

func currentManifestRecord(t *testing.T) *Record {
	t.Helper()
	r := manifestRecord("model", "2026-08-21T12:00:00Z")
	r.SchemaVersion = EvidenceSchemaVersion
	addTestFingerprintV2(t, r)
	if err := r.AttachManifest(digestIdentity(t, "model", "model"), testRunProvenance(t)); err != nil {
		t.Fatal(err)
	}
	return r
}

func cloneManifest(t *testing.T, source *RunManifest) *RunManifest {
	t.Helper()
	b, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var out RunManifest
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return &out
}

func TestRunManifestRejectsEveryMalformedContractField(t *testing.T) {
	base := currentManifestRecord(t).Manifest
	legacy := func(t *testing.T) *RunManifest {
		t.Helper()
		m := cloneManifest(t, base)
		m.Schema = LegacyRunManifestSchema
		m.Provenance = nil
		m.DeviceFingerprintSHA256 = ""
		m.CompletionPublicKey = ""
		return m
	}
	current := func(t *testing.T) *RunManifest {
		t.Helper()
		return cloneManifest(t, base)
	}
	tests := []struct {
		name   string
		make   func(*testing.T) *RunManifest
		mutate func(*RunManifest)
		want   string
	}{
		{"schema", current, func(m *RunManifest) { m.Schema = "future" }, "unsupported"},
		{"legacy provenance", legacy, func(m *RunManifest) { p := testRunProvenance(t); m.Provenance = &p }, "legacy"},
		{"missing provenance", current, func(m *RunManifest) { m.Provenance = nil }, "provenance"},
		{"fingerprint", current, func(m *RunManifest) { m.DeviceFingerprintSHA256 = "" }, "fingerprint"},
		{"completion key", current, func(m *RunManifest) { m.CompletionPublicKey = "bad" }, "public key"},
		{"binding", current, func(m *RunManifest) { m.Model.Binding = "" }, "binding"},
		{"legacy completion key", legacy, func(m *RunManifest) { m.CompletionPublicKey = "present" }, "legacy"},
		{"legacy executor", legacy, func(m *RunManifest) { m.Executor = &eval.ExecutorReceipt{} }, "legacy"},
		{"run id", current, func(m *RunManifest) { m.RunID = "bad" }, "run ID"},
		{"started missing", current, func(m *RunManifest) { m.StartedAt = "" }, "started_at"},
		{"device key", current, func(m *RunManifest) { m.DeviceKey = "" }, "device key"},
		{"profile", current, func(m *RunManifest) { m.Profile = "" }, "profile"},
		{"level", current, func(m *RunManifest) { m.Level = "" }, "level"},
		{"execution policy", current, func(m *RunManifest) { m.ExecutionPolicy = "root" }, "execution policy"},
		{"unsafe missing executor", current, func(m *RunManifest) { m.ExecutionPolicy = ExecutionUnsafe; m.Executor = nil }, "executor"},
		{"disabled executor", current, func(m *RunManifest) { m.Executor = &eval.ExecutorReceipt{} }, "disabled"},
		{"seed set", current, func(m *RunManifest) { m.SeedSet = "" }, "seed"},
		{"repeats", current, func(m *RunManifest) { m.Repeats = 0 }, "repeats"},
		{"context", current, func(m *RunManifest) { m.NumCtx = 0 }, "context"},
		{"started format", current, func(m *RunManifest) { m.StartedAt = "yesterday" }, "started_at"},
		{"task plan", current, func(m *RunManifest) { m.TaskPlan = TaskPlan{} }, "task plan"},
		{"provenance", current, func(m *RunManifest) { m.Provenance.TaskSetSHA256 = "bad" }, "task_set"},
		{"backend protocol", current, func(m *RunManifest) { m.Model.Backend = "openai" }, "does not match"},
		{"model identity", current, func(m *RunManifest) { m.Model.Requested = "" }, "requested model"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			manifest := tc.make(t)
			tc.mutate(manifest)
			if err := manifest.validateFields(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestModelIdentityValidationAndRuntimeBinding(t *testing.T) {
	base := digestIdentity(t, "model", "model")
	tests := []struct {
		name   string
		mutate func(*ModelIdentity)
		want   string
	}{
		{"requested", func(i *ModelIdentity) { i.Requested = "" }, "requested"},
		{"resolved", func(i *ModelIdentity) { i.Resolved = "" }, "resolved"},
		{"backend", func(i *ModelIdentity) { i.Backend = "" }, "backend"},
		{"runtime", func(i *ModelIdentity) { i.Runtime = "" }, "runtime"},
		{"digest", func(i *ModelIdentity) { i.Value = "bad" }, "SHA-256"},
		{"size", func(i *ModelIdentity) { i.SizeBytes = -1 }, "negative"},
		{"content address", func(i *ModelIdentity) { i.ContentAddressed = false }, "content-addressed"},
		{"runtime binding", func(i *ModelIdentity) { i.Binding = IdentityBindingObserved }, "runtime-bound"},
		{"kind", func(i *ModelIdentity) { i.Kind = "mystery" }, "unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			identity := base
			tc.mutate(&identity)
			if err := identity.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
	observed := base
	observed.Kind = IdentityLocalFile
	observed.Binding = IdentityBindingObserved
	if issue := observed.RankingIssue(); !strings.Contains(issue, "not bound") {
		t.Fatalf("observed-only ranking issue = %q", issue)
	}
	if observed.RuntimeBoundDigest() != "" || base.RuntimeBoundDigest() != base.Value {
		t.Fatal("runtime-bound digest classification changed")
	}
}

func TestBackendProtocolNormalizesKnownAndCustomNames(t *testing.T) {
	for input, want := range map[string]string{
		"ollama":            BackendProtocolOllama,
		"LLAMAserver":       BackendProtocolLlamaServerNative,
		"openai-compatible": BackendProtocolOpenAICompatible,
		"My Backend!":       "fitr.backend.my-backend.v1",
		"!!!":               "fitr.backend.unknown.v1",
	} {
		if got := BackendProtocol(input); got != want {
			t.Fatalf("BackendProtocol(%q) = %q, want %q", input, got, want)
		}
	}
	if protocolMatchesBackend("", "ollama") || protocolMatchesBackend(BackendProtocolOllama, "") ||
		!protocolMatchesBackend(strings.ToUpper(BackendProtocolOllama), "OLLAMA") {
		t.Fatal("backend protocol matching is not strict and case-insensitive")
	}
}

func TestValidateManifestRejectsEveryDuplicatedRecordMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Record)
		want   string
	}{
		{"run id", func(r *Record) { r.RunID = "run_other_value" }, "run ID"},
		{"start", func(r *Record) { r.StartedAt = "2026-08-21T13:00:00Z" }, "start time"},
		{"model", func(r *Record) { r.Model = "other" }, "model"},
		{"device", func(r *Record) { r.DeviceKey = "other" }, "device key"},
		{"profile", func(r *Record) { r.Profile = "other" }, "profile"},
		{"level", func(r *Record) { r.Level = "quick" }, "level"},
		{"policy", func(r *Record) { r.ExecutionPolicy = ExecutionUnsafe }, "execution policy"},
		{"plan", func(r *Record) { r.TaskPlan.ToolTrials++ }, "task plan"},
		{"seed", func(r *Record) { r.SeedSet = "other" }, "seed"},
		{"repeats", func(r *Record) { r.Repeats++ }, "repeats"},
		{"context", func(r *Record) { r.NumCtx++ }, "context"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := currentManifestRecord(t)
			tc.mutate(r)
			if err := r.ValidateManifest(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}
