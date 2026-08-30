package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/calibration"
	"github.com/blisspixel/fitr/internal/device"
)

func TestFileSHA256AndRuntimeBlobDiscovery(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OLLAMA_MODELS", root)
	content := []byte("not a GGUF, but an exact immutable blob")
	sum := sha256.Sum256(content)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	blobs := filepath.Join(root, "blobs")
	if err := os.MkdirAll(blobs, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(blobs, "sha256-"+strings.TrimPrefix(digest, "sha256:"))
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := fileSHA256(path)
	if err != nil || got != digest {
		t.Fatalf("file digest = %q, %v; want %q", got, err, digest)
	}
	if gotPath := ollamaBlobPath(strings.ToUpper(digest)); gotPath != path {
		t.Fatalf("runtime blob path = %q, want %q", gotPath, path)
	}
	if gotPath := ollamaBlobPath("sha256:short"); gotPath != "" {
		t.Fatalf("invalid digest resolved to %q", gotPath)
	}
	if _, err := fileSHA256(filepath.Join(root, "missing")); !os.IsNotExist(err) {
		t.Fatalf("missing file digest error = %v", err)
	}
}

func TestHashedRuntimeGGUFFailsClosedOnMissingMismatchAndUnreadableMetadata(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OLLAMA_MODELS", root)
	if got, err := hashedRuntimeGGUF(""); err != nil || got != nil {
		t.Fatalf("blank digest = %v, %v", got, err)
	}
	if got, err := hashedRuntimeGGUF("not-a-digest"); err != nil || got != nil {
		t.Fatalf("invalid digest = %v, %v", got, err)
	}
	if got, err := hashedRuntimeGGUF("sha256:" + strings.Repeat("1", 64)); err != nil || got != nil {
		t.Fatalf("missing blob = %v, %v", got, err)
	}

	blobs := filepath.Join(root, "blobs")
	if err := os.MkdirAll(blobs, 0o700); err != nil {
		t.Fatal(err)
	}
	want := "sha256:" + strings.Repeat("2", 64)
	mismatchPath := filepath.Join(blobs, "sha256-"+strings.Repeat("2", 64))
	if err := os.WriteFile(mismatchPath, []byte("different bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := hashedRuntimeGGUF(want); err == nil || got != nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched blob = %v, %v", got, err)
	}

	content := []byte("verified bytes with unreadable GGUF metadata")
	sum := sha256.Sum256(content)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	path := filepath.Join(blobs, "sha256-"+strings.TrimPrefix(digest, "sha256:"))
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := hashedRuntimeGGUF(digest); err != nil || got != nil {
		t.Fatalf("unreadable GGUF should decline automatic lineage: %v, %v", got, err)
	}
}

func TestRuntimeBlobDiscoveryUsesDocumentedLocalAppDataLayout(t *testing.T) {
	t.Setenv("OLLAMA_MODELS", "")
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	digest := "sha256:" + strings.Repeat("9", 64)
	path := filepath.Join(local, "Ollama", "models", "blobs", "sha256-"+strings.Repeat("9", 64))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("local app data blob"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ollamaBlobPath(digest); got != path {
		t.Fatalf("local app data blob path = %q, want %q", got, path)
	}
}

func TestProfileUncalibratedRequiresExplicitCalibrationEvidence(t *testing.T) {
	for _, tc := range []struct {
		profile device.Profile
		want    bool
	}{
		{profile: device.Profile{}, want: true},
		{profile: device.Profile{Name: "default"}, want: true},
		{profile: device.Profile{Name: "local", Description: "UNCALIBRATED copy"}, want: true},
		{profile: device.Profile{Name: "local", Notes: []string{"not calibrated yet"}}, want: true},
		{profile: device.Profile{Name: "measured", Description: "paired reference profile"}, want: false},
	} {
		if got := profileUncalibrated(tc.profile); got != tc.want {
			t.Fatalf("profileUncalibrated(%+v) = %v, want %v", tc.profile, got, tc.want)
		}
	}
}

func TestAppendUniquePreservesFirstSeenOrder(t *testing.T) {
	got := appendUnique([]string{"a", "b"}, "b", "c", "a", "d", "c")
	want := []string{"a", "b", "c", "d"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("appendUnique = %v, want %v", got, want)
	}
}

func TestAutomaticGGUFLineageDeclinesMissingEvidenceAndSurfacesDigestMismatch(t *testing.T) {
	receipt, err := autoGGUFLineage(calibration.PairReport{})
	if err != nil || receipt.Schema != "" {
		t.Fatalf("empty lineage evidence = %+v, %v", receipt, err)
	}

	root := t.TempDir()
	t.Setenv("OLLAMA_MODELS", root)
	want := "sha256:" + strings.Repeat("3", 64)
	path := filepath.Join(root, "blobs", "sha256-"+strings.Repeat("3", 64))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not the named digest"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := calibration.PairReport{Reference: calibration.Run{ArtifactDigest: want}}
	if _, err := autoGGUFLineage(report); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("automatic lineage mismatch error = %v", err)
	}
}
