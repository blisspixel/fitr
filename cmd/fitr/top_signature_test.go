package main

import (
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/role"
	"github.com/blisspixel/fitr/internal/top"
)

func TestRoleSignatureAttachesOnlyTheNamedRun(t *testing.T) {
	status := role.SelectionStatus{
		Role: "coding", State: "qualified",
		Selection: &role.SelectionReceipt{
			Selected: role.ConfirmationPoint{
				Model:      record.ModelIdentity{Resolved: "qwen3:8b"},
				StartedAt:  "2026-10-17T15:04:05Z",
				Attachment: role.Attachment{RunID: "run-a"},
			},
			ExpiresAt: "2026-11-16T00:00:00Z",
		},
	}
	present := map[string]bool{"run-a": true}
	sig, ok := signatureForSnapshot(status, present)
	if !ok || sig.Word != "fitted" || sig.RunID != "run-a" || !strings.Contains(sig.Body, "measured 2026-10-17") {
		t.Fatalf("signature = %+v ok=%v", sig, ok)
	}
	if _, ok := signatureForSnapshot(status, map[string]bool{}); ok {
		t.Fatal("a run that is not on screen inherited the qualification")
	}
	status.State = "stale"
	sig, ok = signatureForSnapshot(status, present)
	if !ok || sig.Word != "refit" {
		t.Fatalf("stale signature = %+v ok=%v", sig, ok)
	}
	status.Selection = nil
	if _, ok := signatureForSnapshot(status, present); ok {
		t.Fatal("unmeasured selection attached")
	}
}

func TestAttachRoleSignaturesLeavesMeasurementsUntouched(t *testing.T) {
	t.Setenv("FITR_RESULTS", t.TempDir())
	snapshot := top.Snapshot{UpdatedAt: time.Now().UTC(), History: []top.Run{{ID: "run-a", Model: "qwen3:8b"}}}
	attachRoleSignatures(&snapshot)
	if len(snapshot.Signatures) != 0 {
		t.Fatalf("empty role store invented a line: %+v", snapshot.Signatures)
	}
}
