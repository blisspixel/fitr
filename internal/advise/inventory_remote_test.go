package advise

import "testing"

func TestRemoteInventoryOverridesLocalEvidenceAndDefaultAliases(t *testing.T) {
	query := InventoryQuery{
		Tags: []InstalledModel{
			{Name: "cloud", ArtifactDigest: inventoryTestDigest, Size: 40 << 30},
			{Name: "cloud:Latest", Remote: true},
			{Name: "local", ArtifactDigest: inventoryTestDigest},
		},
		Loaded: []string{"cloud", "local"}, CurrentKey: "k", HaveGB: 24, HaveSrc: "nvidia-smi",
		Serving: map[string]int{"cloud": 8192, "local": 8192},
		Evidence: []InventoryEvidence{
			{Model: "cloud", ArtifactDigest: inventoryTestDigest, DeviceKey: "k", Level: "default", NumCtx: 8192, Repeats: 3},
			{Model: "local", ArtifactDigest: inventoryTestDigest, DeviceKey: "k", Level: "default", NumCtx: 8192, Repeats: 3},
		},
	}
	rows := Join(query).Rows
	if len(rows) != 3 || rows[0].Model != "local" || rows[0].State != StateMeasured {
		t.Fatalf("local evidence or inventory order changed: %+v", rows)
	}
	for _, row := range rows[1:] {
		if row.State != StateRemote || row.Loaded || row.SizeB != 0 || row.Fit != "" || row.Ctx != "" ||
			row.MeasuredCtx != 0 || row.ServingKnown || row.ServingCtx != 0 || row.Windows != "" || row.Shape != "" ||
			row.Next != "choose local model" {
			t.Fatalf("remote row acquired local evidence or advice: %+v", row)
		}
	}
}
