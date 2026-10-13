//go:build windows

package listen

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ListeningPID reads the IPv4 and IPv6 owner tables. Either family failing
// closed the inspection: a missing IPv6 table can hide the daemon that is
// actually bound, and guessing from the other family would name the wrong PID.
func ListeningPID(ep Endpoint) (int, error) {
	if !ep.Local || ep.Port < 1 {
		return 0, ErrUnobserved
	}
	v4, err4 := extendedTCPTable(windows.AF_INET)
	v6, err6 := extendedTCPTable(windows.AF_INET6)
	if err4 != nil || err6 != nil {
		return 0, ErrUnobserved
	}
	rows4, err := ParseTCP4OwnerPID(v4)
	if err != nil {
		return 0, ErrUnobserved
	}
	rows6, err := ParseTCP6OwnerPID(v6)
	if err != nil {
		return 0, ErrUnobserved
	}
	return SelectPID(append(rows4, rows6...), ep.Host, ep.Port)
}

// Environ and OpenLogs stay unobserved on Windows. The listener PID is a
// table fact. This process cannot read another process's initial environment
// or open-file list without a proven, tested mechanism, and the unbound
// default log is not a substitute.
func Environ(int) (map[string]string, error) { return nil, ErrUnobserved }

func OpenLogs(int) ([]string, error) { return nil, ErrUnobserved }

func extendedTCPTable(family uint16) ([]byte, error) {
	proc := windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")
	var size uint32
	status, _, _ := proc.Call(0, uintptr(unsafe.Pointer(&size)), 0, uintptr(family), 3, 0)
	if status != uintptr(windows.ERROR_INSUFFICIENT_BUFFER) && status != 0 {
		return nil, syscall.Errno(status)
	}
	for range 3 {
		if size < 4 || size > 1<<20 {
			return nil, ErrUnobserved
		}
		data := make([]byte, size)
		status, _, _ = proc.Call(uintptr(unsafe.Pointer(&data[0])), uintptr(unsafe.Pointer(&size)), 0, uintptr(family), 3, 0)
		if status == uintptr(windows.ERROR_INSUFFICIENT_BUFFER) {
			continue
		}
		if status != 0 {
			return nil, syscall.Errno(status)
		}
		return data, nil
	}
	return nil, ErrUnobserved
}
