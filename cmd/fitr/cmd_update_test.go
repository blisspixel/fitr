package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

func TestUpdateEventJSONContractContainsNoLocalPaths(t *testing.T) {
	event := updateEvent{
		Event: "update", Schema: "fitr.update.v1", Status: "staged",
		Current: "0.9.10", Target: "0.9.11", Asset: "fitr-windows-amd64.exe",
		SHA256:      "sha256:" + strings.Repeat("a", 64),
		Release:     "https://github.com/blisspixel/fitr/releases/tag/v0.9.11",
		Replacement: "after_exit",
	}
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	emitUpdate("json", event)
	_ = w.Close()
	os.Stdout = old
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	var got updateEvent
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got != event {
		t.Fatalf("update event = %+v, want %+v", got, event)
	}
	text := strings.ToLower(string(data))
	for _, forbidden := range []string{"executable_path", "staged_path", "temp", `c:\\`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("update event exposed %q: %s", forbidden, data)
		}
	}
}

func TestUpdatePlainOutputDistinguishesStagedReplacement(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	emitUpdate("plain", updateEvent{
		Status: "staged", Target: "0.9.11", SHA256: "sha256:abc",
		Release: "https://github.com/blisspixel/fitr/releases/tag/v0.9.11",
	})
	_ = w.Close()
	os.Stdout = old
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{"verified and staged", "after this process exits", "checksum", "release"} {
		if !strings.Contains(got, want) {
			t.Fatalf("plain update missing %q:\n%s", want, got)
		}
	}
}
