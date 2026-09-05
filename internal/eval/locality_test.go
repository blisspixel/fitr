package eval

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/blisspixel/fitr/internal/ollama"
)

type remoteShownBackend struct{ fakeBackend }

func (*remoteShownBackend) Show(context.Context, string) (ollama.ModelInfo, error) {
	return ollama.ModelInfo{RemoteHost: "remote", Capabilities: []string{"tools"}}, errors.New("unrelated malformed metadata")
}

func TestPlumbingRejectsNewRemoteMetadataBeforeInference(t *testing.T) {
	b := &remoteShownBackend{}
	r, err := RunPlumbing(context.Background(), b, "model", PlumbingSpec{})
	if !errors.Is(err, ollama.ErrRemoteExecution) || r.Outcome != OutcomeError || r.Healthy || b.chatCalls != 0 || b.generateCalls != 0 {
		t.Fatalf("remote plumbing accepted: %+v, %v, chat=%d generate=%d", r, err, b.chatCalls, b.generateCalls)
	}
}

func TestDoctorStopsAtEveryRemoteResponseWithoutHealthClaim(t *testing.T) {
	// Cold probe, context probe, two text repeats, then two JSON repeats.
	for at := 1; at <= 6; at++ {
		for _, cause := range []error{ollama.ErrRemoteExecution, ollama.ErrInvalidRemoteMetadata} {
			t.Run(fmt.Sprintf("request %d/%v", at, cause), func(t *testing.T) {
				b := &fakeBackend{generateErrAt: map[int]error{at: fmt.Errorf("remote frame: %w", cause)}}
				r, err := RunDoctor(context.Background(), b, "model", 2, DoctorOpts{})
				if !errors.Is(err, cause) || r.Healthy || b.generateCalls != at {
					t.Fatalf("remote doctor accepted or continued: %+v, %v, calls=%d", r, err, b.generateCalls)
				}
			})
		}
	}
}

type remoteMemoryBackend struct {
	fakeBackend
	stopCalls int
	tagErr    error
}

func (b *remoteMemoryBackend) Tags(context.Context) ([]ollama.ModelInfo, error) {
	return []ollama.ModelInfo{{Name: "model:latest", Size: 1 << 30, RemoteModel: "cloud"}}, b.tagErr
}

func (b *remoteMemoryBackend) StopAll(context.Context) ([]string, error) {
	b.stopCalls++
	return nil, nil
}

func TestMemoryRejectsRefreshedRemoteTagBeforeUnloadAndInference(t *testing.T) {
	for _, model := range []string{"model", "model:latest"} {
		b := &remoteMemoryBackend{}
		r, err := RunMemory(context.Background(), b, model, 4096)
		if !errors.Is(err, ollama.ErrRemoteExecution) || r.Outcome != OutcomeError || r.DiskGB != 0 || b.stopCalls != 0 || b.generateCalls != 0 {
			t.Fatalf("remote memory accepted: %+v, %v, stop=%d generate=%d", r, err, b.stopCalls, b.generateCalls)
		}
	}
	b := &remoteMemoryBackend{tagErr: ollama.ErrInvalidRemoteMetadata}
	if _, err := RunMemory(context.Background(), b, "model", 4096); !errors.Is(err, ollama.ErrInvalidRemoteMetadata) || b.stopCalls != 0 || b.generateCalls != 0 {
		t.Fatalf("invalid remote metadata allowed memory load: %v", err)
	}
}
