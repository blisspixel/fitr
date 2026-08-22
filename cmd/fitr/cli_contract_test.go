package main

import (
	"context"
	"strings"
	"testing"
)

func TestPublicCommandsRejectUnexpectedArguments(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		run  func() int
	}{
		{"advise", func() int { return cmdAdvise(ctx, []string{"model-a", "extra"}) }},
		{"apply", func() int { return cmdApply(ctx, []string{"model-a", "extra", "--ctx", "4096"}) }},
		{"tune", func() int { return cmdTune(ctx, []string{"model-a"}) }},
		{"run", func() int { return cmdRun(ctx, []string{"model-a", "extra"}) }},
		{"export", func() int { return cmdExport(ctx, []string{"model-a", "extra"}) }},
		{"view", func() int { return cmdView(ctx, []string{"model-a", "extra"}) }},
		{"board", func() int { return cmdBoard(ctx, []string{"extra"}) }},
		{"top", func() int { return cmdTop(ctx, []string{"extra"}) }},
		{"diag", func() int { return cmdDiag(ctx, []string{"model-a", "extra"}) }},
		{"doctor", func() int { return cmdDoctor(ctx, []string{"model-a", "extra"}) }},
		{"device", func() int { return cmdDevice(ctx, []string{"extra"}) }},
		{"profiles", func() int { return cmdProfiles(ctx, []string{"new", "box", "extra"}) }},
		{"calibrate", func() int { return cmdCalibrate(ctx, []string{"model-a", "model-b", "extra"}) }},
		{"compare", func() int { return cmdCompare(ctx, []string{"model-a", "model-b", "extra"}) }},
		{"screenshots", func() int { return cmdScreenshots(ctx, []string{"one", "extra"}) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout := ""
			stderr, code := captureTopStderr(t, func() int {
				var inner int
				stdout, inner = captureTopStdout(t, tc.run)
				return inner
			})
			if code != exitUsage {
				t.Fatalf("exit = %d, want %d; stdout=%q stderr=%q", code, exitUsage, stdout, stderr)
			}
			if stdout != "" {
				t.Fatalf("invalid invocation wrote to stdout: %q", stdout)
			}
			if !strings.Contains(stderr, "error:") || !strings.Contains(stderr, "hint:") {
				t.Fatalf("diagnostic needs an error and next action: %q", stderr)
			}
		})
	}
}

func TestPublicCommandHelpIsSuccessful(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		run  func() int
	}{
		{"advise", func() int { return cmdAdvise(ctx, []string{"--help"}) }},
		{"apply", func() int { return cmdApply(ctx, []string{"--help"}) }},
		{"tune", func() int { return cmdTune(ctx, []string{"--help"}) }},
		{"run", func() int { return cmdRun(ctx, []string{"--help"}) }},
		{"export", func() int { return cmdExport(ctx, []string{"--help"}) }},
		{"view", func() int { return cmdView(ctx, []string{"--help"}) }},
		{"board", func() int { return cmdBoard(ctx, []string{"--help"}) }},
		{"top", func() int { return cmdTop(ctx, []string{"--help"}) }},
		{"top run", func() int { return cmdTop(ctx, []string{"run", "--help"}) }},
		{"top history", func() int { return cmdTop(ctx, []string{"history", "--help"}) }},
		{"diag", func() int { return cmdDiag(ctx, []string{"--help"}) }},
		{"doctor", func() int { return cmdDoctor(ctx, []string{"--help"}) }},
		{"device", func() int { return cmdDevice(ctx, []string{"--help"}) }},
		{"profiles", func() int { return cmdProfiles(ctx, []string{"--help"}) }},
		{"calibrate", func() int { return cmdCalibrate(ctx, []string{"--help"}) }},
		{"calibrate merge", func() int { return cmdCalibrate(ctx, []string{"merge", "--help"}) }},
		{"compare", func() int { return cmdCompare(ctx, []string{"--help"}) }},
		{"screenshots", func() int { return cmdScreenshots(ctx, []string{"--help"}) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout := ""
			stderr, code := captureTopStderr(t, func() int {
				var inner int
				stdout, inner = captureTopStdout(t, tc.run)
				return inner
			})
			if code != exitOK {
				t.Fatalf("help exit = %d, want %d; stdout=%q stderr=%q", code, exitOK, stdout, stderr)
			}
			if stdout != "" {
				t.Fatalf("help wrote to stdout: %q", stdout)
			}
			if !strings.Contains(strings.ToLower(stderr), "usage") {
				t.Fatalf("help did not include usage: %q", stderr)
			}
		})
	}
}

func TestPublicCommandsRejectInvalidNumericInputsBeforeRuntimeDiscovery(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		run  func() int
		want string
	}{
		{"advise context", func() int { return cmdAdvise(ctx, []string{"model-a", "--ctx=-1"}) }, "context size"},
		{"advise VRAM", func() int { return cmdAdvise(ctx, []string{"model-a", "--vram-gb=-2"}) }, "VRAM size"},
		{"apply context", func() int { return cmdApply(ctx, []string{"model-a", "--ctx=-1"}) }, "context size"},
		{"run context", func() int { return cmdRun(ctx, []string{"model-a", "--ctx=-1"}) }, "context size"},
		{"diag context", func() int { return cmdDiag(ctx, []string{"model-a", "--ctx=-1"}) }, "context size"},
		{"doctor context", func() int { return cmdDoctor(ctx, []string{"model-a", "--ctx=-1"}) }, "context size"},
		{"doctor repeats", func() int { return cmdDoctor(ctx, []string{"model-a", "-n=1"}) }, "repeat count"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stderr, code := captureTopStderr(t, tc.run)
			if code != exitUsage || !strings.Contains(stderr, tc.want) {
				t.Fatalf("exit=%d stderr=%q, want usage mentioning %q", code, stderr, tc.want)
			}
		})
	}
}

func TestPermuteKeepsTopViewValueWithFollowingBooleanFlag(t *testing.T) {
	got := permute([]string{"--view", "board", "--snapshot"})
	want := []string{"--view", "board", "--snapshot"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("permuted args = %q, want %q", got, want)
	}
}

func TestRunFailureHintNamesOpenAIIdentityRemedy(t *testing.T) {
	err := testError("verify resolved model: openai-compat model identity requires FITR_OPENAI_MODEL_SHA256")
	hint := runFailureHint(err, "model-a")
	for _, want := range []string{"FITR_OPENAI_MODEL_SHA256", "independent SHA-256", "same digest"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("identity hint missing %q: %q", want, hint)
		}
	}
	if strings.Contains(hint, "-v") {
		t.Fatalf("identity hint still recommends a flag that adds no error detail: %q", hint)
	}
}

type testError string

func (e testError) Error() string { return string(e) }
