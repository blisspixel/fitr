//go:build !windows

package lock

import "os"

func openHolder(path string) (*os.File, error) { return os.Open(path) }

// Only Windows reserves the name of a deleted file, so a create refused on
// permissions here is a real fault rather than contention.
func createContended(error) bool { return false }
