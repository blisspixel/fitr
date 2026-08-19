// Package llm defines the serving-backend interface the harness measures
// through.
//
// The measurement layer must not know which server it is talking to: a task
// prompts, times, and grades; where the tokens come from is a detail. The
// interface reuses the ollama package's wire types - they are the shapes the
// whole harness already speaks - and each backend adapts its own API to them.
//
// Roughly 10 of 12 relevant local runtimes are OpenAI-shaped; Ollama is the
// notable exception. One native Ollama client plus adapters over the
// OpenAI-compatible surface covers effectively everyone.
package llm

import (
	"context"

	"github.com/blisspixel/fitr/internal/ollama"
)

// Backend is everything the harness needs from a serving runtime.
type Backend interface {
	// Name identifies the runtime kind: "ollama", "llama-server".
	Name() string
	// URL is the base endpoint, for error messages.
	URL() string
	// Version is the runtime's own version string, recorded in the device
	// fingerprint - a runtime upgrade is a different measurement.
	Version(ctx context.Context) string
	Reachable(ctx context.Context) bool

	Generate(ctx context.Context, model, prompt string, s ollama.Sampling) (string, ollama.Metrics, error)
	Chat(ctx context.Context, model string, msgs []ollama.Message, tools []ollama.Tool, s ollama.Sampling) (ollama.Message, error)

	Tags(ctx context.Context) ([]ollama.ModelInfo, error)
	Show(ctx context.Context, model string) (ollama.ModelInfo, error)
	PS(ctx context.Context) ([]ollama.RunningModel, error)

	// StopAll unloads every resident model and returns any that would not go.
	// A single-model server satisfies this trivially: the one-model-resident
	// invariant holds by construction, and it returns (nil, nil).
	StopAll(ctx context.Context) ([]string, error)
}

var _ Backend = (*ollama.Client)(nil)
