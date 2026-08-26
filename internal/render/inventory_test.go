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
		Warnings: []string{"1 saved result file could not be trusted"},
		Rows: []InventoryRow{
			{Model: "qwen3:8b", State: "measured", SizeB: 5 << 30, Ctx: "16k/8k", Next: "fitr apply qwen3:8b",
				Note: "measured ctx=16384; serving ctx=8192", Windows: "2k ok | 4k ok | 8k ok | *16k ok | 32k no"},
			{Model: "gemma4:12b", State: "unproven", SizeB: 8 << 30, Loaded: true, Next: "fitr advise gemma4:12b"},
			{Model: "llama3.1:70b", State: "incompatible", SizeB: 40 << 30, Next: "try a smaller quant", Note: "weights 40.0 GB exceed 16.0 GB (nvidia-smi)"},
		},
	}, "plain")
	got := out.String()
	for _, want := range []string{
		"fitr 0.6.0", "cpu", "gpu", "ollama", "UNCALIBRATED",
		"measured", "unproven", "incompatible", "CTX", "16k/8k", "*16k ok",
		// The verb survives; the model name and the binary's own name do not,
		// because the row already carries both.
		"apply", "advise", "try a smaller quant",
		"* gemma4:12b", "never a recommendation",
		"fitr advise qwen3:8b", // the one worked example in the legend
		"warning", "could not be trusted",
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
	for _, line := range strings.Split(got, "\n") {
		if n := len([]rune(line)); n > DefaultWidth {
			t.Errorf("inventory line is %d cols against %d: %q", n, DefaultWidth, line)
		}
	}
}

// The fit tier lives in the state word now. An unmeasured model that advise has
// already sized says so in one column instead of leaving a second column at "-".
func TestInventoryStateCarriesTheFitTier(t *testing.T) {
	cases := []struct {
		state, fit, want string
	}{
		{"unproven", "compatible", "fits, unproven"},
		{"unproven", "low_memory", "tight, unproven"},
		{"unproven", "", "unproven"},
		{"unproven", "-", "unproven"},
		{"measured", "compatible", "measured"},
		{"incompatible", "", "incompatible"},
	}
	for _, tc := range cases {
		got, _ := inventoryState(InventoryRow{State: tc.state, Fit: tc.fit}, palette{})
		if got != tc.want {
			t.Errorf("state=%q fit=%q -> %q, want %q", tc.state, tc.fit, got, tc.want)
		}
		if len([]rune(got)) > invStateWidth {
			t.Errorf("state %q is %d cols, column is %d", got, len([]rune(got)), invStateWidth)
		}
	}
}

func TestShortNextDropsWhatTheRowAlreadyShows(t *testing.T) {
	cases := []struct{ next, model, want string }{
		{"fitr run mistral-openorca:7b -k 3", "mistral-openorca:7b", "run -k 3"},
		{"fitr advise qwen3-coder:30b", "qwen3-coder:30b", "advise"},
		{`fitr run "model with spaces"`, "model with spaces", "run"},
		{"try a smaller quant", "llama3:70b", "try a smaller quant"},
		{"fitr apply qwen3:8b --ctx 16384", "qwen3:8b", "apply --ctx 16384"},
		{"", "m", "-"},
		// A command that is nothing but the binary and the model still has to
		// render as something a reader can act on.
		{"fitr qwen3:8b", "qwen3:8b", "-"},
	}
	for _, tc := range cases {
		if got := shortNext(tc.next, tc.model); got != tc.want {
			t.Errorf("shortNext(%q, %q) = %q, want %q", tc.next, tc.model, got, tc.want)
		}
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
		Schema   string   `json:"schema"`
		Warnings []string `json:"warnings"`
		Runtime  struct {
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

func TestWriteInventoryJSONIncludesEvidenceWarnings(t *testing.T) {
	var out strings.Builder
	WriteInventory(&out, Inventory{Warnings: []string{"damaged evidence"}}, "json")
	var payload struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(out.String()), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Warnings) != 1 || payload.Warnings[0] != "damaged evidence" {
		t.Fatalf("warnings = %v", payload.Warnings)
	}
}
