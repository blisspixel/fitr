# Prior art in hardware -> model recommendation

Research pass, August 2026. The question: is "what should I run on this box?"
already solved, folklore, or genuinely open?

**Answer: solved at the kernel layer, dead at the decision layer.**

## August 20 decision: calibration before catalog breadth

The available discovery surfaces do not close fitr's evidence gap:

- Ollama's documented [`/api/tags`](https://docs.ollama.com/api/tags) endpoint
  lists models already installed on one server. The public model library has a
  search page, but the serving API does not document an internet-catalog search
  endpoint.
- The Hugging Face Hub has an open
  [model API](https://huggingface.co/docs/hub/en/api) and a first-class
  [GGUF filter and metadata viewer](https://huggingface.co/docs/hub/gguf).
  That solves discovery and file metadata, not task quality for a quant on one
  device.
- llama.cpp's
  [`llama-fit-params`](https://github.com/ggml-org/llama.cpp/blob/master/tools/fit-params/fit-params.cpp)
  prints a context, GPU-layer, tensor-split, and buffer-placement configuration
  that fits available memory. It does not establish structured-output,
  instruction, reasoning, or agentic quality.

So a live catalog would widen the set of uncalibrated answers before it made
them more trustworthy. The order is:

1. Calibrate generated checks and gates across real high/low quant pairs.
2. Recommend among installed models and saved measurements without network
   discovery.
3. Add a live catalog only when every recommendation can cite a measured gate,
   a fit source, and a remedy.

This keeps discovery replaceable. Hugging Face, Ollama, or another index can be
an adapter later; none becomes the source of truth for quality.

## The one thing worth stealing: NIM's three-tier output

NVIDIA NIM does real hardware-aware selection. Per its
[model profiles docs](https://docs.nvidia.com/nim/large-language-models/latest/deployment/model-profiles-and-selection.html)
(updated 2026-08-13):

> "At container startup, NIM selects exactly one profile from the manifest."
> "If you do not set `NIM_MODEL_PROFILE`, NIM automatically selects the best
> compatible profile based on your hardware (GPU device, available VRAM,
> estimated memory requirements, and parallelism constraints)."

It estimates VRAM by analyzing weights, KV cache, activations and overhead, then
reports three tiers:

| Tier | Meaning | What it tells you |
|---|---|---|
| Compatible | estimated VRAM fits | deploy |
| **Low memory** | weights fit, full context does not | **runs with a reduced `--max-model-len` - and the listing includes a suggested value** |
| Incompatible | weights alone exceed VRAM | raise TP, or use a quantized precision |

Real output:

```
(vllm-bf16-tp1-pp1) [requires >=45 GB/gpu, try --max-model-len=4096 to reduce to >=30 GB/gpu]

Estimated VRAM (45.2 GB) exceeds available GPU memory (39.6 GB).
Consider reducing context length with --max-model-len=4096 (estimated 30.1 GB).
```

**That is recommendation plus remediation in one line.** No consumer tool does
this. LM Studio says "Likely too large." `llmfit` says "Too Tight." Both are dead
ends; NIM hands you the flag and the resulting number.

**And it will not run on consumer hardware.** The NIM support matrix lists
**zero GeForce SKUs** - an RTX 4090 gets "0 compatible profiles." The best
fit-and-fix UX in existence is gated to datacenter GPUs.

We can do it *better* on the hardware NIM refuses to touch: llama.cpp's `--fit`
projects through **dummy allocation** rather than metadata arithmetic. That is
stronger than a name-based estimate, but it remains a projection rather than
observed process residency. fitr keeps it descriptive until the fitter's final
context, placement, version, and resource domains are captured.

## NVIDIA's consumer recommenders are both dead

| Layer | Detects GPU | Recommends a *model* | Auto-configures |
|---|---|---|---|
| **NIM** (server) | yes - PCI ID, VRAM, count | picks a *profile*, not among models | backend, precision, TP/PP, KV budget, ctx suggestion |
| **TensorRT for RTX** v1.6 | yes | no | JIT per-SKU kernels; adaptive inference cut JIT 31.92s -> 1.95s |
| **TensorRT-LLM** v1.2.1 | yes (kernel tactics) | no | **autotuner on by default**, KV = 90% of free VRAM |
| **G-Assist** 0.2.2 | 6 GB floor | no - one fixed 8B model | user-toggled modes |
| **NVIGI** | per-model `{"vram": 5124}` | no - "developer has full control" | no |
| **ChatRTX** | yes | yes | **deprecated 2026-01-21** |
| **RTX AI Toolkit** | - | - | **deprecated 2025-11-21** |

NVIDIA's flagship guide, *"How to Get Started With LLMs on NVIDIA RTX PCs"*
(2025-10-01), recommends **Ollama, LM Studio and AnythingLLM** and contains
**no VRAM tier table and no per-GPU model picker.** NVIDIA has fully delegated
consumer model choice to third-party UIs.

## Two facts worth keeping

- NVIGI's own justification: **"Considering that 97% of gamers have <= 8GB of
  VRAM."** That is the market's memory budget, from NVIDIA. Most of the model
  catalog is irrelevant to most of the market - an advisor should prune hard
  toward what actually fits in 8 GB.
- At COMPUTEX 2026-05-31 NVIDIA announced **RTX Spark** PCs (1 PFLOP, 128 GB
  unified) with the **OpenShell** runtime, which *"intelligently route[s]
  queries to local models based on the user's privacy policies"* - the closest
  thing to automatic model selection on consumer NVIDIA hardware, and its
  criterion is **privacy, not hardware or quality**.

## The asymmetry that is the whole opening

TensorRT-LLM's kernel autotuner is on by default. TensorRT for RTX JITs per-SKU
engines automatically. **The industry has decided that kernel-level tuning
should be automatic and invisible.**

Nobody has made the same decision about **quant level, KV dtype, context length,
or draft model** - the settings that actually change what the model *says*.

> The compute layer is solved and commoditized.
> The decision layer is still a Reddit thread.
