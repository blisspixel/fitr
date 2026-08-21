package render

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWriteInventoryPlainCarriesStateInText(t *testing.T) {
	var out strings.Builder
	WriteInventory(&out, Inventory{
		Fitr: "0.6.0", CPU: "Example CPU  (8 logical)", GPU: "Demo GPU",
		MemoryGB: 16, MemorySource: "nvidia-smi",
		RuntimeKind: "ollama", RuntimeURL: "http://127.0.0.1:11434",
		Profile: "default", Uncalibrated: true,
		Rows: []InventoryRow{
			{Model: "qwen3:8b", State: "measured", SizeB: 5 << 30, Next: "fitr view qwen3:8b"},
			{Model: "gemma4:12b", State: "unproven", SizeB: 8 << 30, Loaded: true, Next: "fitr advise gemma4:12b"},
			{Model: "llama3.1:70b", State: "incompatible", SizeB: 40 << 30, Next: "try a smaller quant", Note: "weights 40.0 GB exceed 16.0 GB (nvidia-smi)"},
		},
	}, "plain")
	got := out.String()
	for _, want := range []string{
		"fitr 0.6.0", "cpu", "gpu", "ollama", "UNCALIBRATED",
		"measured", "unproven", "incompatible",
		"fitr view qwen3:8b", "fitr advise gemma4:12b", "try a smaller quant",
		"* gemma4:12b", "never a recommendation",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("plain inventory missing %q:\n%s", want, got)
		}
	}
	if strings.ContainsRune(got, '\x1b') {
		t.Fatalf("plain inventory leaked escapes:\n%s", got)
	}
	if strings.Contains(got, "fitr run llama3.1:70b") {
		t.Fatal("incompatible row must not suggest run")
	}
}

func TestWriteInventoryEmptyRuntime(t *testing.T) {
	var out strings.Builder
	WriteInventory(&out, Inventory{Fitr: "0.6.0", Empty: "none reachable"}, "plain")
	got := out.String()
	if !strings.Contains(got, "none reachable") || !strings.Contains(got, "fitr advise <model>") {
		t.Fatalf("empty runtime:\n%s", got)
	}
	if strings.Contains(got, "MODEL") {
		t.Fatalf("no table without a runtime:\n%s", got)
	}
}

func TestWriteInventoryJSONSchema(t *testing.T) {
	var out strings.Builder
	WriteInventory(&out, Inventory{
		Fitr: "0.6.0", RuntimeKind: "llama-server", RuntimeURL: "http://127.0.0.1:8080",
		Rows: []InventoryRow{{Model: "only", State: "unproven", Next: "fitr advise only"}},
	}, "json")
	var payload struct {
		Schema  string `json:"schema"`
		Runtime struct {
			Kind string `json:"kind"`
		} `json:"runtime"`
		Rows []struct {
			Model string `json:"model"`
			State string `json:"state"`
			Next  string `json:"next"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out.String()), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Schema != InventorySchema || payload.Runtime.Kind != "llama-server" {
		t.Fatalf("json envelope = %+v", payload)
	}
	if len(payload.Rows) != 1 || payload.Rows[0].State != "unproven" {
		t.Fatalf("json rows = %+v", payload.Rows)
	}
}
