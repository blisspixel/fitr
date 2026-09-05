package main

import (
	"context"
	"errors"
	"testing"

	"github.com/blisspixel/fitr/internal/capacity"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/render"
)

func TestSealedPreparationRejectsBeforeLoadingOrInference(t *testing.T) {
	backend := &runIntegrationBackend{digest: integrationDigest(), effectiveCtx: eval.NumCtx}
	display := render.New("none")
	defer display.Close()
	want := errors.New("sealed task protocol changed")
	called := false
	result, err := execute(context.Background(), backend, "model", runOpts{
		level: "full", profile: "default", reps: 3, checksReps: 3,
		validatePrepared: func(run *runExecution) error {
			called = true
			if run.result == nil || run.provenance.TaskSetSHA256 == "" || run.result.TaskPlan.CheckPlanSHA256 == "" {
				t.Fatal("preflight ran before definitions were prepared")
			}
			return want
		},
	}, display)
	if !called || !errors.Is(err, want) || result != nil {
		t.Fatalf("preflight result=%v error=%v called=%v", result, err, called)
	}
	if backend.generateCalls != 0 || backend.stopCalls != 0 {
		t.Fatalf("changed protocol altered runtime: generate=%d stop=%d", backend.generateCalls, backend.stopCalls)
	}
}

func TestRolePreflightUsesCurrentAvailabilityAndContainerLimit(t *testing.T) {
	budget := int64(24)
	base := capacity.Policy{
		CurrentAvailable:  &capacity.MemoryObservation{Bytes: 22},
		UsableBudgetBytes: &budget,
	}
	if err := requireRoleHeadroom(base, 20); err != nil {
		t.Fatal(err)
	}
	for _, policy := range []capacity.Policy{
		{},
		{CurrentAvailable: &capacity.MemoryObservation{Bytes: 18}, UsableBudgetBytes: &budget},
		{CurrentAvailable: base.CurrentAvailable, UsableBudgetBytes: &budget, Container: &capacity.ContainerReceipt{HeadroomBytes: 20}},
	} {
		if err := requireRoleHeadroom(policy, 20); err == nil {
			t.Fatal("missing or insufficient current capacity permitted inference")
		}
	}
	if err := requireRoleHeadroom(base, 0); err == nil {
		t.Fatal("unknown resident allocation permitted inference")
	}
}

func TestChangedConfirmedContextStopsBeforeBattery(t *testing.T) {
	backend := &runIntegrationBackend{digest: integrationDigest(), effectiveCtx: eval.NumCtx}
	display := render.New("none")
	defer display.Close()
	want := errors.New("effective context changed")
	called, before := false, 0
	result, err := execute(context.Background(), backend, "model", runOpts{
		level: "full", profile: "default", reps: 3, checksReps: 3,
		validateContext: func(run *runExecution) error {
			called, before = true, backend.generateCalls
			if run.result.DeviceV2 == nil || run.result.Manifest == nil {
				t.Fatal("context guard ran before verified identity was sealed")
			}
			return want
		},
	}, display)
	if !called || !errors.Is(err, want) || result != nil || backend.generateCalls != before {
		t.Fatalf("battery continued after context guard: result=%v error=%v calls=%d before=%d", result, err, backend.generateCalls, before)
	}
}
