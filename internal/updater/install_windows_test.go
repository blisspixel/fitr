//go:build windows

package updater

import (
	"encoding/base64"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"unicode/utf16"
)

const windowsInstallHelperMode = "FITR_UPDATE_TEST_WINDOWS_INSTALL_HELPER"

func TestEncodePowerShellCommandRoundTripsReplacementScript(t *testing.T) {
	encoded := encodePowerShellCommand(windowsReplaceScript)
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(data)%2 != 0 {
		t.Fatalf("encoded command decoded to %d bytes", len(data))
	}
	units := make([]uint16, len(data)/2)
	for index := range units {
		units[index] = binary.LittleEndian.Uint16(data[index*2:])
	}
	if got := string(utf16.Decode(units)); got != windowsReplaceScript {
		t.Fatal("encoded command did not preserve the replacement script")
	}
}

func TestInstallDefersAndReplacesExpectedTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "fitr.exe")
	staged := filepath.Join(dir, ".fitr-update-candidate.exe")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := HashFile(target)
	if err != nil {
		t.Fatal(err)
	}

	command := exec.Command(os.Args[0], "-test.run=^TestWindowsInstallHelperProcess$")
	command.Env = append(os.Environ(),
		windowsInstallHelperMode+"=1",
		"FITR_UPDATE_TEST_STAGED="+staged,
		"FITR_UPDATE_TEST_TARGET="+target,
		"FITR_UPDATE_TEST_DIGEST="+digest,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("install subprocess: %v\n%s", err, output)
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		got, readErr := os.ReadFile(target)
		_, stagedErr := os.Stat(staged)
		if readErr == nil && string(got) == "new" && os.IsNotExist(stagedErr) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	got, _ := os.ReadFile(target)
	_, stagedErr := os.Stat(staged)
	t.Fatalf("deferred replacement did not complete: target %q, staged error %v", got, stagedErr)
}

func TestWindowsInstallHelperProcess(t *testing.T) {
	if os.Getenv(windowsInstallHelperMode) != "1" {
		t.Skip("helper subprocess only")
	}
	deferred, err := Install(
		os.Getenv("FITR_UPDATE_TEST_STAGED"),
		os.Getenv("FITR_UPDATE_TEST_TARGET"),
		os.Getenv("FITR_UPDATE_TEST_DIGEST"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !deferred {
		t.Fatal("Windows install was not deferred")
	}
}
