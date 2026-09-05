package contextquality

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const testSeed = "0123456789abcdef0123456789abcdef"

func testPolicy(t *testing.T, tiers ...int) Policy {
	t.Helper()
	if len(tiers) == 0 {
		tiers = []int{2048, 4096}
	}
	policy, err := NewPolicy(8192, tiers)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func testPlan(t *testing.T, tiers ...int) Plan {
	t.Helper()
	plan, err := NewPlan(testPolicy(t, tiers...), testSeed)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func cloneTest[T any](t *testing.T, value T) T {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result T
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPolicyBoundsAndNoImplicitTokenConversion(t *testing.T) {
	for _, tiers := range [][]int{nil, {2048}, {2048, 2048}, {4096, 2048}, {2047, 4096}, {2048, 65537}, {2048, 4096, 8192, 16384, 32768}} {
		if _, err := NewPolicy(8192, tiers); err == nil {
			t.Fatal("invalid byte tiers accepted", tiers)
		}
	}
	for _, window := range []int{0, -1, OutputReserveTokens, MaxOperatingWindowTokens + 1} {
		if _, err := NewPolicy(window, []int{2048, 4096}); err == nil {
			t.Fatal("invalid window accepted", window)
		}
	}
	for _, window := range []int{OutputReserveTokens + 1, MaxOperatingWindowTokens} {
		// The pure policy cannot estimate token capacity from ASCII bytes.
		if _, err := NewPolicy(window, []int{2048, 2049, 65535, 65536}); err != nil {
			t.Fatal("valid declared bounds rejected", err)
		}
	}
}

func TestPolicyContractDigestAndStrictJSON(t *testing.T) {
	policy := testPolicy(t)
	data, err := policy.JSON()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := DecodePolicy(data)
	if err != nil || !reflect.DeepEqual(policy, loaded) {
		t.Fatal("policy round trip failed", err)
	}
	digest, err := policy.Digest()
	want := fmt.Sprintf("sha256:%x", sha256.Sum256(append([]byte(PolicySchema+"\x00"), data...)))
	if err != nil || digest != want {
		t.Fatal("policy digest lacks its domain", digest, want, err)
	}
	for _, invalid := range [][]byte{
		append(append([]byte(nil), data...), []byte(` {}`)...),
		bytes.Replace(data, []byte(`"schema":`), []byte(`"Schema":`), 1),
		bytes.Replace(data, []byte(`"schema":`), []byte(`"schema":"duplicate","schema":`), 1),
		bytes.Replace(data, []byte(`"schema":`), []byte(`"extra":true,"schema":`), 1),
		[]byte(`{}`), []byte(`null`), []byte(`[]`), bytes.Repeat([]byte(" "), MaxPolicyBytes+1),
	} {
		if _, err := DecodePolicy(invalid); err == nil {
			t.Fatal("ambiguous, noncanonical or oversized policy accepted")
		}
	}
}

func TestPolicyRefusesChangedContractAndCopiesTierInputs(t *testing.T) {
	tiers := []int{2048, 4096}
	policy, err := NewPolicy(8192, tiers)
	if err != nil {
		t.Fatal(err)
	}
	tiers[0] = 999
	if policy.PayloadUTF8Bytes[0] != 2048 {
		t.Fatal("policy aliases caller's tiers")
	}
	for _, mutate := range []func(*Policy){
		func(p *Policy) { p.Schema += ".other" }, func(p *Policy) { p.TaskPackSHA256 = strings.Repeat("a", 71) },
		func(p *Policy) { p.OutputReserveTokens-- }, func(p *Policy) { p.Qualification = "majority" },
		func(p *Policy) { p.RequestPolicy = "truncate" },
	} {
		changed := cloneTest(t, policy)
		mutate(&changed)
		if changed.Validate() == nil {
			t.Fatal("changed contract accepted")
		}
		if _, err := changed.Digest(); err == nil {
			t.Fatal("invalid policy received a digest")
		}
		if _, err := changed.JSON(); err == nil {
			t.Fatal("invalid policy serialized")
		}
	}
}
