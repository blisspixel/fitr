package lock

import (
	"os"
	"syscall"
)

// Diagnostic readers must not prevent the owning process from releasing a
// lock. os.Open shares reads and writes on Windows, but not deletion.
func openHolder(path string) (*os.File, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}

// createContended reports a create that failed because the lock name is still
// reserved. Removing a lock while a diagnostic reader holds it open leaves the
// name delete-pending: it is neither present nor creatable, and an exclusive
// create against it fails with access-denied until the last handle closes.
// That is contention with a lock being released, not a permission fault.
func createContended(err error) bool { return os.IsPermission(err) }
