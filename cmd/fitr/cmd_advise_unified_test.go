package main

import (
	"context"
	"testing"

	"github.com/blisspixel/fitr/internal/advise"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/ollama"
)

type advisePSBackend struct {
	*runIntegrationBackend
	running []ollama.RunningModel
}

func (b *advisePSBackend) PS(context.Context) ([]ollama.RunningModel, error) {
	return b.running, nil
}

func TestApplyAdviseDeviceEvidencePreservesSharedMemoryDetectionUnderOverride(t *testing.T) {
	for _, source := range []string{
		device.NVIDIAUnifiedMemorySource, device.NVIDIAUnifiedProbeSource, "nvidia-smi",
	} {
		in := advise.Input{HaveGB: 96, HaveSrc: "--vram-gb"}
		applyAdviseDeviceEvidence(&in, device.Fingerprint{
			GPU: "NVIDIA GB10", VRAMGb: 121.7, VRAMSource: source,
		})
		if !in.NVIDIAUnifiedMemory || in.HaveGB != 96 || in.HaveSrc != "--vram-gb" {
			t.Fatalf("source %q changed declared budget or lost shared-memory fact: %+v", source, in)
		}
	}
}

func TestApplyAdviseDeviceEvidenceDoesNotMarkDiscreteNVIDIA(t *testing.T) {
	in := advise.Input{}
	applyAdviseDeviceEvidence(&in, device.Fingerprint{
		GPU: "NVIDIA RTX 5090", VRAMGb: 32, VRAMSource: "nvidia-smi",
	})
	if in.NVIDIAUnifiedMemory {
		t.Fatalf("discrete NVIDIA marked shared memory: %+v", in)
	}
}

func TestParseAdviseLoadRequiresExplicitContext(t *testing.T) {
	_, code, ok := parseAdviseCommand([]string{"model", "--load"})
	if ok || code != exitUsage {
		t.Fatalf("load without context = ok %v code %d, want usage", ok, code)
	}
}

func TestReadResidentAdviseSourcePreservesRuntimeContext(t *testing.T) {
	backend := &advisePSBackend{
		runIntegrationBackend: &runIntegrationBackend{},
		running: []ollama.RunningModel{{
			Name: "model", Size: 7 * advise.GiB, ContextLength: 16384,
		}},
	}
	in := advise.Input{ResidentB: 6 * advise.GiB, ResidentCtx: 4096, ResidentSrc: "stale receipt"}
	readResidentAdviseSource(context.Background(), backend, "model", &in, "runtime status")
	if in.ResidentB != 7*advise.GiB || in.ResidentCtx != 16384 || in.ResidentSrc != "runtime status" {
		t.Fatalf("runtime resident receipt = %+v", in)
	}
}

func TestReadResidentAdviseSourceUsesReportedContextNotRequest(t *testing.T) {
	backend := &advisePSBackend{
		runIntegrationBackend: &runIntegrationBackend{},
		running: []ollama.RunningModel{{
			Name: "model", Size: 7 * advise.GiB, ContextLength: 32768,
		}},
	}
	in := advise.Input{}
	readResidentAdviseSource(context.Background(), backend, "model", &in, "load receipt")
	if in.ResidentCtx != 32768 {
		t.Fatalf("resident context = %d, want runtime-reported context 32768", in.ResidentCtx)
	}
}

func TestReadResidentAdviseSourceRejectsConflictingAliases(t *testing.T) {
	backend := &advisePSBackend{
		runIntegrationBackend: &runIntegrationBackend{},
		running: []ollama.RunningModel{
			{Name: "model", Size: 7 * advise.GiB, ContextLength: 8192},
			{Name: "model:latest", Size: 8 * advise.GiB, ContextLength: 16384},
		},
	}
	in := advise.Input{ResidentB: 6 * advise.GiB, ResidentCtx: 4096, ResidentSrc: "stale receipt"}
	readResidentAdviseSource(context.Background(), backend, "model", &in, "runtime status")
	if in.ResidentB != 0 || in.ResidentCtx != 0 || in.ResidentSrc != "" {
		t.Fatalf("conflicting aliases produced a resident receipt: %+v", in)
	}
}
