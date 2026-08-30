package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/render"
)

const (
	artifactDigestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	artifactDigestB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type countingInventoryBackend struct {
	*inventoryBackend
	tagCalls int
}

func (b *countingInventoryBackend) Tags(ctx context.Context) ([]ollama.ModelInfo, error) {
	b.tagCalls++
	return b.inventoryBackend.Tags(ctx)
}

type artifactVerifierCall struct {
	model    string
	reported string
}

type artifactVerifierTestBackend struct {
	*inventoryBackend
	verify func(model, reported string) (string, error)
	calls  []artifactVerifierCall
}

func (b *artifactVerifierTestBackend) VerifyModelDigest(model, reported string) (string, error) {
	b.calls = append(b.calls, artifactVerifierCall{model: model, reported: reported})
	return b.verify(model, reported)
}

type backendTraceDisplay struct {
	render.Display
	notes  []string
	phases []string
	done   []string
}

func (d *backendTraceDisplay) Note(message, level string) {
	d.notes = append(d.notes, level+":"+message)
	d.Display.Note(message, level)
}

func (d *backendTraceDisplay) Phase(name, detail string) {
	d.phases = append(d.phases, name+":"+detail)
	d.Display.Phase(name, detail)
}

func (d *backendTraceDisplay) Done(name string, seconds float64) {
	d.done = append(d.done, name)
	d.Display.Done(name, seconds)
}

func newBackendTraceDisplay(t *testing.T) *backendTraceDisplay {
	t.Helper()
	base := render.New("none")
	t.Cleanup(base.Close)
	return &backendTraceDisplay{Display: base}
}

func testInventoryBackend(name string, tags []ollama.ModelInfo, err error) *inventoryBackend {
	return &inventoryBackend{
		runIntegrationBackend: &runIntegrationBackend{},
		name:                  name,
		tags:                  tags,
		err:                   err,
	}
}

func TestVerifiedModelArtifactDigestFailsClosed(t *testing.T) {
	ctx := context.Background()
	if got := verifiedModelArtifactDigest(ctx, nil, "model"); got != "" {
		t.Fatalf("nil backend digest = %q", got)
	}

	empty := &countingInventoryBackend{inventoryBackend: testInventoryBackend("fake", nil, nil)}
	if got := verifiedModelArtifactDigest(ctx, empty, " \t "); got != "" {
		t.Fatalf("blank model digest = %q", got)
	}
	if empty.tagCalls != 0 {
		t.Fatalf("blank model queried inventory %d time(s)", empty.tagCalls)
	}

	for _, tc := range []struct {
		name string
		tags []ollama.ModelInfo
		err  error
	}{
		{name: "inventory error", err: errors.New("inventory unavailable")},
		{name: "no matching tag", tags: []ollama.ModelInfo{{Name: "other", Digest: artifactDigestA}}},
		{name: "matching tag without digest", tags: []ollama.ModelInfo{{Name: "model", Digest: "  "}}},
		{name: "matching tag with invalid digest", tags: []ollama.ModelInfo{{Name: "model", Digest: "not-a-digest"}}},
		{name: "conflicting aliases", tags: []ollama.ModelInfo{
			{Name: "model", Digest: artifactDigestA},
			{Name: "model:latest", Digest: artifactDigestB},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := testInventoryBackend("fake", tc.tags, tc.err)
			if got := verifiedModelArtifactDigest(ctx, backend, "model"); got != "" {
				t.Fatalf("digest = %q, want fail-closed empty digest", got)
			}
		})
	}
}

func TestVerifiedModelArtifactDigestAcceptsExactEvidence(t *testing.T) {
	ctx := context.Background()
	backend := testInventoryBackend("fake", []ollama.ModelInfo{
		{Name: "other", Digest: artifactDigestB},
		{Name: "model:latest", Digest: artifactDigestA},
	}, nil)
	if got := verifiedModelArtifactDigest(ctx, backend, "model"); got != artifactDigestA {
		t.Fatalf("exact digest = %q, want %q", got, artifactDigestA)
	}

	backend.tags = []ollama.ModelInfo{
		{Name: "model", Digest: artifactDigestA},
		{Name: "model:latest", Digest: strings.ToUpper(artifactDigestA)},
	}
	if got := verifiedModelArtifactDigest(ctx, backend, "model"); !strings.EqualFold(got, artifactDigestA) {
		t.Fatalf("equivalent duplicate digest = %q, want case-equivalent %q", got, artifactDigestA)
	}
}

func TestVerifiedModelArtifactDigestRequiresVerifierApproval(t *testing.T) {
	ctx := context.Background()
	newBackend := func(tags []ollama.ModelInfo, verify func(string, string) (string, error)) *artifactVerifierTestBackend {
		return &artifactVerifierTestBackend{
			inventoryBackend: testInventoryBackend("verified", tags, nil),
			verify:           verify,
		}
	}

	t.Run("approved report", func(t *testing.T) {
		backend := newBackend([]ollama.ModelInfo{{
			Name: "model:latest", Digest: artifactDigestB, ReportedDigest: "runtime-report",
		}}, func(model, reported string) (string, error) {
			return artifactDigestA, nil
		})
		if got := verifiedModelArtifactDigest(ctx, backend, "model"); got != artifactDigestA {
			t.Fatalf("verified digest = %q, want %q", got, artifactDigestA)
		}
		if len(backend.calls) != 1 || backend.calls[0].model != "model:latest" || backend.calls[0].reported != "runtime-report" {
			t.Fatalf("verifier calls = %+v", backend.calls)
		}
	})

	t.Run("verifier error", func(t *testing.T) {
		backend := newBackend([]ollama.ModelInfo{{Name: "model", ReportedDigest: "untrusted"}},
			func(string, string) (string, error) { return "", errors.New("proof mismatch") })
		if got := verifiedModelArtifactDigest(ctx, backend, "model"); got != "" {
			t.Fatalf("rejected proof produced digest %q", got)
		}
	})

	t.Run("empty verifier result", func(t *testing.T) {
		backend := newBackend([]ollama.ModelInfo{{Name: "model", ReportedDigest: "untrusted"}},
			func(string, string) (string, error) { return " \t", nil })
		if got := verifiedModelArtifactDigest(ctx, backend, "model"); got != "" {
			t.Fatalf("empty proof produced digest %q", got)
		}
	})

	t.Run("conflicting verified aliases", func(t *testing.T) {
		backend := newBackend([]ollama.ModelInfo{
			{Name: "model", ReportedDigest: "first"},
			{Name: "model:latest", ReportedDigest: "second"},
		}, func(_ string, reported string) (string, error) {
			if reported == "first" {
				return artifactDigestA, nil
			}
			return artifactDigestB, nil
		})
		if got := verifiedModelArtifactDigest(ctx, backend, "model"); got != "" {
			t.Fatalf("conflicting proofs produced digest %q", got)
		}
	})
}

func TestCheckModelWithDisplaySkipsInventoryForEmptyModel(t *testing.T) {
	backend := &countingInventoryBackend{inventoryBackend: testInventoryBackend("fake", nil, errors.New("must not be called"))}
	display := newBackendTraceDisplay(t)
	got, code := checkModelWithDisplay(context.Background(), backend, "", false, display)
	if code != exitOK || got != backend {
		t.Fatalf("backend=%T code=%d, want original backend and success", got, code)
	}
	if backend.tagCalls != 0 {
		t.Fatalf("empty model queried inventory %d time(s)", backend.tagCalls)
	}
}

func TestCheckModelWithDisplayIdentityAndHintDecisions(t *testing.T) {
	for _, tc := range []struct {
		name      string
		backend   *inventoryBackend
		model     string
		pull      bool
		wantCode  int
		wantSame  bool
		wantNotes []string
	}{
		{
			name: "inventory error", backend: testInventoryBackend("openai", nil, errors.New("malformed inventory")),
			model: "model", wantCode: exitError,
			wantNotes: []string{"could not list models from openai", "malformed inventory", "model inventory endpoint"},
		},
		{
			name: "served latest alias", backend: testInventoryBackend("openai", []ollama.ModelInfo{{Name: "model:latest"}}, nil),
			model: "model", wantCode: exitOK, wantSame: true,
		},
		{
			name: "empty single model runtime", backend: testInventoryBackend("llama-server", []ollama.ModelInfo{}, nil),
			model: "model", wantCode: exitError,
			wantNotes: []string{"llama-server is serving no models", "load a model in the runtime"},
		},
		{
			name: "Hugging Face ref on loaded runtime", backend: testInventoryBackend("llama-server", []ollama.ModelInfo{{Name: "served.gguf"}}, nil),
			model: "hf.co/acme/model:Q4_K_M", wantCode: exitUsage,
			wantNotes: []string{"Hugging Face refs need Ollama to pull", "pass the served model name"},
		},
		{
			name: "pull on single model runtime warns but resolves", backend: testInventoryBackend("llama-server", []ollama.ModelInfo{{Name: "served.gguf"}}, nil),
			model: "requested.gguf", pull: true, wantCode: exitOK, wantSame: true,
			wantNotes: []string{"--pull is an Ollama feature", "serves \"served.gguf\", not \"requested.gguf\"", "run manifest will record the resolved model"},
		},
		{
			name: "near Ollama tag", backend: testInventoryBackend("ollama", []ollama.ModelInfo{{Name: "qwen3:8b"}}, nil),
			model: "qwen3", wantCode: exitUsage,
			wantNotes: []string{"model \"qwen3\" is not installed", "did you mean: qwen3:8b"},
		},
		{
			name: "missing Ollama tag", backend: testInventoryBackend("ollama", []ollama.ModelInfo{{Name: "other:latest"}}, nil),
			model: "missing", wantCode: exitUsage,
			wantNotes: []string{"model \"missing\" is not installed", "ollama pull missing", "re-run with --pull"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			display := newBackendTraceDisplay(t)
			got, code := checkModelWithDisplay(context.Background(), tc.backend, tc.model, tc.pull, display)
			if code != tc.wantCode {
				t.Fatalf("exit code = %d, want %d", code, tc.wantCode)
			}
			if tc.wantSame && got != tc.backend {
				t.Fatalf("backend = %T, want original backend", got)
			}
			if !tc.wantSame && got != nil {
				t.Fatalf("backend = %T, want nil", got)
			}
			joined := strings.Join(display.notes, "\n")
			for _, want := range tc.wantNotes {
				if !strings.Contains(joined, want) {
					t.Fatalf("notes do not contain %q:\n%s", want, joined)
				}
			}
			if len(tc.wantNotes) == 0 && joined != "" {
				t.Fatalf("successful exact identity emitted notes:\n%s", joined)
			}
		})
	}
}

func TestCheckModelWithDisplayPullsHuggingFaceRefThroughOllama(t *testing.T) {
	var pulls atomic.Int32
	var pulledModel atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tags":
			_, _ = io.WriteString(w, `{"models":[]}`)
		case "/api/pull":
			pulls.Add(1)
			var request struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode pull request: %v", err)
			}
			pulledModel.Store(request.Model)
			_, _ = io.WriteString(w, "{\"status\":\"pulling manifest\"}\n{\"status\":\"success\"}\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &ollama.Client{BaseURL: server.URL, HTTP: server.Client()}
	display := newBackendTraceDisplay(t)
	model := "hf.co/acme/model:Q4_K_M"
	got, code := checkModelWithDisplay(context.Background(), client, model, false, display)
	if code != exitOK || got != client {
		t.Fatalf("backend=%T code=%d, want Ollama client and success", got, code)
	}
	if pulls.Load() != 1 || pulledModel.Load() != model {
		t.Fatalf("pulls=%d model=%v", pulls.Load(), pulledModel.Load())
	}
	if !containsString(display.phases, "pull:Hugging Face via Ollama") || !containsString(display.done, "pull") {
		t.Fatalf("phases=%v done=%v", display.phases, display.done)
	}
}

func TestCheckModelWithDisplayReportsPullFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/tags" {
			_, _ = io.WriteString(w, `{"models":[]}`)
			return
		}
		if r.URL.Path == "/api/pull" {
			http.Error(w, "registry unavailable", http.StatusBadGateway)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := &ollama.Client{BaseURL: server.URL, HTTP: server.Client()}
	display := newBackendTraceDisplay(t)
	got, code := checkModelWithDisplay(context.Background(), client, "missing", true, display)
	if code != exitError || got != nil {
		t.Fatalf("backend=%T code=%d, want nil and error", got, code)
	}
	joined := strings.Join(display.notes, "\n")
	if !strings.Contains(joined, "model pull failed") || !strings.Contains(joined, "502") {
		t.Fatalf("pull failure notes:\n%s", joined)
	}
	if len(display.done) != 0 {
		t.Fatalf("failed pull was marked done: %v", display.done)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
