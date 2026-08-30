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
	if kind == "" || kind == "auto" {
		kind = os.Getenv("FITR_BACKEND")
	}
	if isHFRef(model) && (kind == "" || kind == "auto" || kind == "ollama") {
		o := ollama.New()
		if !o.Reachable(ctx) {
			backendError(disp, "Hugging Face refs need a running Ollama",
				"Ollama pulls GGUFs from hf.co/{user}/{repo}[:quant]; other servers already have a model loaded",
				"start `ollama serve` and re-run, or pass the name of a model already being served")
			return nil, exitError
		}
		return checkModelWithDisplay(ctx, o, model, pull, disp)
	}
	switch kind {
	case "", "auto":
		found, err := llm.Discover(ctx)
		if err != nil {
			backendError(disp, "invalid runtime discovery configuration", err.Error(),
				"fix FITR_DISCOVER_URLS or the configured backend URL, then re-run")
			return nil, exitUsage
		}
		if len(found) == 0 {
			candidates, _ := llm.Candidates()
			backendError(disp, "no serving runtime reachable",
				"tried "+strings.Join(candidates, ", "),
				"start one, or point fitr at it: OLLAMA_BASE_URL, LLAMA_SERVER_URL, FITR_OPENAI_URL, FITR_DISCOVER_URLS, or --backend")
			return nil, exitError
		}
		if len(found) > 1 {
			var extra []string
			for _, f := range found[1:] {
				extra = append(extra, f.Kind+" at "+f.URL)
			}
			message := fmt.Sprintf("also found %s; using %s; set --backend or a URL environment variable to choose",
				strings.Join(extra, ", "), found[0].Kind)
			if disp != nil {
				disp.Note(message, "warn")
			} else {
				fmt.Fprintf(os.Stderr, "! also found %s - using %s at %s; set --backend or a URL env to pick\n",
					terminalText(strings.Join(extra, ", ")), terminalText(found[0].Kind), terminalText(found[0].URL))
			}
		}
		b, err := backendAt(found[0].Kind, found[0].URL)
		if err != nil {
			backendError(disp, "could not configure discovered runtime", err.Error(),
				"set --backend and the matching endpoint environment variable explicitly")
			return nil, exitError
		}
		return checkModelWithDisplay(ctx, b, model, pull, disp)
	case "ollama":
		o := ollama.New()
		if !o.Reachable(ctx) {
			backendError(disp, "cannot reach Ollama",
				"every measurement needs a running server",
				"start it with `ollama serve`, or set OLLAMA_BASE_URL")
			return nil, exitError
		}
		return checkModelWithDisplay(ctx, o, model, pull, disp)
	case "llama-server", "llamaserver":
		l := llamaserver.New()
		if !l.Reachable(ctx) {
			backendError(disp, "cannot reach llama-server",
				"every measurement needs a running server",
				"start it with `llama-server -m model.gguf`, or set LLAMA_SERVER_URL")
			return nil, exitError
		}
		return checkModelWithDisplay(ctx, l, model, pull, disp)
	case "openai":
		g := openaicompat.New()
		if !g.Reachable(ctx) {
			backendError(disp, "cannot reach an OpenAI-compatible server",
				"every measurement needs a running server",
				"start LM Studio / vLLM / SGLang, or set FITR_OPENAI_URL")
			return nil, exitError
		}
		return checkModelWithDisplay(ctx, g, model, pull, disp)
	default:
		backendError(disp, fmt.Sprintf("unknown backend %q", kind), "",
			"valid: auto, ollama, llama-server, openai")
		return nil, exitUsage
	}
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
	found := false
	var near []string
	base := strings.SplitN(model, ":", 2)[0]
	for _, t := range tags {
		if modelref.SameServed(model, t.Name) {
			found = true
		}
		if strings.Contains(t.Name, base) {
			near = append(near, t.Name)
		}
	}
	if found {
		return b, exitOK
	}
	if b.Name() != "ollama" {
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
			if disp != nil {
				disp.Note("--pull is an Ollama feature; "+b.Name()+" serves whatever is already loaded", "warn")
			} else {
				fmt.Fprintf(os.Stderr, "! --pull is an Ollama feature; %s serves whatever is already loaded\n", terminalText(b.Name()))
			}
		}
		message := fmt.Sprintf("%s serves %q, not %q; the run manifest will record the resolved model", b.Name(), tags[0].Name, model)
		if disp != nil {
			disp.Note(message, "warn")
		} else {
			fmt.Fprintf(os.Stderr, "! %s serves %q, not %q - the run manifest will record %q\n",
				terminalText(b.Name()), terminalText(tags[0].Name), terminalText(model), terminalText(tags[0].Name))
		}
		return b, exitOK
	}
	// Pasting an HF URL is the request to fetch it. Regular Ollama tags
	// still need --pull so a typo does not start a multi-gigabyte download.
	if pull || isHFRef(model) {
		o, ok := b.(*ollama.Client)
		if ok {
			src := "Ollama"
			if isHFRef(model) {
				src = "Hugging Face via Ollama"
			}
			if disp != nil {
				disp.Phase("pull", src)
			} else {
				fmt.Fprintf(os.Stderr, "  pulling %s from %s\n", terminalText(model), terminalText(src))
			}
			last := ""
			err := o.Pull(ctx, model, func(status string, pct int) {
				line := terminalText(status)
				if pct >= 0 {
					line = fmt.Sprintf("%s %d%%", line, pct)
				}
				if line != last && disp == nil {
					fmt.Fprintf(os.Stderr, "\r  %-60s", line)
					last = line
				}
				if live, ok := disp.(liveTelemetry); ok && pct >= 0 {
					live.LiveProgress(pct, 100, terminalText(status))
				}
			})
			if disp != nil {
				if err == nil {
					disp.Done("pull", 0)
				}
			} else {
				fmt.Fprintln(os.Stderr)
			}
			if err != nil {
				backendError(disp, "model pull failed", err.Error(), "")
				return nil, exitError
			}
			return b, exitOK
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
