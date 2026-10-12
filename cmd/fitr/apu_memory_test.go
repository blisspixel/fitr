package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/advise"
	"github.com/blisspixel/fitr/internal/capacity"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/render"
)

func TestAPUObservedCPUPlacementUsesHostCapacity(t *testing.T) {
	fp := device.Fingerprint{GPU: "AMD Radeon 780M", InferenceDevice: "CPU",
		RAMGb: 60, VRAMGb: 2, VRAMSource: "drm sysfs"}
	domain := capacityDomain(fp)
	if domain != capacity.DomainHost {
		t.Fatalf("observed CPU runtime selected %q, want host memory", domain)
	}
	got := addressableObservation(fp, domain, time.Unix(1, 0))
	if got == nil || got.Bytes != 60*advise.GiB || got.Source != "system physical memory" {
		t.Fatalf("CPU addressable observation = %+v, want physical host memory", got)
	}
}

func TestAPUSourceProjectionUsesSameCapacitySelection(t *testing.T) {
	for _, test := range []struct {
		name, gpu, source, budget string
		gb, want                  float64
	}{
		{"carveout remains unknown", "AMD Radeon 780M", "drm sysfs", "", 2, 0},
		{"GTT mapping observation", "AMD Radeon 780M", device.AMDGTTMappingSource, "", 30, 30},
		{"operator ceiling survives", "AMD Radeon 780M", "drm sysfs", "8", 2, 8},
		{"missing GPU remains unknown", "", "", "", 0, 0},
		{"explicit component ceiling without GPU", "", "", "8", 0, 8},
	} {
		t.Run(test.name, func(t *testing.T) {
			reads := 0
			services := sourceScreenServices(t, "apache-2.0", sourceScreenHeader(t), &reads)
			services.detect = func(context.Context) device.Fingerprint {
				if test.budget != "" {
					t.Fatal("explicit component ceiling consulted automatic capacity")
				}
				return device.Fingerprint{GPU: test.gpu, RAMGb: 60,
					VRAMGb: test.gb, VRAMSource: test.source}
			}
			args := []string{"resolve", "hf", "--repo", "owner/model", "--revision", "main",
				"--file", "model.gguf", "--out", filepath.Join(sourceTestDirectory(t), "receipt.json"),
				"--fit", "--ctx", "4096", "--display", "json"}
			if test.budget != "" {
				args = append(args, "--fit-budget-gb", test.budget)
			}
			output, code := captureTopStdout(t, func() int {
				return cmdSourceWithServices(context.Background(), args, services)
			})
			var report sourceFitOutput
			if err := json.Unmarshal([]byte(output), &report); err != nil || report.Projection == nil || reads != 1 {
				t.Fatalf("source projection missing: code %d, reads %d, error %v, output %s", code, reads, err, output)
			}
			got := report.Projection.CapacityBytes
			if test.want == 0 {
				if got != nil || code != exitUnresolved {
					t.Fatalf("missing capacity became source addressable capacity: code %d, %+v", code, report.Projection)
				}
			} else if got == nil || *got != int64(test.want*advise.GiB) {
				t.Fatalf("source capacity = %v, want %v GiB", got, test.want)
			}
		})
	}
}

func TestAPUMissingDomainRefusesCapacityPlanWithOrWithoutBudget(t *testing.T) {
	for _, explicitBudget := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic", true: "explicit byte budget"}[explicitBudget], func(t *testing.T) {
			display := render.New("none")
			defer display.Close()
			run := runExecution{ctx: memoryProbeForbiddenContext{context.Background()}, display: display,
				result: &Result{Device: device.Fingerprint{RAMGb: 60, InferenceDevice: "unknown"},
					TaskPlan: record.TaskPlan{Memory: true}}}
			run.resolved.Identity.Value = integrationDigest()
			budget := 8.0
			if explicitBudget {
				run.opts.capacityBudgetGB = &budget
			}
			defer refuseMemoryProbePanic(t)
			if err := run.prepareCapacityPlan(); err == nil || !strings.Contains(err.Error(), "memory resource domain is unobserved") {
				t.Fatalf("unknown serving memory sealed or produced the wrong refusal: %v", err)
			}
			if run.result.CapacityPlan != nil {
				t.Fatal("refused capacity plan was retained")
			}
		})
	}
}

func TestAPUUnknownDomainDoesNotReadHostOrAcceleratorMemory(t *testing.T) {
	probe := "nvidia-smi"
	if runtime.GOOS == "windows" {
		probe += ".exe"
	}
	root := t.TempDir()
	// This executable is only a LookPath sentinel. Any attempted observation
	// fails at its context deadline before a process can execute.
	if err := os.WriteFile(filepath.Join(root, probe), nil, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root)
	fp := device.Fingerprint{GPU: "NVIDIA RTX 4090", RAMGb: 60, VRAMGb: 24, VRAMSource: "nvidia-smi"}
	for _, domain := range []capacity.ResourceDomain{"", "unknown"} {
		t.Run(string(domain), func(t *testing.T) {
			defer refuseMemoryProbePanic(t)
			at := time.Unix(1, 0)
			if got := addressableObservation(fp, domain, at); got != nil {
				t.Errorf("unknown-domain addressable observation = %+v", got)
			}
			ctx := memoryProbeForbiddenContext{context.Background()}
			if got := availableObservation(ctx, fp, domain, at); got != nil {
				t.Errorf("unknown-domain availability = %+v", got)
			}
			if got := containerObservation(domain, at); got != nil {
				t.Errorf("unknown-domain container observation = %+v", got)
			}
		})
	}
}

func TestAPUUnknownOwnedDomainRefusesBeforeAcceleratorProbe(t *testing.T) {
	defer refuseMemoryProbePanic(t)
	ctx := memoryProbeForbiddenContext{context.Background()}
	got, err := autoPlacementAvailable(ctx, "", time.Unix(1, 0), device.Fingerprint{})
	if got != nil || err == nil || !strings.Contains(err.Error(), "memory resource domain") {
		t.Fatalf("unknown owned domain = %+v, error %v", got, err)
	}
}

type memoryProbeForbiddenContext struct{ context.Context }

func (memoryProbeForbiddenContext) Deadline() (time.Time, bool) {
	panic("memory probe consulted a context before the resource domain was established")
}

func refuseMemoryProbePanic(t *testing.T) {
	t.Helper()
	if recovered := recover(); recovered != nil {
		t.Fatalf("memory observation started before domain refusal: %v", recovered)
	}
}

func TestAPUGTTPlanKeepsHostAvailabilityOutOfMappingDomain(t *testing.T) {
	fp := device.Fingerprint{GPU: "AMD Phoenix1", RAMGb: 60, VRAMGb: 30,
		VRAMSource: device.AMDGTTMappingSource}
	if got := availableObservation(context.Background(), fp, capacity.DomainUnified, time.Unix(1, 0)); got != nil {
		t.Fatalf("host availability was copied into GTT mapping domain: %+v", got)
	}
	budget := 24.0
	display := render.New("none")
	defer display.Close()
	run := runExecution{ctx: context.Background(), display: display,
		opts:   runOpts{capacityBudgetGB: &budget},
		result: &Result{Device: fp, TaskPlan: record.TaskPlan{Memory: true}}}
	run.resolved.Identity.Value = integrationDigest()
	if err := run.prepareCapacityPlan(); err != nil {
		t.Fatal(err)
	}
	policy := run.result.CapacityPlan.Policy
	if policy.CurrentAvailable != nil || policy.UsableBudgetBytes == nil ||
		*policy.UsableBudgetBytes != 24*advise.GiB || policy.ResourceDomain != capacity.DomainUnified {
		t.Fatalf("explicit GTT capacity policy = %+v", policy)
	}
	reserve := 1.0
	run.opts.capacityBudgetGB, run.opts.capacityReserveGB = nil, &reserve
	if err := run.prepareCapacityPlan(); err == nil || !strings.Contains(err.Error(), "current memory reading") {
		t.Fatalf("GTT reserve without allocation headroom = %v", err)
	}
}

func TestAPUOverrideStillRequiresResidentContext(t *testing.T) {
	in := advise.Input{HaveGB: 24, HaveSrc: "--vram-gb", Ctx: 8192,
		ResidentB: 7 * advise.GiB, ResidentSrc: "runtime status"}
	applyAdviseDeviceEvidence(&in, device.Fingerprint{
		GPU: "AMD Radeon 780M", VRAMGb: 2, VRAMSource: "drm sysfs",
	})
	got := advise.Evaluate(in)
	if got.Tier != advise.Skip || got.Source == "runtime status" {
		t.Fatalf("unbound shared-memory resident receipt became compatible: %+v", got)
	}
}
