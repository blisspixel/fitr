package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestRemoteModelInspectionPreservesMixedInventory(t *testing.T) {
	c := &Client{BaseURL: "http://127.0.0.1:11434", HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"models":[{"name":"local:latest","size":12},{"name":"cloud","remote_model":"upstream","remote_host":"https://remote.invalid","size":0}]}`
		if req.URL.Path == "/api/show" {
			body = `{"remote_model":"upstream","remote_host":"https://remote.invalid"}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: req}, nil
	})}}
	models, err := c.Tags(context.Background())
	if err != nil || len(models) != 2 {
		t.Fatalf("Tags = %+v, %v", models, err)
	}
	if models[0].IsRemote() || !models[1].IsRemote() || models[1].RemoteModel != "upstream" || models[1].RemoteHost != "https://remote.invalid" {
		t.Fatalf("inventory changed remote provenance: %+v", models)
	}
	shown, err := c.Show(context.Background(), "cloud")
	if err != nil || shown.RemoteModel != models[1].RemoteModel || shown.RemoteHost != models[1].RemoteHost || !shown.IsRemote() {
		t.Fatalf("Show = %+v, %v", shown, err)
	}
	for _, m := range []ModelInfo{{RemoteModel: "upstream"}, {RemoteHost: "remote"}, {RemoteModel: " "}} {
		if !m.IsRemote() {
			t.Fatalf("explicit marker lost: %+v", m)
		}
	}
}

func TestRemoteFieldsPreserveLegacyModelJSON(t *testing.T) {
	const legacy = `{"name":"local","size":12,"digest":"sha256:abc","capabilities":["completion"],"details":{"parameter_size":"","quantization_level":"","family":""}}`
	var m ModelInfo
	if err := json.Unmarshal([]byte(legacy), &m); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(m)
	if err != nil || string(b) != legacy {
		t.Fatalf("legacy bytes changed: %s, %v", b, err)
	}
}

func TestGenerateRejectsRemoteMarkerInEveryFrame(t *testing.T) {
	const localChunk = `{"response":"local-prefix"}` + "\n"
	const localFinal = `{"response":"ok","done":true,"eval_count":2,"eval_duration":1000000}` + "\n"
	for _, field := range []string{"remote_model", "remote_host"} {
		remoteChunk := fmt.Sprintf(`{"%s":"remote","response":"remote-output"}`+"\n", field)
		remoteFinal := fmt.Sprintf(`{"%s":"remote","response":"remote-output","done":true,"eval_count":50,"eval_duration":1}`+"\n", field)
		for _, tc := range []struct{ name, body string }{
			{"first", remoteChunk + localFinal},
			{"middle", localChunk + remoteChunk + localFinal},
			{"terminal", localChunk + remoteFinal},
			{"after terminal", localFinal + remoteChunk},
		} {
			t.Run(field+"/"+tc.name, func(t *testing.T) {
				c, calls := chatServer(t, tc.body)
				out, metrics, err := c.Generate(context.Background(), "m", "hi", Deterministic(64, 4096))
				if !errors.Is(err, ErrRemoteExecution) || out != "" || metrics != (Metrics{}) {
					t.Fatalf("remote output accepted: %q, %+v, %v", out, metrics, err)
				}
				if *calls != 1 {
					t.Fatalf("remote reply retried: %d calls", *calls)
				}
			})
		}
	}
}

func TestChatRejectsRemoteBeforeEmptyReplyRetry(t *testing.T) {
	for _, field := range []string{"remote_model", "remote_host"} {
		for _, reply := range []string{emptyNonTerminal, goodReply, `{"done":false,"eval_count":2}`} {
			t.Run(field+reply, func(t *testing.T) {
				body := fmt.Sprintf(`{"%s":"remote",`, field) + reply[1:]
				c, calls := chatServer(t, body, goodReply)
				message, metrics, err := c.Chat(context.Background(), "m", []Message{{Role: "user", Content: "hi"}}, nil, Deterministic(64, 4096))
				if !errors.Is(err, ErrRemoteExecution) || !reflect.DeepEqual(message, Message{}) || metrics != (Metrics{}) {
					t.Fatalf("remote chat accepted: %+v, %+v, %v", message, metrics, err)
				}
				if *calls != 1 {
					t.Fatalf("remote reply retried: %d calls", *calls)
				}
			})
		}
	}
}

func TestGenerateLocalReplyKeepsTextAndServerMetrics(t *testing.T) {
	c, calls := chatServer(t, `{"response":"ok","done":true,"eval_count":2,"eval_duration":1000000,"prompt_eval_count":3,"prompt_eval_duration":1000000}`+"\n")
	out, metrics, err := c.Generate(context.Background(), "local", "hi", Deterministic(64, 4096))
	if err != nil || out != "ok" || metrics.EvalCount != 2 || metrics.PromptTokens != 3 || metrics.DecodeTPS != 2000 || metrics.ClientDerived || *calls != 1 {
		t.Fatalf("local reply changed: %q, %+v, %v, calls=%d", out, metrics, err, *calls)
	}
}

func TestRemoteMarkerCaseAliasesCannotEraseProvenance(t *testing.T) {
	for _, field := range []string{"remote_model", "remote_host"} {
		for _, reverse := range []bool{false, true} {
			positive := fmt.Sprintf(`"%s":"remote"`, field)
			empty := fmt.Sprintf(`"%s":""`, strings.ToUpper(field))
			markers := positive + "," + empty
			if reverse {
				markers = empty + "," + positive
			}
			for _, surface := range []string{"tags", "show", "generate", "chat"} {
				t.Run(fmt.Sprintf("%s/%s/reverse=%v", surface, field, reverse), func(t *testing.T) {
					body := "{" + markers + `,"name":"m","response":"remote text","message":{"role":"assistant","content":"remote text"},"done":true,"eval_count":5,"eval_duration":1}`
					if surface == "tags" {
						body = `{"models":[` + body + `]}`
					}
					c, _ := chatServer(t, body)
					assertRemoteSurface(t, c, surface)
				})
			}
		}
	}
}

func assertRemoteSurface(t *testing.T, c *Client, surface string) {
	t.Helper()
	switch surface {
	case "tags":
		models, err := c.Tags(context.Background())
		if err != nil || len(models) != 1 || !models[0].IsRemote() {
			t.Fatalf("remote tags erased: %+v, %v", models, err)
		}
	case "show":
		m, err := c.Show(context.Background(), "m")
		if err != nil || !m.IsRemote() {
			t.Fatalf("remote show erased: %+v, %v", m, err)
		}
	case "generate":
		text, metrics, err := c.Generate(context.Background(), "m", "hi", Deterministic(8, 4096))
		if !errors.Is(err, ErrRemoteExecution) || text != "" || metrics != (Metrics{}) {
			t.Fatalf("remote generate erased: %q, %+v, %v", text, metrics, err)
		}
	case "chat":
		message, metrics, err := c.Chat(context.Background(), "m", nil, nil, Deterministic(8, 4096))
		if !errors.Is(err, ErrRemoteExecution) || !reflect.DeepEqual(message, Message{}) || metrics != (Metrics{}) {
			t.Fatalf("remote chat erased: %+v, %+v, %v", message, metrics, err)
		}
	}
}

func TestRemoteModelDecodeKeepsMarkerWhenOtherMetadataIsInvalid(t *testing.T) {
	for _, body := range []string{
		`{"remote_model":"cloud","size":"bad"}`,
		`{"size":"bad","remote_model":"cloud"}`,
		`{"capabilities":false,"remote_host":"remote"}`,
	} {
		c, _ := chatServer(t, body)
		m, err := c.Show(context.Background(), "m")
		if err == nil || !m.IsRemote() {
			t.Fatalf("partial metadata lost locality: %+v, %v", m, err)
		}
	}
}

func TestMalformedRemoteMarkerCannotBecomeUnavailableMetadataFallback(t *testing.T) {
	for _, body := range []string{
		`{"remote_model":123,"remote_host":"cloud"}`,
		`{"remote_host":false,"remote_model":"cloud"}`,
		`{"remote_model":123,"REMOTE_MODEL":"cloud"}`,
		`{"REMOTE_HOST":[],"remote_host":"cloud"}`,
		`{"remote_model":"cloud","remote_host":123}`,
	} {
		c, _ := chatServer(t, body)
		m, err := c.Show(context.Background(), "m")
		if !errors.Is(err, ErrInvalidRemoteMetadata) {
			t.Fatalf("invalid provenance became generic fallback: %+v, %v", m, err)
		}
		text, metrics, err := c.Generate(context.Background(), "m", "hi", Deterministic(8, 4096))
		if (!errors.Is(err, ErrInvalidRemoteMetadata) && !errors.Is(err, ErrRemoteExecution)) || text != "" || metrics != (Metrics{}) {
			t.Fatalf("invalid provenance accepted: %q, %+v, %v", text, metrics, err)
		}
	}
}
