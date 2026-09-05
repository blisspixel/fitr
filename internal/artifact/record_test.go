package artifact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func cloneBinding(t *testing.T, original Binding) Binding {
	t.Helper()
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var result Binding
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRehashedImpossibleObservationsRejected(t *testing.T) {
	original := bindFixture(t)
	cases := []struct {
		name   string
		change func(*Binding)
	}{
		{"schema", func(r *Binding) { r.Schema = "other" }},
		{"version", func(r *Binding) { r.BinderVersion = "bad version" }},
		{"runtime", func(r *Binding) { r.RuntimeState = "bound" }},
		{"quality", func(r *Binding) { r.QualityState = "passed" }},
		{"source", func(r *Binding) { r.ResolutionSHA256 = contentHash([]byte("other")) }},
		{"policy", func(r *Binding) { r.PolicyVersion = "2" }},
		{"mapping", func(r *Binding) { r.Mapping.Files[0].ComponentRole = "other" }},
		{"path", func(r *Binding) { r.Files[0].LocalPath += "other" }},
		{"count", func(r *Binding) { r.Files = nil }},
		{"negative", func(r *Binding) { r.Files[0].BytesRead = -1 }},
		{"partial_hash", func(r *Binding) { r.Files[0].BytesRead--; r.BytesRead-- }},
		{"false_hash", func(r *Binding) { r.Files[0].ObservedSHA256 = contentHash([]byte("other")) }},
		{"after_size", func(r *Binding) { r.Files[0].After.SizeBytes++ }},
		{"after_time", func(r *Binding) { r.Files[0].After.ModifiedAt = "bad" }},
		{"identity", func(r *Binding) { r.Files[0].IdentityState = "unobserved" }},
		{"missing_before", func(r *Binding) { r.Files[0].Before = nil }},
		{"changed_with_hash", func(r *Binding) { r.Files[0].State = "changed" }},
		{"missing_with_data", func(r *Binding) { r.Files[0].State = "missing" }},
		{"aggregate", func(r *Binding) { r.BytesRead-- }},
		{"summary", func(r *Binding) { r.State = "incomplete" }},
		{"gaps", func(r *Binding) { r.Gaps = nil }},
		{"unmapped", func(r *Binding) { r.UnmappedFiles = []string{"a.gguf"} }},
		{"end_before_start", func(r *Binding) { r.CompletedAt = "2000-01-01T00:00:00Z" }},
		{"past_deadline", func(r *Binding) {
			start, _ := time.Parse(time.RFC3339Nano, r.StartedAt)
			r.CompletedAt = start.Add(24 * time.Hour).Format(time.RFC3339Nano)
		}},
		{"limit", func(r *Binding) { r.Limits.MaxBytes = r.BytesRead - 1 }},
		{"timeout_limit", func(r *Binding) { r.Limits.TimeoutMillis = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := cloneBinding(t, original)
			tc.change(&result)
			if digest, err := result.Digest(); err == nil {
				t.Fatalf("impossible facts can be resealed: %s", digest)
			}
		})
	}
}

func TestIncompleteFileSemanticsRejected(t *testing.T) {
	original := bindFixture(t)
	cases := []struct {
		name   string
		change func(*FileObservation)
	}{
		{"false_missing", func(f *FileObservation) { f.State = "missing" }},
		{"false_size", func(f *FileObservation) { f.State = "size_mismatch"; f.BytesRead = 0 }},
		{"false_budget", func(f *FileObservation) { f.State = "budget_exceeded"; f.BytesRead = 0 }},
		{"unknown", func(f *FileObservation) { f.State = "not_read" }},
		{"empty_changed", func(f *FileObservation) {
			f.State = "changed"
			f.Before = nil
			f.BytesRead = 0
			f.IdentityState = "changed"
		}},
		{"cancel_hash", func(f *FileObservation) { f.State = "cancelled"; f.ObservedSHA256 = contentHash(nil) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := cloneBinding(t, original)
			f := &result.Files[0]
			f.After = nil
			f.ObservedSHA256 = ""
			f.IdentityState = "unobserved"
			tc.change(f)
			result.BytesRead = f.BytesRead
			result.State, result.Gaps, result.UnmappedFiles = deriveResult(result)
			if _, err := result.Digest(); err == nil {
				t.Fatal("impossible terminal facts accepted")
			}
		})
	}
}

func TestStrictReceiptAndMappingDecoding(t *testing.T) {
	result := bindFixture(t)
	data, err := result.JSON()
	if err != nil {
		t.Fatal(err)
	}
	root := artifactRoot(t)
	cases := []struct {
		name string
		data string
	}{
		{"duplicate", strings.Replace(string(data), `"schema":`, `"schema":"bad","schema":`, 1)},
		{"case", strings.Replace(string(data), `"schema":`, `"Schema":`, 1)},
		{"nested_case", strings.Replace(string(data), `"source_path":`, `"Source_Path":`, 1)},
		{"unknown", strings.Replace(string(data), `"schema":`, `"unknown":true,"schema":`, 1)},
		{"trailing", string(data) + `{}`},
		{"tamper", strings.Replace(string(data), result.BindingSHA256, contentHash(nil), 1)},
		{"oversize", strings.Repeat(" ", MaxReceiptBytes+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(root, tc.name+".json")
			if err := os.WriteFile(path, []byte(tc.data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadBinding(path); err == nil {
				t.Fatal("ambiguous receipt accepted")
			}
		})
	}
	specData, _ := json.Marshal(result.Mapping)
	path := filepath.Join(root, "spec.json")
	if err := os.WriteFile(path, []byte(strings.Replace(string(specData), `"files":`, `"Files":`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSpec(path); err == nil {
		t.Fatal("case-aliased mapping accepted")
	}
}

func TestOptionsAndPathBounds(t *testing.T) {
	for _, options := range []Options{{MaxBytes: -1}, {MaxBytes: HardMaxBytes + 1}, {Timeout: -time.Second}, {Timeout: time.Nanosecond}, {Timeout: HardTimeout + time.Millisecond}} {
		if options.Validate() == nil {
			t.Fatalf("invalid limits accepted: %+v", options)
		}
	}
	if (Options{}).Validate() != nil {
		t.Fatal("defaults rejected")
	}
	for _, path := range []string{"", "../receipt.json", "a\x00b", "C:/a/file.", "C:/a /file", "//server/share/file", "C:/a/file:stream"} {
		if ValidateOutputPath(path) == nil {
			t.Fatalf("invalid path accepted: %q", path)
		}
	}
	if err := ValidateOutputPath(filepath.Join(artifactRoot(t), "missing-parent", "receipt.json")); err == nil {
		t.Fatal("output created missing parents")
	}
}

func TestStoppedReceiptCannotRetainOtherVerifiedHashes(t *testing.T) {
	r, s := artifactFixture(t, 2)
	original, err := Bind(t.Context(), r, s, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"cancelled", "timeout"} {
		t.Run(state, func(t *testing.T) {
			result := cloneBinding(t, original)
			file := &result.Files[0]
			file.State = state
			file.After = nil
			file.ObservedSHA256 = ""
			file.IdentityState = "unobserved"
			result.State, result.Gaps, result.UnmappedFiles = deriveResult(result)
			if _, err := result.Digest(); err == nil {
				t.Fatal("stopped receipt retained another mapped hash")
			}
			invalidateStopped(&result, state)
			result.State, result.Gaps, result.UnmappedFiles = deriveResult(result)
			if _, err := result.Digest(); err != nil {
				t.Fatal("uniform stopped receipt rejected", err)
			}
		})
	}
}

func TestPreflightSizeConflictCannotHaveReads(t *testing.T) {
	original := bindFixture(t)
	for _, state := range []string{"cancelled", "timeout", "changed", "read_error", "budget_exceeded"} {
		t.Run(state, func(t *testing.T) {
			result := cloneBinding(t, original)
			file := &result.Files[0]
			file.Before.SizeBytes++
			file.After = nil
			file.ObservedSHA256 = ""
			file.State = state
			file.IdentityState = "unobserved"
			if state == "changed" {
				file.IdentityState = "changed"
			}
			result.State, result.Gaps, result.UnmappedFiles = deriveResult(result)
			if _, err := result.Digest(); err == nil {
				t.Fatal("known preflight size mismatch allowed reads")
			}
			if state == "read_error" || state == "budget_exceeded" {
				file.BytesRead = 0
				result.BytesRead = 0
				if _, err := result.Digest(); err == nil {
					t.Fatal("known preflight mismatch hidden by different failure")
				}
			}
		})
	}
}
