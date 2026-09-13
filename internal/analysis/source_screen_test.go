package analysis

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/source"
)

type screenTransport func(*http.Request) (*http.Response, error)

func (transport screenTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func screenFixture(t *testing.T) source.Resolution {
	t.Helper()
	metadata := fmt.Sprintf(`{"id":"owner/model","author":"owner","sha":%q,"cardData":{"license":"apache-2.0","base_model":"owner/base"},"siblings":[{"rfilename":"model.gguf","size":1024,"lfs":{"size":1024,"sha256":%q}}]}`,
		strings.Repeat("a", 40), strings.Repeat("b", 64))
	resolver := source.NewResolver(screenTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(metadata))}, nil
	}))
	resolution, err := resolver.ResolveHF(context.Background(), source.HFRequest{RepoID: "owner/model", Revision: "main", Files: []string{"model.gguf"}})
	if err != nil || resolution.State != "resolved" {
		t.Fatalf("fixture resolution: %+v %v", resolution, err)
	}
	return resolution
}

func screenPolicy() SourceScreenPolicy {
	return SourceScreenPolicy{AllowedLicenses: []string{"apache-2.0"}, AllowedArchitectures: []string{"llama"},
		Context: 4096, ComponentCeilingBytes: 8 << 30}
}

func screenFacts() SourceScreenFacts {
	return SourceScreenFacts{Context: 4096, CeilingBytes: 8 << 30, Architecture: "llama", ArchitectureStatus: "available", ProjectionStatus: "within_ceiling",
		ProjectionReason: "Declared weights plus f16 cache are within the component ceiling; runtime allocation is unmeasured."}
}

func TestSourceScreenOrdersGatesAndPreservesMissingEvidence(t *testing.T) {
	for _, test := range sourceScreenCases() {
		t.Run(test.name, func(t *testing.T) {
			resolution, policy, facts := screenFixture(t), screenPolicy(), screenFacts()
			test.change(&resolution, &policy, &facts)
			var err error
			resolution.ResolutionSHA256, err = resolution.Digest()
			if err != nil {
				t.Fatal(err)
			}
			facts.SourceSHA256 = resolution.ResolutionSHA256
			report, err := AnalyzeSourceScreen(resolution, policy, facts)
			if err != nil || report.State != test.state || report.FirstProblem != test.first {
				t.Fatalf("screen=%+v error=%v", report, err)
			}
			checkScreenOrder(t, report)
		})
	}
}

type sourceScreenCase struct {
	name         string
	change       func(*source.Resolution, *SourceScreenPolicy, *SourceScreenFacts)
	state, first string
}

func sourceScreenCases() []sourceScreenCase {
	return []sourceScreenCase{
		{"clear", func(*source.Resolution, *SourceScreenPolicy, *SourceScreenFacts) {}, "clear", ""},
		{"no publisher", func(r *source.Resolution, _ *SourceScreenPolicy, _ *SourceScreenFacts) { r.Publisher = nil }, "unresolved", "publisher"},
		{"lineage optional", func(r *source.Resolution, _ *SourceScreenPolicy, _ *SourceScreenFacts) {
			r.Publisher.BaseModels, r.Publisher.BaseAuthor, r.Publisher.FirstPartyConversion = nil, "", false
		}, "clear", ""},
		{"lineage required", func(r *source.Resolution, p *SourceScreenPolicy, _ *SourceScreenFacts) {
			p.RequireFirstParty = true
			r.Publisher.BaseModels, r.Publisher.BaseAuthor, r.Publisher.FirstPartyConversion = nil, "", false
		}, "unresolved", "publisher"},
		{"third party allowed", func(r *source.Resolution, _ *SourceScreenPolicy, _ *SourceScreenFacts) {
			r.Publisher.BaseModels, r.Publisher.BaseAuthor, r.Publisher.FirstPartyConversion = []string{"other/base"}, "other", false
		}, "clear", ""},
		{"third party restricted", func(r *source.Resolution, p *SourceScreenPolicy, _ *SourceScreenFacts) {
			p.RequireFirstParty = true
			r.Publisher.BaseModels, r.Publisher.BaseAuthor, r.Publisher.FirstPartyConversion = []string{"other/base"}, "other", false
		}, "blocked", "publisher"},
		{"first party explicit", func(_ *source.Resolution, p *SourceScreenPolicy, _ *SourceScreenFacts) { p.RequireFirstParty = true }, "clear", ""},
		{"no license policy", func(_ *source.Resolution, p *SourceScreenPolicy, _ *SourceScreenFacts) { p.AllowedLicenses = nil }, "unresolved", "license"},
		{"unknown license", func(r *source.Resolution, _ *SourceScreenPolicy, _ *SourceScreenFacts) { r.Publisher.License = "" }, "unresolved", "license"},
		{"custom license", func(r *source.Resolution, p *SourceScreenPolicy, _ *SourceScreenFacts) {
			r.Publisher.License = "other"
			p.AllowedLicenses = []string{"other"}
		}, "unresolved", "license"},
		{"license blocked", func(_ *source.Resolution, p *SourceScreenPolicy, _ *SourceScreenFacts) {
			p.AllowedLicenses = []string{"mit"}
		}, "blocked", "license"},
		{"no architecture policy", func(_ *source.Resolution, p *SourceScreenPolicy, _ *SourceScreenFacts) { p.AllowedArchitectures = nil }, "unresolved", "architecture"},
		{"partial header", func(_ *source.Resolution, _ *SourceScreenPolicy, f *SourceScreenFacts) {
			f.ArchitectureStatus = "unresolved"
			f.ArchitectureReason = "header truncated"
		}, "unresolved", "architecture"},
		{"missing header", func(_ *source.Resolution, _ *SourceScreenPolicy, f *SourceScreenFacts) { *f = SourceScreenFacts{} }, "unresolved", "architecture"},
		{"architecture blocked", func(_ *source.Resolution, _ *SourceScreenPolicy, f *SourceScreenFacts) { f.Architecture = "other" }, "blocked", "architecture"},
		{"components exceed", func(_ *source.Resolution, _ *SourceScreenPolicy, f *SourceScreenFacts) {
			f.ProjectionStatus = "exceeds_ceiling"
		}, "blocked", "fit"},
		{"components absent", func(_ *source.Resolution, _ *SourceScreenPolicy, f *SourceScreenFacts) {
			f.ProjectionStatus, f.ProjectionReason = "unresolved", ""
		}, "unresolved", "fit"},
	}
}

func checkScreenOrder(t *testing.T, report SourceScreen) {
	t.Helper()
	var names []string
	stopped := false
	for _, gate := range report.Gates {
		names = append(names, gate.Name)
		if stopped && gate.State != "not_checked" {
			t.Fatalf("later gate bypassed a gap: %+v", report)
		}
		if gate.State != "clear" {
			stopped = true
		}
	}
	if !reflect.DeepEqual(names, []string{"publisher", "license", "architecture", "fit"}) || report.Runtime != "unmeasured" || report.Quality != "unmeasured" {
		t.Fatalf("screen changed its evidence scope: %+v", report)
	}
}

func TestSourceScreenRejectsInvalidEvidenceAndPolicy(t *testing.T) {
	resolution := screenFixture(t)
	resolution.ResolutionSHA256 = "changed"
	if _, err := AnalyzeSourceScreen(resolution, screenPolicy(), screenFacts()); err == nil {
		t.Fatal("accepted modified receipt")
	}
	for _, change := range []func(*SourceScreenPolicy){
		func(p *SourceScreenPolicy) { p.Context = 0 }, func(p *SourceScreenPolicy) { p.Context = 1<<30 + 1 },
		func(p *SourceScreenPolicy) { p.ComponentCeilingBytes = -1 }, func(p *SourceScreenPolicy) { p.ComponentCeilingBytes = 1<<60 + 1 },
		func(p *SourceScreenPolicy) { p.AllowedLicenses = []string{"MIT"} },
		func(p *SourceScreenPolicy) { p.AllowedLicenses = []string{"mit", "mit"} },
		func(p *SourceScreenPolicy) { p.AllowedArchitectures = []string{"llama\npretend clear"} },
		func(p *SourceScreenPolicy) { p.AllowedLicenses = make([]string, 33) },
	} {
		policy := screenPolicy()
		change(&policy)
		if _, err := AnalyzeSourceScreen(screenFixture(t), policy, screenFacts()); err == nil {
			t.Fatalf("accepted policy %+v", policy)
		}
	}
}

func TestSourceScreenCannotReuseAnotherProjectionConfiguration(t *testing.T) {
	for _, change := range []func(*SourceScreenFacts){
		func(f *SourceScreenFacts) { f.SourceSHA256 = "sha256:" + strings.Repeat("f", 64) },
		func(f *SourceScreenFacts) { f.SourceSHA256 = "" },
		func(f *SourceScreenFacts) { f.Context = 2048 },
		func(f *SourceScreenFacts) { f.CeilingBytes = 16 << 30 },
	} {
		resolution, facts := screenFixture(t), screenFacts()
		facts.SourceSHA256 = resolution.ResolutionSHA256
		change(&facts)
		report, err := AnalyzeSourceScreen(resolution, screenPolicy(), facts)
		if err != nil || report.State != "unresolved" {
			t.Fatalf("reused mismatched projection: %+v %v", report, err)
		}
		if facts.SourceSHA256 == "" && report.FirstProblem != "architecture" {
			t.Fatal("architecture cleared without binding its header to the source receipt")
		}
	}
}
