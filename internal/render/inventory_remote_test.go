package render

import (
	"strings"
	"testing"
)

func TestInventoryRemoteStateAndLocalExample(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	rows := []InventoryRow{
		{Model: "cloud-model", State: "remote", Next: "choose local model", Note: "Runtime reports remote execution; excluded from local fit and measurement."},
		{Model: "local-model", State: "unproven", Next: "fitr advise local-model"},
	}
	for _, mode := range []string{"plain", "rich", "json"} {
		var out strings.Builder
		WriteInventory(&out, Inventory{Rows: rows}, mode)
		text := out.String()
		if !strings.Contains(text, "remote") || !strings.Contains(text, "choose local model") ||
			strings.Contains(text, "fitr advise cloud-model") || strings.Contains(text, "fitr run cloud-model") {
			t.Fatalf("%s output lost provenance or recommended remote inference: %s", mode, text)
		}
	}
	if exampleModel(rows) != "local-model" || exampleModel(rows[:1]) != "<model>" {
		t.Fatal("footer example selected a remote candidate")
	}
}
