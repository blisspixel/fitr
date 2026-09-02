//go:build !windows

package device

import (
	"os"
	"testing"
)

func TestParseMemAvailableRequiresExactKernelField(t *testing.T) {
	value, ok := parseMemAvailable([]byte("MemTotal:       131072 kB\nMemAvailable:    65536 kB\nSwapFree:       1 kB\n"))
	if !ok || value != 65536*1024 {
		t.Fatalf("MemAvailable = %d, %v", value, ok)
	}
	for _, malformed := range []string{
		"MemFree: 65536 kB\n",
		"MemAvailable: -1 kB\n",
		"MemAvailable: 12 MB\n",
		"MemAvailable: 999999999999999999999999 kB\n",
	} {
		if value, ok := parseMemAvailable([]byte(malformed)); ok || value != 0 {
			t.Fatalf("malformed meminfo accepted: %q -> %d, %v", malformed, value, ok)
		}
	}
}

func TestCgroupNumberParserRejectsUnlimitedAndOverflow(t *testing.T) {
	dir := t.TempDir()
	tests := map[string]bool{
		"1234": true, "0": true, "max": false, "-1": false,
		"999999999999999999999999999999": false,
	}
	for text, want := range tests {
		path := dir + "/value"
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		value, ok := readCgroupNumber(path)
		if ok != want || ok && value < 0 {
			t.Fatalf("readCgroupNumber(%q) = %d, %v", text, value, ok)
		}
	}
}

func TestParseCgroupMemoryCandidatesPrefersProcessScope(t *testing.T) {
	candidates := parseCgroupMemoryCandidates("0::/user.slice/fitr.scope\n5:cpu,memory:/jobs/fitr\n")
	if len(candidates) != 2 {
		t.Fatalf("candidate count = %d, want 2", len(candidates))
	}
	if candidates[0].limit != "/sys/fs/cgroup/user.slice/fitr.scope/memory.max" ||
		candidates[0].current != "/sys/fs/cgroup/user.slice/fitr.scope/memory.current" {
		t.Fatalf("v2 process candidate = %+v", candidates[0])
	}
	if candidates[1].limit != "/sys/fs/cgroup/memory/jobs/fitr/memory.limit_in_bytes" ||
		candidates[1].current != "/sys/fs/cgroup/memory/jobs/fitr/memory.usage_in_bytes" {
		t.Fatalf("v1 process candidate = %+v", candidates[1])
	}
}

func TestParseCgroupMemoryCandidatesKeepsPathsInsideCgroupMount(t *testing.T) {
	candidates := parseCgroupMemoryCandidates("0::/../../etc\n0::/\n")
	if len(candidates) != 1 {
		t.Fatalf("candidate count = %d, want one normalized process path", len(candidates))
	}
	if candidates[0].limit != "/sys/fs/cgroup/etc/memory.max" {
		t.Fatalf("normalized candidate = %+v", candidates[0])
	}
}
