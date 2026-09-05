//go:build windows

package autoruntime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOwnedLeaseDeniesReplacementUntilReleased(t *testing.T) {
	spec := fixtureSpec(t)
	lease, _, _, err := inspectInstallation(t.Context(), spec.Executable)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.close() })
	dependency := filepath.Join(filepath.Dir(spec.Executable), "lib", "ollama", "fixture.dll")
	for _, path := range []string{spec.Executable, dependency} {
		if err := os.WriteFile(path, []byte("changed"), 0o600); err == nil {
			t.Fatal("leased installation allowed writes", path)
		}
		if err := os.Rename(path, path+".moved"); err == nil {
			t.Fatal("leased installation allowed replacement", path)
		}
		if err := os.Remove(path); err == nil {
			t.Fatal("leased installation allowed deletion", path)
		}
	}
	if err := lease.verify(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(dependency), "new.dll"), []byte("added"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lease.verify(t.Context()); err == nil {
		t.Fatal("new dependency was omitted from inventory verification")
	}
	if err := lease.close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dependency, []byte("released"), 0o600); err != nil {
		t.Fatal("lease did not release its handles", err)
	}
}

func TestOwnedRuntimeRefusesUninitializedModelStore(t *testing.T) {
	spec := fixtureSpec(t)
	p, err := Prepare(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(spec.ModelStore, "blobs")); err != nil {
		t.Fatal(err)
	}
	if r, err := Start(t.Context(), p); err == nil {
		_ = r.Close()
		t.Fatal("runtime initialized an incomplete model store")
	}
	if _, err := Prepare(t.Context(), spec); err == nil {
		t.Fatal("preparation accepted an incomplete model store")
	}
	entries, err := os.ReadDir(spec.ModelStore)
	if err != nil || len(entries) != 1 || entries[0].Name() != "manifests" {
		t.Fatalf("model store changed: %v %v", entries, err)
	}
}
