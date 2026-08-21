// Package buildinfo owns the version embedded in every fitr binary.
package buildinfo

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

//go:embed version.txt
var rawVersion string

// Version returns the release version from the single canonical version file.
func Version() string {
	return strings.TrimSpace(rawVersion)
}

var binaryDigestOnce = sync.OnceValues(func() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate executable: %w", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open executable: %w", err)
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", fmt.Errorf("hash executable: %w", err)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
})

// BinarySHA256 identifies the exact executable performing a measurement. It
// remains exact for dirty local builds where a source revision alone would be
// insufficient, and it records no local executable path.
func BinarySHA256() (string, error) {
	return binaryDigestOnce()
}
