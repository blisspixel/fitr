//go:build linux

package listen

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestLookupBindsTheSocketOwnerAndNotAnotherProcess(t *testing.T) {
	root := t.TempDir()
	writeProcNet(t, root, "0100007F:2CB6", "194818")
	writeProcNet6(t, root, "00000000000000000000000000000000:0000", "1")
	if err := os.MkdirAll(filepath.Join(root, "10", "fd"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("socket:[194818]", filepath.Join(root, "10", "fd", "3")); err != nil {
		t.Fatal(err)
	}
	pid, err := Lookup(root, Endpoint{Host: "127.0.0.1", Port: 0x2CB6, Local: true})
	if err != nil || pid != 10 {
		t.Fatalf("lookup = %d, %v", pid, err)
	}
	if _, err := Lookup(root, Endpoint{Host: "127.0.0.1", Port: 1, Local: true}); !errors.Is(err, ErrNoListener) {
		t.Fatalf("missing port = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "11", "fd"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("socket:[194818]", filepath.Join(root, "11", "fd", "4")); err != nil {
		t.Fatal(err)
	}
	if _, err := Lookup(root, Endpoint{Host: "127.0.0.1", Port: 0x2CB6, Local: true}); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("two owners = %v", err)
	}
}

func TestLookupWildcardAndMissingTable(t *testing.T) {
	root := t.TempDir()
	writeProcNet(t, root, "00000000:0050", "9")
	writeProcNet6(t, root, "00000000000000000000000000000000:0000", "1")
	if err := os.MkdirAll(filepath.Join(root, "8", "fd"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("socket:[9]", filepath.Join(root, "8", "fd", "1")); err != nil {
		t.Fatal(err)
	}
	pid, err := Lookup(root, Endpoint{Host: "127.0.0.1", Port: 80, Local: true})
	if err != nil || pid != 8 {
		t.Fatalf("wildcard lookup = %d, %v", pid, err)
	}
	if err := os.Remove(filepath.Join(root, "net", "tcp6")); err != nil {
		t.Fatal(err)
	}
	if _, err := Lookup(root, Endpoint{Host: "127.0.0.1", Port: 80, Local: true}); !errors.Is(err, ErrUnobserved) {
		t.Fatalf("missing ipv6 table = %v", err)
	}
}

func TestEnvironAndOpenLogsReadOnlyTheListener(t *testing.T) {
	root := t.TempDir()
	pidDir := filepath.Join(root, "10")
	if err := os.MkdirAll(filepath.Join(pidDir, "fd"), 0o700); err != nil {
		t.Fatal(err)
	}
	env := []byte("OLLAMA_FLASH_ATTENTION=1\x00OLLAMA_KV_CACHE_TYPE=q8_0\x00OTHER=no\x00")
	if err := os.WriteFile(filepath.Join(pidDir, "environ"), env, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readEnviron(root, 10)
	if err != nil || got["OLLAMA_FLASH_ATTENTION"] != "1" || got["OLLAMA_KV_CACHE_TYPE"] != "q8_0" || got["OTHER"] != "no" {
		t.Fatalf("environ = %+v, %v", got, err)
	}
	logPath := filepath.Join(root, "server.log")
	if err := os.WriteFile(logPath, []byte("library=cuda\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(logPath, filepath.Join(pidDir, "fd", "4")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(logPath+" (deleted)", filepath.Join(pidDir, "fd", "5")); err != nil {
		t.Fatal(err)
	}
	paths, err := openLogs(root, 10)
	if err != nil || len(paths) != 1 || paths[0] != logPath {
		t.Fatalf("open logs = %v, %v", paths, err)
	}
	if _, err := readEnviron(root, 11); !errors.Is(err, ErrUnobserved) {
		t.Fatalf("missing environ = %v", err)
	}
}

func TestListeningPIDMatchesThisProcess(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	pid, err := ListeningPID(Endpoint{Host: "127.0.0.1", Port: port, Local: true})
	if err != nil || pid != os.Getpid() {
		t.Fatalf("live pid = %d, want %d, err %v", pid, os.Getpid(), err)
	}
	wild, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer wild.Close()
	wport := wild.Addr().(*net.TCPAddr).Port
	pid, err = ListeningPID(Endpoint{Host: "127.0.0.1", Port: wport, Local: true})
	if err != nil || pid != os.Getpid() {
		t.Fatalf("wildcard live pid = %d, want %d, err %v", pid, os.Getpid(), err)
	}
}

func writeProcNet(t *testing.T, root, local, inode string) {
	t.Helper()
	dir := filepath.Join(root, "net")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	line := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: " + local + " 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 " + inode + " 1 0000000000000000 100 0 0 10 0\n"
	if err := os.WriteFile(filepath.Join(dir, "tcp"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeProcNet6(t *testing.T, root, local, inode string) {
	t.Helper()
	line := "  sl  local_address remote_address st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: " + local + " 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 " + inode + " 1 0000000000000000 100 0 0 10 0\n"
	if err := os.WriteFile(filepath.Join(root, "net", "tcp6"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLiveProcNetInodeMatchesAnOpenSocket(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	text, err := os.ReadFile("/proc/net/tcp")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := ParseProcNet(string(text), "ipv4")
	if err != nil {
		t.Fatal(err)
	}
	var inode string
	for _, row := range rows {
		if row.Row.Port == port && row.Row.IP.Equal(net.IPv4(127, 0, 0, 1)) {
			inode = row.Inode
			break
		}
	}
	if inode == "" {
		t.Fatal("live listener was not a LISTEN row")
	}
	fdDir := filepath.Join("/proc", strconv.Itoa(os.Getpid()), "fd")
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ent := range entries {
		target, err := os.Readlink(filepath.Join(fdDir, ent.Name()))
		if err == nil && target == "socket:["+inode+"]" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("inode %s was not an open socket of this process", inode)
	}
}
