//go:build !windows

package main

import (
	"strings"
	"testing"
)

func TestOwnedLaunchRefusesThisOperatingSystem(t *testing.T) {
	err := ownedLaunchError(tailorDeps{})
	if err == nil || !strings.Contains(err.Error(), "requires Windows") {
		t.Fatal(err)
	}
}
