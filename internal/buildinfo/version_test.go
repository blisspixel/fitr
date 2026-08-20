package buildinfo

import (
	"regexp"
	"testing"
)

func TestVersionIsReleaseShaped(t *testing.T) {
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`).MatchString(Version()) {
		t.Fatalf("version %q is not semver-shaped", Version())
	}
}
