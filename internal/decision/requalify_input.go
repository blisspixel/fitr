package decision

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/blisspixel/fitr/internal/strictjson"
)

// RequalificationInputSchema is the operator document LoadRequalification
// accepts. It is not the derived report, and it is not a sealed run record.
const RequalificationInputSchema = "fitr.requalification-input.v1"

// LoadRequalification derives a report from one operator document.
// The document is the only input. This does not read the host, does not
// submit work, and does not treat a model name or a release announcement
// as installation identity.
func LoadRequalification(data []byte) (RequalificationReport, error) {
	input, err := decodeRequalificationInput(data)
	if err != nil {
		return RequalificationReport{}, err
	}
	prior, err := input.Prior.identity()
	if err != nil {
		return RequalificationReport{}, err
	}
	current, err := input.Current.identity()
	if err != nil {
		return RequalificationReport{}, err
	}
	claims, err := requalificationClaims(input.Claims)
	if err != nil {
		return RequalificationReport{}, err
	}
	return Requalify(prior, current, claims), nil
}

func decodeRequalificationInput(data []byte) (requalificationInput, error) {
	if err := strictjson.Validate(data); err != nil {
		return requalificationInput{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var input requalificationInput
	if err := decoder.Decode(&input); err != nil {
		return requalificationInput{}, err
	}
	if input.Schema != RequalificationInputSchema {
		return requalificationInput{}, errors.New("requalification input schema is not " + RequalificationInputSchema)
	}
	if len(input.Claims) == 0 {
		return requalificationInput{}, errors.New("requalification input names no claims")
	}
	return input, nil
}

// Lines is the text projection of a derived report. It names the same
// states and fields as the JSON report and does not add a quality result.
func (r RequalificationReport) Lines() []string {
	if r.Schema == "" {
		return nil
	}
	lines := []string{r.Schema}
	for _, claim := range r.Claims {
		lines = append(lines, requalifyClaimLine(claim))
	}
	if r.Next == nil {
		return lines
	}
	lines = append(lines, "next  "+r.Next.Code+"  "+strings.Join(r.Next.Claims, ", "))
	if r.Next.Reason != "" {
		lines = append(lines, r.Next.Reason)
	}
	return lines
}

func requalifyClaimLine(claim RequalifiedClaim) string {
	line := claim.State + "  " + claim.ID
	if len(claim.Changed) > 0 {
		line += "  changed " + strings.Join(claim.Changed, ", ")
	}
	if len(claim.Unknown) > 0 {
		line += "  unknown " + strings.Join(claim.Unknown, ", ")
	}
	return line
}

type requalificationInput struct {
	Schema  string                 `json:"schema"`
	Prior   requalificationInstall `json:"prior"`
	Current requalificationInstall `json:"current"`
	Claims  []requalificationClaim `json:"claims"`
}

type requalificationClaim struct {
	ID        string   `json:"id"`
	DependsOn []string `json:"depends_on,omitempty"`
}

type requalificationInstall struct {
	ArtifactDigest   identityFactInput `json:"artifact_digest"`
	Quant            identityFactInput `json:"quant"`
	Runtime          identityFactInput `json:"runtime"`
	ComputeBackend   identityFactInput `json:"compute_backend"`
	Driver           identityFactInput `json:"driver"`
	OS               identityFactInput `json:"os"`
	Kernel           identityFactInput `json:"kernel"`
	Hardware         identityFactInput `json:"hardware"`
	EffectiveContext identityFactInput `json:"effective_context"`
	Concurrency      identityFactInput `json:"concurrency"`
	Residency        identityFactInput `json:"residency"`
	Harness          identityFactInput `json:"harness"`
	Presentation     string            `json:"presentation,omitempty"`
	ModelName        string            `json:"model_name,omitempty"`
	Announcement     string            `json:"announcement,omitempty"`
}

type identityFactInput struct {
	State string `json:"state,omitempty"`
	Value string `json:"value,omitempty"`
}

type namedIdentityFact struct {
	name  string
	input identityFactInput
}

func (in requalificationInstall) identity() (InstallationIdentity, error) {
	if err := plainIgnoredRequalifyText(in.Presentation, in.ModelName, in.Announcement); err != nil {
		return InstallationIdentity{}, err
	}
	out := InstallationIdentity{
		Presentation: in.Presentation, ModelName: in.ModelName, Announcement: in.Announcement,
	}
	for _, fact := range in.comparedFacts() {
		if err := fact.input.validate(fact.name); err != nil {
			return InstallationIdentity{}, err
		}
		if err := assignIdentityFact(&out, fact.name, fact.input.fact()); err != nil {
			return InstallationIdentity{}, err
		}
	}
	return out, nil
}

func (in requalificationInstall) comparedFacts() []namedIdentityFact {
	return []namedIdentityFact{
		{"artifact_digest", in.ArtifactDigest},
		{"quant", in.Quant},
		{"runtime", in.Runtime},
		{"compute_backend", in.ComputeBackend},
		{"driver", in.Driver},
		{"os", in.OS},
		{"kernel", in.Kernel},
		{"hardware", in.Hardware},
		{"effective_context", in.EffectiveContext},
		{"concurrency", in.Concurrency},
		{"residency", in.Residency},
		{"harness", in.Harness},
	}
}

func assignIdentityFact(out *InstallationIdentity, name string, fact IdentityFact) error {
	switch name {
	case "artifact_digest":
		out.ArtifactDigest = fact
	case "quant":
		out.Quant = fact
	case "runtime":
		out.Runtime = fact
	case "compute_backend":
		out.ComputeBackend = fact
	case "driver":
		out.Driver = fact
	case "os":
		out.OS = fact
	case "kernel":
		out.Kernel = fact
	case "hardware":
		out.Hardware = fact
	case "effective_context":
		out.EffectiveContext = fact
	case "concurrency":
		out.Concurrency = fact
	case "residency":
		out.Residency = fact
	case "harness":
		out.Harness = fact
	default:
		return fmt.Errorf("unmapped identity field %s", name)
	}
	return nil
}

func (f identityFactInput) validate(name string) error {
	switch f.State {
	case "", FactUnknown:
		if f.Value != "" {
			return fmt.Errorf("%s is unknown and carries a value", name)
		}
	case FactObserved, FactRequested:
		if !plainRequalifyText(f.Value) {
			return fmt.Errorf("%s value is not plain text", name)
		}
	default:
		return fmt.Errorf("%s has an unrecognized state", name)
	}
	return nil
}

func (f identityFactInput) fact() IdentityFact {
	state := f.State
	if state == "" {
		state = FactUnknown
	}
	return IdentityFact{State: state, Value: f.Value}
}

func requalificationClaims(claims []requalificationClaim) ([]RequalificationClaimInput, error) {
	seen := make(map[string]bool, len(claims))
	out := make([]RequalificationClaimInput, 0, len(claims))
	for _, claim := range claims {
		if !plainRequalifyID(claim.ID) {
			return nil, errors.New("a requalification claim id is empty or not plain text")
		}
		if seen[claim.ID] {
			return nil, fmt.Errorf("requalification claim %s is repeated", claim.ID)
		}
		seen[claim.ID] = true
		depends, err := requalificationDependencies(claim.DependsOn)
		if err != nil {
			return nil, err
		}
		out = append(out, RequalificationClaimInput{ID: claim.ID, DependsOn: depends})
	}
	return out, nil
}

func requalificationDependencies(names []string) ([]string, error) {
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if !knownRequalificationField(name) {
			return nil, fmt.Errorf("unknown requalification dependency %q", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("requalification dependency %s is repeated", name)
		}
		seen[name] = true
	}
	return append([]string(nil), names...), nil
}

func knownRequalificationField(name string) bool {
	switch name {
	case "artifact_digest", "quant", "runtime", "compute_backend", "driver", "os",
		"kernel", "hardware", "effective_context", "concurrency", "residency", "harness":
		return true
	default:
		return false
	}
}

func plainIgnoredRequalifyText(values ...string) error {
	for _, value := range values {
		if !plainRequalifyText(value) {
			return errors.New("a requalification label is not plain text")
		}
	}
	return nil
}

func plainRequalifyID(value string) bool {
	return value != "" && plainRequalifyText(value) && !strings.Contains(value, " ")
}

func plainRequalifyText(value string) bool {
	if strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
