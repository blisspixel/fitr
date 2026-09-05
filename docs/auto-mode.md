# Bounded auto mode

Version 0.10.11 adds a first auto cycle for two to four explicit installed text
models for one role. It checks resources, collects comparable evidence, and gives
a preselected choice one fresh confirmation attempt. It never changes the
role's quality floor to produce a winner.

## Prepare a fitting

Define a role using the current [role schema](roles.md). This first cycle uses
the full execution-disabled battery. Structured output and other supported
text or tool-channel needs can qualify. Requirements that depend on generated
code execution or arbitrary agent workflows fail feasibility before inference.
Multimodal models and additional projector, adapter or draft components are
outside this first runtime profile.

Inspect an explicitly installed Windows Ollama executable and existing model
store. The output path must be new:

```powershell
fitr auto runtime C:/runtime/ollama.exe --models D:/ollama-models --out runtime.json
```

Review the runtime file's context, KV precision, flash-attention setting and
memory reserve. Defaults are 8192 tokens, f16 KV, flash attention and a 2 GiB
reserve. These are an explicit starting configuration, not a recommendation
for every workload. Large-context preferences require their own fitting.

Start with two distinct installed artifacts and the fixed configuration:

```powershell
fitr auto start daily --mode establish --runtime runtime.json --candidate first:tag --candidate second:tag
```

`establish` requires an unselected role. `improve` requires a currently
qualified incumbent with the same owned runtime profile and includes its
exact artifact and model configuration in the shortlist. Legacy unowned
evidence cannot silently become owned-runtime evidence.

There are no implicit downloads, paid providers, arbitrary launch arguments,
context reductions, quantization substitutions or role-policy edits. A smaller
model is a separately declared candidate. The runtime owner starts a private
Ollama child with cloud disabled, fixed settings and an existing model store;
it does not stop the user's tray process or reuse an ambient daemon.

## Allowances and quality

The default limits are 600 inference attempts, 250000 requested output tokens,
8 points and two hours, including downtime. One hour and the full remaining
confirmation request/token schedule are protected from exploration. Use
`--max-requests`, `--max-requested-output-tokens`, `--max-points`, `--max-wall`
and `--confirmation-wall` to declare different finite limits. `-k` fixes both
noisy-task and generated-check repeats, from 3 to 20.

The preflight calculates the complete request envelope from the actual task
set and retry policy. The defaults fund two candidates at the default battery
size; a larger shortlist or additional tasks can require larger declared caps.
If the complete exploration and confirmation schedule cannot be funded, the
session does not begin inference.

Every actual inference attempt, including internal retries and load probes,
is durably charged before dispatch. Requested output caps measure admission
allowance, not actual tokens, billed cost or completed work. An interrupted
request is not refunded. Budget limits never become a progress percentage.

Before loading, every required text component must have a capacity projection
and current usable headroom after the reserve. During the bounded load probe,
the owned process must report the exact context, one resident candidate and
recognized execution placement. Partial offload and unverified components
stop the first profile. The program does not shrink the requested context.

Every inference attempt is followed by fresh resident-artifact, context and
allocation checks before its output is accepted or retried. The original
usable ceiling remains fixed, and fresh free memory must preserve the declared
reserve. Owned allocation logs establish the observed compute family for the
point; they do not provide an ordered attestation of every internal reload.

The first accelerator path requires one NVIDIA device whose identity and total
memory match the machine fingerprint. Multiple devices and other accelerator
types remain unsupported until their available memory can be tied to the
selected device. CPU-only execution uses current host memory.

Quality and resource floors precede weighted preferences. Comparisons retain
measurement uncertainty and the existing simultaneous weight-sensitivity
check. No qualified candidate, overlapping evidence and failed confirmation
remain valid outcomes. A failed or uncertain challenge preserves the selected
role reference.

## Inspect, resume and adopt

```powershell
fitr auto status auto-<id>
fitr auto resume auto-<id>
fitr auto adopt auto-<id>
```

Status leads with the role, current selection, candidate and acceptance state.
The fixed preselected choice remains visible while other candidates collect
confirmation evidence and before manual adoption.
Completed evidence points are separate from consumed allowances. Plain, rich,
JSON and silent modes share exit codes; a terminal unresolved result or an
expired unfinished session returns 4. JSON includes the validated plan,
journal state and presentation state. Progress follows the normal display
contract: text on stderr, structured phase events on stdout in JSON mode, and
no progress in `none` mode. Errors remain diagnostics on stderr.

Unresolved, overlapping and unqualified terminal outcomes include a fresh
review of the original exploration evidence: each candidate's state, exact
gaps and available preference bounds. JSON retains the full review and its
evaluation time. Missing or changed evidence is explicitly unavailable. This
inspection preserves the recorded session outcome. Review the gaps before
declaring a new task schedule or shortlist for another investigation.

Exploration resumes only at completed point boundaries. A fully saved,
integrity-checked signed point can reconcile a crash before its journal event;
an incomplete point cannot be rerun within the session. The original clock,
budgets, software, tasks, device, runtime and role policy still apply.

Interrupted confirmation ends that session's adoption path. It cannot resume,
obtain another seed or gain another attempt. Manual adoption is the default.
`--adoption confirmed-only` authorizes adoption after successful fresh
confirmation and runtime cleanup. Adoption updates fitr's selected role
evidence; it does not change serving aliases. An exact already-durable adoption
can reconcile a crash before the session journal advanced, without adopting
a second time.

First adoption rechecks the original deadline under the role lock after
validating the evidence. Its event records that final admission time. An
already-admitted atomic local write can finish just after the cutoff; this
does not authorize further inference or a new adoption attempt.

## Evidence and limits

The immutable session plan and bounded digest-linked journal live under
`FITR_RESULTS/.auto`. Exploration and confirmation use separate sealed managed
evidence stores under `FITR_RESULTS/.evidence-stores`; the existing selected
record is preserved. Original signed bytes, run IDs and evidence digests are
retained. Ordinary result-writing APIs cannot overwrite managed evidence.

The Windows owner retains installation read handles that deny ordinary writes
and replacement, starts the child in its own process job, checks the listener's
PID, clears inherited provider credentials and proxies, and disables cloud
execution. It terminates its own process tree and private temporary home when
the session ends. Metadata and inference have separate bounded header waits so
a cold model load can complete without extending metadata operations.
Serving logs are continuously drained into bounded line buffers and a bounded
diagnostic tail. Readiness and allocation facts are retained separately, so
ordinary log volume does not impose a lifetime limit on an auto session.

These are process ownership and evidence controls, not an OS sandbox against
another program with the same account's authority. Installation-directory
entry races and filesystem power-loss durability are not claimed solved.
Session seals detect inconsistent edits; they do not authenticate an authorized
writer who replaces the entire local state. No model deletion is part of auto.

Static specification validation and saved status are portable. Starting or
resuming this first runtime owner is Windows-only. Additional platforms,
continuous scheduling, source-driven downloads, context-quality preferences,
named-harness task evaluation and A2A execution remain separate work.
