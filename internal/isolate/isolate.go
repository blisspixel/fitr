// Package isolate runs a generated verifier inside bubblewrap on Linux.
// The sandbox has no network and no view of the user's home directory.
// Other operating systems stay unavailable rather than executing on the host.
package isolate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	cpuSeconds        = 60
	addressSpaceBytes = 4 << 30
	processCeiling    = 8192
	fileCeiling       = 128
	probeTimeout      = 15 * time.Second
)

// Status is whether a verifier can run inside the sandbox. Reason is the
// enablement step when it cannot. It is not a quality score.
type Status struct {
	Available bool
	Reason    string
}

// Probe checks the sandbox with a one-line interpreter. A missing tool or a
// kernel that refuses the namespace is unavailable, not a failed model.
// The result is cached for the process: a run, a doctor check, and the
// inventory screen ask the same question.
func Probe() Status {
	probeOnce.Do(func() { probeResult = probe() })
	return probeResult
}

var (
	probeOnce   sync.Once
	probeResult Status
)

func probe() Status {
	if runtime.GOOS != "linux" {
		return Status{Reason: "isolated execution needs bubblewrap on Linux"}
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		return Status{Reason: "isolated execution is unavailable: bubblewrap (bwrap) is not installed"}
	}
	if _, err := os.Stat("/usr/bin/prlimit"); err != nil {
		return Status{Reason: "isolated execution is unavailable: /usr/bin/prlimit is not installed"}
	}
	python, err := probePython()
	if err != nil {
		return Status{Reason: "isolated execution is unavailable: " + err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	cmd, err := Command(ctx, "", python, []string{"-c", "print(1)"})
	if err != nil {
		return Status{Reason: "isolated execution is unavailable: " + err.Error()}
	}
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "1" {
		detail := strings.TrimSpace(string(out))
		if detail == "" && err != nil {
			detail = err.Error()
		}
		return Status{Reason: "isolated execution is unavailable: bubblewrap did not run the probe (" + detail + ")"}
	}
	return Status{Available: true, Reason: "bubblewrap can run generated verifiers without the user's home directory or network"}
}

func probePython() (string, error) {
	for _, path := range []string{"/usr/bin/python3", "/usr/bin/python"} {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", errors.New("/usr/bin/python3 is not installed")
}

// Command returns a bubblewrap invocation that runs interpreter with args.
// dir is the only writable directory, mounted at /work. An empty dir is the
// availability probe and does not bind a work tree.
func Command(ctx context.Context, dir, interpreter string, args []string) (*exec.Cmd, error) {
	if runtime.GOOS != "linux" {
		return nil, errors.New("bubblewrap isolation is only implemented on Linux")
	}
	if interpreter == "" || !filepath.IsAbs(interpreter) {
		return nil, errors.New("isolated interpreter path must be absolute")
	}
	argv, err := sandboxArgv(dir, interpreter, args)
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, argv[0], argv[1:]...), nil
}

func sandboxArgv(dir, interpreter string, args []string) ([]string, error) {
	argv := []string{
		"bwrap", "--unshare-all", "--die-with-parent", "--new-session",
		"--ro-bind", "/usr", "/usr",
	}
	for _, host := range []string{"/bin", "/lib", "/lib64"} {
		argv = append(argv, bindOrSymlink(host)...)
	}
	root, err := interpreterRoot(interpreter)
	if err != nil {
		return nil, err
	}
	if root != "" {
		argv = append(argv, "--ro-bind", root, root)
	}
	argv = append(argv, "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp")
	if dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil || abs == "/" || abs == "/home" {
			return nil, errors.New("isolated work directory is not a task directory")
		}
		argv = append(argv, "--bind", abs, "/work", "--chdir", "/work")
	}
	argv = append(argv,
		"--clearenv", "--setenv", "PATH", "/usr/bin", "--setenv", "LANG", "C.UTF-8",
		"--", "/usr/bin/prlimit",
		"--cpu="+strconv.Itoa(cpuSeconds),
		"--as="+strconv.Itoa(addressSpaceBytes),
		"--nproc="+strconv.Itoa(processCeiling),
		"--nofile="+strconv.Itoa(fileCeiling),
		"--", interpreter, "-I",
	)
	argv = append(argv, args...)
	return argv, nil
}

func bindOrSymlink(host string) []string {
	info, err := os.Lstat(host)
	if err != nil {
		return nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(host)
		if err != nil || target == "" {
			return nil
		}
		return []string{"--symlink", target, host}
	}
	return []string{"--ro-bind", host, host}
}

// interpreterRoot is the install prefix of an interpreter that does not live
// under /usr. The prefix is mounted read-only at the same path. A prefix that
// is the user's home directory, or a top-level directory, is refused.
// These are Linux bubblewrap paths. The path package keeps slash semantics
// when Windows CI builds this argv: filepath would rewrite separators and
// reject a two-level prefix such as /opt/py.
func interpreterRoot(interpreter string) (string, error) {
	cleaned := path.Clean(interpreter)
	if cleaned == "/" || !strings.HasPrefix(cleaned, "/") {
		return "", fmt.Errorf("interpreter %s is outside /usr and its directory is too broad to bind", interpreter)
	}
	if strings.HasPrefix(cleaned, "/usr/") {
		return "", nil
	}
	dir := path.Dir(cleaned)
	root := dir
	if path.Base(dir) == "bin" {
		root = path.Dir(dir)
	}
	switch root {
	case "", "/", "/home", "/usr", "/opt", "/var", "/tmp", "/root":
		return "", fmt.Errorf("interpreter %s is outside /usr and its directory is too broad to bind", interpreter)
	}
	if strings.Count(strings.Trim(root, "/"), "/") < 1 {
		return "", fmt.Errorf("interpreter %s is outside /usr and its directory is too broad to bind", interpreter)
	}
	return root, nil
}
