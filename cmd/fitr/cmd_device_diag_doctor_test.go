package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/blisspixel/fitr/internal/device"
)

type diagnosticRuntime struct {
	t *testing.T

	mu              sync.Mutex
	chatCalls       int
	generateCalls   int
	capabilities    []string
	chatHTTPError   bool
	generateErrorAt int
	generateEmptyAt int
	cancelAt        int
	cancel          context.CancelFunc
}

func (r *diagnosticRuntime) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case req.Method == http.MethodGet && req.URL.Path == "/api/tags":
		_, _ = fmt.Fprint(w, `{"models":[{"name":"model:latest","size":1024}]}`)
	case req.Method == http.MethodGet && req.URL.Path == "/api/version":
		_, _ = fmt.Fprint(w, `{"version":"test-runtime-v1"}`)
	case req.Method == http.MethodGet && req.URL.Path == "/api/ps":
		_, _ = fmt.Fprint(w, `{"models":[]}`)
	case req.Method == http.MethodPost && req.URL.Path == "/api/show":
		_, _ = fmt.Fprintf(w, `{"capabilities":%s}`, mustJSON(r.t, r.capabilities))
	case req.Method == http.MethodPost && req.URL.Path == "/api/chat":
		r.serveChat(w, req)
	case req.Method == http.MethodPost && req.URL.Path == "/api/generate":
		r.serveGenerate(w, req)
	default:
		r.t.Errorf("unexpected diagnostic runtime request: %s %s", req.Method, req.URL.Path)
		http.NotFound(w, req)
	}
}

func (r *diagnosticRuntime) serveChat(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.chatCalls++
	r.mu.Unlock()
	if r.chatHTTPError {
		http.Error(w, "injected chat failure", http.StatusInternalServerError)
		return
	}
	var body struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		r.t.Errorf("decode chat request: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	content := ""
	toolCalls := "[]"
	switch {
	case len(body.Messages) == 3:
		content = "The temperature is -3 degrees Celsius."
	case len(body.Messages) > 0 && strings.Contains(strings.ToLower(body.Messages[0].Content), "capital of france"):
		content = "Paris"
	default:
		toolCalls = `[{"function":{"name":"get_weather","arguments":{"city":"Oslo"}}}]`
	}
	_, _ = fmt.Fprintf(w,
		`{"message":{"role":"assistant","content":%q,"tool_calls":%s},"done":true,"done_reason":"stop","eval_count":4,"eval_duration":100000000,"prompt_eval_count":16,"prompt_eval_duration":100000000}`,
		content, toolCalls)
}

func (r *diagnosticRuntime) serveGenerate(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.generateCalls++
	call := r.generateCalls
	r.mu.Unlock()
	if call == r.cancelAt && r.cancel != nil {
		r.cancel()
		return
	}
	if call == r.generateErrorAt {
		http.Error(w, "injected generation failure", http.StatusInternalServerError)
		return
	}
	var body struct {
		Prompt string `json:"prompt"`
		Format string `json:"format"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		r.t.Errorf("decode generation request: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	response := "OK"
	promptTokens := 8
	switch {
	case call == r.generateEmptyAt:
		response = ""
	case strings.Contains(body.Prompt, "doctor-ctx-probe"):
		promptTokens = 2800
	case strings.Contains(body.Prompt, "eight planets"):
		response = "Mercury\nVenus\nEarth\nMars\nJupiter\nSaturn\nUranus\nNeptune"
	case body.Format == "json" || strings.Contains(body.Prompt, "JSON object"):
		response = `{"planets":["Mercury","Venus","Earth","Mars","Jupiter","Saturn","Uranus","Neptune"]}`
	}
	_, _ = fmt.Fprintf(w,
		`{"response":%q,"done":true,"done_reason":"stop","eval_count":8,"eval_duration":100000000,"prompt_eval_count":%d,"prompt_eval_duration":100000000,"load_duration":1000000}`+"\n",
		response, promptTokens)
}

func (r *diagnosticRuntime) calls() (chat, generate int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.chatCalls, r.generateCalls
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func useDiagnosticRuntime(t *testing.T, runtime *diagnosticRuntime) {
	t.Helper()
	server := httptest.NewServer(runtime)
	t.Cleanup(server.Close)
	t.Setenv("OLLAMA_BASE_URL", server.URL)
	t.Setenv("OLLAMA_NUM_PARALLEL", "1")
	t.Setenv("OLLAMA_MAX_LOADED_MODELS", "1")
	t.Setenv("OLLAMA_FLASH_ATTENTION", "")
	t.Setenv("OLLAMA_KV_CACHE_TYPE", "")
}

func captureCommandOutput(t *testing.T, fn func() int) (stdout, stderr string, code int) {
	t.Helper()
	stderr, code = captureTopStderr(t, func() int {
		var inner int
		stdout, inner = captureTopStdout(t, fn)
		return inner
	})
	return stdout, stderr, code
}

func TestDeviceDisplayModesExposeOneConsistentFingerprint(t *testing.T) {
	t.Setenv("FITR_PROFILES", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	jsonOut, stderr, code := captureCommandOutput(t, func() int {
		return cmdDevice(ctx, []string{"--display=json"})
	})
	if code != exitOK || stderr != "" {
		t.Fatalf("device JSON exit=%d stderr=%q", code, stderr)
	}
	var payload struct {
		Fingerprint device.Fingerprint `json:"fingerprint"`
		Key         string             `json:"key"`
		Profile     string             `json:"profile"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &payload); err != nil {
		t.Fatalf("device JSON: %v\n%s", err, jsonOut)
	}
	if payload.Fingerprint.Host == "" || payload.Fingerprint.OS == "" || payload.Key == "" || payload.Profile == "" {
		t.Fatalf("device JSON omitted identity fields: %+v", payload)
	}
	if payload.Key != payload.Fingerprint.Key() {
		t.Fatalf("device JSON key = %q, fingerprint key = %q", payload.Key, payload.Fingerprint.Key())
	}

	plain, stderr, code := captureCommandOutput(t, func() int {
		return cmdDevice(ctx, []string{"--display=plain"})
	})
	if code != exitOK || stderr != "" {
		t.Fatalf("device plain exit=%d stderr=%q", code, stderr)
	}
	for _, want := range []string{"host", "os", "cpu", "config", "profile", "key", payload.Fingerprint.Host} {
		if !strings.Contains(plain, want) {
			t.Fatalf("device plain output missing %q:\n%s", want, plain)
		}
	}

	none, stderr, code := captureCommandOutput(t, func() int {
		return cmdDevice(ctx, []string{"--display=none"})
	})
	if code != exitOK || none != "" || stderr != "" {
		t.Fatalf("device none exit=%d stdout=%q stderr=%q", code, none, stderr)
	}

	stdout, stderr, code := captureCommandOutput(t, func() int {
		return cmdDevice(ctx, []string{"--display=invalid"})
	})
	if code != exitUsage || stdout != "" || !strings.Contains(stderr, "invalid display mode") {
		t.Fatalf("invalid display exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestFormatGPUDriverOmitsEmptyParentheses(t *testing.T) {
	for _, tc := range []struct {
		version string
		date    string
		want    string
	}{
		{want: "unknown"},
		{version: "550.1", want: "550.1"},
		{version: "550.1", date: "2026-08-01", want: "550.1 (2026-08-01)"},
	} {
		if got := formatGPUDriver(tc.version, tc.date); got != tc.want {
			t.Errorf("formatGPUDriver(%q, %q) = %q, want %q", tc.version, tc.date, got, tc.want)
		}
	}
}

func TestDiagReportsHealthyPlumbingAndEvidenceFailures(t *testing.T) {
	t.Run("healthy", func(t *testing.T) {
		runtime := &diagnosticRuntime{t: t, capabilities: []string{"completion", "tools"}}
		useDiagnosticRuntime(t, runtime)
		stdout, stderr, code := captureCommandOutput(t, func() int {
			return cmdDiag(context.Background(), []string{"model", "--backend=ollama", "--ctx=4096"})
		})
		if code != exitOK || stderr != "" {
			t.Fatalf("healthy diag exit=%d stderr=%q\n%s", code, stderr, stdout)
		}
		for _, want := range []string{
			"tool plumbing: model", "[PASS] 1_capability", "[PASS] 2_emits_tool_call",
			"[PASS] 3_valid_args", "[PASS] 4_roundtrip", "[PASS] 5_irrelevance",
			"tool plumbing is healthy",
		} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("healthy diag missing %q:\n%s", want, stdout)
			}
		}
		chat, _ := runtime.calls()
		if chat != 3 {
			t.Fatalf("healthy plumbing chat calls = %d, want 3", chat)
		}
	})

	t.Run("capability missing", func(t *testing.T) {
		runtime := &diagnosticRuntime{t: t, capabilities: []string{"completion"}}
		useDiagnosticRuntime(t, runtime)
		stdout, stderr, code := captureCommandOutput(t, func() int {
			return cmdDiag(context.Background(), []string{"model", "--backend=ollama"})
		})
		if code != exitGates || stderr != "" || !strings.Contains(stdout, "no tool support advertised") ||
			!strings.Contains(stdout, "[FAIL] 1_capability") {
			t.Fatalf("unsupported-tools diag exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		chat, _ := runtime.calls()
		if chat != 0 {
			t.Fatalf("unsupported model reached chat %d time(s)", chat)
		}
	})

	t.Run("transport failure", func(t *testing.T) {
		runtime := &diagnosticRuntime{
			t: t, capabilities: []string{"completion", "tools"}, chatHTTPError: true,
		}
		useDiagnosticRuntime(t, runtime)
		stdout, stderr, code := captureCommandOutput(t, func() int {
			return cmdDiag(context.Background(), []string{"model", "--backend=ollama"})
		})
		if code != exitError || !strings.Contains(stdout, "tool plumbing: model") ||
			!strings.Contains(stderr, "plumbing.emit") || !strings.Contains(stderr, "injected chat failure") {
			t.Fatalf("failed diag exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	})
}

func TestDoctorReportsHealthyFailedErroredAndCanceledRuns(t *testing.T) {
	t.Run("healthy", assertHealthyDoctorRun)
	t.Run("failed health gate", assertFailedDoctorHealthGate)
	t.Run("late transport error", assertDoctorTransportError)
	t.Run("canceled", assertCanceledDoctorRun)
}

func assertHealthyDoctorRun(t *testing.T) {
	runtime := &diagnosticRuntime{t: t, capabilities: []string{"completion"}}
	useDiagnosticRuntime(t, runtime)
	stdout, stderr, code := captureCommandOutput(t, func() int {
		return cmdDoctor(context.Background(), []string{"model", "--backend=ollama", "-n=2", "--ctx=4096"})
	})
	if code != exitOK || stderr != "" {
		t.Fatalf("healthy doctor exit=%d stderr=%q\n%s", code, stderr, stdout)
	}
	for _, want := range []string{
		"doctor: model", "real_token", "served_context", "determinism_text",
		"determinism_json", "healthy - measurements on this box mean what they say",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("healthy doctor missing %q:\n%s", want, stdout)
		}
	}
	_, generated := runtime.calls()
	if generated != 6 {
		t.Fatalf("healthy doctor generation calls = %d, want 6", generated)
	}
}

func assertFailedDoctorHealthGate(t *testing.T) {
	runtime := &diagnosticRuntime{t: t, capabilities: []string{"completion"}, generateEmptyAt: 1}
	useDiagnosticRuntime(t, runtime)
	stdout, stderr, code := captureCommandOutput(t, func() int {
		return cmdDoctor(context.Background(), []string{"model", "--backend=ollama", "-n=2"})
	})
	if code != exitGates || stderr != "" || !strings.Contains(stdout, "real_token") ||
		!strings.Contains(stdout, "emits no tokens") {
		t.Fatalf("failed doctor exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func assertDoctorTransportError(t *testing.T) {
	runtime := &diagnosticRuntime{t: t, capabilities: []string{"completion"}, generateErrorAt: 3}
	useDiagnosticRuntime(t, runtime)
	stdout, stderr, code := captureCommandOutput(t, func() int {
		return cmdDoctor(context.Background(), []string{"model", "--backend=ollama", "-n=2"})
	})
	if code != exitError || !strings.Contains(stdout, "doctor: model") ||
		!strings.Contains(stderr, "injected generation failure") {
		t.Fatalf("errored doctor exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func assertCanceledDoctorRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runtime := &diagnosticRuntime{t: t, capabilities: []string{"completion"}, cancelAt: 3, cancel: cancel}
	useDiagnosticRuntime(t, runtime)
	stdout, stderr, code := captureCommandOutput(t, func() int {
		return cmdDoctor(ctx, []string{"model", "--backend=ollama", "-n=2"})
	})
	if code != exitInterrupt || !strings.Contains(stdout, "doctor: model") ||
		!strings.Contains(stderr, "interrupted") {
		t.Fatalf("canceled doctor exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}
