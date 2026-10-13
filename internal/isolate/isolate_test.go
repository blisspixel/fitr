package isolate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSandboxArgvBindsOnlyTheWorkTreeAndUsr(t *testing.T) {
	argv, err := sandboxArgv("/tmp/task", "/usr/bin/python3", []string{"check.py"})
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(argv, " ")
	for _, want := range []string{"--unshare-all", "--ro-bind /usr /usr", "--clearenv", "/usr/bin/prlimit", "--nproc=", "/usr/bin/python3 -I check.py"} {
		if !strings.Contains(text, want) {
			t.Fatalf("argv missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "--bind /home") || strings.Contains(text, "--ro-bind /home") {
		t.Fatalf("home directory was bound: %s", text)
	}
	if _, err := sandboxArgv("/tmp/task", "/opt/python", []string{"check.py"}); err == nil {
		t.Fatal("a top-level interpreter prefix was accepted")
	}
	argv, err = sandboxArgv("/tmp/task", "/opt/py/bin/python", []string{"check.py"})
	if err != nil || !strings.Contains(strings.Join(argv, " "), "--ro-bind /opt/py /opt/py") {
		t.Fatalf("custom prefix = %v, %v", argv, err)
	}
}

func TestProbeAndCommandOnThisMachine(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("bubblewrap worker is Linux-only")
	}
	status := Probe()
	if _, err := exec.LookPath("bwrap"); err != nil {
		if status.Available || !strings.Contains(status.Reason, "bwrap") {
			t.Fatalf("missing bwrap status = %+v", status)
		}
		return
	}
	if !status.Available {
		t.Fatal(status.Reason)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "probe.py")
	body := "import os, socket\n" +
		"print('home', os.path.exists('/home'))\n" +
		"s = socket.socket()\n" +
		"s.settimeout(1)\n" +
		"try:\n" +
		"    s.connect(('1.1.1.1', 80))\n" +
		"    print('net-open')\n" +
		"except OSError:\n" +
		"    print('net-closed')\n"
	if err := os.WriteFile(script, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	cmd, err := Command(ctx, dir, "/usr/bin/python3", []string{"probe.py"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox probe: %v\n%s", err, out)
	}
	text := string(out)
	if !strings.Contains(text, "home False") || !strings.Contains(text, "net-closed") {
		t.Fatalf("sandbox output = %q", text)
	}
}
