#!/usr/bin/env python3
"""Measure REAL resident footprint per model at several context sizes.

Disk size (what `ollama list` shows) is weights only. Resident size is
weights + KV cache + compute buffers, and it grows with num_ctx. This
measures the number that actually competes with Chrome for your 62 GB.

Usage: python memprobe.py <outfile.json> [model ...]
"""
import json, os, sys, time, urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import tasks_core as E

GB = 1024 ** 3


def api_get(path):
    with urllib.request.urlopen(E.OLLAMA + path, timeout=120) as r:
        return json.loads(r.read())


def _still_resident(models):
    """Filter out models that have already expired.

    Ollama keeps an unloaded model in /api/ps for a moment after its
    expires_at passes. Counting those as resident makes stop_all() spin and
    then report a failure for a model that is not actually loaded.
    """
    import datetime as _dt
    now = _dt.datetime.now(_dt.timezone.utc)
    live = []
    for m in models or []:
        exp = m.get("expires_at") or ""
        if exp:
            try:
                t = _dt.datetime.fromisoformat(exp.replace("Z", "+00:00"))
                if t.tzinfo is None:
                    t = t.replace(tzinfo=_dt.timezone.utc)
                if t < now:
                    continue
            except Exception:
                pass
        live.append(m)
    return live


def stop_all(verbose=True):
    """Unload EVERY resident model, then verify nothing is left."""
    for _ in range(6):
        try:
            running = _still_resident(api_get("/api/ps").get("models", []))
        except Exception:
            return True
        if not running:
            return True
        for m in running:
            name = m.get("name") or m.get("model")
            if verbose:
                print(f"    stopping resident: {name}", flush=True)
            try:
                with E.post("/api/generate", {"model": name, "keep_alive": 0, "prompt": ""}) as r:
                    r.read()
            except Exception:
                pass
        time.sleep(3)
    return False


def resident(model):
    """Return (total_bytes, vram_bytes, ctx) for `model` from /api/ps."""
    for m in api_get("/api/ps").get("models", []) or []:
        if (m.get("name") or m.get("model")) == model:
            return (m.get("size", 0), m.get("size_vram", 0),
                    (m.get("details") or {}).get("context_length")
                    or m.get("context_length"))
    return (0, 0, None)


def disk_size(model):
    try:
        for m in api_get("/api/tags").get("models", []) or []:
            if (m.get("name") or m.get("model")) == model:
                return m.get("size", 0)
    except Exception:
        pass
    return 0


def probe(model, ctxs):
    out = {"model": model, "disk_gb": round(disk_size(model) / GB, 2), "ctx": {}}
    for ctx in ctxs:
        if not stop_all(verbose=False):
            out["ctx"][str(ctx)] = {"error": "could not clear resident models"}
            continue
        time.sleep(2)
        rec = {}
        t0 = time.perf_counter()
        try:
            payload = {"model": model, "prompt": "Say OK.", "stream": False,
                       "options": {"num_ctx": ctx, "num_predict": 4},
                       "keep_alive": "2m", "think": False}
            with E.post("/api/generate", payload) as r:
                r.read()
            total, vram, _ = resident(model)
            rec = {"load_s": round(time.perf_counter() - t0, 1),
                   "resident_gb": round(total / GB, 2),
                   "on_gpu_gb": round(vram / GB, 2),
                   "pct_gpu": round(100 * vram / total) if total else 0,
                   "kv_overhead_gb": round((total - disk_size(model)) / GB, 2)}
        except Exception as e:
            rec = {"error": f"{type(e).__name__}: {str(e)[:160]}"}
        out["ctx"][str(ctx)] = rec
        print(f"  {model}  ctx={ctx:>6}: {rec}", flush=True)
    stop_all(verbose=False)
    return out


if __name__ == "__main__":
    outfile = sys.argv[1]
    models = sys.argv[2:] or [
        "huihui_ai/dolphin3-abliterated:8b",
        "huihui_ai/qwen3-coder-abliterated:30b-a3b-instruct-q4_K_M",
        "qwen3-coder:30b",
        "devstral-small-2:24b",
        "qwen3.8:27b",
    ]
    CTXS = [int(x) for x in (os.environ.get('MEMPROBE_CTXS') or '8192,32768').split(',')]
    results = []
    for m in models:
        print(f"=== memprobe {m} ===", flush=True)
        results.append(probe(m, CTXS))
    json.dump(results, open(outfile, "w", encoding="utf-8"), indent=2)
    print("WROTE " + outfile)
