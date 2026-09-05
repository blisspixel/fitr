package autoruntime

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Changes to this fixed policy invalidate the launch and stable profile digests.
const launchPolicy = "windows-job-v1;loopback-pid;no-cloud;no-prune;no-history;no-request-log;one-model;one-parallel;one-queue;keepalive=-1;load-timeout=2m;fit=off;no-parent-env;private-home"
const MaxLogBytes = 1 << 20

func privateHome() (string, error) {
	root, err := os.MkdirTemp("", "fitr-owned-ollama-")
	if err != nil {
		return "", err
	}
	if err := protectDirectory(root); err != nil {
		return "", err
	}
	return root, nil
}

func childEnvironment(spec Spec, home, host string) ([]string, error) {
	env, err := systemEnvironment()
	if err != nil {
		return nil, err
	}
	for _, key := range []string{"HOME", "USERPROFILE", "LOCALAPPDATA", "APPDATA", "TMP", "TEMP", "TMPDIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
		env = append(env, key+"="+home)
	}
	for i, value := range env {
		if strings.HasPrefix(value, "PATH=") {
			env[i] += string(os.PathListSeparator) + filepath.Dir(spec.Executable) + string(os.PathListSeparator) + filepath.Join(filepath.Dir(spec.Executable), "lib", "ollama")
		}
	}
	return append(env,
		"OLLAMA_HOST="+host, "OLLAMA_MODELS="+spec.ModelStore,
		"OLLAMA_NO_CLOUD=true", "OLLAMA_NOPRUNE=true", "OLLAMA_NOHISTORY=true", "OLLAMA_DEBUG_LOG_REQUESTS=false",
		"OLLAMA_MAX_LOADED_MODELS=1", "OLLAMA_NUM_PARALLEL=1", "OLLAMA_MAX_QUEUE=1", "OLLAMA_KEEP_ALIVE=-1", "OLLAMA_LOAD_TIMEOUT=2m",
		"OLLAMA_CONTEXT_LENGTH="+strconv.Itoa(spec.NumCtx), "OLLAMA_KV_CACHE_TYPE="+spec.KVCacheType,
		"OLLAMA_FLASH_ATTENTION="+strconv.FormatBool(spec.FlashAttention), "OLLAMA_GPU_OVERHEAD="+strconv.FormatInt(spec.ReserveBytes, 10),
		"OLLAMA_SCHED_SPREAD=false", "OLLAMA_DEBUG=false", "LLAMA_ARG_FIT=off", "NO_COLOR=1", "TERM=dumb"), nil
}

// logBuffer bounds the isolated version command's complete output. Serving
// diagnostics use streaming facts and a rolling tail instead.
type logBuffer struct {
	mu       sync.Mutex
	data     []byte
	overflow bool
}

func (b *logBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(data) > MaxLogBytes-len(b.data) {
		b.overflow = true
		return 0, errors.New("owned runtime log exceeds 1 MiB")
	}
	b.data = append(b.data, data...)
	return len(data), nil
}
func (b *logBuffer) snapshot() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data), b.overflow
}

func inspectVersion(ctx context.Context, executable string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	home, err := privateHome()
	if err != nil {
		return "", err
	}
	defer removePrivateHome(home) //nolint:errcheck // a version failure still preserves its primary diagnostic
	models := filepath.Join(home, "models")
	if err := os.Mkdir(models, 0o700); err != nil {
		return "", err
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}), ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	env, err := childEnvironment(Spec{Executable: executable, ModelStore: models, NumCtx: 8192, KVCacheType: "f16"}, home, listener.Addr().String())
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, executable, "--version")
	cmd.Env, cmd.Dir = env, home
	output := &logBuffer{}
	cmd.Stdout, cmd.Stderr = output, output
	guard, err := startProcess(cmd)
	if err != nil {
		return "", err
	}
	defer guard.stop() //nolint:errcheck // also closes job after the version process has exited
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case err := <-done:
		if err != nil {
			return "", errors.New("owned runtime version subprocess failed")
		}
	}
	text, overflow := output.snapshot()
	if overflow {
		return "", errors.New("owned runtime version output exceeded its limit")
	}
	return parseClientVersion(text)
}

func parseClientVersion(text string) (string, error) {
	version := ""
	for _, line := range strings.Split(text, "\n") {
		const prefix = "Warning: client version is "
		if strings.HasPrefix(line, prefix) {
			if version != "" {
				return "", errors.New("ambiguous Ollama client version output")
			}
			version = strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	if !versionPattern.MatchString(version) {
		return "", errors.New("runtime did not report a bounded Ollama client version")
	}
	return version, nil
}

func removePrivateHome(home string) error {
	// Only this package's freshly created home is eligible. Never ModelStore.
	if !filepath.IsAbs(home) || !strings.HasPrefix(filepath.Base(home), "fitr-owned-ollama-") {
		return errors.New("invalid owned runtime cleanup target")
	}
	parent, err := filepath.Abs(os.TempDir())
	if err != nil || !strings.EqualFold(filepath.Dir(home), filepath.Clean(parent)) {
		return errors.New("owned runtime cleanup target is outside its temporary parent")
	}
	if _, err := physicalPath(home, true); err != nil {
		return err
	}
	return os.RemoveAll(home)
}
