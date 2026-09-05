package role

import (
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/record"
)

func ownedPreflightFixture(t *testing.T) (ConfirmationPlan, []*record.Record, time.Time) {
	t.Helper()
	plan, points, now := roleRuntimeFixture(t)
	for _, point := range points {
		point.Device.GPUBackend = "vulkan"
		point.DeviceV2.Device.GPUBackend = "vulkan"
		var err error
		point.DeviceKey, err = point.DeviceV2.ComparabilityKey()
		if err != nil {
			t.Fatal(err)
		}
	}
	plan.Protocol.DeviceKey = points[0].Device.Key()
	plan.Protocol.ComparabilityKey = points[0].DeviceKey
	plan.PlanSHA256 = ""
	var err error
	plan.PlanSHA256, err = confirmationDigest(ConfirmationPlanSchema, plan)
	if err != nil {
		t.Fatal(err)
	}
	for index, point := range points {
		binding := ConfirmationPlanBinding(plan, index+1)
		point.Experiment = &binding
		points[index] = roleConfirmationReseal(t, point)
	}
	return plan, points, now
}

func TestOwnedConfirmationPreflightDefersOnlyUnobservedBackend(t *testing.T) {
	plan, points, _ := ownedPreflightFixture(t)
	point := roleConfirmationClone(t, points[0])
	identity, provenance := point.Manifest.Model, *point.Manifest.Provenance
	point.Manifest, point.Completion, point.DeviceV2 = nil, nil, nil
	point.Device.GPUBackend = ""
	if err := ValidatePreparedOwnedConfirmationPoint(plan, 1, point, identity, provenance); err != nil {
		t.Fatal("first owned load was blocked before its backend could be observed", err)
	}
	if point.Device.GPUBackend != "" || point.DeviceV2 != nil {
		t.Fatal("preflight invented an observation")
	}
	if err := ValidatePreparedConfirmationPoint(plan, 1, point, identity, provenance); err == nil {
		t.Fatal("strict completed-evidence check accepted the missing backend")
	}
	for _, mutation := range []string{"seed", "runtime", "known backend", "missing binding"} {
		t.Run(mutation, func(t *testing.T) {
			changed := roleConfirmationClone(t, point)
			switch mutation {
			case "seed":
				changed.SeedSet = "different-seed"
			case "runtime":
				changed.RuntimeBinding.LaunchConfigurationSHA256 = "sha256:" + strings.Repeat("9", 64)
			case "known backend":
				changed.Device.GPUBackend = "cuda"
			case "missing binding":
				changed.RuntimeBinding = nil
			}
			if err := ValidatePreparedOwnedConfirmationPoint(plan, 1, changed, identity, provenance); err == nil {
				t.Fatal("owned preflight accepted changed preparation")
			}
		})
	}
}

func TestOwnedConfirmationPostLoadAndBundleRemainStrict(t *testing.T) {
	plan, points, now := ownedPreflightFixture(t)
	if bundle, err := NewConfirmationBundle(plan, points, now); err != nil || bundle.Report.State != "confirmed" {
		t.Fatal("matching observed fixture did not confirm", err)
	}
	point := points[0]
	if err := ValidatePreparedOwnedConfirmationPoint(plan, 1, point, point.Manifest.Model, *point.Manifest.Provenance); err == nil {
		t.Fatal("pre-load API accepted sealed evidence")
	}
	for _, backend := range []string{"", "cuda"} {
		t.Run("backend="+backend, func(t *testing.T) {
			changed := roleConfirmationClone(t, point)
			changed.Device.GPUBackend = backend
			changed.DeviceV2.Device.GPUBackend = backend
			var err error
			changed.DeviceKey, err = changed.DeviceV2.ComparabilityKey()
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateConfirmationContextPoint(plan, changed); err == nil {
				t.Fatal("post-load check accepted an absent or different observed backend")
			}
			changed = roleConfirmationReseal(t, changed)
			if _, err := NewConfirmationBundle(plan, []*record.Record{changed, points[1]}, now); err == nil {
				t.Fatal("fresh signed evidence bypassed the final backend check")
			}
		})
	}
}

func TestUnownedConfirmationRetainsStrictPreparation(t *testing.T) {
	plan, points, _ := roleConfirmationFixture(t)
	point := points[0]
	identity, provenance := point.Manifest.Model, *point.Manifest.Provenance
	if err := ValidatePreparedConfirmationPoint(plan, 1, point, identity, provenance); err != nil {
		t.Fatal(err)
	}
	point.Manifest, point.Completion, point.DeviceV2 = nil, nil, nil
	if err := ValidatePreparedOwnedConfirmationPoint(plan, 1, point, identity, provenance); err == nil {
		t.Fatal("unowned preparation used the owned exception")
	}
	point.Device.GPUBackend = "changed-backend"
	if err := ValidatePreparedConfirmationPoint(plan, 1, point, identity, provenance); err == nil {
		t.Fatal("ordinary preparation stopped enforcing its original device key")
	}
}
