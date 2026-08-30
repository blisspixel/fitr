# fitr doctor: is this box even measurable?

Every benchmark silently assumes the stack under it is healthy - that a
reachable server actually infers, that the served context is the requested
one, that greedy decoding reproduces. None of that is safe to assume on a
local stack, and nothing else checks. `fitr doctor <model>` does, in about a
minute:

<img src="assets/doctor.svg?v=0.9.10" alt="fitr doctor (mock data)" width="820">

- **A real generated token, not an HTTP 200.** A misconfigured offload can
  accept requests and emit nothing.
- **Determinism, byte-compared.** N identical greedy requests in plain text
  *and* in grammar-constrained JSON mode - a different code path that is
  known to break seed reproducibility on some local stacks while plain text
  reproduces. If your box does not reproduce, every single-run number anyone
  quotes at you is noisier than it looks. Nondeterminism is reported as a
  caveat, not a failure: repeats and intervals survive it; one-shot numbers
  do not. A clean streak is reported with its exact worth: zero divergences
  in n runs bounds the divergence rate below `1 - 0.05^(1/n)`, so 5/5
  identical proves less than 45% at 95% confidence - and the output says
  that, with the sample size that would tighten it.
- **Served context.** A ~2.8K-token probe checks the evaluated-token count -
  the receipt for whether the server actually processed what you sent.
- **Placement.** `GPU 62%` is a partial-placement receipt. It belongs in a
  separate comparison block from `GPU 100%`; placement alone does not prove
  which subsystem limits performance.
- **Config red flags** from the server's own log (authoritative over your
  shell's environment): parallel slots divide the context, a second loaded
  model contaminates timings.

Exit code 3 when a check fails outright; warnings exit 0 - the box is
measurable, with caveats worth knowing before trusting numbers.

Run it before believing any number, including ours.
