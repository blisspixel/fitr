package autoruntime

import (
	"context"
	"testing"
)

func TestAccelerationUsesOwnedAllocationsAndFreshLoadBoundary(t *testing.T) {
	cases := []struct{ name, logs, want string }{
		{"hardware is not execution", `msg="inference compute" library=CUDA device=CUDA0`, ""},
		{"tensor allocation", "load_tensors: CUDA0 model buffer size = 4096 MiB", "cuda"},
		{"weights allocation", `msg="model weights" device=CPU size="4 GiB"`, "cpu"},
		{"CPU metadata plus GPU weights", "load_tensors: CPU_Mapped model buffer size = 10 MiB\nload_tensors: CUDA0 model buffer size = 4096 MiB", "cuda"},
		{"mixed accelerators", "load_tensors: CUDA0 model buffer size = 4096 MiB\nload_tensors: Vulkan0 model buffer size = 4096 MiB", ""},
		{"unrecognized device", `msg="model weights" device=novel0 size="4 GiB"`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := accelerationFromLoads(tc.logs); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	r := &Runtime{logs: &servingLogs{}}
	_, _ = r.logs.writer(0).Write([]byte(cases[1].logs + "\n"))
	r.BeginLoadObservation()
	if got := r.Accel(t.Context()); got != "" {
		t.Fatalf("earlier model allocation transferred: %s", got)
	}
	_, _ = r.logs.writer(0).Write([]byte(cases[2].logs + "\n"))
	if got := r.Accel(t.Context()); got != "cpu" {
		t.Fatalf("fresh load not observed: %s", got)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if r.Accel(ctx) != "" {
		t.Fatal("cancelled observation returned a backend")
	}
	r.closed = true
	if r.Accel(t.Context()) != "" {
		t.Fatal("closed runtime returned a backend")
	}
}
