package autoruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/ollama"
)

const AllocationObservationTimeout = time.Second

// BeginLoadObservation excludes completed rows and partial rows from earlier
// points. It does not reset the process's cloud-disabled readiness observation.
func (r *Runtime) BeginLoadObservation() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs.beginPoint()
}

func (r *Runtime) Accel(ctx context.Context) string {
	if ctx.Err() != nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ""
	}
	backend, _, err := r.logs.allocation()
	if err != nil {
		return ""
	}
	return backend
}

// AllocationSequence counts complete allocation rows, including unknown rows.
// It observes pipe consumption, not an ordering guarantee against HTTP replies.
func (r *Runtime) AllocationSequence() uint64 {
	_, sequence, _ := r.logs.allocation()
	return sequence
}

// WaitAccel waits briefly for the current point's expected allocation family.
// CPU metadata rows may arrive before GPU allocation rows. The current owned
// profile supports host CPU or a single NVIDIA accelerator, requiring CUDA.
// Warm requests may reuse this point observation. The caller must independently
// validate current model/context/residency; this is not proof of every reload.
func (r *Runtime) WaitAccel(ctx context.Context, expected string) (string, error) {
	if expected != "cpu" && expected != "cuda" {
		return "", errors.Join(ollama.ErrUnverifiedLocalExecution, errors.New("expected owned compute backend must be cpu or cuda"))
	}
	ctx, cancel := context.WithTimeout(ctx, AllocationObservationTimeout)
	defer cancel()
	timer := time.NewTicker(10 * time.Millisecond)
	defer timer.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return "", errors.Join(ollama.ErrUnverifiedLocalExecution, err)
		}
		r.mu.Lock()
		closed := r.closed
		backend, _, err := r.logs.allocation()
		r.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return "", errors.Join(ollama.ErrUnverifiedLocalExecution, err)
		}
		if closed {
			return "", errors.Join(ollama.ErrUnverifiedLocalExecution, ErrOwnershipLost)
		}
		if err != nil {
			return "", errors.Join(ollama.ErrUnverifiedLocalExecution, err)
		}
		if backend == expected {
			return backend, nil
		}
		if backend != "" && backend != "cpu" {
			return "", errors.Join(ollama.ErrUnverifiedLocalExecution, fmt.Errorf("owned allocation backend %q differs from expected %q", backend, expected))
		}
		select {
		case <-ctx.Done():
			return "", errors.Join(ollama.ErrUnverifiedLocalExecution, ctx.Err())
		case <-timer.C:
		}
	}
}

type allocationFacts struct {
	cpu     bool
	backend string
	invalid bool
	failure string
}

func (facts *allocationFacts) observe(line string) bool {
	lower := strings.ToLower(line)
	if (!strings.Contains(lower, "load_tensors") || !strings.Contains(lower, "model buffer size")) &&
		!strings.Contains(lower, `msg="model weights"`) {
		return false
	}
	name := allocationDevice(lower)
	backend := normalizeAllocationDevice(name)
	switch {
	case backend == "":
		facts.reject("unknown device", name, line)
	case backend == "cpu":
		facts.cpu = true
	case facts.backend != "" && facts.backend != backend:
		facts.reject("mixed devices", name, line)
	default:
		facts.backend = backend
	}
	return true
}

// Retain only the first rejected allocation row, never prompts or a full log.
// Quoting escapes controls, and byte limits bound retention and error output.
func (facts *allocationFacts) reject(reason, name, line string) {
	if facts.invalid {
		return
	}
	facts.invalid = true
	facts.failure = fmt.Sprintf("%s: device=%q previous_backend=%q allocation_row=%q",
		reason, boundedAllocationText(name, 128), facts.backend, boundedAllocationText(line, 512))
}

func boundedAllocationText(text string, maximum int) string {
	if len(text) <= maximum {
		return text
	}
	return text[:maximum] + "..."
}

func (facts allocationFacts) accel() string {
	if facts.invalid {
		return ""
	}
	if facts.backend != "" {
		return facts.backend
	}
	if facts.cpu {
		return "cpu"
	}
	return ""
}

func allocationDevice(line string) string {
	if before, _, ok := strings.Cut(line, " model buffer size"); ok {
		words := strings.Fields(before)
		if len(words) > 0 {
			return words[len(words)-1]
		}
	}
	_, after, ok := strings.Cut(line, " device=")
	if !ok {
		return ""
	}
	if strings.HasPrefix(after, `"`) {
		name, _, _ := strings.Cut(after[1:], `"`)
		return name
	}
	name, _, _ := strings.Cut(after, " ")
	return name
}

func normalizeAllocationDevice(name string) string {
	if name == "cpu" || name == "cpu_mapped" {
		return "cpu"
	}
	// CUDA_Host uses cudaMallocHost and the CPU buffer interface. It does not
	// establish CUDA device execution; current residency is checked separately.
	// https://github.com/ggml-org/llama.cpp/blob/b10700/ggml/src/ggml-cuda/ggml-cuda.cu#L1155-L1206
	if name == "cuda_host" {
		return "cpu"
	}
	for _, prefix := range []string{"cuda", "rocm", "hip", "vulkan", "metal", "sycl", "opencl"} {
		if suffix, ok := strings.CutPrefix(name, prefix); ok {
			if suffix == "" || onlyDigits(suffix) {
				return device.NormalizeAccel(prefix)
			}
		}
	}
	return ""
}

func onlyDigits(text string) bool {
	for _, r := range text {
		if r < '0' || r > '9' {
			return false
		}
	}
	return text != ""
}

func accelerationFromLoads(logs string) string {
	var facts allocationFacts
	for _, line := range strings.Split(logs, "\n") {
		facts.observe(line)
	}
	return facts.accel()
}
