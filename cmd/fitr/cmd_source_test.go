package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/source"
)

type sourceTestTransport func(*http.Request) (*http.Response, error)

func sourceTestDirectory(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func (transport sourceTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestSourceRejectsInvalidInputsBeforeNetwork(t *testing.T) {
	destination := filepath.Join(sourceTestDirectory(t), "receipt.json")
	called := false
	resolve := func(context.Context, source.HFRequest) (source.Resolution, error) {
		called = true
		return source.Resolution{}, errors.New("unexpected network")
	}
	for _, args := range [][]string{
		{"unknown"}, {"show"}, {"resolve", "other"},
		{"resolve", "hf", "--repo", "owner/model", "--file", "model.gguf", "--out", destination},
		{"resolve", "hf", "--repo", "owner/model", "--revision", "main", "--file", "../model.gguf", "--out", destination},
		{"resolve", "hf", "--repo", "owner/model", "--revision", "main", "--file", "model.gguf", "--out", destination, "--display", "invalid"},
	} {
		_, code := captureTopStderr(t, func() int { return cmdSourceWithResolver(context.Background(), args, resolve) })
		if code != exitUsage || called {
			t.Fatalf("invalid source request invoked resolution: args=%v code=%d called=%v", args, code, called)
		}
	}
	if err := os.WriteFile(destination, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"resolve", "hf", "--repo", "owner/model", "--revision", "main", "--file", "model.gguf", "--out", destination}
	_, code := captureTopStderr(t, func() int { return cmdSourceWithResolver(context.Background(), args, resolve) })
	if code != exitError || called {
		t.Fatal("existing destination was not rejected before network")
	}
	data, _ := os.ReadFile(destination)
	if string(data) != "keep" {
		t.Fatal("existing receipt was overwritten")
	}
}

func TestSourceCLIResolvesAndReopensOnlyMetadata(t *testing.T) {
	destination := filepath.Join(sourceTestDirectory(t), "receipt.json")
	var requested []string
	metadata := fmt.Sprintf(`{"id":"owner/model","sha":%q,"siblings":[{"rfilename":"model.gguf","size":1024,"blobId":%q,"lfs":{"size":1024,"sha256":%q,"pointerSize":128}}]}`,
		strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 64))
	resolver := source.NewResolver(sourceTestTransport(func(request *http.Request) (*http.Response, error) {
		requested = append(requested, request.URL.Path)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(metadata))}, nil
	}))
	args := []string{"resolve", "--repo", "owner/model", "hf", "--revision", "main", "--file", "model.gguf", "--out", destination, "--display", "json"}
	output, code := captureTopStdout(t, func() int { return cmdSourceWithResolver(context.Background(), args, resolver.ResolveHF) })
	if code != exitOK || !strings.Contains(output, source.ResolutionSchema) {
		t.Fatalf("source resolve: code=%d output=%s", code, output)
	}
	want := []string{"/api/models/owner/model/revision/main", "/api/models/owner/model/revision/" + strings.Repeat("a", 40)}
	if !reflect.DeepEqual(requested, want) {
		t.Fatalf("unexpected requests, possible artifact download: %v", requested)
	}
	for _, mode := range []string{"json", "plain", "none"} {
		output, code = captureTopStdout(t, func() int { return cmdSource(context.Background(), []string{"show", destination, "--display", mode}) })
		if code != exitOK || (mode == "none" && output != "") {
			t.Fatalf("source show %s: code=%d output=%s", mode, code, output)
		}
	}
	t.Run("output failure", func(t *testing.T) {
		closed, err := os.CreateTemp(t.TempDir(), "closed-stdout")
		if err != nil {
			t.Fatal(err)
		}
		if err := closed.Close(); err != nil {
			t.Fatal(err)
		}
		previous := os.Stdout
		os.Stdout = closed
		t.Cleanup(func() { os.Stdout = previous })
		for _, mode := range []string{"plain", "rich", "auto", "json"} {
			code := cmdSource(context.Background(), []string{"show", destination, "--display", mode})
			if code != exitError {
				t.Fatalf("failed %s output reported exit %d", mode, code)
			}
		}
		if code := cmdSource(context.Background(), []string{"show", destination, "--display", "none"}); code != exitOK {
			t.Fatalf("silent validation unnecessarily wrote output: exit %d", code)
		}
	})
}

func TestSourceHelpAndRepeatedFileBounds(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"resolve", "--help"}, {"show", "--help"}} {
		_, code := captureTopStderr(t, func() int { return cmdSource(context.Background(), args) })
		if code != exitOK {
			t.Fatalf("help failed: %v", args)
		}
	}
	var files sourceFiles
	for index := range source.MaxFiles {
		if err := files.Set(fmt.Sprintf("file-%d.gguf", index)); err != nil {
			t.Fatal(err)
		}
	}
	if err := files.Set("extra.gguf"); err == nil || !strings.Contains(files.String(), "file-0.gguf") {
		t.Fatal("repeated file bound or display lost")
	}
}

func TestSourceCancellationDoesNotContactProviderOrPublish(t *testing.T) {
	destination := filepath.Join(sourceTestDirectory(t), "cancelled.json")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resolver := source.NewResolver(sourceTestTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("cancelled command contacted provider")
		return nil, errors.New("unexpected request")
	}))
	args := []string{"resolve", "hf", "--repo", "owner/model", "--revision", "main", "--file", "model.gguf", "--out", destination}
	_, code := captureTopStderr(t, func() int { return cmdSourceWithResolver(ctx, args, resolver.ResolveHF) })
	if code != exitInterrupt {
		t.Fatalf("cancelled source resolution exited %d", code)
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled resolution published output: %v", err)
	}
}
