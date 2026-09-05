package source

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSourceRejectsRawParentTraversal(t *testing.T) {
	dir := sourceTempDir(t)
	result := sourceFixture(t)
	for _, suffix := range []string{"/child/../receipt.json", `/child\..\receipt.json`, "/../receipt.json"} {
		rawPath := dir + suffix
		if err := ValidateOutputPath(rawPath); err == nil || !strings.Contains(err.Error(), "parent traversal") {
			t.Fatalf("raw output path accepted: %q, %v", rawPath, err)
		}
		if err := WriteResolution(rawPath, result); err == nil {
			t.Fatalf("raw write accepted: %q", rawPath)
		}
		if _, err := LoadResolution(rawPath); err == nil {
			t.Fatalf("raw read accepted: %q", rawPath)
		}
	}
	if runtime.GOOS == "windows" {
		if _, err := checkedLocalPath(`C:..\receipt.json`); err == nil {
			t.Fatal("drive-relative traversal accepted")
		}
	}
}

func TestSourceRawSymlinkTraversalCannotEscape(t *testing.T) {
	dir := sourceTempDir(t)
	safe := filepath.Join(dir, "safe")
	outside := filepath.Join(dir, "outside")
	child := filepath.Join(outside, "child")
	for _, path := range []string{safe, outside, child} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(child, filepath.Join(safe, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// filepath.Join would erase the exact components that caused the bypass.
	separator := string(filepath.Separator)
	rawPath := safe + separator + "link" + separator + ".." + separator + "receipt.json"
	result := sourceFixture(t)
	if err := ValidateOutputPath(rawPath); err == nil {
		t.Fatal("symlink traversal passed preflight")
	}
	if err := WriteResolution(rawPath, result); err == nil {
		t.Fatal("symlink traversal published a receipt")
	}
	outsideFile := filepath.Join(outside, "receipt.json")
	if _, err := os.Stat(outsideFile); !os.IsNotExist(err) {
		t.Fatal("write escaped checked directory")
	}
	for _, path := range []string{filepath.Join(safe, "receipt.json"), outsideFile} {
		if err := WriteResolution(path, result); err != nil {
			t.Fatal(err)
		}
	}
	if runtime.GOOS != "windows" {
		sourceAssertRawOutside(t, rawPath, outsideFile)
	}
	if _, err := LoadResolution(rawPath); err == nil {
		t.Fatal("symlink traversal read an unchecked outside receipt")
	}
}

func sourceAssertRawOutside(t *testing.T, rawPath, outsideFile string) {
	t.Helper()
	rawInfo, err := os.Stat(rawPath)
	if err != nil {
		t.Fatal(err)
	}
	outsideInfo, err := os.Stat(outsideFile)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(rawInfo, outsideInfo) {
		t.Fatal("regression fixture did not reproduce POSIX symlink traversal")
	}
}

func TestSourceSafeDotRelativePath(t *testing.T) {
	t.Chdir(sourceTempDir(t))
	result := sourceFixture(t)
	path := "./receipt.json"
	if err := ValidateOutputPath(path); err != nil {
		t.Fatal(err)
	}
	if err := WriteResolution(path, result); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadResolution(path)
	if err != nil || loaded.ResolutionSHA256 != result.ResolutionSHA256 {
		t.Fatalf("safe relative path failed: %v", err)
	}
}

func TestSourceQueryOutcomeStatusConsistency(t *testing.T) {
	cases := []struct {
		outcome   string
		good, bad []int
	}{
		{"access_denied", []int{401, 403}, []int{0, 200, 404}},
		{"not_found_or_private", []int{404}, []int{0, 200, 403}},
		{"rate_limited", []int{429}, []int{0, 200, 404}},
		{"redirect_refused", []int{300, 302, 304, 399}, []int{0, 200, 400}},
		{"http_error", []int{101, 201, 206, 400, 500, 599}, []int{0, 99, 200, 302, 401, 403, 404, 429, 600}},
		{"header_limit", []int{101, 200, 302, 403, 500}, []int{0, 99, 600}},
		{"encoding_refused", []int{200}, []int{0, 206, 500}},
		{"metadata_limit", []int{200}, []int{0, 206, 500}},
		{"metadata_invalid", []int{200}, []int{0, 206, 500}},
		{"request_invalid", []int{0}, []int{200, 400}},
		{"timeout", []int{0, 200}, []int{206, 403, 500}},
		{"transport_error", []int{0, 200}, []int{206, 403, 500}},
		{"cancelled", []int{0, 200}, []int{206, 403, 500}},
	}
	fixture := sourceFixture(t)
	for _, test := range cases {
		for _, status := range test.good {
			t.Run(fmt.Sprintf("%s_%d_valid", test.outcome, status), func(t *testing.T) {
				result := sourceUnavailableFixture(t, fixture, test.outcome, status)
				if err := result.Validate(); err != nil {
					t.Fatalf("legitimate observation rejected: %v", err)
				}
			})
		}
		for _, status := range test.bad {
			t.Run(fmt.Sprintf("%s_%d_invalid", test.outcome, status), func(t *testing.T) {
				result := sourceUnavailableFixture(t, fixture, test.outcome, status)
				if err := result.Validate(); err == nil {
					t.Fatal("rehashed impossible observation accepted")
				}
			})
		}
	}
}

func sourceUnavailableFixture(t *testing.T, fixture Resolution, outcome string, status int) Resolution {
	t.Helper()
	result := sourceClone(t, fixture)
	result.State = "unavailable"
	result.ResolvedRepo, result.ResolvedCommit = "", ""
	result.Files, result.InventoryPaths, result.Dependencies = nil, nil, nil
	result.Queries = result.Queries[:1]
	result.Queries[0].Outcome, result.Queries[0].HTTPStatus = outcome, status
	if outcome != "metadata_invalid" {
		result.Queries[0].ResponseSHA256 = ""
	}
	result.Gaps = []string{outcome}
	// Bypass Digest's semantic checks to simulate an attacker recomputing a seal.
	result.ResolutionSHA256 = ""
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	result.ResolutionSHA256 = hashBytes(append([]byte(ResolutionSchema+"\x00"), data...))
	return result
}
