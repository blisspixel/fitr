# reference-python

**This is not the tool. The tool is the Go binary at the repo root.**

```bash
cd .. && make build && ./fitr run <model> --full -k 3
```

## Why this still exists

1. **Provenance for `spec/`.** Every prompt, fixture, and assertion in
   `../spec/tasks/*.json` was *generated* from this code, not hand-written. If
   you want to know why a task looks the way it does, it is here.

2. **A second implementation to diff against.** Porting to Go forced every
   measurement to be re-derived, which surfaced four bugs this version had:
   TTFT measured against a cold model, a guaranteed-false-positive truncation
   flag, a "2.8K token" prompt that was actually 1570 tokens, and a `stop_all`
   that spun on models already unloaded. Two were fixed back here; all four are
   fixed in Go.

3. **A check when the Go numbers look wrong.** Two independent implementations
   that agree on a verdict is worth more than one that is merely self-consistent.

## What it does NOT do

- It is not maintained in step with the Go version.
- Its tok/s figures are **not comparable** to the Go binary's: Go warms the
  model before timing and uses a longer prefill prompt. They agree on verdicts,
  not on numbers. The device-fingerprint rule already forbids mixing them.

## Running it

```bash
pip install -e .
python cli.py run <model> --full -k 3
pytest            # 29 tests, no Ollama required
```

The spec it validates lives at the **repo root** (`../spec/`), shared with the
Go implementation, so the two cannot drift.
