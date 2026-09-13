package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/source"
)

func sourceScreenServices(t *testing.T, license string, body []byte, reads *int) sourceServices {
	t.Helper()
	metadata := fmt.Sprintf(`{"id":"owner/model","author":"owner","sha":%q,"cardData":{"license":%q,"base_model":"owner/base"},"siblings":[{"rfilename":"model.gguf","size":1073741824,"lfs":{"size":1073741824,"sha256":%q}}]}`,
		strings.Repeat("a", 40), license, strings.Repeat("b", 64))
	resolver := source.NewResolver(sourceTestTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(metadata))}, nil
	}))
	return sourceServices{resolve: resolver.ResolveHF,
		prefix: func(_ context.Context, repo, commit, path string, limit int) ([]byte, source.PrefixObservation) {
			*reads++
			if repo != "owner/model" || commit != strings.Repeat("a", 40) || path != "model.gguf" {
				t.Fatal("prefix lost pinned identity")
			}
			if limit != source.MaxArtifactPrefixBytes {
				t.Fatalf("unexpected read bound %d", limit)
			}
			return body, source.PrefixObservation{Outcome: "resolved", Bytes: len(body), Host: "public.example", RequestedHost: "huggingface.co"}
		},
		detect: func(context.Context) device.Fingerprint {
			return device.Fingerprint{VRAMGb: 8, VRAMSource: "fixture addressable capacity"}
		},
	}
}

func sourceScreenHeader(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	buffer.WriteString("GGUF")
	binary.Write(&buffer, binary.LittleEndian, uint32(3))
	binary.Write(&buffer, binary.LittleEndian, uint64(0))
	binary.Write(&buffer, binary.LittleEndian, uint64(7))
	writeString := func(value string) {
		binary.Write(&buffer, binary.LittleEndian, uint64(len(value)))
		buffer.WriteString(value)
	}
	writeString("general.architecture")
	binary.Write(&buffer, binary.LittleEndian, uint32(8))
	writeString("llama")
	for _, item := range []struct {
		name  string
		value uint32
	}{
		{"block_count", 2}, {"attention.head_count", 8}, {"attention.head_count_kv", 2},
		{"attention.key_length", 128}, {"attention.value_length", 128}, {"context_length", 8192},
	} {
		writeString("llama." + item.name)
		binary.Write(&buffer, binary.LittleEndian, uint32(4))
		binary.Write(&buffer, binary.LittleEndian, item.value)
	}
	return buffer.Bytes()
}

func sourceScreenArgs(t *testing.T, mode string) []string {
	t.Helper()
	return []string{"resolve", "hf", "--repo", "owner/model", "--revision", "main", "--file", "model.gguf",
		"--out", filepath.Join(sourceTestDirectory(t), "receipt.json"), "--screen", "--ctx", "4096",
		"--fit-budget-gb", "8", "--allow-license", "apache-2.0", "--allow-architecture", "llama", "--display", mode}
}

func TestSourceScreenNaturalFlagsProduceOneJSONDocument(t *testing.T) {
	reads := 0
	services := sourceScreenServices(t, "apache-2.0", sourceScreenHeader(t), &reads)
	args := sourceScreenArgs(t, "json")
	output, code := captureTopStdout(t, func() int { return cmdSourceWithServices(context.Background(), args, services) })
	if code != exitOK || reads != 1 {
		t.Fatalf("screen code=%d reads=%d output=%s", code, reads, output)
	}
	var report sourceFitOutput
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("screen did not return one JSON document: %v %s", err, output)
	}
	if report.Screen == nil || report.Screen.State != "clear" || report.Screen.Runtime != "unmeasured" || report.Projection.ComponentBytes == nil || len(report.Headers) != 1 || report.Headers[0].SHA256 == "" {
		t.Fatalf("screen lost policy or provenance: %+v", report)
	}
	if strings.Contains(output, "local-files") {
		t.Fatal("local paths leaked into report")
	}
	for index, arg := range args {
		if arg != "--out" {
			continue
		}
		resolution, err := source.LoadResolution(args[index+1])
		if err != nil || resolution.ResolutionSHA256 != report.Resolution.ResolutionSHA256 {
			t.Fatalf("saved metadata changed: %v", err)
		}
	}
}

func TestSourceScreenStopsBeforeExpensiveReadsAndNeverReturnsQualityFailure(t *testing.T) {
	for _, test := range []struct {
		name, license, changeFlag, value, first string
		reads                                   int
	}{
		{"license rejected", "mit", "", "", "license", 0},
		{"license missing", "", "", "", "license", 0},
		{"architecture rejected", "apache-2.0", "--allow-architecture", "other", "architecture", 1},
		{"ceiling exceeded", "apache-2.0", "--fit-budget-gb", "0.5", "fit", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			reads := 0
			services := sourceScreenServices(t, test.license, sourceScreenHeader(t), &reads)
			args := sourceScreenArgs(t, "json")
			for index, arg := range args {
				if arg == test.changeFlag {
					args[index+1] = test.value
				}
			}
			output, code := captureTopStdout(t, func() int { return cmdSourceWithServices(context.Background(), args, services) })
			var report sourceFitOutput
			if err := json.Unmarshal([]byte(output), &report); err != nil {
				t.Fatal(err)
			}
			if code != exitUnresolved || reads != test.reads || report.Screen.FirstProblem != test.first {
				t.Fatalf("code=%d reads=%d screen=%+v", code, reads, report.Screen)
			}
		})
	}
}

func TestSourceProjectionModesAndUnavailableHeaders(t *testing.T) {
	for _, mode := range []string{"none", "plain", "rich", "auto", "json"} {
		for _, valid := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", mode, valid), func(t *testing.T) {
				reads := 0
				body := []byte("unreadable header")
				if valid {
					body = sourceScreenHeader(t)
				}
				services := sourceScreenServices(t, "apache-2.0", body, &reads)
				args := sourceScreenArgs(t, mode)
				output, code := captureTopStdout(t, func() int { return cmdSourceWithServices(context.Background(), args, services) })
				want := exitUnresolved
				if valid {
					want = exitOK
				}
				if code != want || (mode == "none" && output != "") {
					t.Fatalf("code=%d output=%s", code, output)
				}
			})
		}
	}
}

func TestSourceProjectionCancellationAndOutputFailure(t *testing.T) {
	reads := 0
	services := sourceScreenServices(t, "apache-2.0", sourceScreenHeader(t), &reads)
	ctx, cancel := context.WithCancel(context.Background())
	services.prefix = func(context.Context, string, string, string, int) ([]byte, source.PrefixObservation) {
		cancel()
		return nil, source.PrefixObservation{Outcome: "cancelled"}
	}
	args := sourceScreenArgs(t, "json")
	output, code := captureTopStdout(t, func() int { return cmdSourceWithServices(ctx, args, services) })
	if code != exitInterrupt || !strings.Contains(output, "cancelled") {
		t.Fatalf("cancellation=%d %s", code, output)
	}
	closed, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	previous := os.Stdout
	os.Stdout = closed
	t.Cleanup(func() { os.Stdout = previous })
	services = sourceScreenServices(t, "apache-2.0", sourceScreenHeader(t), &reads)
	if code := cmdSourceWithServices(context.Background(), sourceScreenArgs(t, "json"), services); code != exitError {
		t.Fatalf("output failure exited %d", code)
	}
}

func TestSourceScreenRejectsPolicyBeforeNetwork(t *testing.T) {
	for _, extra := range [][]string{
		{"--fit-budget-gb", "NaN"}, {"--fit-budget-gb", "Inf"}, {"--fit-budget-gb=0"}, {"--fit-budget-gb=1073741825"},
		{"--ctx=0"}, {"--ctx=-1"}, {"--allow-license", "MIT"}, {"--allow-license", "apache-2.0"},
		{"--screen=false"}, {"--header-bytes=0"}, {"--header-bytes=8388609"},
	} {
		args := append(sourceScreenArgs(t, "none"), extra...)
		services := sourceServices{resolve: func(context.Context, source.HFRequest) (source.Resolution, error) {
			t.Fatal("invalid policy reached network")
			return source.Resolution{}, errors.New("unexpected")
		}}
		_, code := captureTopStderr(t, func() int { return cmdSourceWithServices(context.Background(), args, services) })
		if code != exitUsage {
			t.Fatalf("args=%v code=%d", extra, code)
		}
	}
}

func TestSourceFitUsesOneEnvelopeAndHonorsExplicitReadBound(t *testing.T) {
	for _, mode := range []string{"json", "none", "plain"} {
		t.Run(mode, func(t *testing.T) {
			reads := 0
			body := sourceScreenHeader(t)
			services := sourceScreenServices(t, "apache-2.0", body, &reads)
			services.prefix = func(_ context.Context, _, _, _ string, limit int) ([]byte, source.PrefixObservation) {
				reads++
				if limit != 1048576 {
					t.Fatalf("explicit header bound was lost: %d", limit)
				}
				return body, source.PrefixObservation{Outcome: "resolved", Bytes: len(body), RequestedBytes: limit}
			}
			args := []string{"resolve", "hf", "--repo", "owner/model", "--revision", "main", "--file", "model.gguf",
				"--out", filepath.Join(sourceTestDirectory(t), "receipt.json"), "--fit", "--header-bytes", "1048576", "--display", mode}
			output, code := captureTopStdout(t, func() int { return cmdSourceWithServices(context.Background(), args, services) })
			if code != exitOK || reads != 1 || (mode == "none" && output != "") {
				t.Fatalf("code=%d reads=%d output=%s", code, reads, output)
			}
			if mode != "json" {
				return
			}
			var report sourceFitOutput
			if err := json.Unmarshal([]byte(output), &report); err != nil {
				t.Fatal(err)
			}
			if report.Screen != nil || report.Projection.Context != 8192 || report.Projection.CapacitySource == "operator component ceiling" {
				t.Fatalf("fit lost default context or capacity provenance: %+v", report)
			}
		})
	}
}
