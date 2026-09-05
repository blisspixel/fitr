//go:build !windows

package lock

import "os"

func openHolder(path string) (*os.File, error) { return os.Open(path) }
