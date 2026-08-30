package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAssetNameCoversReleaseMatrix(t *testing.T) {
	tests := map[string]string{
		"linux/amd64":   "fitr-linux-amd64",
		"linux/arm64":   "fitr-linux-arm64",
		"darwin/amd64":  "fitr-darwin-amd64",
		"darwin/arm64":  "fitr-darwin-arm64",
		"windows/amd64": "fitr-windows-amd64.exe",
		"windows/arm64": "fitr-windows-arm64.exe",
	}
	for target, want := range tests {
		parts := strings.Split(target, "/")
		got, err := AssetName(parts[0], parts[1])
		if err != nil || got != want {
			t.Fatalf("AssetName(%s) = %q, %v; want %q", target, got, err, want)
		}
	}
	if _, err := AssetName("plan9", "amd64"); err == nil {
		t.Fatal("unsupported target must fail")
	}
}

func TestVersionOrdering(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"0.9.10", "0.9.11", -1},
		{"1.0.0", "0.9.99", 1},
		{"1.2.3", "1.2.3", 0},
		{"1.2.3-rc.1", "1.2.3", -1},
		{"1.2.3-rc.2", "1.2.3-rc.10", -1},
	}
	for _, test := range tests {
		a, err := parseVersion(test.a)
		if err != nil {
			t.Fatal(err)
		}
		b, err := parseVersion(test.b)
		if err != nil {
			t.Fatal(err)
		}
		if got := compareVersions(a, b); got != test.want {
			t.Errorf("compareVersions(%s, %s) = %d, want %d", test.a, test.b, got, test.want)
		}
	}
	for _, invalid := range []string{"1.2", "1.02.3", "1.2.x", "1.2.3-", ""} {
		if _, err := parseVersion(invalid); err == nil {
			t.Errorf("parseVersion(%q) succeeded", invalid)
		}
	}
}

func TestLookupValidateAndDownload(t *testing.T) {
	binary := []byte("verified fitr candidate")
	digest := sha256.Sum256(binary)
	assetName, err := AssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			fmt.Fprintf(w, `{"tag_name":"v0.9.12","html_url":"%s/release","draft":false,"prerelease":false,"assets":[{"name":%q,"browser_download_url":"%s/bin","size":%d},{"name":"SHA256SUMS","browser_download_url":"%s/sums","size":128}]}`,
				server.URL, assetName, server.URL, len(binary), server.URL)
		case "/sums":
			fmt.Fprintf(w, "%s  %s\r\n", hex.EncodeToString(digest[:]), assetName)
		case "/bin":
			_, _ = w.Write(binary)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &Client{HTTPClient: server.Client(), LatestURL: server.URL + "/latest", UserAgent: "fitr/test"}
	plan, err := client.Lookup(context.Background(), "0.9.11")
	if err != nil {
		t.Fatal(err)
	}
	if plan.State != StateUpdateAvailable || plan.LatestVersion != "0.9.12" || plan.AssetName != assetName {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	if err := client.Validate(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	path, gotDigest, err := client.Download(context.Background(), plan, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(binary) || gotDigest != "sha256:"+hex.EncodeToString(digest[:]) {
		t.Fatalf("download = %q, %q", got, gotDigest)
	}
}

func TestLookupRejectsIncompleteOrUnsafeRelease(t *testing.T) {
	assetName, err := AssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		body string
	}{
		{"draft", fmt.Sprintf(`{"tag_name":"v1.0.0","draft":true,"assets":[{"name":%q,"browser_download_url":"x"}]}`, assetName)},
		{"prerelease", fmt.Sprintf(`{"tag_name":"v1.0.0-rc.1","prerelease":true,"assets":[{"name":%q,"browser_download_url":"x"}]}`, assetName)},
		{"missing binary", `{"tag_name":"v1.0.0","assets":[{"name":"SHA256SUMS","browser_download_url":"x"}]}`},
		{"duplicate binary", fmt.Sprintf(`{"tag_name":"v1.0.0","assets":[{"name":%q,"browser_download_url":"x"},{"name":%q,"browser_download_url":"y"},{"name":"SHA256SUMS","browser_download_url":"z"}]}`, assetName, assetName)},
		{"missing checksums", fmt.Sprintf(`{"tag_name":"v1.0.0","assets":[{"name":%q,"browser_download_url":"x"}]}`, assetName)},
		{"oversize", fmt.Sprintf(`{"tag_name":"v1.0.0","assets":[{"name":%q,"browser_download_url":"x","size":%d},{"name":"SHA256SUMS","browser_download_url":"z"}]}`, assetName, maxBinaryBytes+1)},
		{"noncanonical tag", fmt.Sprintf(`{"tag_name":"1.0.0","assets":[{"name":%q,"browser_download_url":"x"},{"name":"SHA256SUMS","browser_download_url":"z"}]}`, assetName)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			client := &Client{HTTPClient: server.Client(), LatestURL: server.URL}
			if _, err := client.Lookup(context.Background(), "0.9.11"); err == nil {
				t.Fatal("unsafe release receipt was accepted")
			}
		})
	}
}

func TestChecksumManifestRequiresExactlyOneValidEntry(t *testing.T) {
	valid := strings.Repeat("a", 64)
	tests := []struct {
		name     string
		manifest string
		ok       bool
	}{
		{"plain", valid + "  fitr-linux-amd64\n", true},
		{"starred crlf", valid + " *fitr-linux-amd64\r\n", true},
		{"missing", valid + "  other\n", false},
		{"duplicate", valid + "  fitr-linux-amd64\n" + valid + "  fitr-linux-amd64\n", false},
		{"short", "abcd  fitr-linux-amd64\n", false},
		{"malformed", valid + "\n", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := checksumFor([]byte(test.manifest), "fitr-linux-amd64")
			if test.ok && (err != nil || got != valid) {
				t.Fatalf("checksumFor = %q, %v", got, err)
			}
			if !test.ok && err == nil {
				t.Fatal("invalid manifest was accepted")
			}
		})
	}
}

func TestDownloadRejectsChecksumMismatchAndRemovesStage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sums" {
			fmt.Fprintf(w, "%s  asset\n", strings.Repeat("0", 64))
			return
		}
		_, _ = io.WriteString(w, "corrupt")
	}))
	defer server.Close()
	dir := t.TempDir()
	client := &Client{HTTPClient: server.Client()}
	_, _, err := client.Download(context.Background(), Plan{
		AssetName: "asset", AssetURL: server.URL + "/bin", ChecksumsURL: server.URL + "/sums",
	}, dir)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Download error = %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed download left staged files: %v", entries)
	}
}

func TestFetchRejectsOversizedResponseAndHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/failure" {
			http.Error(w, "nope", http.StatusBadGateway)
			return
		}
		_, _ = io.WriteString(w, "12345")
	}))
	defer server.Close()
	client := &Client{HTTPClient: server.Client()}
	if _, err := client.fetch(context.Background(), server.URL+"/large", 4, "text/plain"); err == nil {
		t.Fatal("oversized response was accepted")
	}
	if _, err := client.fetch(context.Background(), server.URL+"/failure", 100, "text/plain"); err == nil {
		t.Fatal("HTTP failure was accepted")
	}
}

func TestHashFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "binary")
	if err := os.WriteFile(path, []byte("fitr"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := HashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte("fitr"))
	if got != "sha256:"+hex.EncodeToString(want[:]) {
		t.Fatalf("HashFile = %s", got)
	}
}

func TestVerifyVersionRequiresExactCandidateIdentity(t *testing.T) {
	t.Setenv("FITR_UPDATE_TEST_VERSION", "1.2.3")
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyVersion(context.Background(), path, "1.2.3"); err != nil {
		t.Fatal(err)
	}
	if err := VerifyVersion(context.Background(), path, "1.2.4"); err == nil {
		t.Fatal("candidate version mismatch was accepted")
	}
}

func TestVerifyVersionBoundsCandidateOutput(t *testing.T) {
	t.Setenv("FITR_UPDATE_TEST_VERSION", strings.Repeat("x", maxVersionOutput+1))
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyVersion(context.Background(), path, strings.Repeat("x", maxVersionOutput+1)); err == nil ||
		!strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("VerifyVersion overflow error = %v", err)
	}
}

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "version" && os.Getenv("FITR_UPDATE_TEST_VERSION") != "" {
		fmt.Println("fitr", os.Getenv("FITR_UPDATE_TEST_VERSION"))
		os.Exit(0)
	}
	os.Exit(m.Run())
}
