//go:build linux

package listen

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const (
	procRoot         = "/proc"
	maxProcessScan   = 4096
	maxFDsPerProcess = 256
	maxOpenLogs      = 8
	maxEnvironBytes  = 1 << 20
)

var socketInodePattern = regexp.MustCompile(`^socket:\[(\d+)\]$`)

// ListeningPID resolves ep against the host procfs.
func ListeningPID(ep Endpoint) (int, error) {
	return Lookup(procRoot, ep)
}

// Lookup resolves ep using a procfs root. Tests pass a fixture directory.
// Production passes "/proc".
func Lookup(root string, ep Endpoint) (int, error) {
	if !ep.Local || ep.Port < 1 {
		return 0, ErrUnobserved
	}
	rows, err := readListenRows(root)
	if err != nil {
		return 0, err
	}
	var covering []procRow
	for _, row := range rows {
		if row.Row.Port == ep.Port && Covers(row.Row, ep.Host) {
			covering = append(covering, row)
		}
	}
	if len(covering) == 0 {
		return 0, ErrNoListener
	}
	want := map[string][]int{}
	for _, row := range covering {
		want[row.Inode] = nil
	}
	if err := fillSocketOwners(root, want); err != nil {
		return 0, err
	}
	var selected []Row
	for _, row := range covering {
		pids := want[row.Inode]
		if len(pids) == 0 {
			return 0, ErrUnobserved
		}
		for _, pid := range pids {
			owned := row.Row
			owned.PID = pid
			selected = append(selected, owned)
		}
	}
	return SelectPID(selected, ep.Host, ep.Port)
}

// Environ reads the initial environment of pid. A later setenv in that
// process is not visible here, and a permission failure is unobserved.
func Environ(pid int) (map[string]string, error) {
	return readEnviron(procRoot, pid)
}

// OpenLogs returns regular *.log files pid currently has open. The default
// server log path is not consulted. Disagreement is the caller's problem;
// an incomplete listing is unobserved.
func OpenLogs(pid int) ([]string, error) {
	return openLogs(procRoot, pid)
}

func readListenRows(root string) ([]procRow, error) {
	v4, err4 := os.ReadFile(filepath.Join(root, "net", "tcp"))
	v6, err6 := os.ReadFile(filepath.Join(root, "net", "tcp6"))
	if err4 != nil || err6 != nil {
		return nil, ErrUnobserved
	}
	rows4, err := ParseProcNet(string(v4), "ipv4")
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnobserved, err.Error())
	}
	rows6, err := ParseProcNet(string(v6), "ipv6")
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnobserved, err.Error())
	}
	return append(rows4, rows6...), nil
}

func fillSocketOwners(root string, want map[string][]int) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return ErrUnobserved
	}
	scanned := 0
	for _, ent := range entries {
		pid, err := strconv.Atoi(ent.Name())
		if err != nil || pid <= 0 {
			continue
		}
		// Every numeric directory has to be visited. Stopping at the first
		// owner would hide a second process that holds the same socket.
		scanned++
		if scanned > maxProcessScan {
			return ErrUnobserved
		}
		found, err := processSockets(root, pid)
		if err != nil {
			continue
		}
		for inode, ok := range found {
			if !ok {
				continue
			}
			if _, needed := want[inode]; !needed {
				continue
			}
			if !containsPID(want[inode], pid) {
				want[inode] = append(want[inode], pid)
			}
		}
	}
	return nil
}

func containsPID(pids []int, pid int) bool {
	for _, have := range pids {
		if have == pid {
			return true
		}
	}
	return false
}

func processSockets(root string, pid int) (map[string]bool, error) {
	fdDir := filepath.Join(root, strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return nil, err
	}
	if len(entries) > maxFDsPerProcess {
		return nil, errors.New("process fd list exceeds inspection limit")
	}
	found := map[string]bool{}
	for _, ent := range entries {
		target, err := os.Readlink(filepath.Join(fdDir, ent.Name()))
		if err != nil {
			continue
		}
		match := socketInodePattern.FindStringSubmatch(target)
		if match == nil {
			continue
		}
		found[match[1]] = true
	}
	return found, nil
}

func readEnviron(root string, pid int) (map[string]string, error) {
	if pid <= 0 {
		return nil, ErrUnobserved
	}
	f, err := os.Open(filepath.Join(root, strconv.Itoa(pid), "environ"))
	if err != nil {
		return nil, ErrUnobserved
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxEnvironBytes+1))
	if err != nil {
		return nil, ErrUnobserved
	}
	if len(b) > maxEnvironBytes {
		return nil, ErrUnobserved
	}
	return parseEnviron(b), nil
}

func parseEnviron(b []byte) map[string]string {
	out := map[string]string{}
	for _, field := range bytes.Split(b, []byte{0}) {
		if len(field) == 0 {
			continue
		}
		key, val, ok := bytes.Cut(field, []byte{'='})
		if !ok || len(key) == 0 || bytes.ContainsRune(key, 0) {
			continue
		}
		name := string(key)
		if _, exists := out[name]; exists {
			continue
		}
		out[name] = string(val)
	}
	return out
}

func openLogs(root string, pid int) ([]string, error) {
	if pid <= 0 {
		return nil, ErrUnobserved
	}
	fdDir := filepath.Join(root, strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnobserved, err.Error())
	}
	if len(entries) > maxFDsPerProcess {
		return nil, fmt.Errorf("%w: fd list has %d entries", ErrUnobserved, len(entries))
	}
	seen := map[string]bool{}
	var paths []string
	for _, ent := range entries {
		target, err := os.Readlink(filepath.Join(fdDir, ent.Name()))
		if err != nil {
			// The descriptor can close between the directory read and this
			// lookup. That is not a second log.
			continue
		}
		if strings.HasSuffix(target, " (deleted)") || !strings.HasSuffix(strings.ToLower(target), ".log") {
			continue
		}
		info, err := os.Stat(target)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if seen[target] {
			continue
		}
		seen[target] = true
		paths = append(paths, target)
		if len(paths) > maxOpenLogs {
			return nil, fmt.Errorf("%w: more than %d open logs", ErrUnobserved, maxOpenLogs)
		}
	}
	return paths, nil
}
