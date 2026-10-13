package ollama

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/listen"
)

func TestLibraryFromOpenLogsUsesTheTailAndRequiresAgreement(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "server.log")
	body := "library=cpu\n" + strings.Repeat("x", maxServerLogTail) + "\nlibrary=vulkan\n"
	if err := os.WriteFile(current, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := libraryFromOpenLogs([]string{current}); got != "vulkan" {
		t.Fatalf("tail library = %q", got)
	}
	stale := filepath.Join(dir, "old.log")
	if err := os.WriteFile(stale, []byte("library=cuda\n"+strings.Repeat("y", maxServerLogTail+8)), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := libraryFromOpenLogs([]string{stale}); got != "" {
		t.Fatalf("head-only library = %q", got)
	}
	other := filepath.Join(dir, "other.log")
	if err := os.WriteFile(other, []byte("library=cuda\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := libraryFromOpenLogs([]string{current, other}); got != "" {
		t.Fatalf("disagreement produced %q", got)
	}
	if got := libraryFromOpenLogs(nil); got != "" {
		t.Fatalf("no open log produced %q", got)
	}
}

func TestAccelReadsOnlyLogsTheListenerHoldsOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.log")
	if err := os.WriteFile(path, []byte("library=rocm\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := openLogsForAccel
	t.Cleanup(func() { openLogsForAccel = prev })
	openLogsForAccel = func(string) ([]string, error) { return []string{path}, nil }
	c := &Client{BaseURL: "http://127.0.0.1:11434"}
	if got := c.Accel(context.Background()); got != "rocm" {
		t.Fatalf("listener log library = %q", got)
	}
	openLogsForAccel = func(string) ([]string, error) { return nil, listen.ErrUnobserved }
	if got := c.Accel(context.Background()); got != "" {
		t.Fatalf("unobserved listener produced %q", got)
	}
}

func TestHeldLogIsVisibleForTheListeningProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("open-log inspection is implemented for Linux")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	path := filepath.Join(t.TempDir(), "server.log")
	if err := os.WriteFile(path, []byte("library=metal\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	pid, err := listen.ListeningPID(listen.Endpoint{Host: "127.0.0.1", Port: port, Local: true})
	if err != nil || pid != os.Getpid() {
		t.Fatalf("listener = %d, %v", pid, err)
	}
	paths, err := listen.OpenLogs(pid)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, candidate := range paths {
		if candidate == path {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("open logs %v do not include the held server log", paths)
	}
	if got := libraryFromOpenLogs([]string{path}); got != "metal" {
		t.Fatalf("held log library = %q", got)
	}
}
