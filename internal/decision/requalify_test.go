package decision

import (
	"encoding/json"
	"strings"
	"testing"
)

func observed(value string) IdentityFact {
	return IdentityFact{State: FactObserved, Value: value}
}

func installedStack() InstallationIdentity {
	return InstallationIdentity{
		ArtifactDigest:   observed("sha256:abc"),
		Quant:            observed("Q4_K_M"),
		Runtime:          observed("ollama 0.40.7"),
		ComputeBackend:   observed("rocm"),
		Driver:           observed("amdgpu"),
		OS:               observed("linux"),
		Kernel:           observed("6.12.0"),
		Hardware:         observed("apu"),
		EffectiveContext: observed("2048"),
		Concurrency:      observed("1"),
		Residency:        observed("one-model"),
		Harness:          observed("none"),
	}
}

func claim(id string, deps ...string) RequalificationClaimInput {
	return RequalificationClaimInput{ID: id, DependsOn: deps}
}

func TestChangedRuntimeStalesOnlyTheDependentClaim(t *testing.T) {
	prior := installedStack()
	current := installedStack()
	current.Runtime = observed("ollama 0.40.8")
	current.Presentation = "wide"
	current.ModelName = "same-name"
	current.Announcement = "0.40.8 is out"
	report := Requalify(prior, current, []RequalificationClaimInput{
		claim("artifact", "artifact_digest"),
		claim("runtime", "runtime"),
	})
	if report.Schema != RequalificationSchema {
		t.Fatalf("schema %q", report.Schema)
	}
	if got := report.Claims[0]; got.State != RequalifyValid || len(got.Changed) != 0 {
		t.Fatalf("artifact claim changed: %+v", got)
	}
	if got := report.Claims[1]; got.State != RequalifyStale || len(got.Changed) != 1 || got.Changed[0] != "runtime" {
		t.Fatalf("runtime claim was not stale: %+v", got)
	}
	if report.Next == nil || report.Next.Code != RequalifyFreshConfirmation {
		t.Fatalf("next action: %+v", report.Next)
	}
	if len(report.Next.Claims) != 1 || report.Next.Claims[0] != "runtime" {
		t.Fatalf("fresh confirmation listed the wrong claims: %+v", report.Next.Claims)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if strings.Contains(text, "rerun") || strings.Contains(text, `"pass"`) || strings.Contains(text, "artifact") && strings.Contains(text, `"claims":["artifact"`) {
		t.Fatalf("report scheduled a full rerun or a pass: %s", text)
	}
}

func TestPresentationAndNameDoNotCarryOrStaleAClaim(t *testing.T) {
	prior := installedStack()
	prior.ModelName = "library-name"
	current := installedStack()
	current.ModelName = "library-name"
	current.Presentation = "narrow"
	current.Announcement = "please upgrade"
	same := Requalify(prior, current, []RequalificationClaimInput{claim("fit", "artifact_digest", "quant")})
	if same.Claims[0].State != RequalifyValid || same.Next != nil {
		t.Fatalf("presentation scheduled work: %+v", same)
	}
	renamed := installedStack()
	renamed.ArtifactDigest = observed("sha256:other")
	renamed.ModelName = "library-name"
	report := Requalify(prior, renamed, []RequalificationClaimInput{claim("fit", "artifact_digest")})
	if report.Claims[0].State != RequalifyStale {
		t.Fatalf("matching model name carried a different artifact: %+v", report.Claims[0])
	}
}

func TestRequestedOrUnknownIdentityDoesNotInherit(t *testing.T) {
	prior := installedStack()
	current := installedStack()
	current.Runtime = IdentityFact{State: FactRequested, Value: prior.Runtime.Value}
	report := Requalify(prior, current, []RequalificationClaimInput{claim("speed", "runtime")})
	if report.Claims[0].State != RequalifyUnresolved || len(report.Claims[0].Changed) != 0 {
		t.Fatalf("requested runtime became a change or a reuse: %+v", report.Claims[0])
	}
	if len(report.Claims[0].Unknown) != 1 || report.Claims[0].Unknown[0] != "runtime" {
		t.Fatalf("unknown field: %+v", report.Claims[0].Unknown)
	}
	current.Kernel = IdentityFact{State: FactUnknown}
	report = Requalify(prior, current, []RequalificationClaimInput{claim("device", "kernel")})
	if report.Claims[0].State != RequalifyUnresolved || len(report.Claims[0].Changed) != 0 {
		t.Fatalf("unknown kernel became a change: %+v", report.Claims[0])
	}
}

func TestMissingDependencyCoverageStaysUnresolved(t *testing.T) {
	report := Requalify(installedStack(), installedStack(), []RequalificationClaimInput{claim("orphan")})
	if report.Claims[0].State != RequalifyUnresolved {
		t.Fatalf("empty dependencies were treated as unaffected: %+v", report.Claims[0])
	}
	if report.Next == nil || len(report.Next.Claims) != 1 || report.Next.Claims[0] != "orphan" {
		t.Fatalf("next action: %+v", report.Next)
	}
}

func TestKernelChangeIsStaleAndAKnownChangeHidesNothing(t *testing.T) {
	prior := installedStack()
	current := installedStack()
	current.Kernel = observed("6.13.0")
	current.Driver = IdentityFact{}
	report := Requalify(prior, current, []RequalificationClaimInput{
		claim("stack", "kernel", "driver"),
	})
	got := report.Claims[0]
	if got.State != RequalifyStale || len(got.Changed) != 1 || got.Changed[0] != "kernel" {
		t.Fatalf("kernel change: %+v", got)
	}
	if len(got.Unknown) != 1 || got.Unknown[0] != "driver" {
		t.Fatalf("unknown driver was dropped: %+v", got.Unknown)
	}
}
