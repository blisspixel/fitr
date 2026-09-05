package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/blisspixel/fitr/internal/llamaserver"
	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/modelref"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/openaicompat"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/render"
)

// newBackend resolves which serving runtime to measure through.
// Selection order: --backend flag, then $FITR_BACKEND, then auto-probe
// (Ollama first - it is the default URL people have running - then
// llama-server, then any OpenAI-compatible server at the LM Studio port).
//
// A Hugging Face ref (pasted URL or hf.co/...) is an Ollama pull, not a
// label to file results under while measuring whatever else is already
// loaded. Prefer Ollama; refuse to pretend llama-server fetched it.
func newBackend(ctx context.Context, model, kind string, pull bool) (llm.Backend, int) {
	return newBackendWithDisplay(ctx, model, kind, pull, nil)
}

func newBackendWithDisplay(ctx context.Context, model, kind string, pull bool, disp render.Display) (llm.Backend, int) {
	if err := validateModelRefs(model); err != nil {
		backendError(disp, err.Error(), "", hfModelRefHint)
		return nil, exitUsage
	}
	if kind == "" || kind == "auto" {
		kind = os.Getenv("FITR_BACKEND")
	}
	if isHFRef(model) && (kind == "" || kind == "auto" || kind == "ollama") {
		return huggingFaceBackend(ctx, model, pull, disp)
	}
	switch kind {
	case "", "auto":
		return discoveredBackend(ctx, model, pull, disp)
	case "ollama":
		return reachableBackend(ctx, ollama.New(), model, pull, disp,
			"cannot reach Ollama", "start it with `ollama serve`, or set OLLAMA_BASE_URL")
	case "llama-server", "llamaserver":
		return reachableBackend(ctx, llamaserver.New(), model, pull, disp,
			"cannot reach llama-server", "start it with `llama-server -m model.gguf`, or set LLAMA_SERVER_URL")
	case "openai":
		return reachableBackend(ctx, openaicompat.New(), model, pull, disp,
			"cannot reach an OpenAI-compatible server", "start LM Studio / vLLM / SGLang, or set FITR_OPENAI_URL")
	default:
		backendError(disp, fmt.Sprintf("unknown backend %q", kind), "",
			"valid: auto, ollama, llama-server, openai")
		return nil, exitUsage
	}
}

func huggingFaceBackend(ctx context.Context, model string, pull bool, disp render.Display) (llm.Backend, int) {
	backend := ollama.New()
	if !backend.Reachable(ctx) {
		backendError(disp, "Hugging Face refs need a running Ollama",
			"Ollama pulls GGUFs from hf.co/{user}/{repo}[:quant]; other servers already have a model loaded",
			"start `ollama serve` and re-run, or pass the name of a model already being served")
		return nil, exitError
	}
	return checkModelWithDisplay(ctx, backend, model, pull, disp)
}

func discoveredBackend(ctx context.Context, model string, pull bool, disp render.Display) (llm.Backend, int) {
	found, err := llm.Discover(ctx)
	if err != nil {
		backendError(disp, "invalid runtime discovery configuration", err.Error(),
			"fix FITR_DISCOVER_URLS or the configured backend URL, then re-run")
		return nil, exitUsage
	}
	if len(found) == 0 {
		candidates, _ := llm.Candidates()
		backendError(disp, "no serving runtime reachable", "tried "+strings.Join(candidates, ", "),
			"start one, or point fitr at it: OLLAMA_BASE_URL, LLAMA_SERVER_URL, FITR_OPENAI_URL, FITR_DISCOVER_URLS, or --backend")
		return nil, exitError
	}
	warnMultipleBackends(found, disp)
	backend, err := backendAt(found[0].Kind, found[0].URL)
	if err != nil {
		backendError(disp, "could not configure discovered runtime", err.Error(),
			"set --backend and the matching endpoint environment variable explicitly")
		return nil, exitError
	}
	return checkModelWithDisplay(ctx, backend, model, pull, disp)
}

func warnMultipleBackends(found []llm.Found, disp render.Display) {
	if len(found) <= 1 {
		return
	}
	extra := make([]string, 0, len(found)-1)
	for _, backend := range found[1:] {
		extra = append(extra, backend.Kind+" at "+backend.URL)
	}
	message := fmt.Sprintf("also found %s; using %s; set --backend or a URL environment variable to choose",
		strings.Join(extra, ", "), found[0].Kind)
	if disp != nil {
		disp.Note(message, "warn")
		return
	}
	fmt.Fprintf(os.Stderr, "! also found %s - using %s at %s; set --backend or a URL env to pick\n",
		terminalText(strings.Join(extra, ", ")), terminalText(found[0].Kind), terminalText(found[0].URL))
}

func reachableBackend(ctx context.Context, backend llm.Backend, model string, pull bool,
	disp render.Display, message, hint string) (llm.Backend, int) {
	if !backend.Reachable(ctx) {
		backendError(disp, message, "every measurement needs a running server", hint)
		return nil, exitError
	}
	return checkModelWithDisplay(ctx, backend, model, pull, disp)
}

func backendAt(kind, url string) (llm.Backend, error) {
	url = strings.TrimRight(url, "/")
	switch kind {
	case "llama-server", "llamaserver":
		c := llamaserver.New()
		if url != "" {
			c.BaseURL = url
		}
		return c, nil
	case "openai":
		if url == "" {
			return openaicompat.New(), nil
		}
		return openaicompat.NewAt(url, openaicompat.CredentialsDisabled)
	case "ollama":
		c := ollama.New()
		if url != "" {
			c.BaseURL = url
		}
		return c, nil
	default:
		return nil, fmt.Errorf("unknown backend %q", kind)
	}
}

// probeBackend is the no-error variant for commands that merely display state.
func probeBackend(ctx context.Context) llm.Backend {
	if configured := strings.TrimSpace(os.Getenv("FITR_BACKEND")); configured != "" && configured != "auto" {
		if kind, ok := canonicalBackendKind(configured); ok {
			if b, err := backendAt(kind, ""); err == nil {
				return b
			}
		}
	}
	found, _ := llm.Discover(ctx)
	if len(found) == 0 {
		return ollama.New()
	}
	b, err := backendAt(found[0].Kind, found[0].URL)
	if err != nil {
		return ollama.New()
	}
	return b
}

func canonicalBackendKind(kind string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "auto":
		return strings.ToLower(strings.TrimSpace(kind)), true
	case "ollama":
		return "ollama", true
	case "llama-server", "llamaserver":
		return "llama-server", true
	case "openai":
		return "openai", true
	default:
		return "", false
	}
}

func backendError(disp render.Display, message, note, hint string) {
	if disp == nil {
		errPrint(message, note, hint)
		return
	}
	if failed, ok := disp.(runFailureTelemetry); ok {
		failed.RunFailed(errors.New(message))
		return
	}
	detail := message
	if note != "" {
		detail += ": " + note
	}
	if hint != "" {
		detail += "; " + hint
	}
	disp.Note(detail, "warn")
}

// checkModelWithDisplay verifies the model label against what the backend
// serves. On Ollama a missing model is a hard error with a pull hint, or an
// automatic pull when the caller allows it. A single-model server ignores the
// label at request time, so a mismatch there is a warning; otherwise results
// would be filed under a name the server never saw.
func checkModelWithDisplay(ctx context.Context, b llm.Backend, model string, pull bool, disp render.Display) (llm.Backend, int) {
	if model == "" {
		return b, exitOK
	}
	tags, err := b.Tags(ctx)
	if err != nil {
		backendError(disp, "could not list models from "+b.Name(), err.Error(),
			"check the runtime logs and its model inventory endpoint")
		return nil, exitError
	}
	found, near := findServedModel(model, tags)
	if found {
		return b, exitOK
	}
	if b.Name() != "ollama" {
		return checkSingleModelBackend(b, model, pull, tags, disp)
	}
	// Pasting an HF URL is the request to fetch it. Regular Ollama tags
	// still need --pull so a typo does not start a multi-gigabyte download.
	if pull || isHFRef(model) {
		if client, ok := b.(*ollama.Client); ok {
			return pullOllamaModel(ctx, client, b, model, disp)
		}
	}
	hint := "pull it first: `ollama pull " + model + "`, or re-run with --pull"
	if len(near) > 0 {
		hint = "did you mean: " + strings.Join(near, ", ")
	}
	backendError(disp, fmt.Sprintf("model %q is not installed", presentationModelLabel(model)),
		fmt.Sprintf("%d model(s) available", len(tags)), hint)
	return nil, exitUsage
}

func findServedModel(model string, tags []ollama.ModelInfo) (bool, []string) {
	found := false
	var near []string
	base := strings.SplitN(model, ":", 2)[0]
	for _, tag := range tags {
		if modelref.SameServed(model, tag.Name) {
			found = true
		}
		if strings.Contains(tag.Name, base) {
			near = append(near, tag.Name)
		}
	}
	return found, near
}

func checkSingleModelBackend(b llm.Backend, model string, pull bool,
	tags []ollama.ModelInfo, disp render.Display) (llm.Backend, int) {
	if len(tags) == 0 {
		backendError(disp, b.Name()+" is serving no models", "the runtime returned an empty model inventory",
			"load a model in the runtime, then re-run fitr")
		return nil, exitError
	}
	if isHFRef(model) {
		backendError(disp, "Hugging Face refs need Ollama to pull",
			b.Name()+" is serving its own model, not fetching from Hugging Face",
			"start Ollama, or pass the served model name instead of an HF URL")
		return nil, exitUsage
	}
	if pull {
		writeUnsupportedPullWarning(b.Name(), disp)
	}
	writeResolvedModelWarning(b.Name(), model, tags[0].Name, disp)
	return b, exitOK
}

func writeUnsupportedPullWarning(backend string, disp render.Display) {
	if disp != nil {
		disp.Note("--pull is an Ollama feature; "+backend+" serves whatever is already loaded", "warn")
		return
	}
	fmt.Fprintf(os.Stderr, "! --pull is an Ollama feature; %s serves whatever is already loaded\n", terminalText(backend))
}

func writeResolvedModelWarning(backend, requested, resolved string, disp render.Display) {
	message := fmt.Sprintf("%s serves %q, not %q; the run manifest will record the resolved model",
		backend, resolved, requested)
	if disp != nil {
		disp.Note(message, "warn")
		return
	}
	fmt.Fprintf(os.Stderr, "! %s serves %q, not %q - the run manifest will record %q\n",
		terminalText(backend), terminalText(resolved), terminalText(requested), terminalText(resolved))
}

func pullOllamaModel(ctx context.Context, client *ollama.Client, backend llm.Backend,
	model string, disp render.Display) (llm.Backend, int) {
	source := "Ollama"
	if isHFRef(model) {
		source = "Hugging Face via Ollama"
	}
	writePullStart(model, source, disp)
	progress := &pullProgress{display: disp}
	err := client.Pull(ctx, model, progress.update)
	writePullDone(err, disp)
	if err != nil {
		backendError(disp, "model pull failed", err.Error(), "")
		return nil, exitError
	}
	return backend, exitOK
}

type pullProgress struct {
	display render.Display
	last    string
}

func (p *pullProgress) update(status string, pct int) {
	line := terminalText(status)
	if pct >= 0 {
		line = fmt.Sprintf("%s %d%%", line, pct)
	}
	if line != p.last && p.display == nil {
		fmt.Fprintf(os.Stderr, "\r  %-60s", line)
		p.last = line
	}
	if live, ok := p.display.(liveTelemetry); ok && pct >= 0 {
		live.LiveProgress(pct, 100, terminalText(status))
	}
}

func writePullStart(model, source string, disp render.Display) {
	if disp != nil {
		disp.Phase("pull", source)
		return
	}
	fmt.Fprintf(os.Stderr, "  pulling %s from %s\n", terminalText(model), terminalText(source))
}

func writePullDone(err error, disp render.Display) {
	if disp == nil {
		fmt.Fprintln(os.Stderr)
		return
	}
	if err == nil {
		disp.Done("pull", 0)
	}
}

// weightsFromTags recovers the on-disk weight size for a served model from the
// runtime's model list. Ollama's /api/show carries architecture metadata but
// no size field, so advise alone would SKIP every Ollama model for "weights
// were not measured" while bare `fitr` printed that model's size on the same
// screen -- inventory has always read the byte total from /api/tags. This is
// still a runtime reading, not an estimate: same source, same model identity.
// Returns 0 when the runtime does not list the model, which stays SKIP.
func weightsFromTags(ctx context.Context, c llm.Backend, model string) int64 {
	tags, err := c.Tags(ctx)
	if err != nil {
		return 0
	}
	for _, t := range tags {
		if modelref.SameServed(model, t.Name) && t.Size > 0 {
			return t.Size
		}
	}
	return 0
}

// verifiedModelArtifactDigest returns the exact runtime-bound artifact digest
// for one served model. Empty is the fail-closed result for missing,
// ambiguous, unverifiable, or conflicting identity evidence.
func verifiedModelArtifactDigest(ctx context.Context, c llm.Backend, model string) string {
	if c == nil || strings.TrimSpace(model) == "" {
		return ""
	}
	tags, err := c.Tags(ctx)
	if err != nil {
		return ""
	}
	var found string
	for _, tag := range tags {
		if !modelref.SameServed(model, tag.Name) {
			continue
		}
		digest := tag.Digest
		if verifier, ok := c.(llm.ModelDigestVerifier); ok {
			digest, err = verifier.VerifyModelDigest(tag.Name, tag.ReportedDigest)
			if err != nil {
				return ""
			}
		}
		digest = strings.ToLower(strings.TrimSpace(digest))
		encoded, ok := strings.CutPrefix(digest, "sha256:")
		if !ok || len(encoded) != 64 {
			return ""
		}
		if _, err := hex.DecodeString(encoded); err != nil {
			return ""
		}
		if found != "" && found != digest {
			return ""
		}
		if found == "" {
			found = digest
		}
	}
	return found
}

func resolveRunModel(ctx context.Context, b llm.Backend, requested string) (resolvedRunModel, error) {
	models, err := b.Tags(ctx)
	if err != nil {
		return resolvedRunModel{}, fmt.Errorf("resolve model from %s: %w", b.Name(), err)
	}
	selected, err := selectResolvedModel(b.Name(), requested, models)
	if err != nil {
		return resolvedRunModel{}, err
	}
	resolved := selected.Name
	shown, err := b.Show(ctx, resolved)
	if err != nil {
		return resolvedRunModel{}, fmt.Errorf("inspect resolved model %q: %w", resolved, err)
	}
	info := mergeModelInfo(selected, shown)
	info.Name = resolved
	if verifier, ok := b.(llm.ModelDigestVerifier); ok {
		digest, err := verifier.VerifyModelDigest(resolved, info.ReportedDigest)
		if err != nil {
			return resolvedRunModel{}, fmt.Errorf("verify resolved model %q: %w", resolved, err)
		}
		info.Digest = digest
	}
	runtimeVersion := strings.TrimSpace(b.Version(ctx))
	if runtimeVersion == "" {
		runtimeVersion = b.Name() + " (version unavailable)"
	}
	localPath := info.Path
	identity, err := record.NewModelIdentity(presentationModelLabel(requested), resolved, b.Name(), runtimeVersion,
		info.Digest, localPath, info.Size)
	if err != nil {
		return resolvedRunModel{}, err
	}
	// The content digest identifies a local artifact without persisting its
	// directory. Paths remain process-local inspection inputs, never results.
	info.Path = ""
	return resolvedRunModel{Name: resolved, Info: info, Identity: identity}, nil
}

type resolvedRunModel struct {
	Name     string
	Info     ollama.ModelInfo
	Identity record.ModelIdentity
}

func mergeModelInfo(listed, shown ollama.ModelInfo) ollama.ModelInfo {
	out := listed
	if shown.Size > 0 {
		out.Size = shown.Size
	}
	if shown.Digest != "" {
		out.Digest = shown.Digest
	}
	if shown.ReportedDigest != "" {
		out.ReportedDigest = shown.ReportedDigest
	}
	if shown.Path != "" {
		out.Path = shown.Path
	}
	if len(shown.Capabilities) > 0 {
		out.Capabilities = shown.Capabilities
	}
	if len(shown.Info) > 0 {
		out.Info = shown.Info
	}
	if shown.Details.ParameterSize != "" {
		out.Details.ParameterSize = shown.Details.ParameterSize
	}
	if shown.Details.QuantizationLevel != "" {
		out.Details.QuantizationLevel = shown.Details.QuantizationLevel
	}
	if shown.Details.Family != "" {
		out.Details.Family = shown.Details.Family
	}
	return out
}

// selectResolvedModel makes the runtime listing authoritative. Mutable user
// aliases may select one exact listing, but ambiguous or absent selections are
// rejected. llama-server is the only exception because it serves exactly one
// launch-time model and ignores the request model field.
func selectResolvedModel(backend, requested string, models []ollama.ModelInfo) (ollama.ModelInfo, error) {
	var matches []ollama.ModelInfo
	for _, candidate := range models {
		if strings.TrimSpace(candidate.Name) != "" && modelref.SameServed(requested, candidate.Name) {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return ollama.ModelInfo{}, fmt.Errorf("model %q resolves to more than one runtime entry", requested)
	}
	if backend == "llama-server" && len(models) == 1 && strings.TrimSpace(models[0].Name) != "" {
		return models[0], nil
	}
	return ollama.ModelInfo{}, fmt.Errorf("model %q did not resolve to an exact runtime entry", requested)
}
