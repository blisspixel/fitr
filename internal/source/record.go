package source

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/boundedio"
)

func (result Resolution) Validate() error {
	digest, err := result.Digest()
	if err != nil {
		return err
	}
	if result.ResolutionSHA256 != digest {
		return errors.New("source receipt digest mismatch")
	}
	return nil
}

// Digest validates the receipt's semantics before computing its integrity seal.
// It excludes ResolutionSHA256, so callers can detect mutation of a copy.
func (result Resolution) Digest() (string, error) {
	if err := result.validateFields(); err != nil {
		return "", err
	}
	result.ResolutionSHA256 = ""
	data, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	if len(data)+len("sha256:")+64 > MaxReceiptBytes {
		return "", errors.New("source receipt exceeds one MiB")
	}
	return hashBytes(append([]byte(ResolutionSchema+"\x00"), data...)), nil
}

func (result Resolution) validateFields() error {
	if result.Schema != ResolutionSchema || result.PolicyVersion != PolicyVersion ||
		result.Scope != Scope || result.Provider != "huggingface" || !validVersion(result.ResolverVersion) {
		return errors.New("invalid source schema, policy, provider, scope or resolver version")
	}
	if err := result.Request.Validate(); err != nil {
		return err
	}
	if !slices.IsSorted(result.Request.Files) {
		return errors.New("source request files are not canonical")
	}
	if err := result.validateQueries(); err != nil {
		return err
	}
	if result.State == "unavailable" {
		return result.validateUnavailable()
	}
	if result.ResolvedRepo != result.Request.RepoID || !commitPattern.MatchString(result.ResolvedCommit) ||
		len(result.Queries) != 2 || result.Queries[1].Revision != result.ResolvedCommit ||
		result.Queries[1].Outcome != "complete" {
		return errors.New("source metadata lacks consistent pinned observations")
	}
	if commit := requestedCommit(result.Request.Revision); commit != "" && result.ResolvedCommit != commit {
		return errors.New("source receipt changed an explicit commit")
	}
	if err := result.validateFiles(); err != nil {
		return err
	}
	state, gaps := selectedState(result.Files)
	if state != result.State || !slices.Equal(gaps, result.Gaps) {
		return errors.New("source state or gaps disagree with file evidence")
	}
	return result.validateDependencies()
}

func validVersion(value string) bool {
	return versionPattern.MatchString(value)
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.IsZero() || parsed.UTC().Format(time.RFC3339Nano) != value {
		return time.Time{}, errors.New("source timestamps must be canonical UTC instants")
	}
	return parsed, nil
}

func (result Resolution) validateQueries() error {
	observed, err := parseTime(result.ObservedAt)
	if err != nil {
		return err
	}
	if len(result.Queries) < 1 || len(result.Queries) > 2 || result.Queries[0].Revision != result.Request.Revision {
		return errors.New("source receipt requires one or two ordered metadata queries")
	}
	var previous time.Time
	for index, query := range result.Queries {
		start, err := parseTime(query.StartedAt)
		if err != nil {
			return err
		}
		end, err := parseTime(query.CompletedAt)
		if err != nil {
			return err
		}
		if start.Before(previous) || end.Before(start) || observed.Before(end) {
			return errors.New("source query timestamps are out of order")
		}
		if index > 0 && (!commitPattern.MatchString(query.Revision) || result.Queries[index-1].Outcome != "complete") {
			return errors.New("source query does not follow a successful commit resolution")
		}
		if err := validateQuery(query); err != nil {
			return err
		}
		previous = end
	}
	return nil
}

func validateQuery(query QueryObservation) error {
	if !queryStatusMatches(query.Outcome, query.HTTPStatus) {
		return errors.New("invalid source query outcome")
	}
	if query.ResponseSHA256 != "" && (!shaPattern.MatchString(query.ResponseSHA256) || query.HTTPStatus != 200) {
		return errors.New("invalid source response digest")
	}
	if (query.Outcome == "complete" || query.Outcome == "metadata_invalid") &&
		(query.HTTPStatus != 200 || query.ResponseSHA256 == "") {
		return errors.New("complete metadata body is missing")
	}
	if query.Outcome != "complete" && query.Outcome != "metadata_invalid" && query.ResponseSHA256 != "" {
		return errors.New("partial source response cannot have a full-body digest")
	}
	return nil
}

func queryStatusMatches(outcome string, status int) bool {
	if status < 0 || status > 599 || (status > 0 && status < 100) {
		return false
	}
	switch outcome {
	case "complete", "metadata_invalid", "encoding_refused", "metadata_limit":
		return status == http.StatusOK
	case "request_invalid":
		return status == 0
	case "cancelled", "timeout", "transport_error":
		// Reading a bounded HTTP 200 body can fail after its headers arrive.
		return status == 0 || status == http.StatusOK
	case "header_limit":
		return status >= 100
	case "access_denied":
		return status == http.StatusUnauthorized || status == http.StatusForbidden
	case "not_found_or_private":
		return status == http.StatusNotFound
	case "rate_limited":
		return status == http.StatusTooManyRequests
	case "redirect_refused":
		return status >= 300 && status < 400
	case "http_error":
		return status >= 100 && status != http.StatusOK && (status < 300 || status >= 400) &&
			!slices.Contains([]int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusTooManyRequests}, status)
	default:
		return false
	}
}

func (result Resolution) validateUnavailable() error {
	if result.ResolvedRepo != "" || result.ResolvedCommit != "" || len(result.Files) != 0 ||
		len(result.InventoryPaths) != 0 || len(result.Dependencies) != 0 || len(result.Gaps) != 1 {
		return errors.New("unavailable source receipt cannot assert resolved evidence")
	}
	last := result.Queries[len(result.Queries)-1]
	if last.Outcome != "complete" {
		if result.Gaps[0] != last.Outcome {
			return errors.New("source failure gap disagrees with observation")
		}
		return nil
	}
	if !slices.Contains([]string{"repository_mismatch", "commit_mismatch", "metadata_mismatch", "dependency_limit"}, result.Gaps[0]) {
		return errors.New("source failure gap lacks a known consistency failure")
	}
	return nil
}

func validateFile(file FileMetadata) error {
	if !validPath(file.Path, 512) || (file.State != "present" && file.State != "missing") {
		return errors.New("invalid source file path or state")
	}
	if file.State == "missing" && (file.SizeBytes != nil || file.GitBlobOID != "" || file.DeclaredSHA256 != "") {
		return errors.New("missing source file cannot carry metadata")
	}
	if file.SizeBytes != nil && (*file.SizeBytes < 0 || *file.SizeBytes > maxFileBytes) {
		return errors.New("invalid source byte count")
	}
	if file.GitBlobOID != "" && !commitPattern.MatchString(file.GitBlobOID) {
		return errors.New("invalid Git object ID")
	}
	if file.DeclaredSHA256 != "" && !shaPattern.MatchString(file.DeclaredSHA256) {
		return errors.New("invalid provider-declared SHA-256")
	}
	return nil
}

func (result Resolution) validateFiles() error {
	if len(result.Files) != len(result.Request.Files) {
		return errors.New("source selected-file count mismatch")
	}
	for index, file := range result.Files {
		if err := validateFile(file); err != nil {
			return err
		}
		if file.Path != result.Request.Files[index] {
			return errors.New("source selected-file order mismatch")
		}
	}
	return nil
}

func (result Resolution) validateDependencies() error {
	if len(result.InventoryPaths) > MaxInventory || !slices.IsSorted(result.InventoryPaths) || len(result.Dependencies) > MaxDependencies {
		return errors.New("invalid dependency inventory or finding count")
	}
	inventory := make(map[string]FileMetadata, len(result.InventoryPaths))
	for _, path := range result.InventoryPaths {
		if !validPath(path, 512) {
			return errors.New("invalid dependency inventory path")
		}
		if _, duplicate := inventory[path]; duplicate {
			return errors.New("duplicate dependency inventory path")
		}
		inventory[path] = FileMetadata{Path: path, State: "present"}
	}
	for _, file := range result.Files {
		_, present := inventory[file.Path]
		if present != (file.State == "present") {
			return errors.New("selected file contradicts dependency inventory")
		}
	}
	findings, err := findDependencies(result.Request.Files, inventory)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(findings, result.Dependencies) {
		return errors.New("dependency findings disagree with filename evidence")
	}
	return nil
}

func (result Resolution) JSON() ([]byte, error) {
	if err := result.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, err
	}
	if len(data)+1 > MaxReceiptBytes {
		return nil, errors.New("source receipt exceeds one MiB")
	}
	return append(data, '\n'), nil
}

func LoadResolution(path string) (Resolution, error) {
	path, err := checkedLocalPath(path)
	if err != nil {
		return Resolution{}, err
	}
	if err := rejectSymlinkPath(path); err != nil {
		return Resolution{}, err
	}
	data, err := boundedio.ReadFile(path, MaxReceiptBytes)
	if err != nil {
		return Resolution{}, err
	}
	var result Resolution
	if err := decodeJSON(data, &result, false); err != nil {
		return Resolution{}, err
	}
	return result, result.Validate()
}

// ValidateOutputPath performs only read-only destination checks. The existing
// parent directory must have no symlink components. Raw parent traversal is
// rejected before normalization. No directory is created.
func ValidateOutputPath(path string) error {
	path, err := checkedLocalPath(path)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return err
		}
		return errors.New("source output already exists")
	}
	parent := filepath.Dir(path)
	if err := rejectSymlinkPath(parent); err != nil {
		return err
	}
	info, err := os.Stat(parent)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("source output parent is not a directory")
	}
	return nil
}

// WriteResolution publishes a synced private file by exclusive hard link. Like
// atomicfile.Write it prepares a sibling temporary file, but Rename would
// overwrite existing receipts on some platforms. Unsupported filesystems fail
// closed. Path checks do not sandbox a hostile process with equal permissions.
func WriteResolution(path string, result Resolution) error {
	path, err := checkedLocalPath(path)
	if err != nil {
		return err
	}
	data, err := result.JSON()
	if err != nil {
		return err
	}
	if err := ValidateOutputPath(path); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".fitr-source-*")
	if err != nil {
		return err
	}
	defer func() { _ = temporary.Close(); _ = os.Remove(temporary.Name()) }()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := ValidateOutputPath(path); err != nil {
		return err
	}
	return os.Link(temporary.Name(), path)
}

func rejectSymlinkPath(path string) error {
	absolute, err := checkedLocalPath(path)
	if err != nil {
		return err
	}
	for {
		info, err := os.Lstat(absolute)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("source paths cannot contain symbolic links; use the canonical physical directory path")
		}
		parent := filepath.Dir(absolute)
		if parent == absolute {
			return nil
		}
		absolute = parent
	}
}

// Check lexical components before Abs/Dir can discard them. Otherwise a raw
// link/../file path can resolve differently from the cleaned path we inspect.
// Every file operation then uses the same absolute spelling. A leading ./ is
// safe and remains supported; parent traversal is deliberately unsupported.
func checkedLocalPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" || strings.ContainsAny(path, "\x00\r\n") {
		return "", errors.New("invalid source path")
	}
	relative := strings.TrimPrefix(path, filepath.VolumeName(path))
	for _, part := range strings.FieldsFunc(relative, func(char rune) bool { return char == '/' || char == '\\' }) {
		if part == ".." {
			return "", errors.New("source paths cannot contain parent traversal; use the canonical physical path")
		}
	}
	return filepath.Abs(path)
}
