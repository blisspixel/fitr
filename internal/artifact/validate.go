package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/source"
)

var versionPattern = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,128}$`)

func (result Binding) Validate() error {
	digest, err := result.Digest()
	if err != nil {
		return err
	}
	if digest != result.BindingSHA256 {
		return errors.New("artifact binding digest mismatch")
	}
	return nil
}

// Digest validates structural consistency before sealing. A seal is not a
// signature and cannot authenticate facts asserted by a receipt's author.
func (result Binding) Digest() (string, error) {
	if err := result.validateFields(); err != nil {
		return "", err
	}
	result.BindingSHA256 = ""
	data, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	if len(data)+71 > MaxReceiptBytes {
		return "", errors.New("artifact receipt exceeds two MiB")
	}
	sum := sha256.Sum256(append([]byte(BindingSchema+"\x00"), data...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (result Binding) validateFields() error {
	if result.Schema != BindingSchema || result.PolicyVersion != PolicyVersion || !versionPattern.MatchString(result.BinderVersion) ||
		result.RuntimeState != "unbound" || result.DependencyState != "unverified" || result.CapacityState != "unmeasured" || result.QualityState != "unmeasured" {
		return errors.New("invalid artifact schema, version or evidence boundary")
	}
	if err := result.Source.Validate(); err != nil {
		return err
	}
	if result.Source.ResolvedCommit == "" || result.ResolutionSHA256 != result.Source.ResolutionSHA256 || result.Mapping.ResolutionSHA256 != result.ResolutionSHA256 {
		return errors.New("artifact binding lacks its exact pinned source receipt")
	}
	if err := result.Mapping.Validate(); err != nil {
		return err
	}
	if err := validateLimitsAndTimes(result); err != nil {
		return err
	}
	if err := result.validateFiles(); err != nil {
		return err
	}
	state, gaps, unmapped := deriveResult(result)
	if result.State != state || !slices.Equal(result.Gaps, gaps) || !slices.Equal(result.UnmappedFiles, unmapped) {
		return errors.New("artifact summary or gaps disagree with file observations")
	}
	return nil
}

func validateLimitsAndTimes(result Binding) error {
	if result.Limits.MaxBytes < 1 || result.Limits.MaxBytes > HardMaxBytes || result.Limits.TimeoutMillis < 1 || result.Limits.TimeoutMillis > HardTimeout.Milliseconds() || result.BytesRead < 0 || result.BytesRead > result.Limits.MaxBytes {
		return errors.New("invalid artifact limits or byte accounting")
	}
	start, err := canonicalTime(result.StartedAt)
	if err != nil {
		return err
	}
	end, err := canonicalTime(result.CompletedAt)
	if err != nil {
		return err
	}
	if end.Before(start) {
		return errors.New("artifact observation timestamps are out of order")
	}
	if end.Sub(start) > time.Duration(result.Limits.TimeoutMillis)*time.Millisecond && result.State != "timeout" && result.State != "cancelled" {
		return errors.New("artifact completion exceeded its deadline without a stopped outcome")
	}
	// The limit is cooperative; an OS syscall may finish after the deadline.
	return nil
}

func canonicalTime(value string) (time.Time, error) {
	instant, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || instant.IsZero() || instant.UTC().Format(time.RFC3339Nano) != value {
		return time.Time{}, errors.New("artifact timestamps must be canonical UTC instants")
	}
	return instant, nil
}

func (result Binding) validateFiles() error {
	if len(result.Files) != len(result.Mapping.Files) {
		return errors.New("artifact observation count mismatch")
	}
	var total, planned int64
	budgetLimited := false
	for index, file := range result.Files {
		if (result.State == "cancelled" || result.State == "timeout") && file.State != result.State {
			return errors.New("stopped artifact binding must invalidate every mapped file outcome")
		}
		mapping := result.Mapping.Files[index]
		if file.SourcePath != mapping.SourcePath || file.LocalPath != mapping.LocalPath || file.ComponentRole != mapping.ComponentRole ||
			cleanPortablePath(file.LocalPath) != strings.ReplaceAll(file.LocalPath, "\\", "/") ||
			!slices.Contains(result.Source.Request.Files, file.SourcePath) || (index > 0 && result.Files[index-1].SourcePath >= file.SourcePath) {
			return errors.New("artifact file observations disagree with the canonical mapping")
		}
		if err := validateObservation(file, sourceFile(result.Source, file.SourcePath)); err != nil {
			return fmt.Errorf("artifact file %s: %w", file.SourcePath, err)
		}
		if file.BytesRead > result.Limits.MaxBytes-total {
			return errors.New("artifact file reads exceed the aggregate limit")
		}
		total += file.BytesRead
		if file.Before != nil {
			planned = cappedSum(planned, file.Before.SizeBytes)
		}
		budgetLimited = budgetLimited || file.State == "budget_exceeded"
	}
	if total != result.BytesRead || (budgetLimited && (planned <= result.Limits.MaxBytes || total != 0)) || (planned > result.Limits.MaxBytes && total != 0) {
		return errors.New("artifact aggregate reads or preflight budget outcome are inconsistent")
	}
	return nil
}

func cappedSum(total, value int64) int64 {
	if value > HardMaxBytes || total > HardMaxBytes-value {
		return HardMaxBytes + 1
	}
	return total + value
}

func validateObservation(file FileObservation, metadata source.FileMetadata) error {
	for _, observed := range []*FileFacts{file.Before, file.After} {
		if observed == nil {
			continue
		}
		if observed.SizeBytes < 0 {
			return errors.New("negative observed size")
		}
		if _, err := canonicalTime(observed.ModifiedAt); err != nil {
			return err
		}
	}
	if file.BytesRead < 0 || (file.Before == nil && file.BytesRead != 0) || (file.Before != nil && file.BytesRead > file.Before.SizeBytes) {
		return errors.New("invalid observed byte count")
	}
	if file.Before != nil && metadata.SizeBytes != nil && file.Before.SizeBytes != *metadata.SizeBytes &&
		(file.BytesRead != 0 || !slices.Contains([]string{"size_mismatch", "changed", "cancelled", "timeout"}, file.State)) {
		return errors.New("known preflight size disagreement cannot permit reads or a different preflight outcome")
	}
	if hashedState(file.State) {
		return validateHashed(file, metadata)
	}
	if file.ObservedSHA256 != "" || (file.State != "changed" && (file.After != nil || file.IdentityState != "unobserved")) {
		return errors.New("incomplete observation asserts verified bytes or identity")
	}
	return validateIncomplete(file, metadata)
}

func validateIncomplete(file FileObservation, metadata source.FileMetadata) error {
	switch file.State {
	case "size_mismatch":
		if file.Before == nil || metadata.SizeBytes == nil || file.Before.SizeBytes == *metadata.SizeBytes || file.BytesRead != 0 {
			return errors.New("size mismatch lacks conflicting size evidence")
		}
	case "missing":
		if file.Before != nil || file.BytesRead != 0 {
			return errors.New("missing file carries observed data")
		}
	case "changed":
		if file.IdentityState != "changed" || (file.Before == nil && file.After == nil) {
			return errors.New("changed file lacks a prior or subsequent observation")
		}
	case "budget_exceeded":
		if file.Before == nil || file.BytesRead != 0 {
			return errors.New("preflight budget failure cannot contain file reads")
		}
	case "cancelled", "timeout", "read_error":
	default:
		return errors.New("invalid terminal file outcome")
	}
	return nil
}

func validateHashed(file FileObservation, metadata source.FileMetadata) error {
	if file.Before == nil || file.After == nil || *file.Before != *file.After || file.IdentityState != "verified" || file.BytesRead != file.Before.SizeBytes || !shaPattern.MatchString(file.ObservedSHA256) {
		return errors.New("complete hash lacks complete stable file observations")
	}
	if metadata.SizeBytes != nil && *metadata.SizeBytes != file.Before.SizeBytes {
		return errors.New("complete hash conceals a known size mismatch")
	}
	state := "locally_hashed"
	if metadata.DeclaredSHA256 != "" {
		state = "matched"
		if metadata.DeclaredSHA256 != file.ObservedSHA256 {
			state = "hash_mismatch"
		}
	}
	if file.State != state {
		return errors.New("file outcome disagrees with declared and observed hashes")
	}
	return nil
}

func deriveResult(result Binding) (string, []string, []string) {
	gaps := []string{"dependency_compatibility_unverified", "runtime_unbound", "capacity_unmeasured", "quality_unmeasured", "component_roles_operator_declared"}
	for _, gap := range result.Source.Gaps {
		gaps = append(gaps, "source:"+gap)
	}
	mapped, states := map[string]bool{}, map[string]bool{}
	for _, file := range result.Files {
		mapped[file.SourcePath], states[file.State] = true, true
		if file.State != "matched" {
			gaps = append(gaps, "file:"+file.SourcePath+":"+file.State)
		}
	}
	unmapped := []string{}
	for _, path := range result.Source.Request.Files {
		if !mapped[path] {
			unmapped = append(unmapped, path)
			gaps = append(gaps, "unmapped:"+path)
		}
	}
	slices.Sort(gaps)
	return summaryState(states, len(unmapped) > 0, result.Source.State), slices.Compact(gaps), unmapped
}

func summaryState(states map[string]bool, unmapped bool, sourceState string) string {
	for _, state := range []string{"cancelled", "timeout", "budget_exceeded", "changed"} {
		if states[state] {
			return state
		}
	}
	if states["size_mismatch"] || states["hash_mismatch"] {
		return "mismatch"
	}
	if len(states) == 1 && states["read_error"] {
		return "unavailable"
	}
	if unmapped || states["missing"] || states["read_error"] {
		return "incomplete"
	}
	if states["locally_hashed"] {
		return "locally_hashed"
	}
	if sourceState != "resolved" {
		return "incomplete"
	}
	return "matched"
}

func comparePaths(left, right string) int { return strings.Compare(left, right) }
