package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/blisspixel/fitr/internal/discovery"
)

func TestHFModelRefsPreserveUnsupportedIdentity(t *testing.T) {
	for _, ref := range []string{
		"https://huggingface.co/org/model/blob/main/model-Q4_K_M.gguf",
		"https://huggingface.co/org/model/resolve/main/model.Q8_0.gguf?download=true",
		"https://huggingface.co/org/model/blob/0123456789012345678901234567890123456789/model-Q4_K_M-00001-of-00002.gguf",
		"https://huggingface.co/org/model/tree/v1.0",
		"https://huggingface.co/org/model/tree/0123456789012345678901234567890123456789",
		"hf.co/org/model/tree/main/subdir",
		"huggingface.co/org/model/resolve/main/model.gguf",
		"https://www.huggingface.co/org/model/blob/main/Q4_K_M.gguf",
		"http://hf.co/org/model/blob/main/model.gguf",
		"https://huggingface.co/org/model?revision=v1",
		"https://huggingface.co/org/model#revision=v1",
		"hf.co/org/model%2Fresolve%2Fmain%2Fmodel.gguf",
		"hf.co/org/model:Q4_K_M/resolve/main/other.gguf",
		"hf.co/org//model",
		"hf.co/org/model:",
		"hf.co/org/..",
		"hf.co/org",
		"hf.co/org/model\x1b[31m",
	} {
		t.Run(ref, func(t *testing.T) {
			if got := normalizeModelRef("  " + ref + "  "); got != ref {
				t.Fatalf("artifact identity changed: got %q, want %q", got, ref)
			}
			if err := validateModelRefs(ref); err == nil || !strings.Contains(err.Error(), "cannot be preserved") {
				t.Fatalf("unsupported reference was not rejected: %v", err)
			}
		})
	}
}

func TestHFModelRefsKeepUnpinnedAliases(t *testing.T) {
	for input, want := range map[string]string{
		" HTTPS://HUGGINGFACE.CO/org/model/ ":            "hf.co/org/model",
		"https://www.huggingface.co/org/model/tree/main": "hf.co/org/model",
		"www.huggingface.co/org/model:Q4_K_M":            "hf.co/org/model:Q4_K_M",
		"http://www.huggingface.co/org/model":            "hf.co/org/model",
		"http://huggingface.co/org/model":                "hf.co/org/model",
		"https://hf.co/org/model:IQ4_XS":                 "hf.co/org/model:IQ4_XS",
		"hf.co/org/model:filename-Q4_K_M.gguf":           "hf.co/org/model:filename-Q4_K_M.gguf",
		"qwen3:8b":                                       "qwen3:8b",
		`C:\models\model#v1.gguf`:                        `C:\models\model#v1.gguf`,
		"/models/model?v1.gguf":                          "/models/model?v1.gguf",
	} {
		if err := validateModelRefs(input); err != nil {
			t.Errorf("valid alias %q rejected: %v", input, err)
		}
		if got := normalizeModelRef(input); got != want {
			t.Errorf("normalize(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestHFModelRefsRejectBeforeBackendAccess(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	for _, name := range []string{"OLLAMA_BASE_URL", "LLAMA_SERVER_URL", "FITR_OPENAI_URL", "FITR_DISCOVER_URLS"} {
		t.Setenv(name, server.URL)
	}
	t.Setenv("FITR_BACKEND", "")
	for _, kind := range []string{"auto", "ollama", "llama-server", "openai"} {
		for _, suffix := range []string{"blob/main/model-Q4_K_M.gguf", "resolve/v1/model.gguf", "tree/v1"} {
			stderr, code := captureTopStderr(t, func() int {
				backend, code := newBackend(context.Background(), "hf.co/org/model/"+suffix, kind, true)
				if backend != nil {
					t.Fatal("unsupported URL returned a backend")
				}
				return code
			})
			if code != exitUsage || !strings.Contains(stderr, "fitr discover add") {
				t.Fatalf("backend %s accepted %s: exit=%d, stderr=%q", kind, suffix, code, stderr)
			}
		}
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("rejected references contacted backend %d times", got)
	}
}

func TestHFModelRefsRejectedByCommandEntryPoints(t *testing.T) {
	ref := "https://huggingface.co/org/model/blob/main/model-Q4_K_M.gguf"
	for _, name := range []string{"run", "advise", "apply", "export", "view", "diag", "doctor", "tune", "calibrate", "decide"} {
		t.Run(name, func(t *testing.T) {
			stderr, code := captureTopStderr(t, func() int {
				return commandHandler(name)(context.Background(), []string{ref})
			})
			if code != exitUsage || !strings.Contains(stderr, "unsupported Hugging Face model reference") {
				t.Fatalf("exit=%d, stderr=%q", code, stderr)
			}
		})
	}
	for _, action := range []string{"context", "confirm", "workload"} {
		stderr, code := captureTopStderr(t, func() int {
			return cmdExperiment(context.Background(), []string{action, ref})
		})
		if code != exitUsage || !strings.Contains(stderr, "unsupported Hugging Face model reference") {
			t.Fatalf("experiment %s exit=%d, stderr=%q", action, code, stderr)
		}
	}
	stderr, code := captureTopStderr(t, func() int {
		return cmdTopView(context.Background(), []string{ref})
	})
	if code != exitUsage || !strings.Contains(stderr, "unsupported Hugging Face model reference") {
		t.Fatalf("top view exit=%d, stderr=%q", code, stderr)
	}
	if _, err := previewTopRun([]string{ref}); err == nil {
		t.Fatal("top preview accepted an unsupported reference")
	}
}

func TestHFModelRefsRemainInertDiscoverySources(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("FITR_RESULTS", directory)
	ref := "https://huggingface.co/org/model/blob/0123456789012345678901234567890123456789/model-Q4_K_M.gguf"
	if code := cmdDiscover(context.Background(), []string{"add", ref, "--role", "coding", "--model", ref, "--display", "none"}); code != exitOK {
		t.Fatalf("exact source capture exited %d", code)
	}
	ideas, err := discovery.List(filepath.Join(directory, ".discovery"), "coding")
	if err != nil || len(ideas) != 1 || ideas[0].Model != ref {
		t.Fatalf("source capture changed model identity: %+v, %v", ideas, err)
	}
}
