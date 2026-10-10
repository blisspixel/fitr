package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/buildinfo"
)

func TestDesktopStatusEmptyStoreExitsZero(t *testing.T) {
	t.Setenv("FITR_RESULTS", t.TempDir())
	output, code := captureTopStdout(t, func() int {
		return cmdDesktop(context.Background(), []string{"status", "--display", "json"})
	})
	if code != exitOK || !strings.Contains(output, `"schema":"fitr.desktop.status.v1"`) || !strings.Contains(output, `"state":"empty"`) {
		t.Fatalf("code %d output %s", code, output)
	}
	if !strings.Contains(output, buildinfo.Version()) {
		t.Fatalf("status omitted the answering version: %s", output)
	}
}

func TestDesktopBenchmarkWithoutEvidenceDoesNotStart(t *testing.T) {
	t.Setenv("FITR_RESULTS", t.TempDir())
	output, code := captureTopStdout(t, func() int {
		return cmdDesktop(context.Background(), []string{"benchmark", "--confirm", "--display", "json"})
	})
	if code != exitUnresolved || !strings.Contains(output, `"eligible":false`) || !strings.Contains(output, `"started":false`) {
		t.Fatalf("code %d output %s", code, output)
	}
}

func TestDesktopInstallAndRemoveUseAnExplicitDirectory(t *testing.T) {
	parent := t.TempDir()
	_, code := captureTopStdout(t, func() int {
		return cmdDesktop(context.Background(), []string{"install", "--plugins-dir", parent, "--display", "json"})
	})
	if code != exitOK {
		t.Fatalf("install %d", code)
	}
	manifest := filepath.Join(parent, "dev.fitr.evidence", "manifest.json")
	if _, err := os.Stat(manifest); err != nil {
		t.Fatal(err)
	}
	_, code = captureTopStderr(t, func() int {
		return cmdDesktop(context.Background(), []string{"remove", "--plugins-dir", parent, "--display", "json"})
	})
	if code != exitUsage {
		t.Fatalf("remove without yes %d", code)
	}
	if _, err := os.Stat(manifest); err != nil {
		t.Fatal("remove without --yes deleted the plugin")
	}
	_, code = captureTopStdout(t, func() int {
		return cmdDesktop(context.Background(), []string{"remove", "--plugins-dir", parent, "--yes", "--display", "json"})
	})
	if code != exitOK {
		t.Fatalf("remove %d", code)
	}
	if _, err := os.Stat(filepath.Join(parent, "dev.fitr.evidence")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := os.Stat(parent); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopInstallWithoutDirectoryIsUnsupportedOffLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("linux has a default plugins directory")
	}
	_, code := captureTopStderr(t, func() int {
		return cmdDesktop(context.Background(), []string{"install", "--display", "json"})
	})
	if code != exitUnresolved {
		t.Fatalf("code %d", code)
	}
}

func TestDesktopUsageRejectsUnknownActions(t *testing.T) {
	_, code := captureTopStderr(t, func() int {
		return cmdDesktop(context.Background(), []string{"unknown"})
	})
	if code != exitUsage {
		t.Fatalf("code %d", code)
	}
}
