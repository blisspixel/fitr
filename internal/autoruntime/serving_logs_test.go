package autoruntime

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/ollama"
)

func writeServing(t *testing.T, writer io.Writer, text string) {
	t.Helper()
	n, err := io.WriteString(writer, text)
	if err != nil || n != len(text) {
		t.Fatalf("log drain returned %d/%d: %v", n, len(text), err)
	}
}

func TestServingLogsLongStreamKeepsBoundedFacts(t *testing.T) {
	logs := &servingLogs{}
	out := logs.writer(0)
	writeServing(t, out, "Ollama cloud disabled: true\n")
	for range 40 {
		writeServing(t, out, strings.Repeat("ordinary diagnostic row\n", 2048))
	}
	writeServing(t, out, "load_tensors: CUDA0 model buffer size = 4096 MiB\n")
	cloud, err := logs.status()
	backend, sequence, allocationErr := logs.allocation()
	if !cloud || err != nil || backend != "cuda" || sequence != 1 || allocationErr != nil {
		t.Fatalf("long stream lost facts: %v %v %q %d %v", cloud, err, backend, sequence, allocationErr)
	}
	if len(logs.tail) != MaximumServingTailBytes || cap(logs.tail) > 2*MaximumServingTailBytes {
		t.Fatalf("diagnostic tail is not bounded: len=%d cap=%d", len(logs.tail), cap(logs.tail))
	}
	if strings.Contains(string(logs.tail), "cloud disabled") {
		t.Fatal("readiness was retained only by keeping the old diagnostic row")
	}
}

func TestServingLogsSeparateChannelsAndChunkedRows(t *testing.T) {
	logs := &servingLogs{}
	out, stderr := logs.writer(0), logs.writer(1)
	writeServing(t, out, "Ollama cloud disabled: ")
	writeServing(t, stderr, "true\n")
	if cloud, _ := logs.status(); cloud {
		t.Fatal("combined fragments from different streams into readiness")
	}
	writeServing(t, out, "true\r\nload_tensors: CU")
	writeServing(t, stderr, "DA0 model buffer size = 4096 MiB\n")
	if backend, _, err := logs.allocation(); backend != "" || err != nil {
		t.Fatalf("combined unrelated allocation fragments: %q %v", backend, err)
	}
	writeServing(t, out, "DA0 model buffer size = 4096 MiB\r\n")
	if cloud, err := logs.status(); !cloud || err != nil {
		t.Fatalf("split readiness row not recognized: %v %v", cloud, err)
	}
	if backend, sequence, err := logs.allocation(); backend != "cuda" || sequence != 1 || err != nil {
		t.Fatalf("split allocation row not recognized: %q %d %v", backend, sequence, err)
	}
}

func TestServingLogsFreshPointDiscardsPartialRows(t *testing.T) {
	r := &Runtime{logs: &servingLogs{}}
	out := r.logs.writer(0)
	writeServing(t, out, "Ollama cloud disabled: true\nload_tensors: CUDA0 model buffer size = 4096 MiB\n")
	writeServing(t, out, "load_tensors: Vul")
	r.BeginLoadObservation()
	writeServing(t, out, "kan0 model buffer size = 4096 MiB\n")
	if r.Accel(t.Context()) != "" || r.AllocationSequence() != 1 {
		t.Fatal("earlier complete or partial row transferred into a new point")
	}
	writeServing(t, out, `msg="model weights" device=CPU size="4 GiB"`+"\n")
	if r.Accel(t.Context()) != "cpu" || r.AllocationSequence() != 2 {
		t.Fatal("new point's CPU allocation was not retained")
	}
	if cloud, err := r.logs.status(); !cloud || err != nil {
		t.Fatalf("point reset changed readiness: %v %v", cloud, err)
	}
}

func TestServingLogsUnknownMixedAndLookalikeDevices(t *testing.T) {
	for _, rows := range []string{
		"load_tensors: CPU_Mapped model buffer size = 10 MiB\nload_tensors: Novel0 model buffer size = 4096 MiB\n",
		"load_tensors: CUDA0 model buffer size = 4096 MiB\nload_tensors: Vulkan0 model buffer size = 4096 MiB\n",
		`msg="model weights" device=novel0 size="4 GiB" note="cuda"` + "\n",
		`msg="model weights" device=CUDAunexpected size="4 GiB"` + "\n",
		`msg="model weights" device=CPU_GPU size="4 GiB"` + "\n",
	} {
		logs := &servingLogs{}
		writeServing(t, logs.writer(0), rows)
		if backend, _, err := logs.allocation(); backend != "" || err == nil {
			t.Errorf("accepted unknown or mixed allocation %q: %q %v", rows, backend, err)
		}
		writeServing(t, logs.writer(0), "load_tensors: CUDA0 model buffer size = 4096 MiB\n")
		if backend, _, err := logs.allocation(); backend != "" || err == nil {
			t.Fatal("later recognized backend erased an invalid point")
		}
	}
	for _, name := range []string{"CUDA0", "ROCm1", "HIP0", "Vulkan2", "Metal", "SYCL0", "OpenCL1", "CPU_Mapped", "CPU"} {
		logs := &servingLogs{}
		writeServing(t, logs.writer(0), `msg="model weights" device="`+name+`" size="4 GiB"`+"\n")
		if backend, _, err := logs.allocation(); backend == "" || err != nil {
			t.Errorf("supported device %q was not recognized: %q %v", name, backend, err)
		}
	}
}

func TestServingLogsOversizedLineFailsClosedAndKeepsDraining(t *testing.T) {
	for _, chunks := range [][]string{
		{strings.Repeat("x", MaximumServingLineBytes+1)},
		{strings.Repeat("x", MaximumServingLineBytes), "x\n"},
		{"load_tensors: " + strings.Repeat("x", MaximumServingLineBytes), "CUDA0 model buffer size = 4096 MiB\n"},
	} {
		logs := &servingLogs{}
		for _, chunk := range chunks {
			writeServing(t, logs.writer(0), chunk)
		}
		writeServing(t, logs.writer(1), "load_tensors: CUDA0 model buffer size = 4096 MiB\n")
		logs.beginPoint()
		if _, err := logs.status(); err == nil {
			t.Fatal("oversized line fault was absent or reset")
		}
		if backend, _, err := logs.allocation(); backend != "" || err == nil {
			t.Fatalf("oversized line allowed allocation authority: %q %v", backend, err)
		}
		for _, line := range logs.channels {
			if len(line.data) > MaximumServingLineBytes || cap(line.data) > 2*MaximumServingLineBytes {
				t.Fatal("partial line retention exceeded its bound")
			}
		}
	}
	logs := &servingLogs{}
	writeServing(t, logs.writer(0), strings.Repeat("x", MaximumServingLineBytes)+"\n")
	if _, err := logs.status(); err != nil {
		t.Fatal("exact line limit must be allowed", err)
	}
}

func TestServingLogWritersConcurrentWithoutMixedRows(t *testing.T) {
	logs := &servingLogs{}
	var group sync.WaitGroup
	for channel := range 2 {
		group.Go(func() {
			for range 100 {
				_, _ = logs.writer(channel).Write([]byte("load_tensors: CU"))
				_, _ = logs.writer(channel).Write([]byte("DA0 model buffer size = 4096 MiB\n"))
			}
		})
	}
	group.Wait()
	if backend, sequence, err := logs.allocation(); backend != "cuda" || sequence != 200 || err != nil {
		t.Fatalf("channel assembly changed under concurrent writes: %q %d %v", backend, sequence, err)
	}
}

func TestWaitAccelUsesBoundedCurrentPointEvidence(t *testing.T) {
	r := &Runtime{logs: &servingLogs{}}
	go func() {
		time.Sleep(15 * time.Millisecond)
		_, _ = r.logs.writer(0).Write([]byte("load_tensors: CUDA0 model buffer size = 4096 MiB\n"))
	}()
	if backend, err := r.WaitAccel(t.Context(), "cuda"); backend != "cuda" || err != nil {
		t.Fatalf("did not wait for observed allocation: %q %v", backend, err)
	}
	r.BeginLoadObservation()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if backend, err := r.WaitAccel(ctx, "cuda"); backend != "" || !errors.Is(err, context.DeadlineExceeded) || !ollama.IsLocalityError(err) {
		t.Fatalf("missing fresh point facts did not expire: %q %v", backend, err)
	}
	writeServing(t, r.logs.writer(1), "load_tensors: Unknown0 model buffer size = 4096 MiB\n")
	if backend, err := r.WaitAccel(t.Context(), "cuda"); backend != "" || !ollama.IsLocalityError(err) {
		t.Fatalf("unknown backend did not fail immediately: %q %v", backend, err)
	}
	r.BeginLoadObservation()
	r.closed = true
	if backend, err := r.WaitAccel(t.Context(), "cuda"); backend != "" || !errors.Is(err, ErrOwnershipLost) {
		t.Fatalf("closed process retained authority: %q %v", backend, err)
	}
}

func TestVersionLogBufferStillHasTotalByteBound(t *testing.T) {
	logs := &logBuffer{}
	n, err := logs.Write([]byte(strings.Repeat("x", MaxLogBytes)))
	if n != MaxLogBytes || err != nil {
		t.Fatalf("exact version bound rejected: %d %v", n, err)
	}
	n, err = logs.Write([]byte("x"))
	if n != 0 || err == nil {
		t.Fatalf("version overflow accepted: %d %v", n, err)
	}
	text, overflow := logs.snapshot()
	if len(text) != MaxLogBytes || !overflow {
		t.Fatal("version output limit changed")
	}
}

func TestWaitAccelDoesNotAcceptCPUMetadataBeforeExpectedCUDA(t *testing.T) {
	r := &Runtime{logs: &servingLogs{}}
	writeServing(t, r.logs.writer(0), "load_tensors: CPU_Mapped model buffer size = 10 MiB\n")
	go func() {
		time.Sleep(15 * time.Millisecond)
		_, _ = r.logs.writer(1).Write([]byte("load_tensors: CUDA0 model buffer size = 4096 MiB\n"))
	}()
	if backend, err := r.WaitAccel(t.Context(), "cuda"); backend != "cuda" || err != nil || r.AllocationSequence() != 2 {
		t.Fatalf("early CPU metadata ended the GPU observation: %q %v", backend, err)
	}
	r.BeginLoadObservation()
	writeServing(t, r.logs.writer(0), "load_tensors: CPU_Mapped model buffer size = 4096 MiB\n")
	if backend, err := r.WaitAccel(t.Context(), "cpu"); backend != "cpu" || err != nil {
		t.Fatalf("expected host allocation was not recognized: %q %v", backend, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if backend, err := r.WaitAccel(ctx, "cuda"); backend != "" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CPU-only observation qualified an accelerator: %q %v", backend, err)
	}
	if backend, err := r.WaitAccel(t.Context(), "CUDA"); backend != "" || !ollama.IsLocalityError(err) {
		t.Fatalf("noncanonical expected family accepted: %q %v", backend, err)
	}
	r.BeginLoadObservation()
	writeServing(t, r.logs.writer(1), "load_tensors: Vulkan0 model buffer size = 4096 MiB\n")
	if backend, err := r.WaitAccel(t.Context(), "cuda"); backend != "" || !ollama.IsLocalityError(err) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wrong recognized accelerator waited or qualified: %q %v", backend, err)
	}
	r.BeginLoadObservation()
	writeServing(t, r.logs.writer(0), "load_tensors: CPU_Mapped model buffer size = 4096 MiB\n")
	writeServing(t, r.logs.writer(1), "load_tensors: Unknown0 model buffer size = 4096 MiB\n")
	if backend, err := r.WaitAccel(t.Context(), "cpu"); backend != "" || err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("invalid allocation waited or reused the expected CPU observation: %q %v", backend, err)
	}
}

func TestServingAllocationDiagnosticPreservesOnlyFirstFailure(t *testing.T) {
	logs := &servingLogs{}
	out := logs.writer(0)
	writeServing(t, out, "private unrelated diagnostic must stay out\n")
	first := "load_tensors: Novel0 model buffer size = 4096 MiB"
	writeServing(t, out, first+"\n")
	_, _, err := logs.allocation()
	if err == nil || !strings.Contains(err.Error(), `device="novel0"`) ||
		!strings.Contains(err.Error(), `allocation_row="`+first+`"`) {
		t.Fatalf("missing exact first rejected allocation: %v", err)
	}
	original := err.Error()
	writeServing(t, out, "load_tensors: Other0 model buffer size = 4096 MiB\n")
	_, _, err = logs.allocation()
	if err == nil || err.Error() != original || strings.Contains(err.Error(), "private unrelated") {
		t.Fatalf("diagnostic changed or included unrelated logs: %v", err)
	}
	logs.beginPoint()
	writeServing(t, out, "load_tensors: CUDA0 model buffer size = 4096 MiB\n")
	writeServing(t, out, "load_tensors: Vulkan0 model buffer size = 4096 MiB\n")
	_, _, err = logs.allocation()
	if err == nil || !strings.Contains(err.Error(), "mixed devices") ||
		!strings.Contains(err.Error(), `previous_backend="cuda"`) || strings.Contains(err.Error(), "Novel0") {
		t.Fatalf("mixed diagnostic did not preserve the new point boundary: %v", err)
	}
}

func TestServingAllocationDiagnosticQuotesAndBoundsText(t *testing.T) {
	logs := &servingLogs{}
	row := "load_tensors: " + strings.Repeat("\x1b", 800) + " model buffer size = 4096 MiB"
	writeServing(t, logs.writer(0), row+"\n")
	_, _, err := logs.allocation()
	if err == nil || !strings.Contains(err.Error(), `\x1b`) ||
		strings.ContainsRune(err.Error(), '\x1b') || len(err.Error()) > 4096 {
		t.Fatalf("diagnostic is absent, unescaped or too large: %v", err)
	}
	if strings.Contains(logs.facts.failure, "4096 MiB") {
		t.Fatal("diagnostic retained text beyond the bounded allocation prefix")
	}
	// A later recognized row cannot erase the first failure or qualify it.
	writeServing(t, logs.writer(0), "load_tensors: CUDA0 model buffer size = 4096 MiB\n")
	if backend, _, err := logs.allocation(); backend != "" || err == nil {
		t.Fatalf("diagnostic collection relaxed allocation rejection: %q %v", backend, err)
	}
}

func TestServingCUDAHostIsHostAllocationAndNeedsActualCUDADevice(t *testing.T) {
	// Exact native Ollama 0.33.3 row. Pinned upstream semantics are cited in
	// normalizeAllocationDevice; a host buffer alone must not establish CUDA.
	const host = "load_tensors: CUDA_Host model buffer size = 417.66 MiB\n"
	const gpu = "load_tensors: CUDA0 model buffer size = 8192 MiB\n"
	for _, tc := range []struct{ name, rows, want string }{
		{"host only", host, "cpu"},
		{"device then host", gpu + host, "cuda"},
		{"host then device", host + gpu, "cuda"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := &servingLogs{}
			writeServing(t, logs.writer(0), tc.rows)
			if backend, _, err := logs.allocation(); backend != tc.want || err != nil {
				t.Fatalf("host/device allocation classification: %q %v", backend, err)
			}
		})
	}
	for _, name := range []string{"CUDA_Host0", "CUDA_Host_extra", "CUDA-Host", "Novel_Host", "CUDA_Hots"} {
		logs := &servingLogs{}
		writeServing(t, logs.writer(0), gpu+"load_tensors: "+name+" model buffer size = 417.66 MiB\n")
		if backend, _, err := logs.allocation(); backend != "" || err == nil {
			t.Errorf("unknown host spelling %q acquired CUDA authority: %q %v", name, backend, err)
		}
	}
	r := &Runtime{logs: &servingLogs{}}
	writeServing(t, r.logs.writer(0), host)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if backend, err := r.WaitAccel(ctx, "cuda"); backend != "" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("host-only buffer satisfied a CUDA wait: %q %v", backend, err)
	}
}
