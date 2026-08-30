//go:build windows

package updater

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf16"
)

const windowsReplaceScript = `$ErrorActionPreference = 'Stop'
$parent = [int]$env:FITR_UPDATE_PARENT_PID
$source = $env:FITR_UPDATE_SOURCE
$target = $env:FITR_UPDATE_TARGET
$expected = $env:FITR_UPDATE_CURRENT_SHA256
for ($attempt = 0; $attempt -lt 300; $attempt++) {
  if (-not (Get-Process -Id $parent -ErrorAction SilentlyContinue)) {
    try {
	  $stream = [System.IO.File]::OpenRead($target)
	  try {
	    $hasher = [System.Security.Cryptography.SHA256]::Create()
	    try {
	      $actual = ([System.BitConverter]::ToString($hasher.ComputeHash($stream))).Replace('-', '').ToLowerInvariant()
	    } finally {
	      $hasher.Dispose()
	    }
	  } finally {
	    $stream.Dispose()
	  }
	  if ($actual -ne $expected) {
	    Remove-Item -LiteralPath $source -Force -ErrorAction SilentlyContinue
	    exit 2
	  }
      Move-Item -LiteralPath $source -Destination $target -Force
      exit 0
    } catch {
    }
  }
  Start-Sleep -Milliseconds 100
}
Remove-Item -LiteralPath $source -Force -ErrorAction SilentlyContinue
exit 1`

func encodePowerShellCommand(script string) string {
	units := utf16.Encode([]rune(script))
	encoded := make([]byte, len(units)*2)
	for index, unit := range units {
		binary.LittleEndian.PutUint16(encoded[index*2:], unit)
	}
	return base64.StdEncoding.EncodeToString(encoded)
}

// Install launches a detached helper because Windows cannot replace the
// currently running executable. The helper only uses literal paths supplied
// through its private environment.
func Install(stagedPath, targetPath, expectedCurrentDigest string) (deferred bool, err error) {
	if filepath.Dir(stagedPath) != filepath.Dir(targetPath) {
		return false, errors.New("staged update is not beside the target executable")
	}
	shell, err := exec.LookPath("powershell.exe")
	if err != nil {
		shell, err = exec.LookPath("pwsh.exe")
	}
	if err != nil {
		return false, fmt.Errorf("find PowerShell for deferred replacement: %w", err)
	}
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return false, fmt.Errorf("open null output for update helper: %w", err)
	}
	defer null.Close()
	// The helper must survive this process, so it deliberately owns a detached
	// background context rather than inheriting the caller's cancellation.
	cmd := exec.CommandContext(
		context.Background(), shell,
		"-NoLogo", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-EncodedCommand", encodePowerShellCommand(windowsReplaceScript),
	)
	cmd.Env = append(os.Environ(),
		"FITR_UPDATE_PARENT_PID="+strconv.Itoa(os.Getpid()),
		"FITR_UPDATE_SOURCE="+stagedPath,
		"FITR_UPDATE_TARGET="+targetPath,
		"FITR_UPDATE_CURRENT_SHA256="+strings.TrimPrefix(expectedCurrentDigest, "sha256:"),
	)
	cmd.Stdin = nil
	cmd.Stdout = null
	cmd.Stderr = null
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000 | 0x00000200,
	}
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("start deferred update helper: %w", err)
	}
	if err := cmd.Process.Release(); err != nil {
		return false, fmt.Errorf("release deferred update helper: %w", err)
	}
	return true, nil
}
