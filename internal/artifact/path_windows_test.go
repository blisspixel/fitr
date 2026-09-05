package artifact

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestBindingOutputRejectsMissingInputThroughWindowsShortPath(t *testing.T) {
	longDirectory := filepath.Join(artifactRoot(t), "Artifact Binding Long Directory")
	if err := os.Mkdir(longDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	shortDirectory := artifactShortDirectory(t, longDirectory)
	longInfo, err := os.Lstat(longDirectory)
	if err != nil {
		t.Fatal(err)
	}
	shortInfo, err := os.Lstat(shortDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(longInfo, shortInfo) || longInfo.Mode()&os.ModeSymlink != 0 || shortInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatal("short-path fixture is not a physical directory alias")
	}
	resolution, initial := artifactFixture(t, 1)
	for _, test := range []struct {
		name, inputDirectory, outputDirectory, outputName string
	}{
		{"long_input", longDirectory, shortDirectory, "future.gguf"},
		{"short_input", shortDirectory, longDirectory, "future.gguf"},
		{"case_alias", longDirectory, shortDirectory, "FUTURE.GGUF"},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := initial
			spec.Files = append([]Mapping(nil), initial.Files...)
			spec.Files[0].LocalPath = filepath.Join(test.inputDirectory, "future.gguf")
			output := filepath.Join(test.outputDirectory, test.outputName)
			if localKey(output) == localKey(spec.Files[0].LocalPath) {
				t.Fatal("fixture did not bypass lexical overlap detection")
			}
			if err := ValidateBindingOutputPath(output, spec); err == nil || !strings.Contains(err.Error(), "overlap") {
				t.Fatalf("preflight accepted missing-input alias: %v", err)
			}
			binding, err := Bind(t.Context(), resolution, spec, Options{})
			if err != nil || binding.State != "incomplete" || binding.Files[0].State != "missing" || binding.BytesRead != 0 {
				t.Fatalf("missing-input observation failed: %+v, %v", binding, err)
			}
			if err := WriteBinding(output, binding); err == nil || !strings.Contains(err.Error(), "overlap") {
				t.Fatalf("publication accepted missing-input alias: %v", err)
			}
			assertArtifactMissingPaths(t, output, spec.Files[0].LocalPath)
			if err := ValidateBindingOutputPath(filepath.Join(test.outputDirectory, "receipt.json"), spec); err != nil {
				t.Fatalf("short directory alias blocked a distinct output: %v", err)
			}
		})
	}
}

func assertArtifactMissingPaths(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("receipt publication changed an absent input: %v", err)
		}
	}
}

func artifactShortDirectory(t *testing.T, directory string) string {
	t.Helper()
	input, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]uint16, 32768)
	size, err := windows.GetShortPathName(input, &buffer[0], uint32(len(buffer)))
	if err != nil {
		t.Skipf("8.3 paths unavailable on the test filesystem: %v", err)
	}
	if size == 0 || size >= uint32(len(buffer)) {
		t.Fatal("invalid short-path response")
	}
	short := windows.UTF16ToString(buffer[:size])
	if strings.EqualFold(short, directory) {
		t.Skip("test filesystem did not allocate a distinct 8.3 alias")
	}
	return short
}
