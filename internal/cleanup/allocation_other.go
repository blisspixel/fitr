//go:build !unix

package cleanup

import "os"

// observedAllocation reports nothing. A Windows directory listing exposes
// Win32FileAttributeData (Go 1.27.2), which has a file length and no block
// count or link count. Opening the file to ask for either would resolve a
// path the scan deliberately does not resolve again.
func observedAllocation(os.FileInfo) (int64, uint64, uint64, bool) {
	return 0, 0, 0, false
}
