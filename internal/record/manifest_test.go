package record

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/score"
	"github.com/blisspixel/fitr/internal/stats"
)

const testArtifactDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func digestIdentity(t *testing.T, requested, resolved string) ModelIdentity {
	t.Helper()
	i, err := NewModelIdentity(requested, resolved, "ollama", "ollama 1.2.3",
		testArtifactDigest, "", 1234)
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func manifestRecord(model, started string) *Record {
	r := testRecord(model, started)
	r.SeedSet = "release-a"
	r.ExecutionPolicy = ExecutionDisabled
	r.TaskPlan = TaskPlan{
		SpeedSamples: 3, Memory: true, CodeTrials: 6, CheckTrialsLimit: 16,
		Plumbing: true, ToolTrials: 3, Withdrawal: true, RefusalTrials: 3, AgenticTrials: 1,
	}
	return r
}

func TestRunManifestSealsResolvedIdentityAndRejectsMutation(t *testing.T) {
	r := manifestRecord("qwen:latest", "2026-08-21T12:00:00Z")
	if err := r.AttachManifest(digestIdentity(t, "qwen", "qwen:latest")); err != nil {
		t.Fatal(err)
	}
	if r.RunID == "" || r.Manifest == nil || r.Manifest.ManifestSHA256 == "" {
		t.Fatalf("manifest was not sealed: %+v", r.Manifest)
	}
	if !r.Manifest.Model.ContentAddressed || r.Manifest.Model.Requested != "qwen" ||
		r.Manifest.Model.Resolved != r.Model {
		t.Fatalf("resolved identity = %+v", r.Manifest.Model)
	}
	if err := r.ValidateManifest(); err != nil {
		t.Fatal(err)
	}

	r.Repeats++
	if err := r.ValidateManifest(); err == nil || !strings.Contains(err.Error(), "repeats") {
		t.Fatalf("mutated record validation = %v", err)
	}
}

func TestManifestRoundTripAndSecondAttachAreStrict(t *testing.T) {
	r := manifestRecord("model", "2026-08-21T12:00:00Z")
	identity := digestIdentity(t, "model", "model")
	if err := r.AttachManifest(identity); err != nil {
		t.Fatal(err)
	}
	if err := r.AttachManifest(identity); err == nil {
		t.Fatal("a second manifest attachment was accepted")
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Record
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.ValidateManifest(); err != nil {
		t.Fatalf("round-trip manifest: %v", err)
	}
}

func TestModelIdentityStrengthIsExplicitAndPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "private-model.gguf")
	if err := os.WriteFile(path, []byte("model bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	local, err := NewModelIdentity("alias", "private-model.gguf", "llama-server", "llama-server b1", "", path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if local.Kind != IdentityLocalFile || !local.ContentAddressed || local.SizeBytes == 0 {
		t.Fatalf("local identity = %+v", local)
	}
	want := sha256.Sum256([]byte("model bytes"))
	if local.Value != "sha256:"+hex.EncodeToString(want[:]) {
		t.Fatalf("local digest = %q", local.Value)
	}
	b, _ := json.Marshal(local)
	if strings.Contains(string(b), dir) || strings.Contains(string(b), path) {
		t.Fatalf("local identity leaked its private path: %s", b)
	}

	if _, err := NewModelIdentity("model", "model", "openai", "openai-compat 1", "", "", 0); err == nil {
		t.Fatal("runtime-only model name was accepted as artifact identity")
	}
}

func TestBackendProtocolMatchesLlamaServerNativeReceipt(t *testing.T) {
	cases := []struct {
		backend, want string
	}{
		{"ollama", BackendProtocolOllama},
		{"llama-server", BackendProtocolLlamaServerNative},
		{"llamaserver", BackendProtocolLlamaServerNative},
		{"openai", BackendProtocolOpenAICompatible},
		{"openai-compatible", BackendProtocolOpenAICompatible},
	}
	for _, tc := range cases {
		got := BackendProtocol(tc.backend)
		if got != tc.want {
			t.Fatalf("BackendProtocol(%q) = %q, want %q", tc.backend, got, tc.want)
		}
		if !protocolMatchesBackend(got, tc.backend) {
			t.Fatalf("protocol %q was rejected for backend %q", got, tc.backend)
		}
	}
	if protocolMatchesBackend(BackendProtocolOllama, "llama-server") {
		t.Fatal("ollama protocol matched llama-server")
	}
	if protocolMatchesBackend("fitr.backend.llama-server.v1", "llama-server") {
		t.Fatal("truncated llama-server protocol was accepted")
	}

	r := manifestRecord("model", "2026-08-21T12:00:00Z")
	addTestFingerprintV2(t, r)
	identity, err := NewModelIdentity("model", "model", "llama-server", "llama-server b1",
		testArtifactDigest, "", 1234)
	if err != nil {
		t.Fatal(err)
	}
	provenance := testRunProvenance(t)
	provenance.BackendProtocol = BackendProtocol("llama-server")
	if err := r.AttachManifest(identity, provenance); err != nil {
		t.Fatal(err)
	}
	if err := r.ValidateManifest(); err != nil {
		t.Fatalf("llama-server native protocol failed to seal: %v", err)
	}
	if issue := identity.RankingIssue(); issue != "" {
		t.Fatalf("runtime-bound llama-server identity was unrankable: %s", issue)
	}
	if identity.RuntimeBoundDigest() != testArtifactDigest {
		t.Fatalf("runtime-bound digest = %q", identity.RuntimeBoundDigest())
	}
}

func TestObservedLocalIdentityCannotRank(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "private-model.gguf")
	if err := os.WriteFile(path, []byte("model bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := NewModelIdentity("model", "model", "llama-server",
		"llama-server b1", "", path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if identity.RankingIssue() == "" {
		t.Fatal("observed-only local file identity was rankable")
	}

	profile := device.Profile{Name: "default", Description: "test", Gates: map[string]device.Gate{}}
	r := manifestRecord("model", "2026-08-21T12:00:00Z")
	r.SchemaVersion = EvidenceSchemaVersion
	r.TaskPlan = TaskPlan{CodeTrials: 1, CheckTrialsLimit: 1}
	r.CodeWrite = []eval.ExecResult{{Outcome: eval.OutcomeInconclusive}}
	r.Checks = []eval.CheckOutcome{{TaskID: "check", Pass: true, Outcome: eval.OutcomePass}}
	r.Rep = score.RepetitionMetrics("")
	r.Density = score.InformationDensity("")
	r.Scorecard = score.Scorecard{Model: r.Model, Profile: r.Profile, Needs: map[string]score.Verdict{}}
	r.EvidenceCounts = map[string]eval.OutcomeCounts{
		"coding": eval.CountOutcomes(1, eval.OutcomeInconclusive),
		"checks": eval.CountOutcomes(1, eval.OutcomePass),
		"tools":  eval.CountOutcomes(0), "refusal": eval.CountOutcomes(0),
		"plumbing": eval.CountOutcomes(0), "withdrawal": eval.CountOutcomes(0),
		"agentic": eval.CountOutcomes(0),
	}
	addTestFingerprintV2(t, r)
	provenance, err := NewRunProvenance(testRunProvenance(t).TaskSetSHA256, testRunProvenance(t).SpecSHA256,
		profile, CurrentScoringPolicy(), SoftwareReceipt{
			FitrVersion: "0.5.0", SoftwareBuildSHA256: testArtifactDigest,
			BackendProtocol: BackendProtocol("llama-server"),
		})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.AttachManifest(identity, provenance); err != nil {
		t.Fatal(err)
	}
	if err := r.CompleteEvidence(profile); err != nil {
		t.Fatal(err)
	}
	if issue := r.EvidenceIntegrityIssue(); !strings.Contains(issue, "not bound to the serving runtime") {
		t.Fatalf("observed-only identity ranking issue = %q", issue)
	}
}

func TestModelIdentityRejectsRuntimePathThatIsNotLocal(t *testing.T) {
	_, err := NewModelIdentity(
		"requested.gguf",
		"served.gguf",
		"llama-server",
		"1.2.3",
		"",
		filepath.Join(t.TempDir(), "not-mounted.gguf"),
		1024,
	)
	if err == nil || !strings.Contains(err.Error(), "hash local model artifact") {
		t.Fatalf("missing artifact error = %v", err)
	}
}

func TestSplitGGUFIdentityCoversEveryShardAndRejectsIncompleteSets(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "model-00001-of-00002.gguf")
	second := filepath.Join(dir, "model-00002-of-00002.gguf")
	if err := os.WriteFile(first, []byte("first shard"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := localFileDigest(first); err == nil || !strings.Contains(err.Error(), "shard 2 of 2") {
		t.Fatalf("incomplete split set error = %v", err)
	}
	if err := os.WriteFile(second, []byte("second shard"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, sizeA, err := localFileDigest(first)
	if err != nil {
		t.Fatal(err)
	}
	b, sizeB, err := localFileDigest(second)
	if err != nil {
		t.Fatal(err)
	}
	if a != b || sizeA != int64(len("first shard")+len("second shard")) || sizeB != sizeA {
		t.Fatalf("split identities differ: %q/%d vs %q/%d", a, sizeA, b, sizeB)
	}
	if err := os.WriteFile(second, []byte("changed shard"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, _, err := localFileDigest(first)
	if err != nil {
		t.Fatal(err)
	}
	if changed == a {
		t.Fatal("changing a non-selected shard did not change the artifact identity")
	}
}

func TestSchemaFiveContractRejectsMissingAndScoreableExecutableEvidence(t *testing.T) {
	r := completedEvidenceRecord(t, []eval.ExecResult{{Pass: true, Outcome: eval.OutcomePass}},
		[]eval.CheckOutcome{{TaskID: "check", Pass: true, Outcome: eval.OutcomePass}})
	if err := r.ValidateEvidenceContract(); err == nil || !strings.Contains(err.Error(), "cannot be scoreable") {
		t.Fatalf("scoreable executable evidence = %v", err)
	}
	r = completedEvidenceRecord(t, []eval.ExecResult{{Outcome: eval.OutcomeInconclusive}},
		[]eval.CheckOutcome{{TaskID: "check", Pass: true, Outcome: eval.OutcomePass}})
	if err := r.ValidateEvidenceContract(); err != nil {
		t.Fatalf("valid schema-5 contract: %v", err)
	}
	r = completedEvidenceRecord(t, []eval.ExecResult{{Outcome: eval.OutcomeInconclusive}},
		[]eval.CheckOutcome{{TaskID: "check", Outcome: eval.OutcomeError}})
	if err := r.ValidateEvidenceContract(); err == nil || !strings.Contains(err.Error(), "infrastructure error") {
		t.Fatalf("infrastructure error evidence = %v", err)
	}
}

func completedEvidenceRecord(t *testing.T, code []eval.ExecResult, checks []eval.CheckOutcome) *Record {
	t.Helper()
	profile := device.Profile{Name: "default", Description: "test", Gates: map[string]device.Gate{}}
	r := manifestRecord("model", "2026-08-21T12:00:00Z")
	r.SchemaVersion = EvidenceSchemaVersion
	r.TaskPlan = TaskPlan{CodeTrials: len(code), CheckTrialsLimit: len(checks)}
	r.CodeWrite, r.Checks = code, checks
	r.Rep = score.RepetitionMetrics("")
	r.Density = score.InformationDensity("")
	r.Scorecard = score.Scorecard{Model: r.Model, Profile: r.Profile, Needs: map[string]score.Verdict{}}
	r.DecodeSum, r.TTFTSum, r.PrefillSum = stats.Summary{}, stats.Summary{}, stats.Summary{}
	r.EvidenceCounts = map[string]eval.OutcomeCounts{
		"coding": eval.CountOutcomes(len(code), outcomesForExec(code)...),
		"checks": eval.CountOutcomes(len(checks), outcomesForChecks(checks)...),
		"tools":  eval.CountOutcomes(0), "refusal": eval.CountOutcomes(0),
		"plumbing": eval.CountOutcomes(0), "withdrawal": eval.CountOutcomes(0),
		"agentic": eval.CountOutcomes(0),
	}
	addTestFingerprintV2(t, r)
	hashes, err := eval.BuiltinHashes()
	if err != nil {
		t.Fatal(err)
	}
	provenance, err := NewRunProvenance(hashes.TaskSetSHA256, hashes.SpecSHA256, profile,
		CurrentScoringPolicy(), testSoftwareReceipt())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.AttachManifest(digestIdentity(t, "model", "model"), provenance); err != nil {
		t.Fatal(err)
	}
	if err := r.CompleteEvidence(profile); err != nil {
		t.Fatal(err)
	}
	return r
}

func outcomesForExec(values []eval.ExecResult) []eval.Outcome {
	out := make([]eval.Outcome, len(values))
	for i := range values {
		out[i] = values[i].Outcome
	}
	return out
}
func outcomesForChecks(values []eval.CheckOutcome) []eval.Outcome {
	out := make([]eval.Outcome, len(values))
	for i := range values {
		out[i] = values[i].Outcome
	}
	return out
}

func TestEvidenceContractRejectsUnknownFutureSchema(t *testing.T) {
	r := manifestRecord("model", "2026-08-21T12:00:00Z")
	r.SchemaVersion = EvidenceSchemaVersion + 1
	if err := r.ValidateEvidenceContract(); err == nil || !strings.Contains(err.Error(), "unsupported result schema") {
		t.Fatalf("future schema error = %v", err)
	}
}

func repeatOutcome(outcome eval.Outcome, count int) []eval.Outcome {
	values := make([]eval.Outcome, count)
	for i := range values {
		values[i] = outcome
	}
	return values
}

func TestStoreRejectsTamperedManifestBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	r := manifestRecord("model", "2026-08-21T12:00:00Z")
	if err := r.AttachManifest(digestIdentity(t, "model", "model")); err != nil {
		t.Fatal(err)
	}
	r.Manifest.Profile = "tampered"
	if _, err := NewStore(dir).Save(r); err == nil || !strings.Contains(err.Error(), "invalid run manifest") {
		t.Fatalf("tampered save error = %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err == nil && len(entries) != 0 {
		t.Fatalf("tampered record wrote files: %v", entries)
	}
}

func TestReadRejectsManifestTampering(t *testing.T) {
	store := NewStore(t.TempDir())
	r := manifestRecord("model", "2026-08-21T12:00:00Z")
	if err := r.AttachManifest(digestIdentity(t, "model", "model")); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Save(r)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(saved.CanonicalPath)
	if err != nil {
		t.Fatal(err)
	}
	b = []byte(strings.Replace(string(b), `"level": "full"`, `"level": "quick"`, 1))
	if err := os.WriteFile(saved.CanonicalPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(saved.CanonicalPath); err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("tampered read error = %v", err)
	}
}
