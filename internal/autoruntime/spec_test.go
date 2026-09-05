package autoruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testSpec() Spec {
	return Spec{Schema: SpecSchema, Executable: `C:\runtime\ollama.exe`, ExecutableSHA256: "sha256:" + strings.Repeat("a", 64),
		LibrariesSHA256: "sha256:" + strings.Repeat("b", 64), RuntimeVersion: "0.0.1-fixture", ModelStore: `C:\fixture\models`,
		NumCtx: 8192, KVCacheType: "f16", FlashAttention: true, ReserveBytes: 2 << 30}
}

func TestSpecPortableStructuralContract(t *testing.T) {
	base := testSpec()
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Spec){
		"schema": func(s *Spec) { s.Schema = "future" }, "hash": func(s *Spec) { s.ExecutableSHA256 = "a" },
		"libraries": func(s *Spec) { s.LibrariesSHA256 = "" }, "version": func(s *Spec) { s.RuntimeVersion = "x\n0.1.0" },
		"relative": func(s *Spec) { s.Executable = "runtime.exe" }, "parent": func(s *Spec) { s.ModelStore = `C:\safe\..\models` },
		"network": func(s *Spec) { s.ModelStore = `\\host\share\models` }, "context": func(s *Spec) { s.NumCtx = 0 },
		"slash network":    func(s *Spec) { s.ModelStore = `//host/share/models` },
		"alternate stream": func(s *Spec) { s.Executable = `C:\runtime\ollama.exe:stream` },
		"reserve":          func(s *Spec) { s.ReserveBytes = -1 }, "cache": func(s *Spec) { s.KVCacheType = "unknown" },
		"flash": func(s *Spec) { s.KVCacheType = "q8_0"; s.FlashAttention = false },
	} {
		t.Run(name, func(t *testing.T) {
			s := base
			change(&s)
			if s.Validate() == nil {
				t.Fatal("unsafe declaration accepted")
			}
		})
	}
	p, launch, err := base.ProfileDigests()
	if err != nil || !digestPattern.MatchString(p) || !digestPattern.MatchString(launch) {
		t.Fatal(p, launch, err)
	}
	for _, change := range []func(*Spec){func(s *Spec) { s.NumCtx *= 2 }, func(s *Spec) { s.ReserveBytes++ },
		func(s *Spec) { s.LibrariesSHA256 = "sha256:" + strings.Repeat("c", 64) }} {
		s := base
		change(&s)
		next, _, err := s.ProfileDigests()
		if err != nil || next == p {
			t.Fatal("material configuration change did not change profile")
		}
	}
}

func TestInstallationHashBoundsAndPathIntegrity(t *testing.T) {
	// macOS temporary directories can have /var -> /private/var ancestors.
	// Only canonicalize the fixture root; deliberate dependency links below
	// remain untouched so production rejection is still exercised.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "input.bin")
	if err := os.WriteFile(file, []byte("runtime content"), 0o600); err != nil {
		t.Fatal(err)
	}
	var used int64
	hash, size, err := hashFile(context.Background(), file, &used)
	if err != nil || size != 15 || used != 15 || !digestPattern.MatchString(hash) {
		t.Fatal(hash, size, used, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := hashFile(ctx, file, &used); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	used = MaxRuntimeBytes
	if _, _, err := hashFile(context.Background(), file, &used); err == nil {
		t.Fatal("aggregate budget ignored")
	}
	if _, err := physicalPath(root, false); err == nil {
		t.Fatal("directory accepted as executable")
	}
	if _, err := physicalPath(file, true); err == nil {
		t.Fatal("file accepted as model store")
	}
	if err := removePrivateHome(root); err == nil {
		t.Fatal("unowned temp directory eligible for deletion")
	}
	link := filepath.Join(root, "link.bin")
	if err := os.Symlink(file, link); err == nil {
		used = 0
		if _, _, err := hashFile(context.Background(), link, &used); err == nil {
			t.Fatal("symlink dependency accepted")
		}
	}
}

func TestModelConfigurationBindsBehaviorWithoutQualityClaim(t *testing.T) {
	base := `{"model_info":{"general.architecture":"llama"},"template":"{{.Prompt}}","system":"test","parameters":"temperature 0","messages":[{"role":"user","content":"fixture"}],"parser":"test","renderer":"test","capabilities":["completion"]}`
	hash, err := ModelConfigurationSHA256([]byte(base))
	if err != nil || !digestPattern.MatchString(hash) {
		t.Fatal(hash, err)
	}
	for _, field := range []string{"template", "system", "parameters", "messages", "parser", "renderer"} {
		var value map[string]any
		if err := json.Unmarshal([]byte(base), &value); err != nil {
			t.Fatal(err)
		}
		value[field] = "changed"
		if field == "messages" {
			value[field] = []any{map[string]string{"role": "system", "content": "changed"}}
		}
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		next, err := ModelConfigurationSHA256(data)
		if err != nil || next == hash {
			t.Fatalf("behavior change %q was not bound: %v", field, err)
		}
	}
	for _, input := range []string{
		`null`, `{}`, `{"model_info":{}}`, `{"model_info":{},"model_info":{}}`,
		`{"model_info":{"a":1},"remote_host":"cloud"}`, `{"model_info":{"a":1},"REMOTE_HOST":"cloud"}`,
		`{"model_info":{"a":1},"remote_model":false}`, `{"model_info":{"a":1},"projector_info":{"a":1}}`,
		`{"model_info":{"a":1},"modelfile":"ADAPTER /unbound.bin"}`, `{"model_info":{"a":1},"parameters":"draft_model another"}`,
		`{"model_info":{"a":1},"messages":[{"role":"user","content":"x","images":["base64"]}]}`,
		`{"model_info":{"a":1},"system":12}`, `{"model_info":{"a":1},"details":[]}`, `{"model_info":{"a":1},"messages":{}}`,
		`{"model_info":{"a":1},"messages":[12]}`, `{"model_info":{"a":1},"messages":[{"role":12,"content":"x"}]}`,
		`{"model_info":{"a":1},"messages":[{"role":"user","content":12}]}`, `{"model_info":{"a":1},"capabilities":[12]}`,
		`{"model_info":{"a":1},"size":-1}`, `{"model_info":{"a":1},"unknown":1}`,
	} {
		if _, err := ModelConfigurationSHA256([]byte(input)); err == nil {
			t.Fatalf("unsupported declaration accepted: %s", input)
		}
	}
	if (ModelConfiguration{Model: "fixture", SHA256: hash}).Validate() != nil {
		t.Fatal("valid observed identity rejected")
	}
	if (ModelConfiguration{Model: "fixture\n", SHA256: hash}).Validate() == nil {
		t.Fatal("invalid model identity accepted")
	}
}
