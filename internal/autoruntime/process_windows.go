//go:build windows

package autoruntime

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func supported() error { return nil }

func openLockedRead(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

type processGuard struct{ job, process windows.Handle }

func startProcess(cmd *exec.Cmd) (*processGuard, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW}
	if err = cmd.Start(); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|
		windows.PROCESS_SUSPEND_RESUME|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(cmd.Process.Pid))
	if err == nil {
		err = windows.AssignProcessToJobObject(job, process)
	}
	if err == nil {
		status, _, _ := windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess").Call(uintptr(process))
		if status != 0 {
			err = fmt.Errorf("resume owned runtime process: NTSTATUS 0x%x", status)
		}
	}
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if process != 0 {
			_ = windows.CloseHandle(process)
		}
		_ = windows.CloseHandle(job)
		return nil, err
	}
	return &processGuard{job: job, process: process}, nil
}

func (g *processGuard) stop() error {
	return errors.Join(windows.TerminateJobObject(g.job, 1), windows.CloseHandle(g.job), windows.CloseHandle(g.process))
}

func (g *processGuard) alive() error {
	var code uint32
	if err := windows.GetExitCodeProcess(g.process, &code); err != nil {
		return err
	}
	if code != 259 {
		return errors.New("owned runtime process has exited")
	}
	return nil
}

func listenerOwned(pid, port int) error {
	proc := windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")
	var size uint32
	status, _, _ := proc.Call(0, uintptr(unsafe.Pointer(&size)), 0, windows.AF_INET, 3, 0)
	if status != uintptr(windows.ERROR_INSUFFICIENT_BUFFER) && status != 0 {
		return syscall.Errno(status)
	}
	for range 3 {
		if size < 4 || size > 1<<20 {
			return errors.New("owned listener table exceeds inspection limits")
		}
		data := make([]byte, size)
		status, _, _ = proc.Call(uintptr(unsafe.Pointer(&data[0])), uintptr(unsafe.Pointer(&size)), 0, windows.AF_INET, 3, 0)
		if status == uintptr(windows.ERROR_INSUFFICIENT_BUFFER) {
			continue
		}
		if status != 0 {
			return syscall.Errno(status)
		}
		return checkListenerTable(data, pid, port)
	}
	return errors.New("owned listener table changed repeatedly")
}

func checkListenerTable(data []byte, pid, port int) error {
	if len(data) < 4 {
		return errors.New("invalid owned listener table")
	}
	n := binary.LittleEndian.Uint32(data)
	if uint64(n)*24+4 > uint64(len(data)) {
		return errors.New("truncated owned listener table")
	}
	found := false
	for i := range n {
		row := data[4+i*24 : 4+(i+1)*24]
		localPort := int(binary.BigEndian.Uint16(row[8:10]))
		if localPort != port {
			continue
		}
		if binary.LittleEndian.Uint32(row[4:8]) != 0x0100007f || int(binary.LittleEndian.Uint32(row[20:24])) != pid {
			return errors.New("selected loopback listener belongs to another process or a wildcard address")
		}
		found = true
	}
	if !found {
		return errors.New("owned loopback listener is not present")
	}
	return nil
}

func systemEnvironment() ([]string, error) {
	root, err := windows.GetWindowsDirectory()
	if err != nil {
		return nil, err
	}
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, err
	}
	return []string{"SystemRoot=" + root, "WINDIR=" + root, "PATH=" + system}, nil
}

func protectDirectory(path string) error {
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;OW)(A;OICI;FA;;;SY)")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}
