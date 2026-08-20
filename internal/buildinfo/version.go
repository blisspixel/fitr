// Package buildinfo owns the version embedded in every fitr binary.
package buildinfo

import (
	_ "embed"
	"strings"
)

//go:embed version.txt
var rawVersion string

// Version returns the release version from the single canonical version file.
func Version() string {
	return strings.TrimSpace(rawVersion)
}
