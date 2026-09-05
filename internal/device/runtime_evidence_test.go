package device

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/ollama"
)

type placementBackend struct {
	name    string
	running []ollama.RunningModel
	err     error
}

func (b placementBackend) Name() string                   { return b.name }
func (b placementBackend) URL() string                    { return "fake://local" }
func (b placementBackend) Version(context.Context) string { return "fake" }
func (b placementBackend) Reachable(context.Context) bool { return true }
func (b placementBackend) Generate(context.Context, string, string, ollama.Sampling) (string, ollama.Metrics, error) {
	return "", ollama.Metrics{}, nil
}
func (b placementBackend) Chat(context.Context, string, []ollama.Message, []ollama.Tool, ollama.Sampling) (ollama.Message, ollama.Metrics, error) {
	return ollama.Message{}, ollama.Metrics{}, nil
}
func (b placementBackend) Tags(context.Context) ([]ollama.ModelInfo, error) { return nil, nil }
func (b placementBackend) Show(context.Context, string) (ollama.ModelInfo, error) {
	return ollama.ModelInfo{}, nil
}
func (b placementBackend) PS(context.Context) ([]ollama.RunningModel, error) {
	return b.running, b.err
}
func (b placementBackend) StopAll(context.Context) ([]string, error) { return nil, nil }

func TestInferenceDeviceUsesResidentAllocation(t *testing.T) {
	for _, tc := range []struct {
		name, model, want string
		running           []ollama.RunningModel
	}{
		{name: "cpu", model: "m", want: "CPU", running: []ollama.RunningModel{{Name: "m", Size: 100}}},
		{name: "partial gpu", model: "m", want: "GPU 75%", running: []ollama.RunningModel{{Name: "m", Size: 100, SizeVRAM: 75}}},
		{name: "select model", model: "b", want: "GPU 50%", running: []ollama.RunningModel{
			{Name: "a", Size: 100}, {Name: "b", Size: 100, SizeVRAM: 50},
		}},
		{name: "unknown", model: "missing", want: "unknown", running: []ollama.RunningModel{{Name: "m", Size: 100}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := InferenceDeviceFor(context.Background(), placementBackend{name: "fake", running: tc.running}, tc.model)
			if got != tc.want {
				t.Fatalf("placement = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestServerLogFallbackExtractsLatestConfigAndPlacement(t *testing.T) {
	root := t.TempDir()
	var path string
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", root)
		path = filepath.Join(root, "Ollama", "server.log")
	} else {
		t.Setenv("HOME", root)
		path = filepath.Join(root, ".ollama", "logs", "server.log")
	}
	if serverLogPath() != path {
		t.Fatalf("server log path = %q, want %q", serverLogPath(), path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	log := "env=[OLLAMA_KV_CACHE_TYPE:f16 OLLAMA_CONTEXT_LENGTH:4096]\n" +
		"env=[OLLAMA_KV_CACHE_TYPE:q8_0 OLLAMA_CONTEXT_LENGTH:8192]\n" +
		`time=now msg="inference compute" library=cuda description="NVIDIA GPU"` + "\n"
	if err := os.WriteFile(path, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]string{}
	mergeServerLogConfig(cfg)
	if cfg["OLLAMA_KV_CACHE_TYPE"] != "q8_0" || cfg["OLLAMA_CONTEXT_LENGTH"] != "8192" {
		t.Fatalf("latest config = %+v", cfg)
	}
	got := inferenceDevice(context.Background(), placementBackend{name: "ollama", err: os.ErrNotExist}, "m")
	if !strings.Contains(got, "cuda") || !strings.Contains(got, "NVIDIA GPU") {
		t.Fatalf("log placement = %q", got)
	}
	if submatch(`missing=(\S+)`, log) != "" || lastLogMatch("never-present") != "" {
		t.Fatal("missing log matches were fabricated")
	}
}

func TestProfileBoolAcceptsOnlyBooleanValues(t *testing.T) {
	p := Profile{Gates: map[string]Gate{"test": {"yes": true, "text": "true"}}}
	if value, ok := p.Bool("test", "yes"); !ok || !value {
		t.Fatalf("bool hint = %v, %v", value, ok)
	}
	if _, ok := p.Bool("test", "text"); ok {
		t.Fatal("string hint was accepted as a boolean")
	}
}

// An empty configuration value means two different things. When the serving
// runtime's startup log was read, the runtime did not have that variable. When
// it was not, fitr only ever saw its own environment, and a daemon started by
// launchd or systemd carries one this process never sees. Reporting the first
// when only the second is true states the daemon's configuration from evidence
// about fitr's own.
func TestServerConfigObservedSeparatesUnsetFromUnobserved(t *testing.T) {
	root := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", root)
	} else {
		t.Setenv("HOME", root)
	}
	if ServerConfigObserved() {
		t.Fatal("an absent startup log was reported as observed configuration")
	}
	path := serverLogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("env=[OLLAMA_FLASH_ATTENTION:1]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !ServerConfigObserved() {
		t.Fatal("a readable startup log was not reported as observed configuration")
	}
	// An empty file is not a reading of the daemon's environment either.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if ServerConfigObserved() {
		t.Fatal("an empty startup log was reported as observed configuration")
	}
}
