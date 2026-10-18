package main

import (
	"bufio"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/autoruntime"
	"github.com/blisspixel/fitr/internal/fitting"
)

func promptDeps(line string) tailorDeps {
	return tailorDeps{interactive: true, prompt: bufio.NewReader(strings.NewReader(line))}
}

func TestTailorContinueRejectsABadInvocation(t *testing.T) {
	if _, _, code, ok := parseContinueFlags([]string{"--help"}); ok || code != exitOK {
		t.Fatalf("help ok=%v code=%d", ok, code)
	}
	if _, _, code, ok := parseContinueFlags([]string{"extra"}); ok || code != exitUsage {
		t.Fatalf("extra ok=%v code=%d", ok, code)
	}
	if _, _, code, ok := parseContinueFlags([]string{"--display", "sideways"}); ok || code != exitUsage {
		t.Fatalf("display ok=%v code=%d", ok, code)
	}
	display, approve, code, ok := parseContinueFlags([]string{"--display", "plain", "--approve-plan"})
	if !ok || code != exitOK || display != "plain" || !approve {
		t.Fatalf("parsed %q approve=%v ok=%v code=%d", display, approve, ok, code)
	}
	if sessionCanPrompt("json") || sessionCanPrompt("none") || !sessionCanPrompt("plain") {
		t.Fatal("json and none must not prompt")
	}
}

func TestTailorPromptDoesNotInventAnAnswer(t *testing.T) {
	kept := withPrompt(tailorDeps{interactive: true, prompt: bufio.NewReader(strings.NewReader("x"))})
	if kept.prompt == nil {
		t.Fatal("replaced a caller prompt")
	}
	fromIn := withPrompt(tailorDeps{interactive: true, in: strings.NewReader("yes\n")})
	line, err := fromIn.prompt.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "yes" {
		t.Fatalf("line %q %v", line, err)
	}
	if withPrompt(tailorDeps{}).prompt != nil {
		t.Fatal("a non-interactive session gained a prompt")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, code, asked := readTailorAnswer(ctx, promptDeps("yes\n"), "question"); asked || code != exitInterrupt {
		t.Fatalf("interrupt asked=%v code=%d", asked, code)
	}
	if _, code, asked := readTailorAnswer(context.Background(), tailorDeps{interactive: true}, "question"); asked || code != exitUsage {
		t.Fatalf("nil prompt asked=%v code=%d", asked, code)
	}
	if _, code, asked := readTailorAnswer(context.Background(), promptDeps(""), "question"); asked || code != exitUsage {
		t.Fatalf("eof asked=%v code=%d", asked, code)
	}
}

func TestTailorQuestionsRecordOnlyTheGivenAnswer(t *testing.T) {
	ctx := context.Background()
	role, code, ok := fillRole(ctx, tailorPlanFlags{}, promptDeps("daily\n"))
	if !ok || code != exitOK || role.role != "daily" {
		t.Fatalf("role %+v %d %v", role, code, ok)
	}
	work, code, ok := fillWork(ctx, tailorPlanFlags{}, promptDeps("code-review\n"))
	if !ok || code != exitOK || work.workload != "code-review" || len(work.outcomes) != 0 {
		t.Fatalf("workload %+v %d", work, code)
	}
	custom, code, ok := fillWork(ctx, tailorPlanFlags{}, promptDeps("structured_output\n"))
	if !ok || code != exitOK || custom.workload != "" || len(custom.outcomes) != 1 || custom.outcomes[0] != "structured_output" {
		t.Fatalf("outcome %+v %d", custom, code)
	}
	if _, _, ok := fillWork(ctx, tailorPlanFlags{}, tailorDeps{}); ok {
		t.Fatal("a non-interactive session invented work")
	}
	models, code, ok := fillCandidates(ctx, tailorPlanFlags{}, promptDeps(" qwen3:30b, , devstral:24b \n"))
	if !ok || code != exitOK || len(models.candidates) != 2 {
		t.Fatalf("candidates %+v %d", models.candidates, code)
	}
	if _, _, ok := fillCandidates(ctx, tailorPlanFlags{}, promptDeps(" , \n")); ok {
		t.Fatal("an empty model list was accepted")
	}
	window, code, ok := fillContext(ctx, tailorPlanFlags{}, promptDeps("32768\n"))
	if !ok || code != exitOK || window.ctxTokens != 32768 {
		t.Fatalf("ctx %+v %d", window, code)
	}
	if _, _, ok := fillContext(ctx, tailorPlanFlags{}, promptDeps("nope\n")); ok {
		t.Fatal("a non-integer window was accepted")
	}
}

func TestTailorMeaningAndCapacityStayNamed(t *testing.T) {
	ctx := context.Background()
	named, code, ok := fillMeaning(ctx, tailorPlanFlags{desired: 8}, promptDeps("usable-input\n"))
	if !ok || code != exitOK || named.meaning != fitting.ContextUsableInput {
		t.Fatalf("meaning %+v %d %v", named, code, ok)
	}
	if _, _, ok := fillMeaning(ctx, tailorPlanFlags{desired: 8}, promptDeps("tokens\n")); ok {
		t.Fatal("an unnamed context meaning was accepted")
	}
	kept, _, ok := fillMeaning(ctx, tailorPlanFlags{desired: 8, meaning: fitting.ContextTotalWindow}, tailorDeps{})
	if !ok || kept.meaning != fitting.ContextTotalWindow {
		t.Fatal("a sealed meaning was asked again")
	}
	ceiling, reserve, code, ok := parseCapacityAnswer("ceiling 20")
	if !ok || code != exitOK || ceiling != "20" || reserve != "" {
		t.Fatalf("ceiling %q %q %d %v", ceiling, reserve, code, ok)
	}
	ceiling, reserve, code, ok = parseCapacityAnswer("reserve 4")
	if !ok || code != exitOK || ceiling != "" || reserve != "4" {
		t.Fatalf("reserve %q %q %d", ceiling, reserve, code)
	}
	if _, _, _, ok := parseCapacityAnswer("maybe"); ok {
		t.Fatal("an unnamed policy was accepted")
	}
	if _, _, _, ok := parseCapacityAnswer("ceiling"); ok {
		t.Fatal("a policy without a value was accepted")
	}
	filled, code, ok := fillCapacity(ctx, tailorPlanFlags{}, promptDeps("ceiling 20\n"))
	if !ok || code != exitOK || filled.budget != "20" || filled.reserve != "" {
		t.Fatalf("budget %+v %d", filled, code)
	}
	reserved, code, ok := fillCapacity(ctx, tailorPlanFlags{}, promptDeps("reserve 4\n24\n"))
	if !ok || code != exitOK || reserved.reserve != "4" || reserved.memory != "24" {
		t.Fatalf("reserve %+v %d", reserved, code)
	}
}

func TestTailorOwnedPathsAreAskedBeforeInspection(t *testing.T) {
	ctx := context.Background()
	code, ok := refuseUnsupportedOwned(tailorPlanFlags{}, tailorDeps{})
	if !ok || code != exitOK {
		t.Fatalf("unowned %d %v", code, ok)
	}
	code, ok = refuseUnsupportedOwned(tailorPlanFlags{owned: true}, tailorDeps{ownedSupported: func() error { return nil }})
	if !ok || code != exitOK {
		t.Fatalf("supported %d %v", code, ok)
	}
	code, ok = refuseUnsupportedOwned(tailorPlanFlags{owned: true}, tailorDeps{ownedSupported: func() error {
		return errors.New("owned runtime launch requires Windows")
	}})
	if ok || code != exitUnresolved {
		t.Fatalf("unsupported %d %v", code, ok)
	}
	if err := ownedLaunchError(tailorDeps{ownedSupported: func() error { return errors.New("boom") }}); err == nil {
		t.Fatal("hook error was dropped")
	}
	flags, code, ok := fillOwnedPaths(ctx, tailorPlanFlags{owned: true}, promptDeps("C:\\runtime\\ollama.exe\nC:\\runtime\\models\n"))
	if !ok || flags.runtimeExe == "" || flags.models == "" {
		t.Fatalf("paths %+v %d %v", flags, code, ok)
	}
	if _, _, ok := inspectBeforeCreate(ctx, tailorPlanFlags{owned: true}, tailorDeps{}); ok {
		t.Fatal("inspection started without paths")
	}
	spec, code, ok := inspectBeforeCreate(ctx, tailorPlanFlags{}, tailorDeps{})
	if !ok || code != exitOK || spec.Executable != "" {
		t.Fatalf("unowned inspection %+v %d %v", spec, code, ok)
	}
	if _, err := inspectRuntime(ctx, "exe", "models", tailorDeps{inspectRuntime: func(context.Context, string, string) (autoruntime.Spec, error) {
		return autoruntime.Spec{}, errors.New("inspect failed")
	}}); err == nil {
		t.Fatal("inspect error was dropped")
	}
}

func TestTailorPreviewAndApprovalDoNotMeasureOnAHedge(t *testing.T) {
	ctx := context.Background()
	one := previewExtras(ctx, fitting.Plan{Candidates: []string{"qwen3:30b"}}, tailorDeps{memoryLine: func(context.Context) string {
		return "free memory was not read"
	}})
	if len(one) != 3 || !strings.Contains(one[1], "non-comparative") {
		t.Fatalf("one candidate: %v", one)
	}
	many := previewExtras(ctx, fitting.Plan{Candidates: []string{"a", "b"}}, tailorDeps{})
	if !strings.Contains(many[1], "were named") || !strings.Contains(many[0], "not a hardware allocation cap") {
		t.Fatalf("many: %v", many)
	}
	if names, listed, note := previewInventory(ctx, fitting.Session{Plan: fitting.Plan{OwnedRuntime: true}}, tailorDeps{}); listed || names != nil || note != "" {
		t.Fatal("owned runtime listed a store")
	}
	if _, listed, note := previewInventory(ctx, fitting.Session{}, tailorDeps{listModels: func(context.Context, string) ([]string, error) {
		return nil, errors.New("down")
	}}); listed || !strings.Contains(note, "not listed") {
		t.Fatal(note)
	}
	names, listed, note := previewInventory(ctx, fitting.Session{}, tailorDeps{listModels: func(context.Context, string) ([]string, error) {
		return []string{"qwen3:30b"}, nil
	}})
	if !listed || len(names) != 1 || !strings.Contains(note, "qwen3:30b") || !strings.Contains(note, "not a recommendation") {
		t.Fatalf("%v %v %q", names, listed, note)
	}
	if !strings.Contains(inventoryLine(nil), "none") {
		t.Fatal(inventoryLine(nil))
	}
	session := fitting.Session{Plan: fitting.Plan{ID: "fit-preview"}}
	if code, ok := approveSealedPlan(ctx, session, tailorPlanFlags{approve: true}, tailorDeps{}); !ok || code != exitOK {
		t.Fatalf("flag %d %v", code, ok)
	}
	if _, ok := approveSealedPlan(ctx, session, tailorPlanFlags{}, promptDeps("yes\n")); !ok {
		t.Fatal("yes did not approve")
	}
	if _, ok := approveSealedPlan(ctx, session, tailorPlanFlags{}, promptDeps("no\n")); ok {
		t.Fatal("no started measurement")
	}
	if _, ok := approveSealedPlan(ctx, session, tailorPlanFlags{}, tailorDeps{interactive: true}); ok {
		t.Fatal("a missing prompt started measurement")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if code, ok := approveSealedPlan(cancelled, session, tailorPlanFlags{}, promptDeps("yes\n")); ok || code != exitInterrupt {
		t.Fatalf("interrupt %d %v", code, ok)
	}
}

func TestTailorInventoryRecheckAndRuntimeReuse(t *testing.T) {
	ctx := context.Background()
	root := tailorEnv(t)
	code := tailorSession(ctx, []string{"fit-" + strings.Repeat("a", 32)}, tailorDeps{})
	if code != exitError {
		t.Fatalf("missing session exit %d", code)
	}
	if entries, err := filepath.Glob(filepath.Join(root, "*")); err != nil || len(entries) != 0 {
		t.Fatalf("created %v %v", entries, err)
	}
	same := fitting.Session{InventoryChecked: true, InventoryNames: []string{"b", "a"}}
	deps := tailorDeps{listModels: func(context.Context, string) ([]string, error) {
		return []string{"a", "b"}, nil
	}}
	if err := recheckInventory(ctx, same, deps); err != nil {
		t.Fatal(err)
	}
	deps.listModels = func(context.Context, string) ([]string, error) { return nil, errors.New("down") }
	if err := recheckInventory(ctx, same, deps); err == nil || !strings.Contains(err.Error(), "could not be read") {
		t.Fatal(err)
	}
	deps.listModels = func(context.Context, string) ([]string, error) { return []string{"c"}, nil }
	if err := recheckInventory(ctx, same, deps); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatal(err)
	}
	if !sameNames([]string{"b", "a"}, []string{"a", "b"}) || sameNames([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("name comparison")
	}
	blank := autoruntime.Spec{ExecutableSHA256: "sha256:" + strings.Repeat("ab", 32)}
	if runtimeChanged(fitting.Session{}, blank) {
		t.Fatal("a blank inspection was treated as a change")
	}
	saved := fitting.Session{InspectedExecutableSHA256: blank.ExecutableSHA256, InspectedLibrariesSHA256: blank.ExecutableSHA256, InspectedRuntimeVersion: "0.11.4"}
	if !runtimeChanged(saved, autoruntime.Spec{}) || runtimeChanged(saved, autoruntime.Spec{
		ExecutableSHA256: saved.InspectedExecutableSHA256, LibrariesSHA256: saved.InspectedLibrariesSHA256, RuntimeVersion: saved.InspectedRuntimeVersion,
	}) {
		t.Fatal("runtime change detection")
	}
	if code := launchOwnedSession(ctx, fitting.Session{}, "plain", tailorDeps{}); code != exitUsage {
		t.Fatalf("launch %d", code)
	}
	line := tailorObservedMemory(ctx)
	if !strings.Contains(line, "free memory") || !strings.Contains(line, "not a hardware allocation cap") {
		t.Fatal(line)
	}
	if _, err := tailorListInstalled(ctx, ""); err == nil {
		t.Fatal("listed models without an endpoint")
	}
	if _, err := tailorListInstalled(ctx, "owned-process"); err == nil {
		t.Fatal("listed models for an owned process")
	}
}

func TestExclusiveRuntimeReuseRejectsADifferentSpec(t *testing.T) {
	digest := "sha256:" + strings.Repeat("ab", 32)
	spec := autoruntime.Spec{
		Schema: autoruntime.SpecSchema, Executable: `C:\runtime\ollama.exe`, ModelStore: `C:\runtime\models`,
		ExecutableSHA256: digest, LibrariesSHA256: digest, RuntimeVersion: "0.11.4",
		NumCtx: 32768, KVCacheType: "f16", FlashAttention: true, ReserveBytes: 1024,
	}
	path := filepath.Join(t.TempDir(), "runtime.json")
	if err := writeExclusiveRuntime(filepath.Join(t.TempDir(), "missing", "runtime.json"), spec); err == nil {
		t.Fatal("wrote through a missing directory")
	}
	if err := writeExclusiveRuntime(path, spec); err != nil {
		t.Fatal(err)
	}
	if got, err := reuseRuntimeFile(path, spec); err != nil || got != path {
		t.Fatalf("reuse %q %v", got, err)
	}
	spec.NumCtx = 4096
	if _, err := reuseRuntimeFile(path, spec); !errors.Is(err, errRuntimeMismatch) {
		t.Fatal(err)
	}
	if _, err := reuseRuntimeFile(filepath.Join(t.TempDir(), "absent.json"), spec); err == nil {
		t.Fatal("missing runtime file was reused")
	}
}
