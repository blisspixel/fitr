package source

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

const (
	ComponentPlanSchema = "fitr.source.component-plan.v1"
	MaxPlannedFiles     = 64
)

type ComponentState string

const (
	ComponentRequired   ComponentState = "required"
	ComponentOptional   ComponentState = "optional"
	ComponentDisabled   ComponentState = "disabled"
	ComponentUnresolved ComponentState = "unresolved"
)

type ComponentKind string

const (
	ComponentKindModelShard ComponentKind = "model_shard"
	ComponentKindProjector  ComponentKind = "projector"
	ComponentKindEncoder    ComponentKind = "encoder"
	ComponentKindTokenizer  ComponentKind = "tokenizer"
	ComponentKindCompanion  ComponentKind = "companion"
)

type ComponentPlanStatus string

const (
	PlanComplete   ComponentPlanStatus = "complete"
	PlanUnresolved ComponentPlanStatus = "unresolved"
	PlanBlocked    ComponentPlanStatus = "blocked"
)

// PlannedComponent defines one model shard, companion or auxiliary file
// evaluated before download ownership. State records whether it is required,
// optional, disabled by the operator, or unresolved.
type PlannedComponent struct {
	Path           string         `json:"path"`
	Kind           ComponentKind  `json:"kind"`
	State          ComponentState `json:"state"`
	SizeBytes      *int64         `json:"size_bytes,omitempty"`
	DeclaredSHA256 string         `json:"declared_sha256,omitempty"`
	Role           string         `json:"role,omitempty"`
	Reason         string         `json:"reason,omitempty"`
}

// ComponentPlan makes required, optional, disabled and unresolved components
// explicit before an owned download or source-driven local experiment is planned.
// TotalRequiredBytes accounts for all required model shards and companions.
type ComponentPlan struct {
	Schema             string              `json:"schema"`
	PlanSHA256         string              `json:"plan_sha256,omitempty"`
	SourceSHA256       string              `json:"source_sha256"`
	Status             ComponentPlanStatus `json:"status"`
	Reason             string              `json:"reason,omitempty"`
	Components         []PlannedComponent  `json:"components"`
	TotalRequiredBytes int64               `json:"total_required_bytes"`
	TotalOptionalBytes int64               `json:"total_optional_bytes"`
	Gaps               []string            `json:"gaps,omitempty"`
}

func (p *ComponentPlan) Validate() error {
	if p == nil {
		return errors.New("component plan is nil")
	}
	if p.Schema != ComponentPlanSchema {
		return fmt.Errorf("unexpected component plan schema: %q", p.Schema)
	}
	if !shaPattern.MatchString(p.SourceSHA256) {
		return fmt.Errorf("invalid source_sha256 digest: %q", p.SourceSHA256)
	}
	switch p.Status {
	case PlanComplete, PlanUnresolved, PlanBlocked:
	default:
		return fmt.Errorf("invalid component plan status: %q", p.Status)
	}
	if len(p.Components) > MaxPlannedFiles {
		return fmt.Errorf("too many planned components (max %d)", MaxPlannedFiles)
	}
	if p.TotalRequiredBytes < 0 || p.TotalOptionalBytes < 0 {
		return errors.New("component plan byte totals cannot be negative")
	}
	seen := make(map[string]bool, len(p.Components))
	for _, comp := range p.Components {
		if comp.Path != "" {
			if seen[comp.Path] {
				return fmt.Errorf("duplicate planned component path: %q", comp.Path)
			}
			seen[comp.Path] = true
		}
		switch comp.State {
		case ComponentRequired, ComponentOptional, ComponentDisabled, ComponentUnresolved:
		default:
			return fmt.Errorf("invalid component state: %q", comp.State)
		}
		if comp.SizeBytes != nil && *comp.SizeBytes < 0 {
			return fmt.Errorf("component %q has negative size", comp.Path)
		}
	}
	return nil
}

// Digest computes the deterministic sha256: hash of the canonical JSON bytes,
// omitting PlanSHA256.
func (p *ComponentPlan) Digest() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	clone := *p
	clone.PlanSHA256 = ""
	comps := slices.Clone(clone.Components)
	slices.SortFunc(comps, func(a, b PlannedComponent) int {
		return strings.Compare(a.Path+string(a.Kind), b.Path+string(b.Kind))
	})
	clone.Components = comps
	data, err := json.Marshal(clone)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// Seal computes Digest and sets PlanSHA256.
func (p *ComponentPlan) Seal() error {
	digest, err := p.Digest()
	if err != nil {
		return err
	}
	p.PlanSHA256 = digest
	return nil
}

// BuildComponentPlanOptions parameterizes component planning.
type BuildComponentPlanOptions struct {
	Architecture       string
	RequiredCompanions []string
	DisabledPaths      []string
	OptionalPaths      []string
}

// BuildComponentPlan binds dependency findings and runtime companion requirements
// into an explicit, sealed component plan.
func BuildComponentPlan(resolution Resolution, opts BuildComponentPlanOptions) (*ComponentPlan, error) {
	plan := &ComponentPlan{
		Schema:       ComponentPlanSchema,
		SourceSHA256: resolution.ResolutionSHA256,
		Status:       PlanUnresolved,
		Components:   []PlannedComponent{},
		Gaps:         []string{},
	}
	if resolution.State != "resolved" {
		plan.Reason = "source resolution is incomplete or unresolved"
		_ = plan.Seal()
		return plan, nil
	}

	planModelShards(plan, resolution, opts)
	planRequiredCompanions(plan, resolution, opts)
	planOptionalFiles(plan, resolution, opts)
	concludePlanStatus(plan)

	if err := plan.Seal(); err != nil {
		return nil, err
	}
	return plan, nil
}

func planModelShards(plan *ComponentPlan, resolution Resolution, opts BuildComponentPlanOptions) {
	for _, dep := range resolution.Dependencies {
		if dep.Kind == "shard" && (dep.Status == "missing" || dep.Status == "unselected") {
			plan.Components = append(plan.Components, PlannedComponent{
				Path:   dep.TargetFile,
				Kind:   ComponentKindModelShard,
				State:  ComponentUnresolved,
				Reason: fmt.Sprintf("numbered shard is %s in source repository", dep.Status),
			})
			plan.Gaps = append(plan.Gaps, fmt.Sprintf("shard_%s: %s", dep.Status, dep.TargetFile))
		}
	}

	for _, f := range resolution.Files {
		if !strings.HasSuffix(strings.ToLower(f.Path), ".gguf") || candidateKind(f.Path) != "" {
			continue
		}
		addModelShard(plan, f, opts)
	}
}

func addModelShard(plan *ComponentPlan, f FileMetadata, opts BuildComponentPlanOptions) {
	if slices.Contains(opts.DisabledPaths, f.Path) {
		plan.Components = append(plan.Components, PlannedComponent{
			Path:      f.Path,
			Kind:      ComponentKindModelShard,
			State:     ComponentDisabled,
			SizeBytes: f.SizeBytes,
			Role:      "disabled_weights",
			Reason:    "operator explicitly disabled file",
		})
		return
	}
	if slices.Contains(opts.OptionalPaths, f.Path) {
		plan.Components = append(plan.Components, PlannedComponent{
			Path:           f.Path,
			Kind:           ComponentKindModelShard,
			State:          ComponentOptional,
			SizeBytes:      f.SizeBytes,
			DeclaredSHA256: f.DeclaredSHA256,
			Role:           "optional_model",
		})
		if f.SizeBytes != nil && *f.SizeBytes > 0 {
			plan.TotalOptionalBytes += *f.SizeBytes
		}
		return
	}
	if f.State != "present" || f.SizeBytes == nil || *f.SizeBytes <= 0 || f.DeclaredSHA256 == "" {
		plan.Components = append(plan.Components, PlannedComponent{
			Path:   f.Path,
			Kind:   ComponentKindModelShard,
			State:  ComponentUnresolved,
			Reason: "selected shard metadata missing or incomplete",
		})
		plan.Gaps = append(plan.Gaps, "incomplete_model_shard: "+f.Path)
		return
	}
	role := "primary_weights"
	if shardPattern.MatchString(f.Path) {
		role = "model_shard"
	}
	plan.Components = append(plan.Components, PlannedComponent{
		Path:           f.Path,
		Kind:           ComponentKindModelShard,
		State:          ComponentRequired,
		SizeBytes:      f.SizeBytes,
		DeclaredSHA256: f.DeclaredSHA256,
		Role:           role,
	})
	plan.TotalRequiredBytes += *f.SizeBytes
}

func planRequiredCompanions(plan *ComponentPlan, resolution Resolution, opts BuildComponentPlanOptions) {
	for _, reqComp := range opts.RequiredCompanions {
		normComp := strings.ToLower(strings.TrimSpace(reqComp))
		matched := findSelectedCompanion(resolution.Files, normComp)
		if matched != nil {
			addSelectedCompanion(plan, *matched, normComp, opts)
		} else {
			addMissingCompanion(plan, resolution.InventoryPaths, normComp)
		}
	}
}

func findSelectedCompanion(files []FileMetadata, kind string) *FileMetadata {
	for _, f := range files {
		if candidateKind(f.Path) == kind {
			fCopy := f
			return &fCopy
		}
	}
	return nil
}

func addSelectedCompanion(plan *ComponentPlan, matched FileMetadata, kind string, opts BuildComponentPlanOptions) {
	if slices.Contains(opts.DisabledPaths, matched.Path) {
		plan.Components = append(plan.Components, PlannedComponent{
			Path:      matched.Path,
			Kind:      ComponentKind(kind),
			State:     ComponentDisabled,
			SizeBytes: matched.SizeBytes,
			Role:      kind + "_companion",
			Reason:    "operator explicitly disabled required companion",
		})
		plan.Gaps = append(plan.Gaps, "required_companion_disabled: "+kind)
		return
	}
	if matched.State != "present" || matched.SizeBytes == nil || *matched.SizeBytes <= 0 || matched.DeclaredSHA256 == "" {
		plan.Components = append(plan.Components, PlannedComponent{
			Path:   matched.Path,
			Kind:   ComponentKind(kind),
			State:  ComponentUnresolved,
			Reason: "companion metadata missing or incomplete",
		})
		plan.Gaps = append(plan.Gaps, "incomplete_companion: "+kind)
		return
	}
	plan.Components = append(plan.Components, PlannedComponent{
		Path:           matched.Path,
		Kind:           ComponentKind(kind),
		State:          ComponentRequired,
		SizeBytes:      matched.SizeBytes,
		DeclaredSHA256: matched.DeclaredSHA256,
		Role:           kind + "_companion",
	})
	plan.TotalRequiredBytes += *matched.SizeBytes
}

func addMissingCompanion(plan *ComponentPlan, inventory []string, kind string) {
	for _, inv := range inventory {
		if candidateKind(inv) == kind {
			plan.Components = append(plan.Components, PlannedComponent{
				Path:   inv,
				Kind:   ComponentKind(kind),
				State:  ComponentUnresolved,
				Reason: fmt.Sprintf("required companion %s present in repository inventory (%s) but not selected", kind, inv),
			})
			plan.Gaps = append(plan.Gaps, fmt.Sprintf("required_companion_unselected: %s (%s)", kind, inv))
			return
		}
	}
	plan.Components = append(plan.Components, PlannedComponent{
		Kind:   ComponentKind(kind),
		State:  ComponentUnresolved,
		Reason: fmt.Sprintf("required companion %s missing from repository inventory", kind),
	})
	plan.Gaps = append(plan.Gaps, "required_companion_missing: "+kind)
}

func planOptionalFiles(plan *ComponentPlan, resolution Resolution, opts BuildComponentPlanOptions) {
	plannedPaths := make(map[string]bool, len(plan.Components))
	for _, c := range plan.Components {
		if c.Path != "" {
			plannedPaths[c.Path] = true
		}
	}
	for _, f := range resolution.Files {
		if plannedPaths[f.Path] {
			continue
		}
		kind := candidateKind(f.Path)
		if kind == "" {
			kind = "companion"
		}
		if slices.Contains(opts.DisabledPaths, f.Path) {
			plan.Components = append(plan.Components, PlannedComponent{
				Path:      f.Path,
				Kind:      ComponentKind(kind),
				State:     ComponentDisabled,
				SizeBytes: f.SizeBytes,
				Role:      "auxiliary",
				Reason:    "operator explicitly disabled file",
			})
			continue
		}
		plan.Components = append(plan.Components, PlannedComponent{
			Path:           f.Path,
			Kind:           ComponentKind(kind),
			State:          ComponentOptional,
			SizeBytes:      f.SizeBytes,
			DeclaredSHA256: f.DeclaredSHA256,
			Role:           "optional_" + kind,
		})
		if f.SizeBytes != nil && *f.SizeBytes > 0 {
			plan.TotalOptionalBytes += *f.SizeBytes
		}
	}
}

func concludePlanStatus(plan *ComponentPlan) {
	slices.Sort(plan.Gaps)
	plan.Gaps = slices.Compact(plan.Gaps)

	hasUnresolved := false
	hasBlocked := false
	for _, c := range plan.Components {
		if c.State == ComponentUnresolved {
			hasUnresolved = true
		}
		if c.State == ComponentDisabled && c.Role != "auxiliary" && c.Role != "disabled_weights" {
			hasBlocked = true
		}
	}

	switch {
	case hasBlocked:
		plan.Status = PlanBlocked
		plan.Reason = "a required companion was disabled by operator"
	case hasUnresolved:
		plan.Status = PlanUnresolved
		plan.Reason = "one or more required model shards or companions are unselected, missing or incomplete"
	case plan.TotalRequiredBytes <= 0:
		plan.Status = PlanUnresolved
		plan.Reason = "no required model shards or weights were selected"
	default:
		plan.Status = PlanComplete
		plan.Reason = "all required model shards and companions are explicitly planned and sized"
	}
}
