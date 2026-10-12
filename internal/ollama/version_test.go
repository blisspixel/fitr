package ollama

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A copied test executable is a portable installed-CLI fixture. Only the
// explicit fixture environment and --version invocation enter this branch.
func TestMain(m *testing.M) {
	if output := os.Getenv("FITR_TEST_OLLAMA_VERSION_OUTPUT"); output != "" && len(os.Args) == 2 && os.Args[1] == "--version" {
		if err := os.WriteFile(os.Getenv("FITR_TEST_OLLAMA_VERSION_MARKER"), []byte("called"), 0o600); err != nil {
			os.Exit(3)
		}
		fmt.Print(output)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func versionCLIFixture(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	name := "ollama"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), bytes, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "cli-called")
	t.Setenv("PATH", dir)
	t.Setenv("FITR_TEST_OLLAMA_VERSION_MARKER", marker)
	return marker
}

func TestVersionObservesOnlyTheConfiguredDaemon(t *testing.T) {
	marker := versionCLIFixture(t)
	// Ollama v0.40.3 cmd.versionHandler emits these same separate daemon/client
	// lines. The client uses OLLAMA_HOST, not this Client's configured BaseURL.
	for _, tc := range []struct {
		name, body, cli, want string
		status                int
		closed, cancelled     bool
	}{
		{name: "serving daemon wins over a different client", status: 200,
			body: `{"version":"0.40.3"}`, cli: "ollama version is 0.33.3\nWarning: client version is 0.32.0\n", want: "0.40.3"},
		{name: "development daemon identity is preserved", status: 200,
			body: `{"version":"0.40.3-rc1-abcdef"}`, cli: "ollama version is 0.33.3\n", want: "0.40.3-rc1-abcdef"},
		{name: "stopped daemon with installed client", closed: true,
			cli: "Warning: could not connect to a running Ollama instance\nWarning: client version is 0.33.3\n"},
		{name: "another daemon reported by CLI is not this endpoint", status: 503,
			body: `{"error":"unavailable"}`, cli: "ollama version is 0.33.3\n"},
		{name: "malformed API response stays missing", status: 200,
			body: `not JSON`, cli: "ollama version is 0.33.3\n"},
		{name: "empty API version stays missing", status: 200,
			body: `{"version":""}`, cli: "ollama version is 0.33.3\n"},
		{name: "absent API version stays missing", status: 200,
			body: `{}`, cli: "ollama version is 0.33.3\n"},
		{name: "cancelled observation stays missing", cancelled: true, status: 200,
			body: `{"version":"0.40.3"}`, cli: "ollama version is 0.33.3\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FITR_TEST_OLLAMA_VERSION_OUTPUT", tc.cli)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/version" {
					t.Errorf("version observation requested %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			if tc.closed {
				server.Close()
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancelled {
				cancel()
			}
			client := &Client{BaseURL: server.URL, HTTP: server.Client()}
			if got := client.Version(ctx); got != tc.want {
				t.Errorf("serving version = %q, want %q", got, tc.want)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Errorf("serving identity consulted the installed CLI: %v", err)
				if err := os.Remove(marker); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
