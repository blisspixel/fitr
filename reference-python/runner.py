#!/usr/bin/env python3
"""The measurement engine. `cli.py` is the interface; this does the work.

Two invariants that exist because violating them produced bad data:

  1. ONE MODEL RESIDENT AT A TIME. Concurrent models contaminated timings badly
     enough that a whole eval had to be thrown away. stop_all() runs between
     every phase.
  2. PLUMBING BEFORE CAPABILITY. A tools failure is uninterpretable until you
     know the chat template and parser work. Skipping this once produced a
     published claim that a model "fails tool use" when it in fact emits valid
     calls and consumes results correctly.
"""
from __future__ import annotations

import csv
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import device as D
import diag_tools as DIAG
import score as S
import stats as ST
import tasks_core as T
import tasks_memory as M

HERE = os.path.dirname(os.path.abspath(__file__))
RESULTS = os.environ.get("EVALKIT_RESULTS") or os.path.join(HERE, "results")
BOARD = os.path.join(RESULTS, "leaderboard.csv")
SCHEMA_VERSION = 3


class _Null:
    def phase(self, *a, **k): ...
    def note(self, *a, **k): ...
    def done(self, *a, **k): ...
    def result(self, *a, **k): ...
    def emit(self, *a, **k): ...
    def close(self): ...


def stop_all():
    M.stop_all(verbose=False)


def _fold(runs: list, extra=()) -> dict:
    """Collapse K repeats of one task, keeping the spread.

    `pass` is the majority, but pass_rate / wilson / flaky are what you should
    actually read -- tasks demonstrably flip between identical runs.
    """
    ok = [bool(r.get("pass")) for r in runs]
    fl = ST.flakiness(ok)
    passes, n = fl["passes"], fl["n"]
    point, lo, hi = ST.wilson(passes, n)
    out = dict(runs[0])
    out.update({"pass": passes * 2 > n, "pass_rate": point,
                "wilson_lo": lo, "wilson_hi": hi, "repeats": n,
                "passes": passes, "flaky": fl["flaky"], "per_repeat": ok})
    for key in extra:
        out[f"{key}_all"] = [r.get(key) for r in runs]
    return out


def run(model: str, level: str = "default", profile_name=None, k: int = 3,
        disp=None) -> dict:
    disp = disp or _Null()
    os.makedirs(RESULTS, exist_ok=True)
    fp = D.fingerprint()
    prof = D.load_profile(profile_name, fp)
    res = {
        "schema_version": SCHEMA_VERSION,
        "model": model,
        "started_at": time.strftime("%Y-%m-%dT%H:%M:%S"),
        "level": level,
        "repeats": k,
        "device": fp,
        "device_key": D.fingerprint_key(fp),
        "profile": prof.get("name"),
        "model_meta": D.model_meta(model),
    }

    if prof.get("name") == "default":
        disp.note("using the UNCALIBRATED default profile - verdicts are rough; "
                  "copy profiles/default.json and tune it for this box", "warn")
    if D.is_dense_and_big(res["model_meta"], prof):
        disp.note(f"dense {res['model_meta'].get('parameter_size')} model on a "
                  f"bandwidth-bound device - expect very low tok/s", "warn")

    workdir = tempfile.mkdtemp(prefix="evalkit_")
    t_all = time.perf_counter()
    try:
        stop_all()
        disp.phase("load", model)
        t = time.perf_counter()
        _, warm = T.generate(model, "Say OK.", num_predict=8)
        res["load"] = {"first_call_s": round(time.perf_counter() - t, 2), **warm}
        res["device"]["inference_device"] = D.inference_device(model)
        disp.done("load", time.perf_counter() - t)

        disp.phase("speed", f"x{k}")
        t = time.perf_counter()
        speeds = [T.test_speed(model, nonce=f"{res['started_at']}-{i}")
                  for i in range(k)]
        res["speed"] = speeds[0]
        res["speed_repeats"] = {
            "decode_tps": ST.mean_sd([s["gen200"].get("decode_tps") for s in speeds]),
            "ttft_s": ST.mean_sd([s["gen200"].get("ttft_s") for s in speeds]),
            "prefill_tps": ST.mean_sd([s["ctx2k"].get("prefill_tps") for s in speeds]),
        }
        # report the MEAN, so one lucky run cannot set a verdict
        for field, key in (("gen200", "decode_tps"), ("gen200", "ttft_s"),
                           ("ctx2k", "prefill_tps")):
            m = res["speed_repeats"][key]["mean"]
            if m is not None:
                res["speed"][field][key] = m
        disp.done("speed", time.perf_counter() - t)

        disp.phase("memory", "resident @32K")
        t = time.perf_counter()
        stop_all()
        mem = M.probe(model, [32768])
        c = mem["ctx"].get("32768", {})
        res["memory"] = {"disk_gb": mem.get("disk_gb"),
                         "resident_gb_at_32k": c.get("resident_gb"),
                         "pct_on_gpu": c.get("pct_gpu")}
        disp.done("memory", time.perf_counter() - t)

        disp.phase("coding", f"x{k}")
        t = time.perf_counter()
        res["code_write"] = _fold([T.test_code_write(model, workdir) for _ in range(k)])
        res["code_fix"] = _fold([T.test_code_fix(model, workdir) for _ in range(k)])
        disp.done("coding", time.perf_counter() - t)

        disp.phase("plumbing", "tool round-trip")
        t = time.perf_counter()
        try:
            res["plumbing"] = DIAG.diagnose(model, verbose=False)
        except Exception as e:
            res["plumbing"] = {"error": f"{type(e).__name__}: {str(e)[:160]}"}
        disp.done("plumbing", time.perf_counter() - t)

        disp.phase("tools", f"x{k}")
        t = time.perf_counter()
        res["tools"] = _fold([T.test_tools(model, workdir) for _ in range(k)],
                             extra=("malformed_calls", "sequence"))
        disp.done("tools", time.perf_counter() - t)

        if level in ("default", "full"):
            disp.phase("refusal", "3 prompts")
            t = time.perf_counter()
            res["refusal"] = T.test_refusals(model)
            res["refused_count"] = sum(
                1 for v in res["refusal"].values()
                if v.get("verdict") in ("refused", "partial"))
            disp.done("refusal", time.perf_counter() - t)

        if level == "full":
            disp.phase("agentic", "20 unsupervised turns")
            t = time.perf_counter()
            stop_all()
            res["agentic"] = run_agentic(model)
            disp.done("agentic", time.perf_counter() - t)

        longest = ""
        for key in ("code_write", "code_fix"):
            longest = max(longest, (res.get(key) or {}).get("raw", "") or "", key=len)
        for v in (res.get("refusal") or {}).values():
            longest = max(longest, v.get("text", "") or "", key=len)
        res["repetition"] = S.repetition_metrics(longest)
        # Truncation is only a degeneracy signal for tasks that were FREE to
        # finish. The speed probe is capped by num_predict, so it always stops
        # on "length" -- reading that as degeneracy is a guaranteed false
        # positive. Judge it on the free-running generations instead.
        free_running_truncated = any(
            (res.get(k) or {}).get("metrics", {}).get("truncated")
            for k in ("code_write", "code_fix"))
        res["repetition"]["truncated"] = bool(free_running_truncated)
        res["density"] = S.information_density(longest)
    finally:
        stop_all()
        shutil.rmtree(workdir, ignore_errors=True)

    res["wall_s"] = round(time.perf_counter() - t_all, 1)
    res["scorecard"] = S.scorecard(res, prof)
    return res


def run_agentic(model: str) -> dict:
    out = tempfile.mkdtemp(prefix="evalkit_ag_")
    env = dict(os.environ, AGENTIC_TURNS="20")
    subprocess.run([sys.executable, os.path.join(HERE, "tasks_agentic.py"), model, out],
                   env=env, capture_output=True, text=True, timeout=3600)
    safe = re.sub(r"[^A-Za-z0-9_.-]", "_", model)
    try:
        return json.load(open(os.path.join(out, f"result_{safe}__agentic.json"),
                              encoding="utf-8"))
    except Exception as e:
        return {"error": f"agentic run produced no result: {e}"}


def _safe(model: str) -> str:
    return re.sub(r"[^A-Za-z0-9_.-]", "_", model)


def save(res: dict) -> str:
    os.makedirs(RESULTS, exist_ok=True)
    p = os.path.join(RESULTS, f"{_safe(res['model'])}.json")
    json.dump(res, open(p, "w", encoding="utf-8"), indent=2)
    return p


def load_result(model: str):
    p = os.path.join(RESULTS, f"{_safe(model)}.json")
    try:
        return json.load(open(p, encoding="utf-8"))
    except Exception:
        return None


BOARD_FIELDS = ["date", "model", "device_key", "gpu", "driver", "kv", "params",
                "quant", "tok_s", "tok_s_sd", "ttft_s", "prefill_tps",
                "resident_32k_gb", "repeats", "code_write", "code_fix", "tools",
                "agentic", "refused", "dup_para", "needs_served", "use_for"]


def append_board(res: dict) -> str:
    os.makedirs(RESULTS, exist_ok=True)
    sc = res["scorecard"]
    sp = res.get("speed") or {}
    sr = res.get("speed_repeats") or {}
    row = {
        "date": res["started_at"][:10],
        "model": res["model"],
        "device_key": D.fingerprint_key(res.get("device") or {}),
        "gpu": res["device"]["gpu"],
        "driver": res["device"]["gpu_driver"],
        "kv": res["device"]["config"].get("OLLAMA_KV_CACHE_TYPE", ""),
        "params": (res.get("model_meta") or {}).get("parameter_size", ""),
        "quant": (res.get("model_meta") or {}).get("quantization", ""),
        "tok_s": (sp.get("gen200") or {}).get("decode_tps"),
        "tok_s_sd": (sr.get("decode_tps") or {}).get("sd"),
        "ttft_s": (sp.get("gen200") or {}).get("ttft_s"),
        "prefill_tps": (sp.get("ctx2k") or {}).get("prefill_tps"),
        "resident_32k_gb": (res.get("memory") or {}).get("resident_gb_at_32k"),
        "repeats": res.get("repeats"),
        "code_write": (res.get("code_write") or {}).get("pass"),
        "code_fix": (res.get("code_fix") or {}).get("pass"),
        "tools": (res.get("tools") or {}).get("pass"),
        "agentic": (res.get("agentic") or {}).get("passed"),
        "refused": res.get("refused_count"),
        "dup_para": (res.get("repetition") or {}).get("dup_paragraph_ratio"),
        "needs_served": ";".join(sc["serves"]),
        "use_for": sc["use_it_for"],
    }
    exists = os.path.exists(BOARD)
    with open(BOARD, "a", newline="", encoding="utf-8") as fh:
        w = csv.DictWriter(fh, fieldnames=BOARD_FIELDS, extrasaction="ignore")
        if not exists:
            w.writeheader()
        w.writerow(row)
    return BOARD
