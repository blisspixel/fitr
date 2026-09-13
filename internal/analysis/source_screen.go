package analysis

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/blisspixel/fitr/internal/source"
)

const SourceScreenSchema = "fitr.analysis.source_screen.v1"

// SourceScreenPolicy filters declarations and a component projection. An
// operator's accepted architecture names are not runtime capability evidence,
// and an accepted license identifier is not an interpretation of legal terms.
type SourceScreenPolicy struct {
	RequireFirstParty     bool     `json:"require_first_party"`
	AllowedLicenses       []string `json:"allowed_licenses"`
	AllowedArchitectures  []string `json:"allowed_architectures"`
	Context               int      `json:"context"`
	ComponentCeilingBytes int64    `json:"component_ceiling_bytes"`
}

var sourcePolicyIdentifier = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

func (policy SourceScreenPolicy) Validate() error {
	if policy.Context <= 0 || policy.Context > 1<<30 || policy.ComponentCeilingBytes <= 0 || policy.ComponentCeilingBytes > 1<<60 {
		return errors.New("screening requires a positive bounded context and component memory ceiling")
	}
	for _, values := range [][]string{policy.AllowedLicenses, policy.AllowedArchitectures} {
		if len(values) > 32 {
			return errors.New("screening accepts at most 32 identifiers per policy list")
		}
		seen := make(map[string]bool)
		for _, value := range values {
			if !sourcePolicyIdentifier.MatchString(value) || seen[value] {
				return errors.New("screening identifiers must be unique lowercase names containing letters, numbers, dot, underscore or hyphen")
			}
			seen[value] = true
		}
	}
	return nil
}

type SourceScreenGate struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Reason string `json:"reason"`
}

// SourceScreenFacts are derived by the GGUF arithmetic owner. They never
// contain a runtime verdict; this projection only compares modeled components.
type SourceScreenFacts struct {
	SourceSHA256       string
	Context            int
	CeilingBytes       int64
	Architecture       string
	ArchitectureStatus string
	ArchitectureReason string
	ProjectionStatus   string
	ProjectionReason   string
}

type SourceScreen struct {
	Schema       string             `json:"schema"`
	SourceSHA256 string             `json:"source_sha256"`
	Policy       SourceScreenPolicy `json:"policy"`
	State        string             `json:"state"`
	FirstProblem string             `json:"first_problem,omitempty"`
	Gates        []SourceScreenGate `json:"gates"`
	Runtime      string             `json:"runtime"`
	Quality      string             `json:"quality"`
	Next         string             `json:"next"`
}

// AnalyzeSourceScreen orders inexpensive declarations before header reads.
// Later gates stay not_checked after the first problem, even if a caller has
// facts for them. That prevents one favorable fact from bypassing a requirement.
func AnalyzeSourceScreen(resolution source.Resolution, policy SourceScreenPolicy, facts SourceScreenFacts) (SourceScreen, error) {
	if err := resolution.Validate(); err != nil {
		return SourceScreen{}, err
	}
	if err := policy.Validate(); err != nil {
		return SourceScreen{}, err
	}
	report := SourceScreen{Schema: SourceScreenSchema, SourceSHA256: resolution.ResolutionSHA256,
		Policy: policy, State: "clear", Runtime: "unmeasured", Quality: "unmeasured",
		Next: "Review dependencies and license terms, then bind the exact local files and measure the intended runtime configuration."}
	gates := []SourceScreenGate{
		screenPublisher(resolution, policy), screenLicense(resolution.Publisher, policy),
		screenArchitecture(resolution.ResolutionSHA256, policy, facts), screenComponents(policy, facts),
	}
	for _, gate := range gates {
		if report.FirstProblem != "" {
			gate.State, gate.Reason = "not_checked", "Resolve the earlier "+report.FirstProblem+" gate first."
		} else if gate.State != "clear" {
			report.State, report.FirstProblem = gate.State, gate.Name
			report.Next = gate.Reason
		}
		report.Gates = append(report.Gates, gate)
	}
	return report, nil
}

func screenPublisher(resolution source.Resolution, policy SourceScreenPolicy) SourceScreenGate {
	gate := SourceScreenGate{Name: "publisher", State: "unresolved"}
	if resolution.State != "resolved" {
		gate.Reason = "Resolve consistent metadata for every selected file before screening this candidate."
		return gate
	}
	publisher := resolution.Publisher
	if publisher == nil || publisher.Author == "" {
		gate.Reason = "Publisher metadata is missing; authorship cannot be inferred."
		return gate
	}
	gate.State, gate.Reason = "clear", publisher.Summary()+"; no publisher restriction requested."
	if !policy.RequireFirstParty {
		return gate
	}
	switch {
	case !publisher.Lineage():
		gate.State, gate.Reason = "unresolved", "The first-party policy requires declared base-model lineage, which is missing."
	case !publisher.FirstPartyConversion:
		gate.State, gate.Reason = "blocked", "The declared lineage does not meet the requested first-party policy; this is not a quality judgment."
	default:
		gate.Reason = "The repository author matches every declared base-model author."
	}
	return gate
}

func screenLicense(publisher *source.Publisher, policy SourceScreenPolicy) SourceScreenGate {
	gate := SourceScreenGate{Name: "license", State: "unresolved"}
	if len(policy.AllowedLicenses) == 0 {
		gate.Reason = "Declare accepted license identifiers with --allow-license after reviewing their terms."
		return gate
	}
	if publisher == nil || publisher.License == "" || publisher.License == "other" {
		gate.Reason = "A specific declared license identifier is missing; inspect the repository's license terms."
		return gate
	}
	gate.State = "blocked"
	gate.Reason = fmt.Sprintf("Declared license %q is outside the explicit accepted list.", publisher.License)
	if slices.Contains(policy.AllowedLicenses, publisher.License) {
		gate.State = "clear"
		gate.Reason = "Declared license " + publisher.License + " matches the accepted list; terms and dependency licenses remain to be reviewed."
	}
	return gate
}

func screenArchitecture(sourceDigest string, policy SourceScreenPolicy, facts SourceScreenFacts) SourceScreenGate {
	gate := SourceScreenGate{Name: "architecture", State: "unresolved"}
	if len(policy.AllowedArchitectures) == 0 {
		gate.Reason = "Declare accepted GGUF architecture identifiers with --allow-architecture; this policy does not prove runtime support."
		return gate
	}
	if facts.ArchitectureStatus == "available" && facts.SourceSHA256 != sourceDigest {
		gate.Reason = "The header projection belongs to a different source receipt; collect evidence for this selection."
		return gate
	}
	if facts.ArchitectureStatus != "available" || facts.Architecture == "" {
		gate.Reason = facts.ArchitectureReason
		if gate.Reason == "" {
			gate.Reason = "Read complete, consistent GGUF metadata before applying the architecture policy."
		}
		return gate
	}
	gate.State, gate.Reason = "blocked", "Observed GGUF architecture "+facts.Architecture+" is outside the accepted list."
	if slices.Contains(policy.AllowedArchitectures, facts.Architecture) {
		gate.State, gate.Reason = "clear", "Observed GGUF architecture "+facts.Architecture+" matches the accepted list; runtime support remains unmeasured."
	}
	return gate
}

func screenComponents(policy SourceScreenPolicy, facts SourceScreenFacts) SourceScreenGate {
	gate := SourceScreenGate{Name: "fit", State: "unresolved", Reason: facts.ProjectionReason}
	if facts.SourceSHA256 == "" || facts.Context != policy.Context || facts.CeilingBytes != policy.ComponentCeilingBytes {
		gate.Reason = "The component comparison is not bound to this source, requested context and ceiling."
		return gate
	}
	switch facts.ProjectionStatus {
	case "within_ceiling":
		gate.State = "clear"
	case "exceeds_ceiling":
		gate.State = "blocked"
	}
	if strings.TrimSpace(gate.Reason) == "" {
		gate.Reason = "A complete weights-plus-cache projection at the requested context is missing."
	}
	return gate
}
