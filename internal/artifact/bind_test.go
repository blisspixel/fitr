package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/source"
)

func artifactRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func contentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func artifactFixture(t *testing.T, count int) (source.Resolution, Spec) {
	t.Helper()
	root := artifactRoot(t)
	when := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	commit := strings.Repeat("a", 40)
	resolution := source.Resolution{Schema: source.ResolutionSchema, PolicyVersion: source.PolicyVersion,
		ResolverVersion: "0.10.8", Provider: "huggingface", Scope: source.Scope, ObservedAt: when,
		Request:      source.HFRequest{RepoID: "owner/model", Revision: "main", Files: []string{}},
		ResolvedRepo: "owner/model", ResolvedCommit: commit, State: "resolved", Files: []source.FileMetadata{},
		InventoryPaths: []string{}, Dependencies: []source.DependencyFinding{}, Gaps: []string{"dependency_closure_unverified", "local_artifact_unverified"},
		Queries: []source.QueryObservation{{Revision: "main", StartedAt: when, CompletedAt: when, HTTPStatus: 200, Outcome: "complete", ResponseSHA256: contentHash([]byte("metadata"))},
			{Revision: commit, StartedAt: when, CompletedAt: when, HTTPStatus: 200, Outcome: "complete", ResponseSHA256: contentHash([]byte("metadata"))}}}
	for _, kind := range []string{"cross_repository", "encoder", "projector", "tokenizer"} {
		resolution.Dependencies = append(resolution.Dependencies, source.DependencyFinding{Kind: kind, Status: "unknown", Basis: "not_inspected"})
	}
	spec := Spec{Schema: SpecSchema, Files: []Mapping{}}
	for index := range count {
		name := string(rune('a'+index)) + ".gguf"
		if index >= 26 {
			name = "z" + string(rune('a'+index-26)) + ".gguf"
		}
		data := []byte("artifact:" + name)
		path, size := filepath.Join(root, name), int64(len(data))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		resolution.Request.Files = append(resolution.Request.Files, name)
		resolution.InventoryPaths = append(resolution.InventoryPaths, name)
		resolution.Files = append(resolution.Files, source.FileMetadata{Path: name, State: "present", SizeBytes: &size, DeclaredSHA256: contentHash(data), GitBlobOID: strings.Repeat("b", 40)})
		spec.Files = append(spec.Files, Mapping{SourcePath: name, LocalPath: path, ComponentRole: "weights"})
	}
	resealSource(t, &resolution, &spec)
	return resolution, spec
}

func resealSource(t *testing.T, resolution *source.Resolution, spec *Spec) {
	t.Helper()
	digest, err := resolution.Digest()
	if err != nil {
		t.Fatal(err)
	}
	resolution.ResolutionSHA256, spec.ResolutionSHA256 = digest, digest
}

func bindFixture(t *testing.T) Binding {
	t.Helper()
	resolution, spec := artifactFixture(t, 1)
	result, err := Bind(t.Context(), resolution, spec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestBindExactBudgetAndIndependentCopies(t *testing.T) {
	resolution, spec := artifactFixture(t, 2)
	budget := *resolution.Files[0].SizeBytes + *resolution.Files[1].SizeBytes
	slices.Reverse(spec.Files)
	result, err := Bind(t.Context(), resolution, spec, Options{MaxBytes: budget})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "matched" || result.BytesRead != budget || result.Validate() != nil || result.Files[0].SourcePath != "a.gguf" {
		t.Fatalf("bad result: %+v", result)
	}
	if result.RuntimeState != "unbound" || result.CapacityState != "unmeasured" || result.QualityState != "unmeasured" || result.DependencyState != "unverified" {
		t.Fatal("binding promoted evidence")
	}
	*resolution.Files[0].SizeBytes = 999
	resolution.Request.Files[0], spec.Files[0].ComponentRole = "changed", "other"
	if result.Validate() != nil {
		t.Fatal("caller retained nested receipt aliases")
	}
	if result.Files[0].ObservedSHA256 == result.Source.Files[0].GitBlobOID {
		t.Fatal("Git OID conflated with whole-file hash")
	}
}

func TestBindLocalOutcomes(t *testing.T) {
	cases := []struct {
		name, want string
		change     func(*testing.T, *source.Resolution, *Spec)
	}{
		{"missing", "incomplete", func(t *testing.T, _ *source.Resolution, s *Spec) {
			t.Helper()
			if err := os.Remove(s.Files[0].LocalPath); err != nil {
				t.Fatal(err)
			}
		}},
		{"size", "mismatch", func(t *testing.T, _ *source.Resolution, s *Spec) {
			t.Helper()
			if err := os.WriteFile(s.Files[0].LocalPath, []byte("short"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"hash", "mismatch", func(t *testing.T, _ *source.Resolution, s *Spec) {
			t.Helper()
			if err := os.WriteFile(s.Files[0].LocalPath, []byte("different-bytes"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"unmapped", "incomplete", func(t *testing.T, _ *source.Resolution, s *Spec) { t.Helper(); s.Files = s.Files[:1] }},
		{"no_sha", "locally_hashed", func(t *testing.T, r *source.Resolution, s *Spec) {
			t.Helper()
			r.Files[0].DeclaredSHA256 = ""
			r.State = "incomplete"
			r.Gaps = append(r.Gaps, "content_sha256_unavailable")
			slices.Sort(r.Gaps)
			resealSource(t, r, s)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, s := artifactFixture(t, 2)
			tc.change(t, &r, &s)
			result, err := Bind(t.Context(), r, s, Options{})
			if err != nil || result.State != tc.want || result.Validate() != nil {
				t.Fatalf("state=%s err=%v", result.State, err)
			}
			if tc.name == "unmapped" && !reflect.DeepEqual(result.UnmappedFiles, []string{"b.gguf"}) {
				t.Fatal("unmapped source file lost")
			}
		})
	}
}

func TestBindBudgetAndPreCancelled(t *testing.T) {
	r, s := artifactFixture(t, 2)
	result, err := Bind(t.Context(), r, s, Options{MaxBytes: 1})
	if err != nil || result.State != "budget_exceeded" || result.BytesRead != 0 {
		t.Fatalf("budget: %+v %v", result, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err = Bind(ctx, r, s, Options{})
	if err != nil || result.State != "cancelled" || result.BytesRead != 0 {
		t.Fatalf("cancel: %+v %v", result, err)
	}
	ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	result, err = Bind(ctx, r, s, Options{})
	if err != nil || result.State != "timeout" || result.BytesRead != 0 {
		t.Fatalf("timeout: %+v %v", result, err)
	}
}

func TestBindRejectsInvalidBeforeReads(t *testing.T) {
	r, s := artifactFixture(t, 2)
	cases := []struct {
		name   string
		change func(*Spec)
	}{
		{"digest", func(s *Spec) { s.ResolutionSHA256 = "sha256:" + strings.Repeat("0", 64) }},
		{"unselected", func(s *Spec) { s.Files[1].SourcePath = "unselected.gguf" }},
		{"source_alias", func(s *Spec) { s.Files[1].SourcePath = s.Files[0].SourcePath }},
		{"local_alias", func(s *Spec) { s.Files[1].LocalPath = s.Files[0].LocalPath }},
		{"relative", func(s *Spec) { s.Files[1].LocalPath = "relative.gguf" }},
		{"parent", func(s *Spec) { s.Files[1].LocalPath += string(os.PathSeparator) + ".." }},
		{"role", func(s *Spec) { s.Files[1].ComponentRole = "compatible_projector" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			copySpec := s
			copySpec.Files = slices.Clone(s.Files)
			tc.change(&copySpec)
			result, err := Bind(t.Context(), r, copySpec, Options{})
			if err == nil || result.BytesRead != 0 {
				t.Fatal("invalid input was observed")
			}
		})
	}
}

func TestBindingRoundTripSurvivesLocalDeletion(t *testing.T) {
	result := bindFixture(t)
	output := filepath.Join(artifactRoot(t), "binding.json")
	if err := WriteBinding(output, result); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(result.Files[0].LocalPath); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadBinding(output)
	if err != nil || !reflect.DeepEqual(loaded, result) {
		t.Fatalf("receipt depended on local paths: %v", err)
	}
	if err := WriteBinding(output, result); err == nil {
		t.Fatal("receipt overwritten")
	}
	if err := WriteBinding(result.Files[0].LocalPath, result); err == nil {
		t.Fatal("receipt published over a missing input")
	}
	data, _ := json.Marshal(result.Mapping)
	specPath := filepath.Join(filepath.Dir(output), "mapping.json")
	if err := os.WriteFile(specPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if spec, err := LoadSpec(specPath); err != nil || !reflect.DeepEqual(spec, result.Mapping) {
		t.Fatalf("mapping load: %v", err)
	}
}
