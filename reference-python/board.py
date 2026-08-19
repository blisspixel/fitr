#!/usr/bin/env python3
"""Compare every model measured so far -- grouped by device fingerprint.

Rows measured under different hardware/config are NOT comparable, so they are
printed as separate blocks rather than silently ranked against each other.

    python board.py            # all groups
    python board.py --current  # only rows matching this machine right now
"""
from __future__ import annotations

import collections
import csv
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import device as D

BOARD = os.environ.get("EVALKIT_BOARD") or os.path.join(
    os.environ.get("EVALKIT_RESULTS")
    or os.path.join(os.path.dirname(os.path.abspath(__file__)), "results"),
    "leaderboard.csv")


def load():
    if not os.path.exists(BOARD):
        return []
    with open(BOARD, encoding="utf-8") as fh:
        return list(csv.DictReader(fh))


def group(rows):
    """Group by device fingerprint. Rows from different fingerprints are NOT
    comparable and must never be ranked against each other."""
    import collections
    g = collections.OrderedDict()
    for r in rows:
        g.setdefault(r.get("device_key", "?"), []).append(r)
    return g


def num(x, d=None):
    try:
        return float(x)
    except Exception:
        return d


def fmt(rows):
    hdr = f"{'model':<34}{'params':>8}{'tok/s':>8}{'TTFT':>7}{'prefill':>9}{'res32G':>9}   {'needs served'}"
    out = [hdr, "-" * len(hdr)]
    rows = sorted(rows, key=lambda r: -(num(r.get("tok_s"), 0) or 0))
    for r in rows:
        served = (r.get("needs_served") or "").replace("_", " ")
        out.append(
            f"{r['model'][:33]:<34}{r.get('params',''):>8}"
            f"{r.get('tok_s',''):>8}{r.get('ttft_s',''):>7}"
            f"{r.get('prefill_tps',''):>9}{r.get('resident_32k_gb',''):>9}   {served}"
        )
    return "\n".join(out)


def main():
    rows = load()
    if not rows:
        print("no results yet -- run:  python run.py <model> --full")
        return
    only_current = "--current" in sys.argv
    cur = D.fingerprint_key(D.fingerprint())

    groups = collections.OrderedDict()
    for r in rows:
        groups.setdefault(r.get("device_key", "?"), []).append(r)

    for key, rs in groups.items():
        if only_current and key != cur:
            continue
        is_cur = " (THIS MACHINE, CURRENT CONFIG)" if key == cur else " (different hardware/config -- not comparable to the above)"
        g = rs[-1]
        print(f"\n### {g.get('gpu','?')} | driver {g.get('driver','?')} | KV {g.get('kv','?')}{is_cur}")
        print(fmt(rs))

    if not only_current and len(groups) > 1:
        print("\nNOTE: groups above were measured under different hardware/config.")
        print("      Do not rank models across groups -- re-measure instead.")

    print("\nuse-for summary (current config):")
    for r in groups.get(cur, []):
        print(f"  {r['model'][:36]:<38} {r.get('use_for','')}")


if __name__ == "__main__":
    main()
