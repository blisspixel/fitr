package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/artifact"
	"github.com/blisspixel/fitr/internal/source"
)

func artifactNeverBind(t *testing.T) artifactBinder {
	t.Helper()
	return func(context.Context, source.Resolution, artifact.Spec, artifact.Options) (artifact.Binding, error) {
		t.Error("preflight failure reached file hashing")
		return artifact.Binding{}, errors.New("unexpected bind")
	}
}

func TestArtifactCLIRejectsMalformedFlagsBeforeBind(t *testing.T) {
	fixture := artifactFixture(t, "matched")
	for _, args := range [][]string{
		{"unknown"}, {"show"}, {"show", fixture.sourcePath, "extra"}, {"show", fixture.sourcePath, "--display", "invalid"},
		{"bind"}, {"bind", "--source", fixture.sourcePath}, {"bind", "--unknown"},
		append(fixture.args("none"), "extra"), append(fixture.args("none"), "--display", "invalid"),
		append(fixture.args("none"), "--max-bytes", "0"), append(fixture.args("none"), "--max-bytes=-1"),
		append(fixture.args("none"), "--max-bytes", "1099511627777"), append(fixture.args("none"), "--max-bytes", "overflow"),
		append(fixture.args("none"), "--timeout", "0s"), append(fixture.args("none"), "--timeout", "1ns"),
		append(fixture.args("none"), "--timeout", "61m"), append(fixture.args("none"), "--timeout", "invalid"),
	} {
		_, code := captureTopStderr(t, func() int { return cmdArtifactWithBinder(t.Context(), args, artifactNeverBind(t)) })
		if code != exitUsage {
			t.Fatalf("args=%v exited %d", args, code)
		}
	}
	assertArtifactPathMissing(t, fixture.outputPath)
}

func TestArtifactCLIRejectsMappingProblemsBeforeBind(t *testing.T) {
	for _, change := range []string{"relative", "digest", "duplicate", "role", "missing", "malformed"} {
		t.Run(change, func(t *testing.T) {
			fixture := artifactFixture(t, "matched")
			alterArtifactMapping(t, fixture, change)
			_, code := captureTopStderr(t, func() int {
				return cmdArtifactWithBinder(t.Context(), fixture.args("none"), artifactNeverBind(t))
			})
			if code != exitError {
				t.Fatalf("bad mapping exited %d", code)
			}
			assertArtifactPathMissing(t, fixture.outputPath)
		})
	}
}

func alterArtifactMapping(t *testing.T, fixture artifactCLIFixture, change string) {
	t.Helper()
	switch change {
	case "relative":
		fixture.spec.Files[0].LocalPath = "model.gguf"
	case "digest":
		fixture.spec.ResolutionSHA256 = "sha256:" + strings.Repeat("f", 64)
	case "duplicate":
		fixture.spec.Files = append(fixture.spec.Files, fixture.spec.Files[0])
	case "role":
		fixture.spec.Files[0].ComponentRole = "runtime"
	case "missing":
		if err := os.Remove(fixture.mappingPath); err != nil {
			t.Fatal(err)
		}
		return
	case "malformed":
		if err := os.WriteFile(fixture.mappingPath, []byte(`{"schema":`), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	writeArtifactFixtureSpec(t, fixture.mappingPath, fixture.spec)
}

func TestArtifactCLIRejectsOutputOverlapBeforeBind(t *testing.T) {
	for _, destination := range []string{"source", "mapping", "weight", "missing-weight", "absent-parent"} {
		t.Run(destination, func(t *testing.T) {
			fixture := artifactFixture(t, "matched")
			path := artifactConflictingOutput(t, fixture, destination)
			before, beforeErr := os.ReadFile(path)
			args := append(fixture.args("none"), "--out", path)
			_, code := captureTopStderr(t, func() int { return cmdArtifactWithBinder(t.Context(), args, artifactNeverBind(t)) })
			if code != exitError {
				t.Fatalf("overlapping output exited %d", code)
			}
			if errors.Is(beforeErr, os.ErrNotExist) {
				assertArtifactPathMissing(t, path)
				return
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(before) {
				t.Fatalf("input was changed: %v", err)
			}
		})
	}
}

func artifactConflictingOutput(t *testing.T, fixture artifactCLIFixture, destination string) string {
	t.Helper()
	switch destination {
	case "source":
		return fixture.sourcePath
	case "mapping":
		return fixture.mappingPath
	case "weight":
		return fixture.localPath
	case "missing-weight":
		if err := os.Remove(fixture.localPath); err != nil {
			t.Fatal(err)
		}
		return fixture.localPath
	default:
		return filepath.Join(filepath.Dir(fixture.outputPath), "missing", "binding.json")
	}
}

func assertArtifactPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected output %s: %v", path, err)
	}
}

func TestArtifactCLICancellationBeforeHashDoesNotPublish(t *testing.T) {
	fixture := artifactFixture(t, "matched")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if code := cmdArtifactWithBinder(ctx, fixture.args("none"), artifactNeverBind(t)); code != exitInterrupt {
		t.Fatalf("pre-cancelled bind exited %d", code)
	}
	assertArtifactPathMissing(t, fixture.outputPath)
}

func TestArtifactCLIStartedCancellationSavesReceipt(t *testing.T) {
	fixture := artifactFixture(t, "matched")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	bind := func(_ context.Context, resolution source.Resolution, spec artifact.Spec, options artifact.Options) (artifact.Binding, error) {
		binding, err := artifact.Bind(t.Context(), resolution, spec, options)
		cancel()
		return binding, err
	}
	if code := cmdArtifactWithBinder(ctx, fixture.args("none"), bind); code != exitInterrupt {
		t.Fatalf("started cancellation exited %d", code)
	}
	if _, err := artifact.LoadBinding(fixture.outputPath); err != nil {
		t.Fatalf("completed observation was lost after interruption: %v", err)
	}
}

func TestArtifactCLIBindErrorsNeverPublish(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		fixture := artifactFixture(t, "matched")
		ctx, cancel := context.WithCancel(t.Context())
		bind := func(context.Context, source.Resolution, artifact.Spec, artifact.Options) (artifact.Binding, error) {
			if interrupted {
				cancel()
			}
			return artifact.Binding{}, errors.New("file observation failed")
		}
		_, code := captureTopStderr(t, func() int { return cmdArtifactWithBinder(ctx, fixture.args("none"), bind) })
		cancel()
		want := exitError
		if interrupted {
			want = exitInterrupt
		}
		if code != want {
			t.Fatalf("interrupted=%t exited %d", interrupted, code)
		}
		assertArtifactPathMissing(t, fixture.outputPath)
	}
}

func TestArtifactCLIHelpAndDispatch(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"--help"}, {"-h"}, {"bind", "--help"}, {"show", "--help"}} {
		_, code := captureTopStderr(t, func() int { return cmdArtifact(t.Context(), args) })
		if code != exitOK {
			t.Fatalf("help %v exited %d", args, code)
		}
	}
	if commandHandler("artifact") == nil || !strings.Contains(usageText, "fitr artifact bind") {
		t.Fatal("artifact is missing from CLI dispatch or global help")
	}
}

func TestArtifactCLIDefaultLimitsAndEqualsFlags(t *testing.T) {
	command, code, ok := parseArtifactBind([]string{"--source=source.json", "--mapping=mapping.json", "--out=binding.json"})
	if !ok || code != exitOK || command.options.MaxBytes != artifact.DefaultMaxBytes || command.options.Timeout != artifact.DefaultTimeout {
		t.Fatalf("default limits changed: ok=%t code=%d command=%+v", ok, code, command)
	}
	if command.sourcePath != "source.json" || command.mappingPath != "mapping.json" || command.outputPath != "binding.json" {
		t.Fatalf("equals flags lost their values: %+v", command)
	}
}

func TestArtifactCLIRejectsUnavailableSourceBeforeBind(t *testing.T) {
	fixture := artifactFixture(t, "matched")
	if err := os.WriteFile(fixture.sourcePath, []byte(`{"schema":"invalid"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, code := captureTopStderr(t, func() int {
		return cmdArtifactWithBinder(t.Context(), fixture.args("none"), artifactNeverBind(t))
	})
	if code != exitError {
		t.Fatalf("invalid source exited %d", code)
	}
	assertArtifactPathMissing(t, fixture.outputPath)
}

func TestArtifactCLIOutputCreatedDuringBindIsPreserved(t *testing.T) {
	fixture := artifactFixture(t, "matched")
	bind := func(ctx context.Context, resolution source.Resolution, spec artifact.Spec, options artifact.Options) (artifact.Binding, error) {
		binding, err := artifact.Bind(ctx, resolution, spec, options)
		if err != nil {
			return binding, err
		}
		if err := os.WriteFile(fixture.outputPath, []byte("keep concurrent output"), 0o600); err != nil {
			t.Fatal(err)
		}
		return binding, nil
	}
	_, code := captureTopStderr(t, func() int { return cmdArtifactWithBinder(t.Context(), fixture.args("none"), bind) })
	data, err := os.ReadFile(fixture.outputPath)
	if code != exitError || err != nil || string(data) != "keep concurrent output" {
		t.Fatalf("publication overwrote concurrent output: code=%d err=%v data=%q", code, err, data)
	}
}
