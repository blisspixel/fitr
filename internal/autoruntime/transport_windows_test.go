//go:build windows

package autoruntime

import (
	"context"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/ollama"
)

func TestOwnedTransportAllowsColdLoadWithoutRelaxingMetadataDeadline(t *testing.T) {
	spec := fixtureSpec(t)
	spec.NumCtx = 517
	p, err := Prepare(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Start(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	transport := r.client.Transport.(*ownedTransport)
	transport.base.ResponseHeaderTimeout = 5 * time.Millisecond
	transport.inference.ResponseHeaderTimeout = time.Second
	attempts := 0
	client := &ollama.Client{BaseURL: r.URL(), HTTP: r.HTTPClient(), Admission: func(context.Context, ollama.InferenceRequest) (ollama.InferencePermit, error) {
		attempts++
		return ollama.InferencePermit{Deadline: time.Now().Add(2 * time.Second)}, nil
	}}
	text, _, err := client.Generate(t.Context(), "fixture", "probe", ollama.Sampling{NumPredict: 1})
	if err != nil || text != "fixture" || attempts != 1 {
		t.Fatalf("cold-load request failed or repeated: %q %d %v", text, attempts, err)
	}
	if _, err := r.ModelConfiguration(t.Context(), "fixture"); err == nil {
		t.Fatal("metadata inherited the long cold-load allowance")
	}
}
