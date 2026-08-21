package buildinfo

import (
	"regexp"
	"strings"
	"testing"
)

func TestVersionIsReleaseShaped(t *testing.T) {
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`).MatchString(Version()) {
		t.Fatalf("version %q is not semver-shaped", Version())
	}
}

func TestBinarySHA256IdentifiesExactExecutable(t *testing.T) {
	digest, err := BinarySHA256()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 {
		t.Fatalf("binary digest = %q", digest)
	}
	second, err := BinarySHA256()
	if err != nil || second != digest {
		t.Fatalf("binary digest changed: %q, %v", second, err)
	}
}
