package device

import (
	"context"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// GPUConsumer is one process the accelerator driver reports as holding a
// context on the GPU.
//
// UsedMiB is only meaningful when UsedKnown is true. Windows WDDM does not
// expose per-process accelerator memory at all, and nvidia-smi reports the
// field as unavailable there, so on that platform fitr can name the processes
// on the GPU but never say how much each one holds. An unknown share is left
// unknown rather than divided out of the total.
type GPUConsumer struct {
	PID       int
	Name      string
	UsedMiB   int
	UsedKnown bool
}

// GPUContention is what else was on the accelerator when a measurement was
// about to be taken. Observed is false when no vendor tool could answer, which
// is different from an idle GPU.
type GPUContention struct {
	Observed   bool
	PerProcess bool
	TotalMiB   int
	UsedMiB    int
	FreeMiB    int
	Consumers  []GPUConsumer
}

// observeGPUContention asks the vendor tool what holds the accelerator. It is
// display and preflight evidence: memory in use by other processes moves
// minute to minute, so it never enters a comparability key.
func observeGPUContention(ctx context.Context) GPUContention {
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		return GPUContention{}
	}
	cctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	out := GPUContention{}
	totals, err := runSMI(cctx, "--query-gpu=memory.total,memory.used,memory.free")
	if err != nil || len(totals) == 0 {
		return GPUContention{}
	}
	fields := splitCSV(totals[0])
	if len(fields) < 3 {
		return GPUContention{}
	}
	out.TotalMiB, out.UsedMiB, out.FreeMiB = atoiOr(fields[0]), atoiOr(fields[1]), atoiOr(fields[2])
	if out.TotalMiB <= 0 {
		return GPUContention{}
	}
	out.Observed = true
	apps, err := runSMI(cctx, "--query-compute-apps=pid,used_memory,process_name")
	if err != nil {
		return out
	}
	for _, line := range apps {
		f := splitCSV(line)
		if len(f) < 3 {
			continue
		}
		pid := atoiOr(f[0])
		if pid <= 0 {
			continue
		}
		c := GPUConsumer{PID: pid, Name: processLeaf(f[2])}
		if mib, err := strconv.Atoi(f[1]); err == nil && mib >= 0 {
			c.UsedMiB, c.UsedKnown = mib, true
			out.PerProcess = true
		}
		out.Consumers = append(out.Consumers, c)
	}
	return out
}

func runSMI(ctx context.Context, query string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "nvidia-smi", query, "--format=csv,noheader,nounits")
	cmd.WaitDelay = 250 * time.Millisecond
	raw, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

func splitCSV(line string) []string {
	parts := strings.Split(line, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// atoiOr returns 0 for the vendor tool's unavailable markers, which are text
// rather than numbers: "[N/A]" and "[Insufficient Permissions]" both appear.
func atoiOr(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

// processLeaf keeps the executable name and drops the directory. A full path
// can carry a user name, and this text reaches the terminal and the doctor
// report.
//
// The vendor tool substitutes a bracketed marker where it has no name to give,
// most often for a process owned by another user. Printing that marker as
// though it were a program would tell the reader to go close something called
// "[Insufficient Permissions]".
func processLeaf(path string) string {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "[") && strings.HasSuffix(path, "]") {
		return ""
	}
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		path = path[i+1:]
	}
	return path
}

// ServingRuntime reports whether a consumer is the model server itself. It is
// the one process on the list that must not be suggested for closing: it is
// the subject of the measurement, not competition for it.
func (c GPUConsumer) ServingRuntime() bool {
	name := strings.ToLower(c.Name)
	return strings.Contains(name, "ollama") || strings.Contains(name, "llama-server") ||
		strings.Contains(name, "llama_server")
}

// Named reports whether the driver gave this process a usable name.
func (c GPUConsumer) Named() bool { return c.Name != "" }

// GPUContentionNow reports what currently holds the accelerator.
func GPUContentionNow(ctx context.Context) GPUContention { return observeGPUContention(ctx) }

// Busy reports whether enough of the accelerator is held by other work that a
// measurement taken now would describe a different machine than an idle one.
// The threshold is a share of total capacity rather than an absolute figure,
// because the same spare gigabyte means different things on an 8 GB card and a
// 96 GB one.
func (c GPUContention) Busy() bool {
	if !c.Observed || c.TotalMiB <= 0 {
		return false
	}
	return float64(c.UsedMiB) >= 0.10*float64(c.TotalMiB)
}

// Top returns the largest consumers first when per-process bytes are
// available, and otherwise the processes in the order the driver listed them,
// capped so a report cannot be flooded by a desktop session.
func (c GPUContention) Top(n int) []GPUConsumer {
	out := append([]GPUConsumer(nil), c.Consumers...)
	if c.PerProcess {
		sort.SliceStable(out, func(i, j int) bool { return out[i].UsedMiB > out[j].UsedMiB })
	}
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}
