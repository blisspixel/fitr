# Optional external validation

OpenRouter can be useful to develop and validate parts of fitr, but it is not
part of the local measurement trust boundary. A remote model cannot establish
local memory fit, placement, load time, TTFT, prefill, decode, residency, or
the identity of bytes served on the user's machine.

The intended role is optional and explicit:

- generate candidate adversarial cases for human review
- compare grader behavior against several independent model families
- help build labeled calibration corpora for heuristic graders
- act as a shadow or negative control during task-pack development
- provide a separately labeled model-judged observation when a workload pack
  explicitly permits that evidence class

It must never:

- convert an unavailable local observation into an estimate
- issue a core behavioral PASS or FAIL by itself
- replace a deterministic assertion or independent state verifier
- enter a local FIT or PERFORMANCE claim
- run automatically, upload prompts silently, or become required for normal
  installation, measurement, decisions, or exports

## What works today

OpenRouter speaks an OpenAI-compatible protocol. A developer can opt into
protocol diagnostics through the existing backend without adding credentials
to command history:

```bash
export FITR_OPENAI_URL=https://openrouter.ai/api
export FITR_OPENAI_API_KEY=...
fitr diag provider/model --backend openai
```

Use the corresponding PowerShell environment assignment on Windows. The API
key is captured from the environment, sent only to the configured HTTPS
origin, and never stored in a fitr result.

This does not make `fitr run` an OpenRouter benchmark. Decision-bearing runs
require a runtime-reported artifact digest that matches the independently
pinned `FITR_OPENAI_MODEL_SHA256`. OpenRouter normally exposes a routed model
identifier, not the exact served weight artifact. The identity check therefore
fails closed instead of pretending the remote route is a stable local
configuration.

## Future validation-provider contract

An external validation provider should be attached to a specific experiment,
not added as a hidden global judge. Its receipt needs at least:

```text
provider protocol and endpoint origin
requested provider model and returned model identifier
request policy, sampling, and response-format policy
task-pack and rubric digests
prompt and response hashes
raw-retention and redaction policy
request start/end times
token usage and reported cost when available
terminal provider status
```

The evidence class remains `model_judged` or `external_reference`. The worker
does not grade itself, but a second model is still a heuristic observer rather
than ground truth. fitr should report agreement, disagreement, abstention, and
known calibration error separately from deterministic correctness.

## Development and CI

Repository tests must stay deterministic, offline, and free of user secrets.
Optional provider acceptance belongs in an explicitly invoked development or
native-acceptance job with a spending cap, timeout, allowlisted task pack, and
no release gate that ordinary contributors cannot reproduce. Recorded test
fixtures may exercise protocol parsing, but they must not be presented as
fresh provider evidence.

This boundary keeps OpenRouter genuinely useful without making the local-first
product smaller, cloud-dependent, or less trustworthy.
