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

func TestFirstExistingPrefersTheEarliestPresentPath(t *testing.T) {
	dir := t.TempDir()
	python3 := filepath.Join(dir, "python3")
	python := filepath.Join(dir, "python")
	if err := os.WriteFile(python3, []byte("python3"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(python, []byte("python"), 0o644); err != nil {
		t.Fatal(err)
	}
	both, err := firstExisting(python3, python)
	if err != nil || both != python3 {
		t.Fatalf("both present = %q, %v", both, err)
	}
	onlyPython, err := firstExisting(filepath.Join(dir, "missing-python3"), python)
	if err != nil || onlyPython != python {
		t.Fatalf("python only = %q, %v", onlyPython, err)
	}
	_, err = firstExisting(filepath.Join(dir, "absent-python3"), filepath.Join(dir, "absent-python"))
	if err == nil || !strings.Contains(err.Error(), "absent-python3") {
		t.Fatalf("neither = %v", err)
	}
	if runtime.GOOS != "linux" {
		_, err = probePython()
		if err == nil || !strings.Contains(err.Error(), "/usr/bin/python3") {
			t.Fatalf("probePython = %v", err)
		}
	}
}

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
		if status.Available || status.Interpreter != "" || !strings.Contains(status.Reason, "bwrap") {
			t.Fatalf("missing bwrap status = %+v", status)
		}
		return
	}
	if !status.Available {
		t.Fatal(status.Reason)
	}
	if !filepath.IsAbs(status.Interpreter) {
		t.Fatalf("available probe did not name an absolute interpreter: %+v", status)
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
