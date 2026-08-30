#!/usr/bin/env python3
"""Small-N statistics for local evals.

Why this file exists: a single run is not a measurement. Published reruns of the
SAME model with the SAME config show 10-20 percentage-point swings (one
tool-calling audit measured SD 5.4pp, spread 18.9pp). We saw it ourselves --
the same coding task flipped pass/fail between runs on three different models.

So: repeat each task K times, report uncertainty per measured need, and refuse
behavior winner claims without identical paired instances. The production Go
instrument additionally clusters generated observations by task family.

Below N~300 the CLT is the wrong tool. Wilson score intervals are the standard
recommendation for binary pass/fail, and they behave correctly near p=0 and p=1
where the normal approximation falls apart.
"""
from __future__ import annotations

import math

Z95 = 1.959963984540054


def wilson(passes: int, n: int, z: float = Z95) -> tuple:
    """Wilson score interval for a binomial proportion.

    At n=10, p=0.8 this returns roughly (0.49, 0.94) -- which is the honest
    width of a 10-sample eval, and the reason not to over-claim.
    """
    if n <= 0:
        return (0.0, 0.0, 1.0)
    p = passes / n
    denom = 1 + z * z / n
    centre = (p + z * z / (2 * n)) / denom
    margin = (z / denom) * math.sqrt(p * (1 - p) / n + z * z / (4 * n * n))
    return (round(p, 4), round(max(0.0, centre - margin), 4),
            round(min(1.0, centre + margin), 4))


def mean_sd(xs) -> dict:
    xs = [x for x in xs if isinstance(x, (int, float))]
    if not xs:
        return {"mean": None, "sd": None, "n": 0, "min": None, "max": None}
    n = len(xs)
    m = sum(xs) / n
    sd = math.sqrt(sum((x - m) ** 2 for x in xs) / (n - 1)) if n > 1 else 0.0
    return {"mean": round(m, 3), "sd": round(sd, 3), "n": n,
            "min": round(min(xs), 3), "max": round(max(xs), 3),
            "spread": round(max(xs) - min(xs), 3)}


def cv(stat: dict):
    """Coefficient of variation -- how noisy is this measurement, relatively."""
    if not stat or not stat.get("mean"):
        return None
    return round((stat.get("sd") or 0) / stat["mean"], 4)


def intervals_overlap(a: tuple, b: tuple) -> bool:
    """a, b = (point, lo, hi). True => do NOT claim one is better."""
    if not a or not b:
        return True
    return not (a[2] < b[1] or b[2] < a[1])


def compare(a_name: str, a: tuple, b_name: str, b: tuple) -> str:
    """Honest pairwise verdict for two binary rates."""
    if intervals_overlap(a, b):
        return f"{a_name} and {b_name} are INDISTINGUISHABLE on this sample size"
    hi, lo = (a_name, b_name) if a[0] > b[0] else (b_name, a_name)
    return f"{hi} > {lo} (intervals do not overlap)"


def flakiness(results) -> dict:
    """A task that flips across repeats is telling you something the mean hides.

    results: list of bool (one per repeat)
    """
    vals = [bool(x) for x in results if x is not None]
    if not vals:
        return {"n": 0, "passes": 0, "flaky": False, "rate": None}
    p = sum(vals)
    return {"n": len(vals), "passes": p,
            "flaky": 0 < p < len(vals),          # neither always-pass nor always-fail
            "rate": round(p / len(vals), 3)}


def summarize_repeats(runs: list, key_path) -> dict:
    """Collect one numeric metric across repeats. key_path is a callable."""
    return mean_sd([key_path(r) for r in runs if r])
