//go:build !windows

package autoruntime

import (
	"errors"
	"os"
	"os/exec"
)

func supported() error {
	return errors.New("owned Ollama launch currently requires Windows process-tree and listener ownership support")
}

type processGuard struct{}

func openLockedRead(string) (*os.File, error) { return nil, supported() }

func startProcess(*exec.Cmd) (*processGuard, error) { return nil, supported() }
func (*processGuard) stop() error                   { return supported() }
func (*processGuard) alive() error                  { return supported() }
func listenerOwned(int, int) error                  { return supported() }
func systemEnvironment() ([]string, error)          { return nil, supported() }
func protectDirectory(string) error                 { return supported() }
