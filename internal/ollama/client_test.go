package ollama

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPullStreamsProgressAndSurfacesErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/pull" {
			t.Errorf("path = %s, want /api/pull", r.URL.Path)
		}
		flusher, _ := w.(http.Flusher)
		fmt.Fprintln(w, `{"status":"pulling manifest"}`)
		flusher.Flush()
		fmt.Fprintln(w, `{"status":"downloading","total":100,"completed":40}`)
		flusher.Flush()
		fmt.Fprintln(w, `{"status":"success","total":100,"completed":100}`)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	var last string
	var lastPct int
	if err := c.Pull(context.Background(), "hf.co/org/model:Q4_K_M", func(status string, pct int) {
		last, lastPct = status, pct
	}); err != nil {
		t.Fatal(err)
	}
	if last != "success" || lastPct != 100 {
		t.Fatalf("last progress = %q %d%%, want success 100%%", last, lastPct)
	}

	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"status":"pulling manifest"}`)
		fmt.Fprintln(w, `{"error":"file does not exist"}`)
	}))
	defer errSrv.Close()
	c = &Client{BaseURL: errSrv.URL, HTTP: errSrv.Client()}
	if err := c.Pull(context.Background(), "hf.co/missing/model", nil); err == nil {
		t.Fatal("a pull error from the server must surface")
	}
}

func TestShowCapturesModelInfoAndSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/show" {
			t.Errorf("path = %s", r.URL.Path)
		}
		fmt.Fprintln(w, `{
			"details":{"parameter_size":"30.5B","quantization_level":"Q4_K_M","family":"qwen3moe"},
			"size":20401094656,
			"model_info":{
				"general.architecture":"qwen3moe",
				"general.parameter_count":30532132864,
				"qwen3moe.block_count":48,
				"qwen3moe.expert_count":128,
				"qwen3moe.expert_used_count":8
			}
		}`)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	mi, err := c.Show(context.Background(), "qwen3:30b")
	if err != nil {
		t.Fatal(err)
	}
	if mi.Size != 20401094656 || mi.Details.Family != "qwen3moe" {
		t.Fatalf("size/family = %d %q", mi.Size, mi.Details.Family)
	}
	if mi.Info["general.architecture"] != "qwen3moe" {
		t.Fatalf("model_info dropped: %v", mi.Info)
	}
	if mi.Info["qwen3moe.expert_used_count"] != float64(8) {
		t.Fatalf("expert_used_count = %v (JSON numbers must survive)", mi.Info["qwen3moe.expert_used_count"])
	}
}

func TestTagsPreservesImmutableModelDigest(t *testing.T) {
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("path = %s, want /api/tags", r.URL.Path)
		}
		fmt.Fprintf(w, `{"models":[{"name":"qwen:latest","size":123,"digest":%q}]}`, digest)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	models, err := c.Tags(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Digest != digest {
		t.Fatalf("tags lost resolved identity fields: %+v", models)
	}
}

func TestEffectiveContextUsesExactLoadedModelReceipt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ps" {
			t.Errorf("path = %s, want /api/ps", r.URL.Path)
		}
		fmt.Fprint(w, `{"models":[`+
			`{"name":"other:latest","context_length":4096},`+
			`{"name":"qwen:latest","context_length":32768}]}`)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}

	running, err := c.PS(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 2 || running[1].ContextLength != 32768 {
		t.Fatalf("PS context receipt = %+v", running)
	}
	got, observed, err := c.EffectiveContext(context.Background(), "qwen:latest")
	if err != nil || !observed || got != 32768 {
		t.Fatalf("EffectiveContext = %d, %v, %v", got, observed, err)
	}
	got, observed, err = c.EffectiveContext(context.Background(), "qwen")
	if err != nil || observed || got != 0 {
		t.Fatalf("mutable alias must not select a receipt: %d, %v, %v", got, observed, err)
	}
}

func TestEffectiveContextPreservesMissingAndRejectsInvalidReceipts(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"missing field", `{"models":[{"name":"model"}]}`, ""},
		{"negative field", `{"models":[{"name":"model","context_length":-1}]}`, "negative"},
		{"ambiguous model", `{"models":[{"name":"model","context_length":4096},{"name":"model","context_length":8192}]}`, "ambiguous"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
			got, observed, err := c.EffectiveContext(context.Background(), "model")
			if tc.wantErr == "" {
				if err != nil || observed || got != 0 {
					t.Fatalf("missing observation = %d, %v, %v", got, observed, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) || observed || got != 0 {
				t.Fatalf("invalid observation = %d, %v, %v", got, observed, err)
			}
		})
	}
}

func TestGenerateRequiresStrictFramesAndTerminalReceipt(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantText  string
		wantError string
	}{
		{
			name: "legitimate stream",
			body: `{"response":"ok","done":false}` + "\n" +
				`{"done":true,"done_reason":"stop","eval_count":1,"eval_duration":1000}` + "\n",
			wantText: "ok",
		},
		{name: "malformed frame", body: "{\n", wantError: "generate frame"},
		{name: "trailing JSON in frame", body: `{"done":true} {}` + "\n", wantError: "content after JSON frame"},
		{name: "early EOF", body: `{"response":"partial","done":false}` + "\n", wantText: "partial", wantError: "before a terminal frame"},
		{name: "data after terminal", body: `{"done":true}` + "\n" + `{"response":"spoof"}` + "\n", wantError: "after the terminal frame"},
		{name: "negative receipt", body: `{"done":true,"eval_count":-1}` + "\n", wantError: "negative metric"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
			text, _, err := c.Generate(context.Background(), "m", "p", Deterministic(8, 8192))
			if text != tc.wantText {
				t.Fatalf("text = %q, want %q", text, tc.wantText)
			}
			if tc.wantError == "" && err != nil {
				t.Fatal(err)
			}
			if tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("error = %v, want %q", err, tc.wantError)
			}
		})
	}
}

func TestChatRequiresSingleBoundedTerminalResponse(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantError string
	}{
		{"legitimate response", `{"message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop"}`, ""},
		{"missing terminal", `{"message":{"role":"assistant","content":"partial"}}`, "terminal receipt"},
		{"trailing response", `{"done":true} {}`, "content after JSON frame"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
			_, _, err := c.Chat(context.Background(), "m", nil, nil, Deterministic(8, 8192))
			if tc.wantError == "" && err != nil {
				t.Fatal(err)
			}
			if tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("error = %v, want %q", err, tc.wantError)
			}
		})
	}
}

func TestPullRejectsMalformedAndIncompleteStreams(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"malformed", "{\n", "pull frame"},
		{"early EOF", `{"status":"downloading","total":10,"completed":5}` + "\n", "before a success receipt"},
		{"after success", `{"status":"success"}` + "\n" + `{"status":"extra"}` + "\n", "after the terminal frame"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
			if err := c.Pull(context.Background(), "m", nil); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}
