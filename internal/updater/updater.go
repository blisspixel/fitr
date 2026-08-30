// Package updater securely discovers and stages official fitr releases.
package updater

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	RepositoryURL    = "https://github.com/blisspixel/fitr"
	LatestReleaseURL = "https://api.github.com/repos/blisspixel/fitr/releases/latest"
	checksumsAsset   = "SHA256SUMS"
	maxMetadataBytes = 2 << 20
	maxChecksumBytes = 1 << 20
	maxBinaryBytes   = 64 << 20
	maxVersionOutput = 4 << 10
)

// State describes the relationship between the running build and the latest
// stable release.
type State string

const (
	StateCurrent         State = "current"
	StateUpdateAvailable State = "update_available"
	StateAhead           State = "ahead"
)

// Plan is an immutable update decision made from one GitHub release receipt.
type Plan struct {
	State             State
	CurrentVersion    string
	LatestVersion     string
	AssetName         string
	AssetURL          string
	ChecksumsURL      string
	ReleaseURL        string
	ReportedAssetSize int64
}

// Client fetches official release metadata and assets. LatestURL is exported
// only so tests can use an isolated HTTP server.
type Client struct {
	HTTPClient *http.Client
	LatestURL  string
	UserAgent  string
}

// NewClient returns a bounded client for the official fitr repository.
func NewClient(version string) *Client {
	return &Client{
		HTTPClient: &http.Client{
			Timeout: 45 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("too many release download redirects")
				}
				if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
					return errors.New("release download redirected away from HTTPS")
				}
				return nil
			},
		},
		LatestURL: LatestReleaseURL,
		UserAgent: "fitr/" + strings.TrimSpace(version),
	}
}

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

type releaseReceipt struct {
	TagName    string         `json:"tag_name"`
	HTMLURL    string         `json:"html_url"`
	Draft      bool           `json:"draft"`
	Prerelease bool           `json:"prerelease"`
	Assets     []releaseAsset `json:"assets"`
}

// AssetName returns the exact release asset for an operating system and
// architecture. Unsupported targets fail closed.
func AssetName(goos, goarch string) (string, error) {
	switch goos + "/" + goarch {
	case "linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64":
		return "fitr-" + goos + "-" + goarch, nil
	case "windows/amd64", "windows/arm64":
		return "fitr-" + goos + "-" + goarch + ".exe", nil
	default:
		return "", fmt.Errorf("no official fitr release asset for %s/%s", goos, goarch)
	}
}

// Lookup discovers the latest stable release and selects the current machine's
// asset. GitHub's latest-release endpoint excludes prereleases.
func (c *Client) Lookup(ctx context.Context, currentVersion string) (Plan, error) {
	current, err := parseVersion(currentVersion)
	if err != nil {
		return Plan{}, fmt.Errorf("parse running version: %w", err)
	}
	assetName, err := AssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return Plan{}, err
	}
	body, err := c.fetch(ctx, c.latestURL(), maxMetadataBytes, "application/vnd.github+json")
	if err != nil {
		return Plan{}, fmt.Errorf("read latest release: %w", err)
	}
	var release releaseReceipt
	if err := json.Unmarshal(body, &release); err != nil {
		return Plan{}, fmt.Errorf("decode latest release: %w", err)
	}
	latestText, latest, err := stableReleaseVersion(release)
	if err != nil {
		return Plan{}, err
	}
	asset, checksums, err := releaseAssets(release.Assets, assetName)
	if err != nil {
		return Plan{}, err
	}
	if asset.Size <= 0 || asset.Size > maxBinaryBytes {
		return Plan{}, fmt.Errorf("release asset %s reports an invalid size of %d bytes", assetName, asset.Size)
	}
	if err := c.validateOfficialAssetURLs(assetName, asset.BrowserDownloadURL, checksums.BrowserDownloadURL); err != nil {
		return Plan{}, err
	}
	return buildPlan(currentVersion, current, latestText, latest, release, asset, checksums), nil
}

func stableReleaseVersion(release releaseReceipt) (string, semVersion, error) {
	if release.Draft || release.Prerelease {
		return "", semVersion{}, errors.New("GitHub latest release receipt unexpectedly names a draft or prerelease")
	}
	tagName := strings.TrimSpace(release.TagName)
	if !strings.HasPrefix(tagName, "v") || strings.Contains(tagName, "+") {
		return "", semVersion{}, fmt.Errorf("latest release tag %q is not canonical vMAJOR.MINOR.PATCH", release.TagName)
	}
	latestText := strings.TrimPrefix(tagName, "v")
	latest, err := parseVersion(latestText)
	if err != nil {
		return "", semVersion{}, fmt.Errorf("parse latest release tag %q: %w", release.TagName, err)
	}
	if latest.prerelease != "" {
		return "", semVersion{}, fmt.Errorf("latest release tag %q is not stable", release.TagName)
	}
	return latestText, latest, nil
}

func releaseAssets(assets []releaseAsset, assetName string) (releaseAsset, releaseAsset, error) {
	asset, err := exactlyOneAsset(assets, assetName)
	if err != nil {
		return releaseAsset{}, releaseAsset{}, err
	}
	checksums, err := exactlyOneAsset(assets, checksumsAsset)
	if err != nil {
		return releaseAsset{}, releaseAsset{}, err
	}
	return asset, checksums, nil
}

func (c *Client) validateOfficialAssetURLs(assetName, assetURL, checksumsURL string) error {
	if c.latestURL() != LatestReleaseURL {
		return nil
	}
	for name, rawURL := range map[string]string{assetName: assetURL, checksumsAsset: checksumsURL} {
		if !strings.HasPrefix(rawURL, "https://") {
			return fmt.Errorf("official release asset %s does not use HTTPS", name)
		}
	}
	return nil
}

func buildPlan(currentVersion string, current semVersion, latestText string, latest semVersion,
	release releaseReceipt, asset, checksums releaseAsset) Plan {
	state := StateCurrent
	switch compareVersions(current, latest) {
	case -1:
		state = StateUpdateAvailable
	case 1:
		state = StateAhead
	}
	releaseURL := strings.TrimSpace(release.HTMLURL)
	if releaseURL == "" {
		releaseURL = RepositoryURL + "/releases/tag/v" + latestText
	}
	return Plan{
		State:             state,
		CurrentVersion:    strings.TrimSpace(currentVersion),
		LatestVersion:     latestText,
		AssetName:         asset.Name,
		AssetURL:          asset.BrowserDownloadURL,
		ChecksumsURL:      checksums.BrowserDownloadURL,
		ReleaseURL:        releaseURL,
		ReportedAssetSize: asset.Size,
	}
}

// Validate confirms that the release manifest contains one valid checksum for
// the selected asset. It intentionally does not download the binary.
func (c *Client) Validate(ctx context.Context, plan Plan) error {
	manifest, err := c.fetch(ctx, plan.ChecksumsURL, maxChecksumBytes, "application/octet-stream")
	if err != nil {
		return fmt.Errorf("download release checksums: %w", err)
	}
	if _, err := checksumFor(manifest, plan.AssetName); err != nil {
		return err
	}
	return nil
}

func (c *Client) latestURL() string {
	if strings.TrimSpace(c.LatestURL) != "" {
		return c.LatestURL
	}
	return LatestReleaseURL
}

func exactlyOneAsset(assets []releaseAsset, name string) (releaseAsset, error) {
	var matches []releaseAsset
	for _, asset := range assets {
		if asset.Name == name {
			matches = append(matches, asset)
		}
	}
	if len(matches) != 1 {
		return releaseAsset{}, fmt.Errorf("latest release has %d assets named %s; want exactly one", len(matches), name)
	}
	if strings.TrimSpace(matches[0].BrowserDownloadURL) == "" {
		return releaseAsset{}, fmt.Errorf("release asset %s has no download URL", name)
	}
	return matches[0], nil
}

// Download stages the selected binary in targetDir and verifies it against the
// checksum manifest from the same release. The caller owns the returned file.
func (c *Client) Download(ctx context.Context, plan Plan, targetDir string) (path, digest string, err error) {
	manifest, err := c.fetch(ctx, plan.ChecksumsURL, maxChecksumBytes, "application/octet-stream")
	if err != nil {
		return "", "", fmt.Errorf("download release checksums: %w", err)
	}
	expected, err := checksumFor(manifest, plan.AssetName)
	if err != nil {
		return "", "", err
	}

	response, err := c.request(ctx, plan.AssetURL, "application/octet-stream")
	if err != nil {
		return "", "", fmt.Errorf("download %s: %w", plan.AssetName, err)
	}
	defer response.Body.Close()
	if response.ContentLength > maxBinaryBytes {
		return "", "", fmt.Errorf("download %s exceeds the %d-byte safety limit", plan.AssetName, maxBinaryBytes)
	}
	return stageDownload(response.Body, plan, targetDir, expected)
}

func stageDownload(source io.Reader, plan Plan, targetDir, expected string) (path, digest string, err error) {
	pattern := ".fitr-update-*"
	if runtime.GOOS == "windows" {
		pattern += ".exe"
	}
	file, err := os.CreateTemp(targetDir, pattern)
	if err != nil {
		return "", "", fmt.Errorf("create staged update beside executable: %w", err)
	}
	stagedPath := file.Name()
	path = stagedPath
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(stagedPath)
		}
	}()
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(source, maxBinaryBytes+1))
	if copyErr != nil {
		_ = file.Close()
		return "", "", fmt.Errorf("write staged update: %w", copyErr)
	}
	if written > maxBinaryBytes {
		_ = file.Close()
		return "", "", fmt.Errorf("download %s exceeds the %d-byte safety limit", plan.AssetName, maxBinaryBytes)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", "", fmt.Errorf("sync staged update: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", "", fmt.Errorf("close staged update: %w", err)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, expected) {
		return "", "", fmt.Errorf("checksum mismatch for %s: got %s, want %s", plan.AssetName, actual, expected)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		return "", "", fmt.Errorf("make staged update executable: %w", err)
	}
	keep = true
	return path, "sha256:" + actual, nil
}

func (c *Client) request(ctx context.Context, url, accept string) (*http.Response, error) {
	if strings.TrimSpace(url) == "" {
		return nil, errors.New("release receipt contains an empty download URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("%s returned HTTP %d: %s", url, response.StatusCode, strings.TrimSpace(string(body)))
	}
	return response, nil
}

func (c *Client) fetch(ctx context.Context, url string, limit int64, accept string) ([]byte, error) {
	response, err := c.request(ctx, url, accept)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.ContentLength > limit {
		return nil, fmt.Errorf("response exceeds the %d-byte safety limit", limit)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response exceeds the %d-byte safety limit", limit)
	}
	return body, nil
}

func checksumFor(manifest []byte, assetName string) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(manifest)))
	var matches []string
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) != 2 {
			return "", fmt.Errorf("malformed checksum manifest line %d", line)
		}
		name := strings.TrimPrefix(fields[1], "*")
		if name != assetName {
			continue
		}
		digest := strings.ToLower(fields[0])
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != sha256.Size {
			return "", fmt.Errorf("invalid SHA-256 for %s in checksum manifest", assetName)
		}
		matches = append(matches, digest)
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read checksum manifest: %w", err)
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("checksum manifest has %d entries for %s; want exactly one", len(matches), assetName)
	}
	return matches[0], nil
}

// VerifyVersion executes the staged binary and requires an exact version
// receipt before replacement.
func VerifyVersion(ctx context.Context, stagedPath, expectedVersion string) error {
	verifyCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	output := &cappedBuffer{limit: maxVersionOutput}
	command := exec.CommandContext(verifyCtx, stagedPath, "version")
	command.Stdout, command.Stderr = output, output
	err := command.Run()
	if err != nil {
		return fmt.Errorf("execute staged update: %w", err)
	}
	if output.overflow {
		return fmt.Errorf("staged update version output exceeds %d bytes", maxVersionOutput)
	}
	want := "fitr " + strings.TrimSpace(expectedVersion)
	if strings.TrimSpace(output.String()) != want {
		return fmt.Errorf("staged update identified itself as %q, want %q", strings.TrimSpace(output.String()), want)
	}
	return nil
}

type cappedBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (buffer *cappedBuffer) Write(data []byte) (int, error) {
	written := len(data)
	remaining := max(buffer.limit-buffer.buf.Len(), 0)
	if len(data) > remaining {
		buffer.overflow = true
		data = data[:remaining]
	}
	_, _ = buffer.buf.Write(data)
	return written, nil
}

func (buffer *cappedBuffer) String() string { return buffer.buf.String() }

// HashFile returns the SHA-256 receipt used by the pre-replacement digest
// guard. The prefix keeps it distinct from an untyped identifier.
func HashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

type semVersion struct {
	major, minor, patch int
	prerelease          string
}

func parseVersion(text string) (semVersion, error) {
	text = strings.TrimPrefix(strings.TrimSpace(text), "v")
	core := text
	prerelease := ""
	if at := strings.IndexAny(core, "-+"); at >= 0 {
		if core[at] == '-' {
			prerelease = core[at+1:]
		}
		core = core[:at]
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 || prerelease == "" && strings.Contains(text, "-") {
		return semVersion{}, fmt.Errorf("%q is not a supported semantic version", text)
	}
	values := make([]int, 3)
	for i, part := range parts {
		if part == "" || len(part) > 1 && part[0] == '0' {
			return semVersion{}, fmt.Errorf("%q is not a supported semantic version", text)
		}
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return semVersion{}, fmt.Errorf("%q is not a supported semantic version", text)
		}
		values[i] = value
	}
	if prerelease != "" {
		for _, part := range strings.Split(prerelease, ".") {
			if part == "" {
				return semVersion{}, fmt.Errorf("%q is not a supported semantic version", text)
			}
		}
	}
	return semVersion{major: values[0], minor: values[1], patch: values[2], prerelease: prerelease}, nil
}

func compareVersions(a, b semVersion) int {
	for _, pair := range [][2]int{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if a.prerelease == b.prerelease {
		return 0
	}
	if a.prerelease == "" {
		return 1
	}
	if b.prerelease == "" {
		return -1
	}
	return comparePrerelease(a.prerelease, b.prerelease)
}

func comparePrerelease(a, b string) int {
	ap, bp := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(ap) && i < len(bp); i++ {
		if ap[i] == bp[i] {
			continue
		}
		ai, aerr := strconv.Atoi(ap[i])
		bi, berr := strconv.Atoi(bp[i])
		switch {
		case aerr == nil && berr == nil && ai < bi:
			return -1
		case aerr == nil && berr == nil:
			return 1
		case aerr == nil:
			return -1
		case berr == nil:
			return 1
		case ap[i] < bp[i]:
			return -1
		default:
			return 1
		}
	}
	if len(ap) < len(bp) {
		return -1
	}
	return 1
}

// TargetPath resolves and validates the executable that would be replaced.
// Managed symlinks fail closed so fitr does not bypass their package manager.
func TargetPath() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate running executable: %w", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve running executable path: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect running executable: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("running executable is a symlink: update it with the installer or package manager that owns %s", path)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("running executable is not a regular file: %s", path)
	}
	return path, nil
}
