package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func selectionRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func selectionTree(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			result[path] = "directory"
			return nil
		}
		data, err := os.ReadFile(path)
		if err == nil {
			result[path] = selectionDigest(string(data))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSelectionStatusOrdinaryAndManagedAreQualifiedRedactedAndReadOnly(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "auto-managed"}[managed], func(t *testing.T) {
			f := newSelectionFixture(t, selectionRoot(t), managed)
			before := selectionTree(t, f.source.root)
			value, err := f.source.statusAt(t.Context(), f.library.Name, f.now)
			if err != nil {
				t.Fatal(err)
			}
			got := value.(statusSummary)
			want, err := f.roles.ReviewSelection(f.library.Name, f.records, f.now)
			if err != nil || want.State != "qualified" || !reflect.DeepEqual(got, summarizeSelection(f.library.CurrentRevision, want)) {
				t.Fatal("MCP status differs from authoritative role review", got, want, err)
			}
			if got.Selection == nil || got.Selection.EvidenceSHA256 != f.points[0].Completion.EvidenceSHA256 || got.AdoptionAuthorized {
				t.Fatal("wrong selected evidence or authority", got)
			}
			list, err := f.source.list(t.Context())
			if err != nil || list.(roleList).Roles[0].CandidateCount != 0 {
				t.Fatal("selection changed ordinary attachment semantics", err)
			}
			review, err := f.source.review(t.Context(), f.library.Name)
			if err != nil || len(review.(reviewSummary).Candidates) != 0 {
				t.Fatal("selected evidence leaked into exploration attachments", err)
			}
			assertSelectionRedacted(t, f, got)
			if !reflect.DeepEqual(before, selectionTree(t, f.source.root)) {
				t.Fatal("read-only status mutated files or created a directory")
			}
		})
	}
}

func assertSelectionRedacted(t *testing.T, f selectionFixture, got statusSummary) {
	t.Helper()
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private", "runtime-1", f.source.root, f.points[0].RunID, "check-", "family-", "ownership", "model", "reason"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatalf("private evidence leaked: %q in %s", secret, data)
		}
	}
}

func TestSelectionStatusUnselectedAndInputFailures(t *testing.T) {
	root := selectionRoot(t)
	evidenceLibrary(t, root)
	source, err := newLocalEvidence(root)
	if err != nil {
		t.Fatal(err)
	}
	before := selectionTree(t, root)
	value, err := source.status(t.Context(), "coding")
	if err != nil || value.(statusSummary).State != "unselected" || value.(statusSummary).Selection != nil {
		t.Fatal(value, err)
	}
	if !reflect.DeepEqual(before, selectionTree(t, root)) {
		t.Fatal("absent lifecycle review created state")
	}
	for _, name := range []string{"missing", "../coding", "CODING", ""} {
		if _, err := source.status(t.Context(), name); err == nil {
			t.Fatal("invalid or missing role accepted", name)
		}
	}
	if _, err := source.statusAt(t.Context(), "coding", time.Time{}); err == nil {
		t.Fatal("zero time accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := source.status(ctx, "coding"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored", err)
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if _, err := source.status(t.Context(), "coding"); err == nil {
		t.Fatal("concurrent review admitted")
	}
}

func TestSelectionStatusRechecksFreshnessRevisionAndTwins(t *testing.T) {
	for _, change := range []string{"expired", "clock", "revision", "missing", "history-copy", "record-tamper", "manifest-tamper"} {
		t.Run(change, func(t *testing.T) {
			f := newSelectionFixture(t, selectionRoot(t), true)
			now := f.now
			selected := f.life.Events[len(f.life.Events)-1].Selection.Selected.Attachment.Path
			switch change {
			case "expired":
				now = now.Add(31 * 24 * time.Hour)
			case "clock":
				now = now.Add(-time.Hour)
			case "revision":
				spec := selectionSpec()
				spec.Description += " changed"
				if _, err := f.roles.Define(spec); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(selected); err != nil {
					t.Fatal(err)
				}
			case "history-copy":
				history := filepath.Join(filepath.Dir(selected), ".history")
				entries, err := os.ReadDir(history)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Join(history, entries[0].Name()), filepath.Join(history, "copied.json")); err != nil {
					t.Fatal(err)
				}
			case "record-tamper":
				selectionWrite(t, selected, []byte(`{"schema_version":6}`))
			case "manifest-tamper":
				selectionWrite(t, filepath.Join(filepath.Dir(selected), ".fitr-managed-store.json"), []byte(`{}`))
			}
			got, err := f.source.statusAt(t.Context(), f.library.Name, now)
			if err != nil || got.(statusSummary).State != "stale" {
				t.Fatal("changed selection must become stale", got, err)
			}
		})
	}
}

func selectionWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSelectionStatusWireContractAndOpaqueFailures(t *testing.T) {
	root := selectionRoot(t)
	evidenceLibrary(t, root)
	source, err := newLocalEvidence(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"coding", "private-missing-role"} {
		var output bytes.Buffer
		request := testRequest("tools/call", `"name":"fitr_role_status","arguments":{"role":"`+name+`"}`)
		if err := serve(t.Context(), io.NopCloser(strings.NewReader(request+"\n")), &output, source, "fixture"); err != nil {
			t.Fatal(err)
		}
		if name == "coding" {
			if !strings.Contains(output.String(), `"schema":"fitr.mcp.role-status.v1"`) || !strings.Contains(output.String(), `"adoption_authorized":false`) {
				t.Fatal(output.String())
			}
		} else if strings.Contains(output.String(), "private") || !strings.Contains(output.String(), "Inspect it with fitr role status locally.") {
			t.Fatal("failure not redacted", output.String())
		}
	}
	schema := statusSchema()
	if schema["additionalProperties"] != false {
		t.Fatal("output schema is open")
	}
	properties := schema["properties"].(map[string]any)
	if len(properties) != 9 || properties["selection"].(map[string]any)["additionalProperties"] != false {
		t.Fatal("unexpected output fields", properties)
	}
}

func TestSelectionStatusDoesNotRequireObsoleteManagedStores(t *testing.T) {
	first := newSelectionFixture(t, selectionRoot(t), true)
	second := newSelectionFixtureAt(t, first.source.root, true, first.now.Add(time.Minute), "private-next-confirmation")
	obsolete := filepath.Join(first.source.root, ".evidence-stores", first.storeID)
	if err := os.Rename(obsolete, filepath.Join(selectionRoot(t), "obsolete-store")); err != nil {
		t.Fatal(err)
	}
	want, err := second.roles.ReviewSelection(second.library.Name, second.records, second.now)
	if err != nil || want.State != "qualified" {
		t.Fatal("CLI status must retain the newer incumbent", want, err)
	}
	got, err := second.source.statusAt(t.Context(), second.library.Name, second.now)
	if err != nil || !reflect.DeepEqual(got, summarizeSelection(second.library.CurrentRevision, want)) {
		t.Fatal("obsolete evidence blocked a valid current selection", got, err)
	}
	current := filepath.Join(first.source.root, ".evidence-stores", second.storeID)
	if err := os.Rename(current, filepath.Join(selectionRoot(t), "current-store")); err != nil {
		t.Fatal(err)
	}
	if got, err := second.source.statusAt(t.Context(), second.library.Name, second.now); err == nil {
		t.Fatal("missing current store must fail the bounded preflight", got)
	}
}
