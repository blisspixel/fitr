//go:build !windows

package updater

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallAtomicallyReplacesExpectedTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "fitr")
	staged := filepath.Join(dir, ".fitr-update-candidate")
	if err := os.WriteFile(target, []byte("old"), 0o751); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := HashFile(target)
	if err != nil {
		t.Fatal(err)
	}
	deferred, err := Install(staged, target, digest)
	if err != nil || deferred {
		t.Fatalf("Install = deferred %v, err %v", deferred, err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("target = %q", got)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o751 {
		t.Fatalf("target mode = %o", info.Mode().Perm())
	}
}

func TestInstallRefusesChangedTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "fitr")
	staged := filepath.Join(dir, ".fitr-update-candidate")
	if err := os.WriteFile(target, []byte("changed"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(staged, target, "sha256:"+strings.Repeat("0", 64)); err == nil {
		t.Fatal("changed target was replaced")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "changed" {
		t.Fatalf("target changed to %q", got)
	}
}
