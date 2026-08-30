package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/blisspixel/fitr/internal/advise"
)

const adviseTestModel = "model:latest"

func adviseOllamaServer(t *testing.T, showOK bool, resident *atomic.Bool, generateCalls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tags":
			fmt.Fprintf(w, `{"models":[{"name":%q,"size":2147483648}]}`, adviseTestModel)
		case "/api/version":
			io.WriteString(w, `{"version":"test-runtime"}`)
		case "/api/show":
			if !showOK {
				http.Error(w, `{"error":"metadata unavailable"}`, http.StatusInternalServerError)
				return
			}
			io.WriteString(w, `{
				"details":{"parameter_size":"3B","quantization_level":"Q4_K_M","family":"llama"},
				"size":2147483648,
				"model_info":{
					"general.architecture":"llama",
					"general.parameter_count":3000000000,
					"llama.block_count":32,
					"llama.embedding_length":4096,
					"llama.attention.head_count":32,
					"llama.attention.head_count_kv":8,
					"llama.context_length":32768
				}
			}`)
		case "/api/ps":
			if resident != nil && resident.Load() {
				fmt.Fprintf(w, `{"models":[{"name":%q,"size":3221225472,"size_vram":3221225472,"context_length":4096}]}`, adviseTestModel)
				return
			}
			io.WriteString(w, `{"models":[]}`)
		case "/api/generate":
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), `"keep_alive":0`) {
				if resident != nil {
					resident.Store(false)
				}
				io.WriteString(w, `{}`)
				return
			}
			if generateCalls != nil {
				generateCalls.Add(1)
			}
			if resident != nil {
				resident.Store(true)
			}
			io.WriteString(w, `{"response":"ok","done":true,"done_reason":"stop","eval_count":1,"eval_duration":1000000,"prompt_eval_count":1,"prompt_eval_duration":1000000}`+"\n")
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestCmdAdviseUsesRuntimeMetadataAndResidentReceipt(t *testing.T) {
	var resident atomic.Bool
	resident.Store(true)
	server := adviseOllamaServer(t, true, &resident, nil)
	defer server.Close()
	t.Setenv("OLLAMA_BASE_URL", server.URL)
	t.Setenv("FITR_RESULTS", t.TempDir())

	out, code := captureTopStdout(t, func() int {
		return cmdAdvise(context.Background(), []string{
			"--backend=ollama", "--vram-gb=8", "--ctx=4096", "--display=json", "model",
		})
	})
	if code != exitOK {
		t.Fatalf("advise exit=%d output=%s", code, out)
	}
	var report advise.Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, out)
	}
	if report.Schema != advise.ReportSchema || report.Tier != advise.Compatible {
		t.Fatalf("report identity/tier = %+v", report)
	}
	if report.ObservedGB != 3 || report.WeightsGB != 2 || report.Ctx != 4096 {
		t.Fatalf("runtime receipts were not preserved: %+v", report)
	}
	if report.Quant != "Q4_K_M" || report.Source != "Ollama /api/show" {
		t.Fatalf("runtime metadata was not used: %+v", report)
	}
}

func TestCmdAdviseFallsBackToInventoryAndAvoidsImpossibleLoad(t *testing.T) {
	var resident atomic.Bool
	var generates atomic.Int32
	server := adviseOllamaServer(t, false, &resident, &generates)
	defer server.Close()
	t.Setenv("OLLAMA_BASE_URL", server.URL)
	t.Setenv("FITR_RESULTS", t.TempDir())

	out, code := captureTopStdout(t, func() int {
		return cmdAdvise(context.Background(), []string{
			"--backend=ollama", "--vram-gb=1", "--ctx=4096", "--load", "--display=json", "model",
		})
	})
	if code != exitGates {
		t.Fatalf("advise exit=%d output=%s", code, out)
	}
	var report advise.Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, out)
	}
	if report.Tier != advise.Incompatible || report.WeightsGB != 2 {
		t.Fatalf("inventory fallback did not establish the capacity failure: %+v", report)
	}
	if report.Source != "ollama (no architecture metadata)" {
		t.Fatalf("fallback source = %q", report.Source)
	}
	if generates.Load() != 0 {
		t.Fatalf("impossible allocation still generated %d time(s)", generates.Load())
	}
}

func TestCmdAdviseLoadsThenMeasuresResidentAllocation(t *testing.T) {
	var resident atomic.Bool
	var generates atomic.Int32
	server := adviseOllamaServer(t, true, &resident, &generates)
	defer server.Close()
	t.Setenv("OLLAMA_BASE_URL", server.URL)
	t.Setenv("FITR_RESULTS", t.TempDir())

	out, code := captureTopStdout(t, func() int {
		return cmdAdvise(context.Background(), []string{
			"--backend=ollama", "--vram-gb=8", "--ctx=4096", "--load", "--display=json", "model",
		})
	})
	if code != exitOK {
		t.Fatalf("advise exit=%d output=%s", code, out)
	}
	var report advise.Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, out)
	}
	if report.ObservedGB != 3 || generates.Load() != 1 {
		t.Fatalf("load receipt=%+v generate_calls=%d", report, generates.Load())
	}
	if resident.Load() {
		t.Fatal("advise --load left the measured model resident")
	}
}

func TestCmdAdviseFitRequiresKnownGGUFPath(t *testing.T) {
	server := adviseOllamaServer(t, true, nil, nil)
	defer server.Close()
	t.Setenv("OLLAMA_BASE_URL", server.URL)

	stderr, code := captureTopStderr(t, func() int {
		return cmdAdvise(context.Background(), []string{
			"--backend=ollama", "--vram-gb=8", "--fit", "--display=none", "model",
		})
	})
	if code != exitUsage || !strings.Contains(stderr, "--fit needs a GGUF path") {
		t.Fatalf("fit exit=%d stderr=%q", code, stderr)
	}
}

func TestCmdAdvisePlainOutputIncludesActionableNextStep(t *testing.T) {
	var resident atomic.Bool
	server := adviseOllamaServer(t, true, &resident, nil)
	defer server.Close()
	t.Setenv("OLLAMA_BASE_URL", server.URL)
	t.Setenv("FITR_RESULTS", t.TempDir())

	var stdout string
	stderr, code := captureTopStderr(t, func() int {
		var inner int
		stdout, inner = captureTopStdout(t, func() int {
			return cmdAdvise(context.Background(), []string{
				"--backend=ollama", "--vram-gb=8", "--ctx=4096", "--display=plain", "model",
			})
		})
		return inner
	})
	if code != exitOK {
		t.Fatalf("advise exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "[compatible]") || !strings.Contains(stdout, "WEIGHT") {
		t.Fatalf("plain advise output = %q", stdout)
	}
	if !strings.Contains(stderr, "next") || !strings.Contains(stderr, "fitr run") {
		t.Fatalf("plain advise next step = %q", stderr)
	}
}

func TestCmdAdviseRejectsLoadForBareGGUF(t *testing.T) {
	path := writeMiniGGUF(t)
	stderr, code := captureTopStderr(t, func() int {
		return cmdAdvise(context.Background(), []string{
			"--vram-gb=8", "--load", "--display=none", path,
		})
	})
	if code != exitUsage || !strings.Contains(stderr, "--load needs a running Ollama") {
		t.Fatalf("bare GGUF load exit=%d stderr=%q", code, stderr)
	}
}

func TestCmdAdvisePlainLowMemoryNamesThePersistStep(t *testing.T) {
	var resident atomic.Bool
	server := adviseOllamaServer(t, true, &resident, nil)
	defer server.Close()
	t.Setenv("OLLAMA_BASE_URL", server.URL)
	t.Setenv("FITR_RESULTS", t.TempDir())

	stderr, code := captureTopStderr(t, func() int {
		_, inner := captureTopStdout(t, func() int {
			return cmdAdvise(context.Background(), []string{
				"--backend=ollama", "--vram-gb=5", "--ctx=32768", "--display=plain", "model",
			})
		})
		return inner
	})
	if code != exitGates {
		t.Fatalf("low-memory advise exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "fitr apply") || !strings.Contains(stderr, "num_ctx=") {
		t.Fatalf("low-memory persist step = %q", stderr)
	}
}
