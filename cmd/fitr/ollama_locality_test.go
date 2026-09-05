package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/blisspixel/fitr/internal/advise"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/render"
)

type localityFixture struct {
	server    *httptest.Server
	tags      []ollama.ModelInfo
	shown     ollama.ModelInfo
	showAfter int32
	response  string
	showBody  string
	shows     atomic.Int32
	pulls     atomic.Int32
	inference atomic.Int32
}

func newLocalityFixture(t *testing.T, tags []ollama.ModelInfo, shown ollama.ModelInfo) *localityFixture {
	t.Helper()
	f := &localityFixture{tags: tags, shown: shown}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": f.tags})
		case "/api/show":
			if f.showBody != "" {
				_, _ = io.WriteString(w, f.showBody)
				return
			}
			shown := f.shown
			if f.shows.Add(1) <= f.showAfter {
				shown = ollama.ModelInfo{}
			}
			_ = json.NewEncoder(w).Encode(shown)
		case "/api/version":
			_, _ = io.WriteString(w, `{"version":"test"}`)
		case "/api/ps":
			_, _ = io.WriteString(w, `{"models":[]}`)
		case "/api/pull":
			f.pulls.Add(1)
			_, _ = io.WriteString(w, `{"status":"success"}`+"\n")
		case "/api/generate", "/api/chat":
			f.inference.Add(1)
			_, _ = io.WriteString(w, f.response)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *localityFixture) client() *ollama.Client {
	return &ollama.Client{BaseURL: f.server.URL, HTTP: f.server.Client()}
}

func TestOllamaLocalityCommandsRejectRemoteBeforeInference(t *testing.T) {
	for _, source := range []string{"tags", "show", "show with invalid size"} {
		for _, command := range []struct {
			name string
			run  func(context.Context, []string) int
			args []string
		}{
			{"run", cmdRun, []string{"--quick", "--display=none"}},
			{"advise", cmdAdvise, []string{"--load", "--ctx=4096", "--display=none"}},
			{"diag", cmdDiag, nil},
			{"doctor", cmdDoctor, nil},
		} {
			t.Run(source+"/"+command.name, func(t *testing.T) {
				selected := ollama.ModelInfo{Name: "selected:latest", Size: 32, Digest: artifactDigestA}
				shown := ollama.ModelInfo{}
				if source == "tags" {
					selected.RemoteModel = "cloud"
				} else {
					shown.RemoteHost = "https://remote.invalid"
					if source == "show with invalid size" {
						shown.Size = -1
					}
				}
				f := newLocalityFixture(t, []ollama.ModelInfo{selected, {Name: "local", Size: 12}}, shown)
				t.Setenv("OLLAMA_BASE_URL", f.server.URL)
				t.Setenv("FITR_RESULTS", t.TempDir())
				args := append([]string{"--backend=ollama", "selected"}, command.args...)
				_, code := captureTopStdout(t, func() int { return command.run(context.Background(), args) })
				if code != exitError || f.inference.Load() != 0 || f.pulls.Load() != 0 {
					t.Fatalf("code=%d inference=%d pulls=%d", code, f.inference.Load(), f.pulls.Load())
				}
			})
		}
	}
}

func TestOllamaLocalityResolutionProtectsDirectPreparedRuns(t *testing.T) {
	for _, metadata := range []string{"tags", "show"} {
		t.Run(metadata, func(t *testing.T) {
			selected := ollama.ModelInfo{Name: "selected:latest", Size: 32, Digest: artifactDigestA}
			shown := ollama.ModelInfo{}
			if metadata == "tags" {
				selected.RemoteHost = "remote"
			} else {
				shown.RemoteModel = "cloud"
			}
			f := newLocalityFixture(t, []ollama.ModelInfo{selected}, shown)
			d := render.New("none")
			defer d.Close()
			t.Setenv("FITR_RESULTS", t.TempDir())
			validated := false
			result, err := execute(context.Background(), f.client(), "selected", runOpts{
				validatePrepared: func(*runExecution) error { validated = true; return nil },
			}, d)
			if !errors.Is(err, ollama.ErrRemoteExecution) || result != nil || validated || f.inference.Load() != 0 {
				t.Fatalf("result=%+v error=%v prepared=%v calls=%d", result, err, validated, f.inference.Load())
			}
		})
	}
}

func TestOllamaLocalityKeepsSelectedLocalModelUsable(t *testing.T) {
	f := newLocalityFixture(t, []ollama.ModelInfo{
		{Name: "selected:latest", Size: 32, Digest: artifactDigestA},
		{Name: "remote", RemoteModel: "cloud", RemoteHost: "remote"},
	}, ollama.ModelInfo{})
	c := f.client()
	got, code := checkModelWithDisplay(context.Background(), c, "selected", false, newBackendTraceDisplay(t))
	if code != exitOK || got != c {
		t.Fatalf("local selection rejected: %T, %d", got, code)
	}
	resolved, err := resolveRunModel(context.Background(), c, "selected")
	if err != nil || resolved.Identity.Value != artifactDigestA || resolved.Info.IsRemote() || f.inference.Load() != 0 {
		t.Fatalf("local identity changed: %+v, %v", resolved, err)
	}
}

func TestOllamaLocalityPostPullRejectsRemote(t *testing.T) {
	f := newLocalityFixture(t, []ollama.ModelInfo{}, ollama.ModelInfo{RemoteModel: "cloud"})
	got, code := checkModelWithDisplay(context.Background(), f.client(), "selected", true, newBackendTraceDisplay(t))
	if code != exitError || got != nil || f.pulls.Load() != 1 || f.inference.Load() != 0 {
		t.Fatalf("post-pull remote accepted: %T, %d, pulls=%d inference=%d", got, code, f.pulls.Load(), f.inference.Load())
	}
}

func TestOllamaLocalityAdviseRejectsLaterShowAndLoadMarkers(t *testing.T) {
	for _, phase := range []string{"later show", "load response", "malformed load response"} {
		t.Run(phase, func(t *testing.T) {
			f := newLocalityFixture(t, []ollama.ModelInfo{{Name: "selected:latest", Size: 32}}, ollama.ModelInfo{})
			wantCalls := int32(1)
			if phase == "later show" {
				f.shown = ollama.ModelInfo{RemoteModel: "cloud", Size: -1}
				f.showAfter = 1
				wantCalls = 0
			} else {
				f.response = `{"remote_host":"remote","done":true,"response":"cloud output","eval_count":1}` + "\n"
				if phase == "malformed load response" {
					f.response = `{"remote_model":123,"remote_host":"remote","done":true}` + "\n"
				}
			}
			t.Setenv("OLLAMA_BASE_URL", f.server.URL)
			t.Setenv("FITR_RESULTS", t.TempDir())
			out, code := captureTopStdout(t, func() int {
				return cmdAdvise(context.Background(), []string{"selected", "--backend=ollama", "--load", "--ctx=4096", "--vram-gb=8", "--display=json"})
			})
			if code != exitError || out != "" || f.inference.Load() != wantCalls {
				t.Fatalf("remote classified as capacity: code=%d output=%s calls=%d", code, out, f.inference.Load())
			}
		})
	}
}

func TestOllamaLocalitySuppressesLocalEvidenceHelpers(t *testing.T) {
	f := newLocalityFixture(t, []ollama.ModelInfo{{Name: "selected", Size: 32, Digest: artifactDigestA, RemoteModel: "cloud"}},
		ollama.ModelInfo{RemoteHost: "remote", Size: 32, Info: llama8BKVs()})
	c := f.client()
	if weightsFromTags(context.Background(), c, "selected") != 0 || verifiedModelArtifactDigest(context.Background(), c, "selected") != "" {
		t.Fatal("remote metadata promoted to local evidence")
	}
	if largerFittingContext(context.Background(), c, "selected", device.Fingerprint{VRAMGb: 24}, 4096) != 0 {
		t.Fatal("remote metadata produced a local capacity hint")
	}
	in := advise.Input{}
	if _, err := readShownAdviseSource(context.Background(), c, "selected", &in); !errors.Is(err, ollama.ErrRemoteExecution) || in.WeightsB != 0 {
		t.Fatalf("remote Show evidence accepted: %+v, %v", in, err)
	}
	merged := mergeModelInfo(ollama.ModelInfo{RemoteModel: "cloud"}, ollama.ModelInfo{RemoteHost: "remote"})
	if merged.RemoteModel != "cloud" || merged.RemoteHost != "remote" {
		t.Fatalf("merge erased locality: %+v", merged)
	}
}

func TestOllamaLocalityRejectsMalformedMarkerBeforeMetadataFallback(t *testing.T) {
	for _, body := range []string{
		`{"remote_model":123,"remote_host":"cloud"}`,
		`{"remote_host":"cloud","remote_model":123}`,
		`{"REMOTE_MODEL":false,"remote_model":"cloud"}`,
	} {
		f := newLocalityFixture(t, []ollama.ModelInfo{{Name: "selected", Size: 32}}, ollama.ModelInfo{})
		f.showBody = body
		got, code := checkModelWithDisplay(context.Background(), f.client(), "selected", false, newBackendTraceDisplay(t))
		if code != exitError || got != nil || f.inference.Load() != 0 {
			t.Fatalf("malformed remote preflight accepted: %T, %d, calls=%d", got, code, f.inference.Load())
		}
		in := advise.Input{}
		if _, err := readShownAdviseSource(context.Background(), f.client(), "selected", &in); err == nil || in.WeightsB != 0 {
			t.Fatalf("malformed remote advise fallback: %+v, %v", in, err)
		}
	}
}

func TestOllamaLocalityWeightsRejectConflictingRemoteAlias(t *testing.T) {
	tags := []ollama.ModelInfo{{Name: "selected", Size: 32}, {Name: "selected:latest", Size: 32, RemoteHost: "remote"}}
	for range 2 {
		f := newLocalityFixture(t, tags, ollama.ModelInfo{})
		if got := weightsFromTags(context.Background(), f.client(), "selected"); got != 0 {
			t.Fatalf("remote alias preserved local weights: %d", got)
		}
		tags[0], tags[1] = tags[1], tags[0]
	}
}
