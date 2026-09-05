// Package role stores explicit quality floors and evidence attachments for personal roles.
package role

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/blisspixel/fitr/internal/boundedio"
	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/score"
	"github.com/blisspixel/fitr/internal/strictjson"
)

const (
	SpecSchema          = "fitr.role.spec.v1"
	LibrarySchema       = "fitr.role.library.v1"
	maximumLibraryBytes = 1 << 20
)

var roleNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

type Spec struct {
	Schema      string                `json:"schema"`
	Name        string                `json:"name"`
	Description string                `json:"description,omitempty"`
	Decision    decision.DecisionSpec `json:"decision"`
	Preferences []Preference          `json:"preferences"`
	MaxAgeDays  int                   `json:"max_age_days"`
}

// Preference anchors use the referenced requirement's observation units:
// fractions for behavior, bytes for capacity, and the performance metric's units.
type Preference struct {
	Requirement string  `json:"requirement"`
	Weight      float64 `json:"weight"`
	Worst       float64 `json:"worst"`
	Best        float64 `json:"best"`
}

func (spec Spec) Validate() error {
	if spec.Schema != SpecSchema || !roleNamePattern.MatchString(spec.Name) {
		return errors.New("invalid role schema or name")
	}
	if !roleTextValid(spec.Description, 512, true) {
		return errors.New("role description is oversized or contains invalid text")
	}
	if spec.MaxAgeDays < 1 || spec.MaxAgeDays > 3650 {
		return errors.New("role max_age_days must be between 1 and 3650")
	}
	if err := spec.Decision.Validate(); err != nil {
		return fmt.Errorf("role decision: %w", err)
	}
	if spec.Decision.Evidence != decision.EvidenceDecide || spec.Decision.Objective != nil ||
		spec.Decision.Confirmation.FreshEvidence || spec.Decision.Confirmation.FreshTasks {
		return errors.New("role decision must use decide evidence without an objective or confirmation flags")
	}
	if err := validateRoleFloors(spec.Decision); err != nil {
		return err
	}
	if len(spec.Preferences) < 1 || len(spec.Preferences) > 8 {
		return errors.New("role requires between one and eight preferences")
	}
	requirements := make(map[string]decision.Requirement, len(spec.Decision.Requirements))
	for _, requirement := range spec.Decision.Requirements {
		requirements[requirement.ID] = requirement
	}
	seen := make(map[string]bool, len(spec.Preferences))
	for _, preference := range spec.Preferences {
		requirement, ok := requirements[preference.Requirement]
		if !ok || seen[preference.Requirement] {
			return errors.New("role preference references an unknown or repeated requirement")
		}
		seen[preference.Requirement] = true
		if err := validatePreference(preference, requirement); err != nil {
			return fmt.Errorf("role preference %q: %w", preference.Requirement, err)
		}
	}
	return nil
}

func validateRoleFloors(spec decision.DecisionSpec) error {
	var quality, context, capacity bool
	for _, requirement := range spec.Requirements {
		if !roleTextValid(requirement.ID, 128, false) {
			return errors.New("role requirement ID is oversized or contains invalid text")
		}
		if behavior := requirement.Behavior; behavior != nil {
			if behavior.MinimumRate != nil && !decision.SupportsBehaviorRate(behavior.Need) {
				return fmt.Errorf("role behavior %q has no numeric rate evidence; use required_state or a supported rate need", behavior.Need)
			}
			quality = quality || behavior.RequiredState == score.Pass ||
				(behavior.MinimumRate != nil && *behavior.MinimumRate > 0)
		}
		context = context || requirement.Context != nil
		capacity = capacity || requirement.Capacity != nil
	}
	if !quality || !context || !capacity {
		return errors.New("role requires an explicit behavior quality floor, context floor, and capacity limit")
	}
	return nil
}

func validatePreference(preference Preference, requirement decision.Requirement) error {
	if !roleFinite(preference.Weight) || preference.Weight <= 0 || preference.Weight > 1e6 {
		return errors.New("weight must be finite, positive, and at most 1000000")
	}
	if !roleFinite(preference.Worst) || !roleFinite(preference.Best) ||
		preference.Worst < 0 || preference.Best < 0 ||
		preference.Worst > 1e18 || preference.Best > 1e18 || preference.Worst == preference.Best {
		return errors.New("anchors must be distinct finite nonnegative values at most 1e18")
	}
	var maximize bool
	switch {
	case requirement.Behavior != nil && requirement.Behavior.MinimumRate != nil:
		maximize = true
		if preference.Best > 1 || preference.Worst > 1 {
			return errors.New("behavior anchors must be fractions between zero and one")
		}
	case requirement.Performance != nil:
		maximize = requirement.Performance.AtLeast != nil
	case requirement.Capacity != nil:
		maximize = false
	default:
		return errors.New("preference must reference numeric behavior, performance, or capacity")
	}
	if maximize != (preference.Best > preference.Worst) {
		return errors.New("anchor direction conflicts with the requirement bound")
	}
	return nil
}

func (spec Spec) Digest() (string, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	if len(data) > maximumLibraryBytes {
		return "", errors.New("role specification exceeds one MiB")
	}
	sum := sha256.Sum256(append([]byte(SpecSchema+"\x00"), data...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func LoadSpec(path string) (Spec, error) {
	var spec Spec
	if err := readRoleJSON(path, &spec); err != nil {
		return Spec{}, err
	}
	return spec, spec.Validate()
}

func readRoleJSON(path string, destination any) error {
	if err := rejectRoleSymlink(path); err != nil {
		return err
	}
	data, err := boundedio.ReadFile(path, maximumLibraryBytes)
	if err != nil {
		return err
	}
	if err := strictjson.Validate(data); err != nil {
		return fmt.Errorf("invalid role JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("invalid role fields: %w", err)
	}
	return nil
}

func roleTextValid(value string, maximum int, optional bool) bool {
	if optional && value == "" {
		return true
	}
	return utf8.ValidString(value) && len(value) <= maximum && strings.TrimSpace(value) != "" &&
		strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) < 0
}

func roleFinite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
