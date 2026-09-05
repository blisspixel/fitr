package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalEvidencePathRejectsRemoteDeviceAndAmbiguousSpellingsWithoutIO(t *testing.T) {
	for _, path := range []string{
		`\\server\share\results`, `//server/share/results`, `\/server/share/results`, `/\server/share/results`,
		`\\?\C:\results`, `\\.\pipe\named`, `//?/UNC/server/share/results`, `C:\results\data.json:stream`,
		`C:relative`, `C:..\results`, `C:\safe\link\..\results`, `/safe/link/../results`,
		`/tmp/data:stream`, "C:/results\x00", "", ".\x00", "./results\n",
	} {
		if err := localEvidencePath(path); err == nil {
			t.Fatal("unsafe path spelling accepted", path)
		}
	}
	for _, path := range []string{`C:\results\safe.json`, `C:/TMP~1/RESULT~1/safe.json`, `/var/folders/results`, `/private/var/results`, `./results`} {
		if err := localEvidencePath(path); err != nil {
			t.Fatal("local alias spelling rejected", path, err)
		}
	}
}

func TestEvidenceAliasTargetsRejectRemoteBeforeAnyMetadataRead(t *testing.T) {
	root := selectionRoot(t)
	for _, target := range []string{`\\server\share`, `//server/share`, `\\?\C:\private`, `\\.\pipe\named`, `C:\file:stream`, `C:relative`} {
		// expandEvidenceAlias is pure and is called immediately after Readlink,
		// before any Lstat/EvalSymlinks of its result. No remote link is created.
		if _, _, err := expandEvidenceAlias(root, target, []string{"receipt.json"}); err == nil {
			t.Fatal("remote or device link target accepted", target)
		}
	}
}

func TestEvidenceResolverPreservesLocalAbsoluteRelativeAndParentAliases(t *testing.T) {
	root := selectionRoot(t)
	physical := filepath.Join(root, "physical")
	if err := os.Mkdir(physical, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{physical, "physical", "physical" + string(filepath.Separator) + ".." + string(filepath.Separator) + "physical"} {
		alias := filepath.Join(root, "alias")
		if err := os.Symlink(target, alias); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		got, err := resolveLocalEvidence(alias)
		if err != nil || !samePath(got, physical) {
			t.Fatal("local alias lost physical identity", got, err)
		}
		if err := os.Remove(alias); err != nil {
			t.Fatal(err)
		}
	}
	missing, err := resolveEvidenceRoot(filepath.Join(physical, "missing", "results"))
	if err != nil || missing != filepath.Join(physical, "missing", "results") {
		t.Fatal("missing local root failed", missing, err)
	}
	selectionWrite(t, filepath.Join(root, "file"), nil)
	if _, err := resolveLocalEvidence(filepath.Join(root, "file", "child")); err == nil {
		t.Fatal("regular file used as directory")
	}
}

func TestEvidenceResolverBoundsLinkCyclesAndExpandedPaths(t *testing.T) {
	root := selectionRoot(t)
	cycle := filepath.Join(root, "cycle")
	if err := os.Symlink(cycle, cycle); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := resolveLocalEvidence(cycle); err == nil || !strings.Contains(err.Error(), "link limit") {
		t.Fatal("link cycle was not bounded", err)
	}
	if _, err := resolveLocalEvidence("relative"); err == nil {
		t.Fatal("relative resolver path accepted")
	}
	if _, err := resolveLocalEvidence(root + strings.Repeat("/x", 2048)); err == nil {
		t.Fatal("unbounded input path accepted")
	}
}

func TestLocalEvidenceReadersRejectUntrustedNetworkPathsBeforeResolution(t *testing.T) {
	// Invalid paths are tested through the pure guard; no actual network target
	// is probed. The source constructor rejects the same spellings before Abs.
	for _, path := range []string{`\\server\share\results`, `//server/share/results`, `C:\results:stream`} {
		if _, err := newLocalEvidence(path); err == nil {
			t.Fatal("remote/device root accepted")
		}
		if err := selectionNoLinks(path); err == nil {
			t.Fatal("selection path bypassed lexical guard")
		}
	}
	root := selectionRoot(t)
	if _, err := newLocalEvidence(filepath.Join(root, "missing")); err != nil {
		t.Fatal("safe missing root rejected", err)
	}
}
