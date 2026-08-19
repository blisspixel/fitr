# distillr - local (Ollama) provider: 3 bugs + 1 config trap

**Found:** 2026-08-17 · **distillr** v0.19.56 (installed from `main`) · **Ollama** 0.32.14
**Env:** Windows 11, Python 3.12.10, AMD Radeon 780M (Vulkan), models in `C:\models`

Summary: on a current Ollama, distillr's local route is **dead on arrival** - `doctor`
reports `Ollama: not running` while Ollama is serving normally. Fixing that unblocks a
second bug that rejects the fastest local coder models. A third bug breaks web discovery
on Python 3.12 whenever Playwright isn't installed. All three are small.

Priority: **#1 blocks all local use** -> **#2 blocks the best local models** -> **#3 blocks
discovery on fresh installs** -> **#4 is a silent perf trap.**

---

## #1 - `/api/tags` parse rejects Ollama's `capabilities` field (BLOCKER)

**File:** `distill/llm/providers/_ollama_registry.py` -> `_bounded_model()`

**Symptom:** `distill doctor` prints `Ollama: not running` and
`Configured: ollama / <model> (not ready)` even though Ollama is up and `/api/tags`
returns 200. Every local command refuses.

**Root cause:** Ollama ≥0.32 added a **top-level** `capabilities` list to each model in
`/api/tags`:

```json
{"name":"qwen3-coder:30b", "size":..., "details":{...},
 "capabilities":["completion","tools"]}
```

`_bounded_model()` handles list values **only** under `details` (via `_bounded_details`).
Every other key goes to `_bounded_scalar()`, which accepts str/int/float/bool/None and
raises on lists:

```
ValueError: Ollama model field 'capabilities' has an invalid value shape
```

That propagates out of `list_models()`. `doctor/checks.py::_check_ollama_status` catches
`(ConnectionError, Exception)` and returns `"unavailable"` - so a **parse** error is
reported to the user as a **connectivity** problem. That masking is arguably the real
defect; the error text never reaches the operator.

**Repro:**
```bash
ollama serve                      # 0.32.x
python -c "import asyncio; from distill.llm.providers.ollama import OllamaProvider; \
           print(asyncio.run(OllamaProvider().list_models()))"
```

**Fix** - mirror the existing `details` list handling, keeping the same bounds:

```python
        if raw_key == "details":
            bounded[raw_key] = _bounded_details(raw_value, limits=limits)
        elif isinstance(raw_value, list):
            values = cast(list[object], raw_value)
            if len(values) > limits.list_items:
                raise ValueError(f"Ollama model field {raw_key!r} list exceeds its item limit")
            bounded[raw_key] = [
                _bounded_scalar(item, field=raw_key, limits=limits) for item in values
            ]
        else:
            bounded[raw_key] = _bounded_scalar(raw_value, field=raw_key, limits=limits)
```

**Also worth doing:** stop flattening parse failures into `"unavailable"` in
`_check_ollama_status`. Distinguish *unreachable* from *reachable but unparseable*, and
surface the exception - this bug would have been a 30-second diagnosis instead of a
stack-trace hunt.

---

## #2 - `_is_thinking_model` prefix match hits `qwen3-coder*` (HTTP 400)

**File:** `distill/llm/providers/_ollama_metadata.py` -> `_is_thinking_model()`

**Symptom:** every non-structured call (analysis, synthesis, report) fails against
`qwen3-coder:30b` / `qwen3-coder-next:*`:

```
HTTP 400 {"error":"\"qwen3-coder:30b\" does not support thinking"}
```

Structured calls survive only because `_uses_structured_json()` forces `think=False`.

**Root cause:**
```python
_THINKING_MODEL_PREFIXES = ("qwen3", "deepseek-r1", "deepseek-v3", "gpt-oss", "gemma4")

def _is_thinking_model(model: str) -> bool:
    model_lower = model.lower()
    return any(model_lower.startswith(prefix) for prefix in _THINKING_MODEL_PREFIXES)
```

`"qwen3-coder:30b".startswith("qwen3")` -> `True`. But qwen3-**coder** is an *instruct*
model with no thinking support, and Ollama rejects the request rather than ignoring the
flag. The prefix list cannot distinguish `qwen3.8` (thinking) from `qwen3-coder` (not).

**Fix - do not extend the prefix list.** Once #1 is fixed, `/api/tags` hands you the
authoritative answer per model:

| model | `capabilities` |
|---|---|
| `qwen3.8:27b` | `['completion','tools','thinking','vision']` |
| `qwen3-coder:30b` | `['completion','tools']` |
| `qwen3-coder-next:q3-agentic` | `['completion']` |
| `huihui_ai/dolphin3-abliterated:8b` | `['completion','tools']` |

So: `think = "thinking" in capabilities_for(model)`, cached off the tags call that
`list_models()` already makes. That deletes the hardcoded list entirely and is correct for
every future model without a code change.

Belt-and-braces: treat a 400 containing `does not support thinking` as retryable once with
`think` removed, so an unknown model degrades instead of failing the run.

---

## #3 - `check_hostname` passed to `HTTPSConnection` breaks on Python 3.12

**File:** `distill/ingestors/net.py` -> `_DeadlineHTTPSHandler.https_open()`

**Symptom:** `distill discover ... --videos-only` dies with:

```
TypeError: HTTPSConnection.__init__() got an unexpected keyword argument 'check_hostname'
```

**Root cause:** Python 3.12 removed `check_hostname` from
`http.client.HTTPSConnection.__init__`. CPython's own `HTTPSHandler.https_open` passes
only `context=` on 3.12+; hostname verification is carried by the `SSLContext`.

**Why it hides:** this is the *fallback* path in
`ingestors/youtube/browser_search.py::_fetch_search_html` - it only runs when the
Playwright browser is unavailable. So it passes on a dev box with browsers installed and
fails on a fresh install, with an error that names neither Playwright nor the real cause.
`pyproject.toml` declares `requires-python >= 3.12`, so this path can never work as
shipped.

**Fix:**
```python
    def https_open(self, req):
        kwargs: dict[str, Any] = {"context": getattr(self, "_context", None)}
        check_hostname = getattr(self, "_check_hostname", None)
        if check_hostname is not None and sys.version_info < (3, 12):
            kwargs["check_hostname"] = check_hostname
        return self.do_open(_DeadlineHTTPSConnection, req, **kwargs)
```
(`net.py` does not currently import `sys`.)

Consider also logging the Playwright failure before falling back, so the fallback's failure
isn't the first thing the user sees.

---

## #4 - per-workload model vars silently fall back to a different local model

**Not a crash - a performance trap.**

With `DISTILL_PROVIDER=ollama` and `DISTILL_MODEL=qwen3-coder:30b` set (and the
`*_PROVIDER` vars all set to `ollama`), a run still loaded **`qwen3.8:27b`** alongside the
configured model - confirmed in the Ollama server log:

```
msg="template selection" model=registry.ollama.ai/library/qwen3.8:27b
msg="template selection" model=registry.ollama.ai/library/qwen3-coder:30b
```

`DISTILL_FAST_MODEL` / `PREMIUM` / `SITE` / `ACCORDION` / `REPORT` were unset and resolved
to something other than `DISTILL_MODEL`. On this hardware that is a **7× slowdown**
(24.8 tok/s -> 3.5 tok/s), plus a second 17 GB model resident.

**Ask:** when the provider is local and a per-workload model is unset, inherit
`DISTILL_MODEL` rather than a tier default - or at minimum log which model each workload
resolved to. Cloud tiers can absorb a silent tier swap; a local box cannot.

**Related:** local calls were issued with `num_ctx` as low as **4096** (seen in
`ollama ps`) while analysing long video transcripts. If that's a tier default rather than a
measured choice, it forces avoidable chunking. Worth making the local `num_ctx` explicit
and configurable.

---

## Verification

With #1-#3 patched locally, the full local pipeline runs green at **$0.00**:

```
distill --cost-mode no-metered doctor
  Ollama:     running  (5 model(s))
  Configured: ollama / qwen3-coder:30b  (ready)

distill --cost-mode no-metered discover "<goal>" --topic pokemon-market \
        --videos-only --video-limit 4 --preview
  Found 44 videos, ~13h 42m of content across 5 search(es)
  Reranking against goal... -> 4 goal-ranked items, scores 0.90/0.85/0.80/0.80
  Estimated ingest cost: ~$0.00
```

Model used: `qwen3-coder:30b` (Q4, MoE ~3B active) - 24.8 tok/s decode, 232 tok/s prefill,
18.9 GB resident @32K on a Radeon 780M via Vulkan. It emitted valid JSON on every
structured call.

**Suggested regression tests**
1. `parse_tags_response` fixture containing a top-level `capabilities` list.
2. `_is_thinking_model` (or its replacement) - assert `qwen3-coder:30b` -> `False` and
   `qwen3.8:27b` -> `True`.
3. `_check_ollama_status` - assert a parse error is reported distinctly from unreachable.
4. `https_open` - assert no `check_hostname` kwarg is forwarded on Python ≥3.12.

---

# Part 2 - Output quality review (local run, qwen3-coder:30b)

Corpus: 2 topics, 7 videos ingested (1 skipped, no transcript), full analyse ->
channel-synthesis -> topic-synthesis -> corpus-synthesis chain. Total run cost
**$0.0000**. Ingest for one topic: 27m 35s (56,797 in / 20,893 out).

**Verdict: the pipeline produces genuinely good work on a local 30B MoE.** Insights
carry direct quotes, per-item prices, source URL/video-id, and a creator's-take section
that separates the creator's claims from fact. Syntheses are well-structured
(consensus / disagreement / signals / takeaways / gaps / what-changed) and attribute
positions to named channels. No repetition loops, no truncation, no JSON leakage into
prose, no refusals. Provenance metadata (`model`, `prompt_id`, `temperature: 0.0`) is
recorded in every artifact - that made this review possible.

## #5 - Claim verifier: number-word blindness produces 100% false positives (HIGH)

**Component:** `*_Verify.json` numeric claim checker (schema_version 3, `mode: warn`)

**Symptom:** one source flagged 17 of 35 claims `unsupported` - every dollar figure and
every percentage. Corpus-wide: `checked=321, supported=303, unsupported=18`, and **all 18
came from that one video**, kinds `{money: 14, percent: 4}`.

**Root cause: the flags are wrong.** The creator narrates numbers as words; the checker
matches numeral tokens literally against the transcript. Verified by hand:

| Insights output | Transcript (verbatim) | Correct? |
|---|---|---|
| `$377 raw (up $30 in one month)` | "sitting around three hundred seventy seven dollars raw, and it just jumped another thirty bucks this month" | yes |
| `Cynthia's Garchomp ex: $237` | "Cynthia's Garchomp ex is at two thirty seven" | yes |
| `Ethan's Ho-Oh ex: just under $200` | "Ethan's Ho-Oh ex is just under two hundred" | yes |
| `Vintage PSA 10 cards have risen 23%` | "vintage PSA ten cards are up twenty three percent" | yes |

`grep -E "[0-9]+"` over that transcript returns only `151 2018 2021 2023 2026`. There are
no numerals to match. The model's extraction was correct in every case checked.

**Why it matters**
- The verifier is the product's trust story. Here it reports ~half a document as
  unsupported when the document is accurate.
- It's source-dependent, not model-dependent: creators who *say* numbers get flagged,
  creators whose captions render numerals don't. **This affects cloud runs identically**  - 
  it is not a local-provider issue.
- In `warn` mode it's noise that trains users to ignore it. In any strict/gating mode it
  would reject correct content from an entire class of sources.

**Fix:** normalise number-words -> numerals on both sides before matching
(`"twenty three percent"` -> `23%`, `"three hundred seventy seven dollars"` -> `$377`,
`"two thirty seven"` -> `237`). Handle the spoken-shorthand form ("two thirty seven",
"eight fifty") as well as formal form. Failing that, downgrade money/percent tokens to
`unverifiable` rather than `unsupported` when the transcript contains no numerals at all  - 
that single guard would have suppressed all 18 false positives here.

**Test case:** the transcript above is an ideal fixture - one source, four spelled-out
formats, zero numerals.

## #6 - Corpus synthesis asserts a time-series it cannot have (MEDIUM)

`pokemon_market_Corpus_Synthesis.md` -> section **"What Changed In The Overall Topic Story"**:

> "Three months ago, content focused on rising prices... Now, there is an emphasis on
> market corrections."

This was a **first run**. The corpus is 4 videos (3 from Aug 2026, 1 from May 2026)
ingested in a single pass. There is no prior synthesis to diff against, so the
"three months ago" baseline is fabricated narrative - the one place in the output where
the model states something the corpus cannot support.

Not really a model failure: the section prompt *asks* for a longitudinal delta
unconditionally. Suggest suppressing it on first synthesis (no previous version to
compare), or constraining it to the corpus's own date range and requiring per-claim source
attribution like the "Strongest Signals" section already does.

## #7 - Inconsistent attribution rigor between synthesis sections (LOW)

Same document, two standards:

- **"Strongest Signals"** - attributes properly: *"Dose of Pokémon and MoczyTCG agree…"*
- **"Cross-Source Consensus"** - asserts unanimity: *"All four channels agree that market
  corrections occur…"*

The second is overstated: one of the four is a grading-company comparison that never
addresses correction cycles, as the document's own "Disagree" section implies. The prompt
already knows how to demand named support in one section; applying the same requirement to
the consensus section would remove the inflation. Small models over-generalise to
"all sources agree" - a template that requires naming supporters resists that.

## #8 - Provenance is lost between layers (MEDIUM, feature request)

Insights carry `url`, `source_id`, `upload_date`. The syntheses - the artifacts you
actually read and query - carry **none of it**. The corpus synthesis cites *"Pika Fun
documents a 23% YoY increase"* with no link back to the video or timestamp. The receipt
exists one layer down but there's no path to it from the claim.

For a corpus meant to be browsed in Obsidian and queried over MCP, synthesis-level claims
should carry at least the source id/url, ideally a timestamp anchor.

## Also worth knowing

- **`num_ctx` varies by stage.** `ollama ps` showed **4096** during ingest and **41487**
  during report on the same model. If the ingest tier default is 4096, long transcripts are
  being chunked far more than necessary on a machine that had 45 GB of headroom. Worth
  making the local `num_ctx` explicit/configurable per stage.
- **Failure reporting is good.** A video without a transcript was skipped, surfaced as
  `1 failed / 1 run issue`, written to `latest_run_errors.md`, with "re-run to retry, already
  ingested sources are skipped." That is the right behaviour and it worked.
- **Cost accounting on local routes reads `~$0.0000` with real token counts** - correct,
  and the `no-metered` guard held throughout with cloud keys absent from the environment.

## #9 - `report` destroys the whole report when the writer echoes a section title (HIGH)

**File:** `distill/pipeline/report/assembly.py` -> `audit_assembled_report()`
(from `report/accordion.py::run_sequential_report`, ~line 346)

**Symptom:** `distill report <topic>` completes research + writing, then dies. **No report
file is written** - ~25-30 min of local inference discarded, traceback the only output.

```
ValueError: assembled report must contain one heading for Evidence Map and Key Findings
ValueError: assembled report must contain one heading for Recommendations and Next Questions
```

Different section each run. **`--no-qa` does NOT avoid it** (verified) - so this is not the
QA/rewrite pass.

**Root cause (confirmed by evidence in a run that succeeded):** the section writer emits
the section title *again* as a heading at the top of its own content. `assemble_report()`
then adds its own `## {title}`, so the title appears twice. The audit requires exactly one:

```python
heading_pattern = re.compile(rf"^##[ 	]+{re.escape(section['title'])}[ 	]*$", re.MULTILINE)
if len(matches) != 1:      # <-- 2 matches when the echo is also H2
    raise ValueError(...)
```

The echo's *heading level is non-deterministic*. In `pokemon_30th_Report.md` (which
succeeded) the echo landed at `###` twice, which the H2 regex ignores:

```
line 36  ## Executive Synthesis
line 38  ### Executive Synthesis          <- echo, H3, harmless
line 78  ## Evidence Map and Key Findings
line 80  ### Evidence Map and Key Findings <- echo, H3, harmless
```

`Evidence Map and Key Findings` is exactly the section that failed on the other topic. Same
echo behaviour; when the model picks `##` instead of `###`, the run is destroyed. That is
why the failing section name changes between runs and why retrying sometimes "fixes" it.

**Fix (in order of value):**
1. **Strip an echoed leading title heading from section content before assembly**, at any
   heading level. That removes the cause and also fixes the cosmetic duplicate `## X` /
   `### X` pair visible in reports that do succeed.
2. **Never discard the work.** The pre-rewrite assembly is valid by construction - on audit
   failure, write it anyway and warn. A duplicated heading beats no report.
3. Persist the pre-audit draft before auditing so failures are recoverable.
4. Make the audit tolerant: require `>= 1` plus the existing ordering check, rather than
   `== 1`.

**Not local-specific.** Any model that restates the section title as an H2 triggers it;
smaller models just do it more often. Worth a fixture test with a section body beginning
`## <same title>`.

**Diagnostic aid:** the current message names the section but not the match count or what
headings *were* present. Adding both turns this from a stack-trace hunt into a one-line
read.

---

# Part 3 - Report quality + validation guidance

Two reports, same pipeline, same model (`qwen3-coder:30b`), same day. One is good. The
other is 2x longer and materially worse - and **every existing audit passed on both**.

| metric | pokemon-30th | pokemon-market |
|---|---|---|
| words | 6,268 | 13,345 (2.1x) |
| `(per <source>)` citations | 111 - 1 per 56 words | 57 - 1 per 234 words (**4x sparser**) |
| distinct `$` figures | **20** | **3** |
| duplicate paragraphs | **0** of 121 | **73 of 294 (25%)** |
| duplicate sentences | 0 of 229 | 16 of 372 |
| output tokens | 10,269 | 24,465 |
| wall time | 24m 35s | 46m 41s |

**What happened in the bad one:** the writer looped inside `Evidence Map and Key Findings`
and re-emitted the *same* source-evidence table **11 times verbatim**. 100 of the 73
duplicate paragraph instances live in that one section. Meanwhile the corpus's most
number-dense source (a video quoting ~15 distinct prices) yielded 3 distinct dollar figures
in the finished report.

**The important part: the pipeline could not tell these apart.** Headings, ordering, and
citation resolution all validated. `audit_assembled_report` will reject a report for a
duplicated *heading* but happily ship one that is 25% duplicated *content*.

## Cheap deterministic gates worth adding

None of these need a model call. All are computable on the assembled report in
milliseconds, and each one would have caught something real in this run.

1. **Repetition gate.** Hash normalised paragraphs (and 12-gram shingles); fail or warn
   above a duplicate ratio (~5%). Would have flagged the 11x table immediately.
   *This is the single highest-value check.*
2. **Citation density floor.** Citations per 1,000 words, computed per section. A section
   4x sparser than the document median is a degeneration signal.
3. **Distinct-fact density.** Count unique numerics/entities per section. A section that
   grows in length while distinct facts stay flat is padding - that is exactly the shape of
   the failure above.
4. **Section proportion sanity.** One section consuming ~70% of the document (Evidence Map
   ran lines 173-1185 of 1,461) is worth a warning.
5. **Echoed-title strip.** See #9 - removes both a crash and a cosmetic duplicate.

## Testing guidance

**Run the same topic 3x and diff.** Non-determinism is where both real bugs surfaced: the
heading echo (#9) alternated between `##` and `###` across runs, and the table loop appeared
in one report but not the other. A single green run proves very little about this pipeline.
A 3x-and-compare harness on a frozen corpus would have caught both without any model
insight.

**Freeze a golden corpus.** The 7 ingested videos here make a good fixture: transcripts are
already on disk, so the discovery/ingest stages can be bypassed and the writing stages
tested deterministically at temperature 0.

**Test with a small local model deliberately.** Frontier models mask these defects - they
rarely echo a heading as H2 and rarely loop. That makes a 7B/30B local model the *better*
test subject for structural robustness, not a lesser one. Every finding in this document
reproduces logic that is provider-agnostic.

**Assert on the low-information failure mode.** The current invariants are all structural
(does the heading exist, do citations resolve, is ordering right). The observed failure was
semantic-adjacent but still mechanically detectable: more words, fewer facts. Worth an
explicit test that a longer report is not a worse one.

**Note the temperature.** Insights/synthesis artifacts recorded `temperature: 0.0`; the
report recorded `temperature: 0.5`. The looping occurred in the 0.5 stage. Worth checking
whether report temperature needs to be that high, and whether local routes should default
lower.
