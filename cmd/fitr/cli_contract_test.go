package main

import (
	"context"
	"os"
	"strconv"
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
		{"decide", func() int { return cmdDecide(ctx, []string{"model-a", "extra", "--spec", "decision.json"}) }},
		{"experiment", func() int { return cmdExperiment(ctx, []string{"unknown"}) }},
		{"experiment confirm", func() int {
			return cmdExperiment(ctx, []string{"confirm", "a", "b", "c", "d", "e", "--spec", "decision.json"})
		}},
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
		{"decide", func() int { return cmdDecide(ctx, []string{"--help"}) }},
		{"experiment", func() int { return cmdExperiment(ctx, []string{"--help"}) }},
		{"experiment context", func() int { return cmdExperiment(ctx, []string{"context", "--help"}) }},
		{"experiment quant", func() int { return cmdExperiment(ctx, []string{"quant", "--help"}) }},
		{"experiment confirm", func() int { return cmdExperiment(ctx, []string{"confirm", "--help"}) }},
		{"experiment workload", func() int { return cmdExperiment(ctx, []string{"workload", "--help"}) }},
		{"experiment reference", func() int { return cmdExperiment(ctx, []string{"reference", "--help"}) }},
		{"experiment modelset", func() int { return cmdExperiment(ctx, []string{"modelset", "--help"}) }},
		{"screenshots", func() int { return cmdScreenshots(ctx, []string{"--help"}) }},
	}
	for _, width := range []string{"80", "40"} {
		t.Run("width "+width, func(t *testing.T) {
			t.Setenv("FITR_WIDTH", width)
			limit, err := strconv.Atoi(width)
			if err != nil {
				t.Fatal(err)
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					stdout := ""
					stderr, code := captureTopStderr(t, func() int {
						var inner int
						stdout, inner = captureTopStdout(t, tc.run)
						return inner
					})
					assertPublicHelp(t, tc.name, stdout, stderr, code, limit)
				})
			}
		})
	}
}

func assertPublicHelp(t *testing.T, name, stdout, stderr string, code, limit int) {
	t.Helper()
	if code != exitOK {
		t.Fatalf("help exit = %d, want %d; stdout=%q stderr=%q", code, exitOK, stdout, stderr)
	}
	if stderr != "" {
		t.Fatalf("help wrote to stderr: %q", stderr)
	}
	if !strings.Contains(strings.ToLower(stdout), "usage") {
		t.Fatalf("help did not include usage: %q", stdout)
	}
	if strings.Contains(stdout, "Usage of ") || strings.Contains(stdout, "-backend string") {
		t.Fatalf("help used the flag package's page: %q", stdout)
	}
	if name == "run" && !strings.Contains(stdout, "--backend") {
		t.Fatalf("run help missing --backend: %q", stdout)
	}
	for _, line := range strings.Split(stdout, "\n") {
		if len(line) > limit {
			t.Fatalf("help line is %d columns, wider than %d: %q", len(line), limit, line)
		}
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
		{"confirmation context", func() int {
			return cmdExperiment(ctx, []string{"confirm", "model-a", "model-b", "--spec", "decision.json", "--ctx=-1"})
		}, "measurement plan"},
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

func TestRunRejectsANonNumericContextInTheNoticeVoice(t *testing.T) {
	stdout, stderr, code := captureCommandOutput(t, func() int {
		return cmdRun(context.Background(), []string{"model-a", "--ctx", "abc"})
	})
	if code != exitUsage || stdout != "" {
		t.Fatalf("exit %d stdout %q\n%s", code, stdout, stderr)
	}
	if strings.Contains(stderr, "flag -ctx") || strings.Contains(stderr, "ParseInt") || strings.Contains(stderr, "invalid value") || strings.Contains(stderr, "usage:") {
		t.Fatalf("raw flag parse error or help dump:\n%s", stderr)
	}
	if !strings.Contains(stderr, `error: invalid --ctx value "abc"`) || !strings.Contains(stderr, " note:") {
		t.Fatalf("stderr:\n%s", stderr)
	}
}

func TestAdviseRejectsANonNumericContextInTheNoticeVoice(t *testing.T) {
	stderr, code := captureTopStderr(t, func() int {
		return cmdAdvise(context.Background(), []string{"qwen3-8b", "--ctx", "abc"})
	})
	if code != exitUsage {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	if strings.Contains(stderr, "flag -ctx") || strings.Contains(stderr, "ParseInt") || strings.Contains(stderr, "invalid value") {
		t.Fatalf("raw flag parse error:\n%s", stderr)
	}
	if !strings.Contains(stderr, `error: invalid --ctx value "abc"`) || !strings.Contains(stderr, " note:") {
		t.Fatalf("stderr:\n%s", stderr)
	}
}

func TestMistypedSubcommandHintsBeforeDiscovery(t *testing.T) {
	previous := os.Args
	t.Cleanup(func() { os.Args = previous })
	os.Args = []string{"fitr", "sugest"}
	stderr, code := captureTopStderr(t, run)
	if code != exitUsage || strings.Contains(stderr, "no serving runtime") || strings.Contains(stderr, "not installed") ||
		!strings.Contains(stderr, `error: "sugest" is not a command`) || !strings.Contains(stderr, "did you mean fitr suggest?") {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	os.Args = []string{"fitr", "boad"}
	stderr, code = captureTopStderr(t, run)
	if code != exitUsage || strings.Contains(stderr, "no serving runtime") || !strings.Contains(stderr, "fitr board") {
		t.Fatalf("board exit %d\n%s", code, stderr)
	}
	os.Args = []string{"fitr", "frobnicate"}
	stderr, code = captureTopStderr(t, run)
	if code == exitOK || strings.Contains(stderr, "did you mean fitr") || strings.Contains(stderr, "is not a command") {
		t.Fatalf("distant token was treated as a command: exit %d\n%s", code, stderr)
	}
	os.Args = []string{"fitr", "Suggest"}
	stderr, code = captureTopStderr(t, run)
	if code != exitUsage || !strings.Contains(stderr, "did you mean fitr suggest?") {
		t.Fatalf("case exit %d\n%s", code, stderr)
	}
}

func TestClosestCommandLeavesModelReferencesAlone(t *testing.T) {
	for _, typed := range []string{"", "suggest", "qwen3:8b", "models/foo", `C:\m`, "model.gguf", "frobnicate"} {
		if got, ok := closestCommand(typed); ok {
			t.Fatalf("%q hinted %q", typed, got)
		}
	}
	for _, typed := range []struct{ in, want string }{
		{"sugest", "suggest"},
		{"boad", "board"},
		{"Suggest", "suggest"},
		{"HELP", "help"},
	} {
		got, ok := closestCommand(typed.in)
		if !ok || got != typed.want {
			t.Fatalf("%q -> %q ok=%v, want %s", typed.in, got, ok, typed.want)
		}
	}
}

func TestHintListCoversEveryHelpCommand(t *testing.T) {
	listed := map[string]bool{}
	for _, name := range userCommands {
		if listed[name] {
			t.Fatalf("duplicate hint %s", name)
		}
		listed[name] = true
	}
	for _, line := range strings.Split(usageText, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 || fields[0] != "fitr" {
			continue
		}
		name := fields[1]
		if strings.HasPrefix(name, "<") || strings.HasPrefix(name, "[") || strings.HasPrefix(name, "-") {
			continue
		}
		if !listed[name] {
			t.Errorf("help names %q, the hint list does not", name)
		}
	}
	for _, name := range userCommands {
		if name == "screenshots" || name == "version" {
			continue
		}
		if !strings.Contains(usageText, "fitr "+name) {
			t.Errorf("hint list names %q, help does not", name)
		}
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
