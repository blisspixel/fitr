//go:build windows

package autoruntime

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/blisspixel/fitr/internal/ollama"
)

func init() {
	if len(os.Args) != 2 || !strings.HasPrefix(filepath.Base(os.Args[0]), "ollama-fixture") {
		return
	}
	switch os.Args[1] {
	case "--version":
		fmt.Println("Warning: client version is 0.0.1-fixture")
		os.Exit(0)
	case "fixture-descendant":
		time.Sleep(time.Minute)
		os.Exit(0)
	case "serve":
		serveFixture()
		os.Exit(0)
	}
}

func serveFixture() {
	for _, key := range []string{"HTTPS_PROXY", "HTTP_PROXY", "FITR_OPENAI_API_KEY", "OLLAMA_API_KEY", "AWS_SECRET_ACCESS_KEY"} {
		if os.Getenv(key) != "" {
			os.Exit(7)
		}
	}
	if os.Getenv("OLLAMA_NO_CLOUD") != "true" || os.Getenv("OLLAMA_NOPRUNE") != "true" || os.Getenv("OLLAMA_NUM_PARALLEL") != "1" {
		os.Exit(8)
	}
	if os.Getenv("OLLAMA_CONTEXT_LENGTH") == "513" {
		os.Exit(9)
	}
	listener, err := net.Listen("tcp4", os.Getenv("OLLAMA_HOST"))
	if err != nil {
		os.Exit(10)
	}
	if os.Getenv("OLLAMA_CONTEXT_LENGTH") == "516" {
		child := exec.Command(os.Args[0], "fixture-descendant")
		if child.Start() != nil {
			os.Exit(11)
		}
		if os.WriteFile(filepath.Join(os.Getenv("HOME"), "descendant.pid"), []byte(strconv.Itoa(child.Process.Pid)), 0o600) != nil {
			os.Exit(12)
		}
	}
	fmt.Println("Ollama cloud disabled: true")
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/version":
			_, _ = io.WriteString(w, `{"version":"0.0.1-fixture"}`)
		case "/api/show":
			if os.Getenv("OLLAMA_CONTEXT_LENGTH") == "517" {
				time.Sleep(60 * time.Millisecond)
			}
			_, _ = io.WriteString(w, `{"model_info":{"general.architecture":"llama"},"template":"fixture","parameters":"temperature 0","capabilities":["completion"]}`)
		case "/api/generate":
			time.Sleep(60 * time.Millisecond)
			_, _ = io.WriteString(w, `{"response":"fixture","done":true,"eval_count":1,"eval_duration":1000000}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}), ReadHeaderTimeout: time.Second}
	_ = server.Serve(listener)
}

func fixtureSpec(t *testing.T) Spec {
	t.Helper()
	root := t.TempDir()
	executable := filepath.Join(root, "ollama-fixture.exe")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.Create(executable)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(output, input); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	libs := filepath.Join(root, "lib", "ollama")
	if err := os.MkdirAll(libs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libs, "fixture.dll"), []byte("synthetic runtime dependency"), 0o600); err != nil {
		t.Fatal(err)
	}
	models := filepath.Join(root, "models")
	if err := os.Mkdir(models, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"blobs", "manifests"} {
		if err := os.Mkdir(filepath.Join(models, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	spec, err := Inspect(context.Background(), executable, models)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestOwnedRuntimeLifecycleRejectsEnvironmentAndEndpointLeaks(t *testing.T) {
	spec := fixtureSpec(t)
	for _, key := range []string{"HTTPS_PROXY", "HTTP_PROXY", "FITR_OPENAI_API_KEY", "OLLAMA_API_KEY", "AWS_SECRET_ACCESS_KEY"} {
		t.Setenv(key, "must-not-inherit")
	}
	prepared, err := Prepare(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, err := Start(ctx, prepared)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if r.Accel(ctx) != "" {
		t.Fatal("unknown acceleration was invented")
	}
	if _, err := r.BindingMetadata(spec.ExecutableSHA256, spec.ExecutableSHA256); err == nil {
		t.Fatal("unobserved model configuration bound")
	}
	configuration, err := r.ModelConfiguration(ctx, "fixture")
	if err != nil || configuration.Validate() != nil {
		t.Fatal(configuration, err)
	}
	binding, err := r.BindingMetadata(configuration.SHA256, spec.ExecutableSHA256)
	if err != nil || binding.Validate() != nil || binding.ProfileSHA256 != prepared.ProfileSHA256 {
		t.Fatal(binding, err)
	}
	for _, path := range []string{"/api/pull", "/api/show?model=other", "/api/delete"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL()+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if response, err := r.HTTPClient().Do(req); err == nil {
			_ = response.Body.Close()
			t.Fatalf("unsupported endpoint accepted: %s", path)
		}
	}
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for r.Alive() == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := r.Alive(); !errors.Is(err, ErrOwnershipLost) || !ollama.IsLocalityError(err) {
		t.Fatalf("ownership failure is not fatal locality: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.home); !os.IsNotExist(err) {
		t.Fatalf("private home remained after close: %v", err)
	}
}

func TestOwnedRuntimeStopsRunnerDescendantAndRejectsChangedPreparation(t *testing.T) {
	spec := fixtureSpec(t)
	spec.NumCtx = 516
	p, err := Prepare(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	changed := p
	changed.Spec.ReserveBytes++
	if _, err := Start(context.Background(), changed); err == nil {
		t.Fatal("mutated preparation launched")
	}
	r, err := Start(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	data, err := os.ReadFile(filepath.Join(r.home, "descendant.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	state, err := windows.WaitForSingleObject(handle, 1000)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("runner escaped owned job: %d, %v", state, err)
	}
	if err := r.Close(); err != nil {
		t.Fatal("close is not idempotent:", err)
	}
	if _, err := r.ModelConfiguration(context.Background(), "fixture"); !ollama.IsLocalityError(err) {
		t.Fatalf("closed runtime metadata accepted: %v", err)
	}
}

func TestOwnedRuntimeStartupAndLibraryDriftFailBeforeUse(t *testing.T) {
	spec := fixtureSpec(t)
	spec.NumCtx = 513
	p, err := Prepare(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Start(context.Background(), p); err == nil {
		t.Fatal("early-exiting child accepted")
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(spec.Executable), "lib", "ollama", "fixture.dll"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(context.Background(), spec); err == nil {
		t.Fatal("changed runtime library accepted")
	}
}

func TestNativeListenerTableRejectsWrongProcessAndWildcard(t *testing.T) {
	data := make([]byte, 28)
	binary.LittleEndian.PutUint32(data, 1)
	binary.LittleEndian.PutUint32(data[8:12], 0x0100007f)
	binary.BigEndian.PutUint16(data[12:14], 12345)
	binary.LittleEndian.PutUint32(data[24:28], 100)
	if err := checkListenerTable(data, 100, 12345); err != nil {
		t.Fatal(err)
	}
	if checkListenerTable(data, 101, 12345) == nil {
		t.Fatal("another process accepted")
	}
	binary.LittleEndian.PutUint32(data[8:12], 0)
	if checkListenerTable(data, 100, 12345) == nil {
		t.Fatal("wildcard listener accepted")
	}
	if checkListenerTable(data[:10], 100, 12345) == nil {
		t.Fatal("truncated table accepted")
	}
}

func TestInspectInstalledRuntimeOptIn(t *testing.T) {
	executable := os.Getenv("FITR_AUTORUNTIME_INSPECT_EXE")
	if executable == "" {
		t.Skip("explicit software inspection is opt-in; never starts serving")
	}
	spec, err := Inspect(context.Background(), executable, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(data))
}
