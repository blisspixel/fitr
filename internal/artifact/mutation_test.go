package artifact

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func preparedFixture(t *testing.T, count int) (Binding, []openedFile) {
	t.Helper()
	r, s := artifactFixture(t, count)
	result, err := newBinding(r, s, Options{})
	if err != nil {
		t.Fatal(err)
	}
	opened := make([]openedFile, len(result.Files))
	t.Cleanup(func() { closeFiles(opened) })
	if err := preflight(t.Context(), &result, opened); err != nil {
		t.Fatal(err)
	}
	return result, opened
}

func sealObservation(t *testing.T, result *Binding) {
	t.Helper()
	result.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
	result.State, result.Gaps, result.UnmappedFiles = deriveResult(*result)
	digest, err := result.Digest()
	if err != nil {
		t.Fatal(err)
	}
	result.BindingSHA256 = digest
}

func replaceSameFacts(t *testing.T, path string) error {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(filepath.Dir(path), "replacement.bin")
	if err := os.WriteFile(replacement, make([]byte, info.Size()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	return os.Rename(replacement, path)
}

func TestReplacementBeforeOpenPreservesSizeAndTime(t *testing.T) {
	result, opened := preparedFixture(t, 1)
	if err := replaceSameFacts(t, result.Files[0].LocalPath); err != nil {
		t.Fatal(err)
	}
	if err := openFiles(t.Context(), &result, opened); err != nil {
		t.Fatal(err)
	}
	hashFiles(t.Context(), &result, opened)
	finalRecheck(t.Context(), &result, opened)
	sealObservation(t, &result)
	if result.State != "changed" || result.BytesRead != 0 {
		t.Fatal("replacement was hashed")
	}
}

func TestReplacementAfterHashChecksRetainedIdentity(t *testing.T) {
	result, opened := preparedFixture(t, 2)
	if err := openFiles(t.Context(), &result, opened); err != nil {
		t.Fatal(err)
	}
	hashFiles(t.Context(), &result, opened)
	replaceErr := replaceSameFacts(t, result.Files[0].LocalPath)
	finalRecheck(t.Context(), &result, opened)
	sealObservation(t, &result)
	if replaceErr == nil {
		if result.State != "changed" || result.Files[0].ObservedSHA256 != "" {
			t.Fatal("post-hash replacement retained authority")
		}
	} else {
		if runtime.GOOS != "windows" {
			t.Fatal(replaceErr)
		}
		if result.State != "matched" {
			t.Fatal("OS-prevented replacement should preserve unchanged observation")
		}
		t.Log("Windows prevented replacement while the read handle remained open")
	}
}

func TestFinalSetRecheckDetectsEarlierFileChange(t *testing.T) {
	result, opened := preparedFixture(t, 2)
	if err := openFiles(t.Context(), &result, opened); err != nil {
		t.Fatal(err)
	}
	hashFiles(t.Context(), &result, opened)
	if err := os.Chtimes(result.Files[0].LocalPath, time.Now(), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	finalRecheck(t.Context(), &result, opened)
	sealObservation(t, &result)
	if result.State != "changed" || result.Files[0].ObservedSHA256 != "" || result.Files[1].State != "matched" {
		t.Fatal("full-set recheck lost earlier change")
	}
}

type observingReader struct {
	reader io.Reader
	after  func()
}

func (reader *observingReader) Read(buffer []byte) (int, error) {
	count, err := reader.reader.Read(buffer)
	if reader.after != nil {
		action := reader.after
		reader.after = nil
		action()
	}
	return count, err
}

func TestCancellationAfterReadAccountsForBytes(t *testing.T) {
	r, s := artifactFixture(t, 1)
	file, err := os.Open(s.Files[0].LocalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader := &observingReader{reader: file, after: cancel}
	digest, count, state := readHash(ctx, reader, *r.Files[0].SizeBytes, HardMaxBytes, make([]byte, 4))
	if digest != "" || count != 4 || state != "cancelled" {
		t.Fatalf("cancel accounting: %s %d %s", digest, count, state)
	}
}

func TestGrowthAndShrinkDuringHash(t *testing.T) {
	for _, grow := range []bool{false, true} {
		t.Run(map[bool]string{false: "shrink", true: "grow"}[grow], func(t *testing.T) {
			result, opened := preparedFixture(t, 1)
			if err := openFiles(t.Context(), &result, opened); err != nil {
				t.Fatal(err)
			}
			path := result.Files[0].LocalPath
			reader := &observingReader{reader: opened[0].file, after: func() {
				size := int64(2)
				if grow {
					size = result.Files[0].Before.SizeBytes + 1
				}
				if err := os.Truncate(path, size); err != nil {
					t.Fatal(err)
				}
			}}
			digest, count, state := readHash(t.Context(), reader, result.Files[0].Before.SizeBytes, HardMaxBytes, make([]byte, 4))
			file := &result.Files[0]
			file.BytesRead, file.ObservedSHA256, result.BytesRead = count, digest, count
			if grow {
				file.State = "hash_mismatch"
			} else {
				file.State, file.IdentityState = state, "changed"
			}
			finalRecheck(t.Context(), &result, opened)
			sealObservation(t, &result)
			if result.State != "changed" || file.ObservedSHA256 != "" {
				t.Fatal("size mutation was accepted")
			}
		})
	}
}

func TestHardLinksRejectedEvenWithoutHashing(t *testing.T) {
	for _, budget := range []int64{1, DefaultMaxBytes} {
		t.Run(time.Duration(budget).String(), func(t *testing.T) {
			r, s := artifactFixture(t, 2)
			if err := os.Remove(s.Files[1].LocalPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(s.Files[0].LocalPath, s.Files[1].LocalPath); err != nil {
				t.Fatal(err)
			}
			result, err := Bind(t.Context(), r, s, Options{MaxBytes: budget})
			if err == nil || result.BytesRead != 0 {
				t.Fatal("hard-link aliases were observed")
			}
		})
	}
}

func TestSymlinkTraversalAndSpecialInputRefused(t *testing.T) {
	r, s := artifactFixture(t, 1)
	root := filepath.Dir(s.Files[0].LocalPath)
	link := filepath.Join(root, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	s.Files[0].LocalPath = link + string(os.PathSeparator) + "a.gguf"
	if _, err := Bind(t.Context(), r, s, Options{}); err == nil {
		t.Fatal("symlink ancestor accepted")
	}
	raw := link + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "binding.json"
	if ValidateOutputPath(raw) == nil {
		t.Fatal("raw traversal accepted")
	}
	if _, err := LoadBinding(raw); err == nil {
		t.Fatal("raw traversal read accepted")
	}
	s.Files[0].LocalPath = root
	if _, err := Bind(t.Context(), r, s, Options{}); err == nil {
		t.Fatal("directory input accepted")
	}
}

type failingReader struct {
	data []byte
	err  error
}

func (reader failingReader) Read(buffer []byte) (int, error) {
	return copy(buffer, reader.data), reader.err
}

func TestReadFailuresAndBudgetKeepActualCounts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reader io.Reader
		budget int64
		want   string
		count  int64
	}{
		{"partial_error", failingReader{[]byte("ab"), errors.New("disk failure")}, 4, "read_error", 2},
		{"short_eof", failingReader{[]byte("ab"), io.EOF}, 4, "changed", 2},
		{"empty", failingReader{}, 4, "read_error", 0},
		{"budget", failingReader{[]byte("ab"), nil}, 2, "budget_exceeded", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			digest, count, state := readHash(t.Context(), tc.reader, 4, tc.budget, make([]byte, 4))
			if digest != "" || count != tc.count || state != tc.want {
				t.Fatalf("got %s %d %s", digest, count, state)
			}
		})
	}
}
