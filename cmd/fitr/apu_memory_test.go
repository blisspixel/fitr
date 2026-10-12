package main

import (
	"context"
	"encoding/json"
	"path/filepath"
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
		name, source, budget string
		gb, want             float64
	}{
		{"carveout remains unknown", "drm sysfs", "", 2, 0},
		{"GTT mapping observation", device.AMDGTTMappingSource, "", 30, 30},
		{"operator ceiling survives", "drm sysfs", "8", 2, 8},
	} {
		t.Run(test.name, func(t *testing.T) {
			reads := 0
			services := sourceScreenServices(t, "apache-2.0", sourceScreenHeader(t), &reads)
			services.detect = func(context.Context) device.Fingerprint {
				if test.budget != "" {
					t.Fatal("explicit component ceiling consulted automatic capacity")
				}
				return device.Fingerprint{GPU: "AMD Radeon 780M", RAMGb: 60,
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
					t.Fatalf("carveout became source addressable capacity: code %d, %+v", code, report.Projection)
				}
			} else if got == nil || *got != int64(test.want*advise.GiB) {
				t.Fatalf("source capacity = %v, want %v GiB", got, test.want)
			}
		})
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
