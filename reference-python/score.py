#!/usr/bin/env python3
"""Turn raw measurements into a device-specific verdict.

Three ideas here:

1. SCORE AGAINST NEEDS, NOT ONE ROLE. "Is it good" is unanswerable. "Do I need
   great coding", "do I need it to never refuse", "do I need fast-and-decent",
   "do I need it to run unattended" are all separate, legitimate needs, and a
   model can serve one brilliantly and another not at all. dolphin3-8b is a bad
   agent and an excellent no-filter chat model. One string cannot say that, so
   we emit a SET of needs served.

2. GATES ARE DEVICE-SPECIFIC. The same model was "crashes on load" and "daily
   driver" on one laptop depending only on the GPU driver. Thresholds live in
   profiles/*.json and are selected per machine.

3. DEGENERACY IS MEASURED. On 2026-08-17 a local model produced a 104 KB report
   that passed every structural check while 25% of its paragraphs were exact
   duplicates -- it had looped one table 11 times. Length correlated NEGATIVELY
   with quality. These metrics catch that for free, with no model call.

Verdict states are PASS / FAIL / SKIP. SKIP means "not measured at this level"
and must never read as failure.
"""
from __future__ import annotations

import collections
import gzip
import hashlib
import re


PASS, FAIL, SKIP = "PASS", "FAIL", "SKIP"
NA = "n/a"          # not applicable -- the model never claimed this capability
BLOCKED = "BLKD"    # we could not get it to work; suspect plumbing, not weights

# A FAIL is a statement about THIS MODEL IN THIS CONFIGURATION, not a verdict on
# the weights. Field reports converge on ~4 in 5 "model can't do tools" cases
# being the chat template, the tool-call parser, the quant, or the context size.
# Run diag_tools.py before treating a tools FAIL as a property of the model.
#
# And a capability a model does not have is not a deficiency: a text-only model
# is not "bad at vision", it is simply not a vision model. That is N/A, and it
# says nothing about how good the model is at what it IS for.


# ------------------------------------------------------------ degeneracy
def repetition_metrics(text: str) -> dict:
    """Cheap deterministic degeneration detectors. No model call needed."""
    if not text or len(text) < 200:
        return {"dup_paragraph_ratio": 0.0, "dup_sentence_ratio": 0.0,
                "repetition_score": 0.0, "distinct_4gram": 1.0, "words": 0}

    paras = [p.strip() for p in re.split(r"\n\s*\n", text) if len(p.strip()) > 120]
    ph = [hashlib.md5(re.sub(r"\s+", " ", p).lower().encode()).hexdigest() for p in paras]
    pc = collections.Counter(ph)
    dup_par = sum(c - 1 for c in pc.values() if c > 1)

    sents = [s.strip() for s in re.split(r"(?<=[.!?])\s+", text) if len(s.strip()) > 60]
    sc = collections.Counter(re.sub(r"\s+", " ", s).lower() for s in sents)
    dup_sent = sum(c - 1 for c in sc.values() if c > 1)

    words = re.findall(r"[A-Za-z']+", text.lower())
    grams = [tuple(words[i:i + 4]) for i in range(max(0, len(words) - 3))]
    distinct4 = (len(set(grams)) / len(grams)) if grams else 1.0

    # Gopher-family: character share held by the single most frequent n-gram.
    # Catches one phrase dominating even when paragraphs differ.
    def top_ngram_char_frac(nn):
        gs = [" ".join(words[i:i + nn]) for i in range(max(0, len(words) - nn + 1))]
        if not gs:
            return 0.0
        top, cnt = collections.Counter(gs).most_common(1)[0]
        return round(len(top) * cnt / max(1, len(text)), 4)

    # Duplicate line fraction (Gopher threshold 0.30 for corpus filtering; we
    # use a much tighter gate because generated output is not a web crawl).
    lines = [l.strip() for l in text.splitlines() if l.strip()]
    lc = collections.Counter(lines)
    dup_lines = sum(c - 1 for c in lc.values() if c > 1)

    # gzip compression ratio. Human prose ~2.28; model output 2.36-2.72.
    # Higher = more repetitive. Length-sensitive, so treat as a signal not a gate.
    raw = text.encode("utf-8", "ignore")
    cr = round(len(raw) / max(1, len(gzip.compress(raw))), 3)

    return {
        "dup_paragraph_ratio": round(dup_par / len(paras), 4) if paras else 0.0,
        "dup_sentence_ratio": round(dup_sent / len(sents), 4) if sents else 0.0,
        "dup_line_ratio": round(dup_lines / len(lines), 4) if lines else 0.0,
        "top_2gram_char_frac": top_ngram_char_frac(2),
        "top_4gram_char_frac": top_ngram_char_frac(4),
        "gzip_compression_ratio": cr,
        "repetition_score": round(1.0 - distinct4, 4),
        "distinct_4gram": round(distinct4, 4),
        "words": len(words),
    }


def information_density(text: str) -> dict:
    """Padding detector: length rising while distinct facts stay flat = filler."""
    words = len(re.findall(r"[A-Za-z']+", text or ""))
    nums = re.findall(r"\$?\d[\d,]*\.?\d*%?", text or "")
    uniq = len(set(nums))
    return {
        "distinct_numerics": uniq,
        "numerics_per_1k_words": round(1000 * uniq / words, 2) if words else 0.0,
    }


# ------------------------------------------------------------ helpers
def _pull(r: dict) -> dict:
    sp = r.get("speed") or {}
    gen = sp.get("gen200") or {}
    ctx = sp.get("ctx2k") or {}
    ag = r.get("agentic") or {}
    return {
        "tps": gen.get("decode_tps"),
        "ttft": gen.get("ttft_s"),
        "prefill": ctx.get("prefill_tps"),
        "res32": (r.get("memory") or {}).get("resident_gb_at_32k"),
        "caps": (r.get("model_meta") or {}).get("capabilities") or [],
        "cw": (r.get("code_write") or {}).get("pass"),
        "cf": (r.get("code_fix") or {}).get("pass"),
        "cw_r": r.get("code_write") or {},
        "cf_r": r.get("code_fix") or {},
        "tools_pass": (r.get("tools") or {}).get("pass"),
        "tools_bad": (r.get("tools") or {}).get("malformed_calls"),
        "ag_pass": ag.get("passed"),
        "ag_bad": ag.get("malformed_calls"),
        "ag_ran": bool(ag) and "error" not in ag,
        "refused": r.get("refused_count"),
        "ref_ran": r.get("refusal") is not None,
        "rep": r.get("repetition") or {},
        "plumb": r.get("plumbing") or {},
        "truncated": bool((r.get("repetition") or {}).get("truncated")),
    }


def _v(state, why):
    return {"state": state, "why": why}


# ------------------------------------------------------------ needs
def needs(r: dict, profile: dict) -> dict:
    """Each entry answers one real need, independently.

    Gates come from the selected device profile. A need whose gate is absent
    from the profile is SKIPped, never failed.
    """
    gates = profile.get("gates", {}) or {}
    p = _pull(r)
    n = {}

    def gate(name):
        return gates.get(name) or {}

    # --- "I need it fast and pretty good" (chat responsiveness)
    fc = gate("fast_chat")
    if not fc:
        n["fast_and_decent"] = _v(SKIP, "no fast_chat gate in profile")
    elif p["tps"] is None or p["ttft"] is None:
        n["fast_and_decent"] = _v(SKIP, "speed not measured")
    else:
        ok = (p["tps"] >= fc.get("decode_tps_min", 0)
              and p["ttft"] <= fc.get("ttft_s_max", 1e9))
        n["fast_and_decent"] = _v(
            PASS if ok else FAIL,
            f"{p['tps']} tok/s (need >={fc.get('decode_tps_min')}), "
            f"TTFT {p['ttft']}s (need <={fc.get('ttft_s_max')})")

    # --- "I need great coding / reasoning"
    if p["cw"] is None and p["cf"] is None:
        n["coding"] = _v(SKIP, "not measured")
    else:
        ok = bool(p["cw"]) and bool(p["cf"])
        def _r(d, label):
            rep = d.get("repeats")
            if not rep or rep < 2:
                return f"{label}={d.get('pass')}"
            flag = " FLAKY" if d.get("flaky") else ""
            return (f"{label} {d.get('passes')}/{rep}"
                    f" [{d.get('wilson_lo')}-{d.get('wilson_hi')}]{flag}")
        n["coding"] = _v(PASS if ok else FAIL,
                         f"{_r(p['cw_r'],'write')}, {_r(p['cf_r'],'fix')}")

    # --- "I need no filtering / it must not refuse"
    if not p["ref_ran"]:
        n["uncensored"] = _v(SKIP, "refusal battery not run (use default/--full)")
    else:
        ok = p["refused"] <= gate("uncensored").get("refused_max", 0)
        n["uncensored"] = _v(PASS if ok else FAIL,
                             f"refused/partial {p['refused']}/3 "
                             f"(need <={gate('uncensored').get('refused_max', 0)})")

    # --- "I need it to work unattended" (prefill + tool reliability over turns)
    ua = gate("unattended_agentic")
    plumb = p["plumb"]
    rungs = (plumb.get("rungs") or {})
    plumbing_broken = bool(rungs) and not all(
        rungs.get(k, {}).get("pass") for k in ("1_capability", "2_emits_tool_call",
                                               "3_valid_args", "4_roundtrip"))
    if plumbing_broken:
        n["unattended_agentic"] = _v(
            BLOCKED,
            "tool plumbing failed before capability could be judged: "
            + (plumb.get("verdict") or "") + " -- fix template/parser, then re-run")
    elif not p["ag_ran"]:
        # tools test is a weaker proxy; report it but don't claim the full need
        if p["tools_pass"] is None:
            n["unattended_agentic"] = _v(SKIP, "not measured (run with --full)")
        else:
            n["unattended_agentic"] = _v(
                SKIP,
                f"only 4-call proxy ran: tools={p['tools_pass']} "
                f"bad={p['tools_bad']}; run --full for the 20-turn verdict")
    elif p["prefill"] is None:
        n["unattended_agentic"] = _v(SKIP, "prefill not measured")
    else:
        pmin = ua.get("prefill_tps_min")
        bmax = ua.get("malformed_tool_calls_max", 0)
        if pmin is None:
            n["unattended_agentic"] = _v(SKIP, "no unattended_agentic gate in profile")
        else:
            ok = (p["prefill"] >= pmin and (p["ag_bad"] or 0) <= bmax
                  and bool(p["ag_pass"]))
            n["unattended_agentic"] = _v(
                PASS if ok else FAIL,
                f"prefill {p['prefill']} tok/s (need >={pmin}), "
                f"20-turn pass={p['ag_pass']}, malformed={p['ag_bad']}")

    # --- tool restraint: with tools present, does it leave them alone when
    #     they are irrelevant? Needs no ground truth, and it is the most common
    #     local-model tool failure.
    irr = rungs.get("5_irrelevance")
    if not irr:
        n["tool_restraint"] = _v(SKIP, "plumbing diagnostic not run")
    elif not rungs.get("2_emits_tool_call", {}).get("pass"):
        n["tool_restraint"] = _v(NA, "model does not emit tool calls at all")
    else:
        n["tool_restraint"] = _v(
            PASS if irr["pass"] else FAIL,
            "left tools alone on an unrelated question" if irr["pass"]
            else f"fired {irr.get('spurious_calls')} tool call(s) on an unrelated question")

    # --- "I need it resident alongside other things"
    ao = gate("always_on_capable")
    lim = ao.get("resident_gb_at_32k_max")
    if not ao or lim is None:
        n["low_footprint"] = _v(SKIP, "no always_on_capable gate in profile")
    elif p["res32"] is None:
        n["low_footprint"] = _v(SKIP, "memory not measured")
    else:
        ok = p["res32"] <= lim
        n["low_footprint"] = _v(PASS if ok else FAIL,
                                f"resident@32K {p['res32']} GB (need <={lim})")

    # --- "I need it to read images"
    # Not claiming vision is not a failure -- it is a different kind of model.
    if "vision" in p["caps"]:
        n["vision"] = _v(PASS, f"capabilities={p['caps']}")
    else:
        n["vision"] = _v(NA, "text-only model -- not a deficiency, just not what it is for")

    # --- output health (not a need; a disqualifier)
    # Multiple independent signals: any single metric has blind spots. A looping
    # table scored dup_paragraph_ratio 0.0 while dup_line_ratio was 0.91.
    q, rep = gate("quality"), p["rep"]
    if not q or not rep or rep.get("words", 0) < 150:
        n["output_health"] = _v(SKIP, "not enough generated text to judge")
    else:
        checks = [
            ("dup_paragraphs", rep.get("dup_paragraph_ratio"), q.get("dup_paragraph_ratio_max")),
            ("dup_lines", rep.get("dup_line_ratio"), q.get("dup_line_ratio_max")),
            ("top_4gram_share", rep.get("top_4gram_char_frac"), q.get("top_4gram_char_frac_max")),
            ("gzip_ratio", rep.get("gzip_compression_ratio"), q.get("gzip_compression_ratio_max")),
            ("repetition", rep.get("repetition_score"), q.get("repetition_score_max")),
        ]
        breached = [f"{name} {val} > {lim}"
                    for name, val, lim in checks
                    if val is not None and lim is not None and val > lim]
        if p.get("truncated") and not q.get("allow_truncation", False):
            breached.append("output TRUNCATED (hit token cap; ~92% of truncations are loops)")
        measured = [f"{name} {val}" for name, val, lim in checks if val is not None]
        missing = [name for name, val, lim in checks if val is None]
        note = ("; ".join(breached) if breached else ", ".join(measured)) or "no metrics"
        if missing:
            note += f"  (not measured: {', '.join(missing)} -- re-run to refresh)"
        n["output_health"] = _v(FAIL if breached else PASS, note)
    return n


NEED_LABEL = {
    "fast_and_decent": "fast + pretty good (chat)",
    "coding": "great coding / reasoning",
    "uncensored": "no filtering / low refusal",
    "unattended_agentic": "works unattended (agent loop)",
    "low_footprint": "small enough to keep resident",
    "tool_restraint": "leaves tools alone when irrelevant",
    "vision": "reads images",
    "output_health": "no degenerate output",
}


def serves(n: dict) -> list:
    """Which needs this model actually satisfies here."""
    return [k for k, v in n.items() if v["state"] == PASS and k != "output_health"]


def unproven(n: dict) -> list:
    """Needs we could not fairly judge -- not measured, not applicable, or blocked."""
    return [k for k, v in n.items() if v["state"] in (SKIP, NA, BLOCKED)]


def use_it_for(r: dict, n: dict) -> str:
    caps = (r.get("model_meta") or {}).get("capabilities") or []
    if "embedding" in caps:
        return "embeddings / local search"
    got = set(serves(n))
    if n["output_health"]["state"] == FAIL:
        return "AVOID -- produces degenerate/looping output"
    bits = []
    if {"coding", "unattended_agentic"} <= got:
        bits.append("daily driver (coding + agents)")
    elif "coding" in got:
        bits.append("coding")
    elif "unattended_agentic" in got:
        bits.append("unattended agent")
    if "uncensored" in got:
        bits.append("no-filter writing/chat")
    if "fast_and_decent" in got and not bits:
        bits.append("quick chat")
    if "vision" in got:
        bits.append("vision one-shot")
    if not bits:
        unk = unproven(n)
        if unk:
            return ("no need PASSED yet, but " + str(len(unk)) +
                    " were unmeasured/blocked -- this is not a verdict, run --full")
        return "none of the measured needs on this device (may still suit needs not tested here)"
    if "low_footprint" in got:
        bits.append("(small footprint)")
    return ", ".join(bits)


def scorecard(r: dict, profile: dict) -> dict:
    n = needs(r, profile)
    return {
        "model": r.get("model"),
        "profile": profile.get("name"),
        "needs": n,
        "serves": serves(n),
        "use_it_for": use_it_for(r, n),
        "unproven": unproven(n),
        "passes": sum(1 for v in n.values() if v["state"] == PASS),
        "fails": sum(1 for v in n.values() if v["state"] == FAIL),
        "skipped": sum(1 for v in n.values() if v["state"] in (SKIP, NA, BLOCKED)),
    }


def render(sc: dict, r: dict) -> str:
    m = r.get("model_meta") or {}
    L = [
        f"  MODEL   {sc['model']}   ({m.get('parameter_size','?')} {m.get('quantization','')} {m.get('family','')})",
        f"  USE FOR {sc['use_it_for']}",
        f"  NEEDS   {sc['passes']} served / {sc['fails']} failed / {sc['skipped']} n-a or unmeasured",
        "",
    ]
    for k, v in sc["needs"].items():
        L.append(f"    [{v['state']:<4}] {NEED_LABEL.get(k,k):<32} {v['why']}")
    return "\n".join(L)
