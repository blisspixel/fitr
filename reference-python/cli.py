#!/usr/bin/env python3
"""evalkit — is this model any good ON THIS DEVICE?

    evalkit run <model> [--quick|--full] [-k N]
    evalkit board [--current]
    evalkit diag <model>
    evalkit device
    evalkit profiles
    evalkit compare <model-a> <model-b>

Design notes, all borrowed from tools that got it right:

  * Verbosity is uv's Printer model: -q/-qq are distinct levels, and -v HIDES
    the progress bar (otherwise it interleaves with debug output).
  * Progress goes to STDERR, results to STDOUT, so `evalkit run ... > out.txt`
    is clean and pipeable (promptfoo).
  * One `is_human_readable` predicate gates ALL chrome — headers, hints,
    footers — so machine modes are never polluted (ruff).
  * Errors are error/note/hint, and go to stderr as PLAIN TEXT even in --json;
    nobody wraps errors in JSON. The exit code is the machine channel (rustc,
    gh, uv).
"""
from __future__ import annotations

import argparse
import json
import os
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

# Exit codes: small, documented, domain-specific. Not sysexits (nobody uses it).
EXIT_OK = 0        # ran, and every measured need passed
EXIT_ERROR = 1     # something broke
EXIT_USAGE = 2     # bad invocation
EXIT_GATES = 3     # ran fine, but a need FAILED (useful as a CI gate)
EXIT_INTERRUPT = 130


class Printer:
    """uv's model. -q silences chrome, -qq silences everything, -v hides the
    progress bar so it cannot interleave with diagnostics."""

    def __init__(self, quiet: int = 0, verbose: int = 0, no_progress: bool = False):
        self.quiet, self.verbose, self.no_progress = quiet, verbose, no_progress

    @property
    def silent(self) -> bool:
        return self.quiet > 1

    @property
    def is_human_readable(self) -> bool:
        """Gates ALL chrome. One predicate, checked everywhere."""
        return not self.silent and self.quiet == 0

    @property
    def suppresses_progress(self) -> bool:
        return self.quiet > 0 or self.verbose > 0 or self.no_progress


def err_print(msg: str, note: str = "", hint: str = "") -> None:
    """error / note / hint as separate levels (rustc). Plain text, stderr,
    always -- even under --json."""
    print(f"error: {msg}", file=sys.stderr)
    if note:
        print(f" note: {note}", file=sys.stderr)
    if hint:
        print(f" hint: {hint}", file=sys.stderr)


def _preflight(model: str = "") -> None:
    import device as D
    if not D.ollama_reachable():
        err_print(
            f"cannot reach Ollama at {D.OLLAMA}",
            note="every measurement needs a running server",
            hint="start it with `ollama serve`, or set OLLAMA_BASE_URL")
        raise SystemExit(EXIT_ERROR)
    if model:
        installed = D.installed_models()
        if installed and model not in installed:
            near = [m for m in installed if model.split(":")[0] in m]
            err_print(
                f"model {model!r} is not installed",
                note=f"{len(installed)} model(s) available",
                hint=(f"did you mean: {', '.join(near[:3])}" if near
                      else f"pull it first: `ollama pull {model}`"))
            raise SystemExit(EXIT_USAGE)


# ------------------------------------------------------------------ commands
def cmd_run(a) -> int:
    import device as D
    import display as DISP
    import runner as R
    import score as S

    _preflight(a.model)
    level = "quick" if a.quick else ("full" if a.full else "default")
    k = a.repeats if a.repeats else (1 if level == "quick" else 3)

    pr = Printer(a.quiet, a.verbose, a.no_progress)
    mode = a.display
    if pr.suppresses_progress and mode == "auto":
        mode = "plain"
    if pr.silent:
        mode = "none"
    disp = DISP.make_display(mode, total_phases=7)

    try:
        res = R.run(a.model, level, a.profile, k, disp=disp)
    except KeyboardInterrupt:
        disp.close()
        print("\ninterrupted", file=sys.stderr)
        return EXIT_INTERRUPT
    except Exception as e:
        disp.close()
        err_print(f"{type(e).__name__}: {e}",
                  hint="re-run with -v for the full traceback"
                  if not a.verbose else "")
        if a.verbose:
            import traceback
            traceback.print_exc()
        return EXIT_ERROR

    path = R.save(res)
    R.append_board(res)
    sc = res["scorecard"]
    disp.result(sc, res, S.NEED_LABEL)
    disp.close()

    if pr.is_human_readable and DISP.resolve_mode(mode) != "json":
        print(f"\n  saved  {path}", file=sys.stderr)
        print(f"  next   evalkit board          compare against previous runs",
              file=sys.stderr)
        if k < 3:
            print(f"         evalkit run {a.model} -k 3   for a rankable result",
                  file=sys.stderr)

    return EXIT_GATES if sc["fails"] else EXIT_OK


def cmd_board(a) -> int:
    import board as B
    import device as D
    import display as DISP

    rows = B.load()
    if not rows:
        err_print("no results yet",
                  hint="run one first:  evalkit run <model> --full")
        return EXIT_ERROR
    groups = B.group(rows)
    cur = D.fingerprint_key(D.fingerprint())
    if a.current:
        groups = {k: v for k, v in groups.items() if k == cur}
        if not groups:
            err_print("no results for this machine's current config",
                      note="hardware or Ollama settings changed since the last run",
                      hint="re-measure:  evalkit run <model> --full")
            return EXIT_ERROR
    if DISP.resolve_mode(a.display) == "json":
        print(json.dumps({"groups": {k: v for k, v in groups.items()},
                          "current": cur}))
        return EXIT_OK
    from rich.console import Console
    Console(no_color=DISP._no_color()).print(DISP.board_renderable(groups, cur))
    return EXIT_OK


def cmd_diag(a) -> int:
    import diag_tools as T
    _preflight(a.model)
    print(f"tool plumbing: {a.model}")
    out = T.diagnose(a.model, verbose=True)
    if a.display == "json":
        print(json.dumps(out, indent=2))
    rungs = out.get("rungs") or {}
    ok = all(v.get("pass") for k, v in rungs.items()
             if k != "5_irrelevance")   # irrelevance is a model trait, not plumbing
    return EXIT_OK if ok else EXIT_GATES


def cmd_device(a) -> int:
    import device as D
    fp = D.fingerprint()
    prof = D.load_profile(a.profile, fp)
    if a.display == "json":
        print(json.dumps({"fingerprint": fp, "key": D.fingerprint_key(fp),
                          "profile": prof.get("name")}, indent=2))
        return EXIT_OK
    for k, v in fp.items():
        if k == "config":
            continue
        print(f"  {k:<18} {v}")
    print("  config")
    for k, v in (fp.get("config") or {}).items():
        print(f"    {k:<24} {v or '(unset)'}")
    print(f"  profile            {prof.get('name')} — {prof.get('description','')}")
    print(f"  key                {D.fingerprint_key(fp)}")
    return EXIT_OK


def cmd_profiles(a) -> int:
    import device as D
    fp = D.fingerprint()
    active = D.load_profile(None, fp).get("name")
    for p in D._profiles():
        mark = "*" if p.get("name") == active else " "
        print(f" {mark} {p.get('name','?'):<12} {p.get('description','')}")
    print("\n  * = auto-selected for this machine")
    return EXIT_OK


def cmd_compare(a) -> int:
    """Paired comparison with honest uncertainty.

    Speed ratios use hyperfine's error propagation:
        sigma_ratio = ratio * sqrt((sd_a/mean_a)^2 + (sd_b/mean_b)^2)
    A ratio without a +/- is not a claim.
    """
    import math
    import runner as R
    import stats as ST

    ra, rb = R.load_result(a.model_a), R.load_result(a.model_b)
    for name, r in ((a.model_a, ra), (a.model_b, rb)):
        if not r:
            err_print(f"no stored result for {name!r}",
                      hint=f"evalkit run {name} --full")
            return EXIT_ERROR
    import device as D
    ka = D.fingerprint_key(ra.get("device") or {})
    kb = D.fingerprint_key(rb.get("device") or {})
    if ka != kb:
        err_print("these two results were measured on different hardware/config",
                  note="tok/s is device-specific; the comparison would be meaningless",
                  hint="re-measure both on this machine")
        return EXIT_ERROR

    def spd(r, key):
        st = ((r.get("speed_repeats") or {}).get(key) or {})
        if st.get("mean"):
            return st["mean"], (st.get("sd") or 0.0), (st.get("n") or 1)
        gen = ((r.get("speed") or {}).get("gen200") or {})
        ctx = ((r.get("speed") or {}).get("ctx2k") or {})
        v = gen.get("decode_tps") if key == "decode_tps" else ctx.get("prefill_tps")
        return (v or 0.0), 0.0, 1

    print(f"  {a.model_a}  vs  {a.model_b}\n")
    for key, label in (("decode_tps", "decode tok/s"), ("prefill_tps", "prefill tok/s")):
        (ma, sa, na), (mb, sb, nb) = spd(ra, key), spd(rb, key)
        if not ma or not mb:
            continue
        ratio = ma / mb
        if sa and sb and na > 1 and nb > 1:
            sd = ratio * math.hypot(sa / ma, sb / mb)
            print(f"  {label:<16} {ma:8.2f} vs {mb:8.2f}   "
                  f"{ratio:5.2f}x +/-{sd:.2f}")
        else:
            print(f"  {label:<16} {ma:8.2f} vs {mb:8.2f}   {ratio:5.2f}x "
                  f"(no +/- : single observation)")

    print()
    for key in ("code_write", "code_fix"):
        da, db = ra.get(key) or {}, rb.get(key) or {}
        wa = ST.wilson(da.get("passes", int(bool(da.get("pass")))), da.get("repeats", 1))
        wb = ST.wilson(db.get("passes", int(bool(db.get("pass")))), db.get("repeats", 1))
        print(f"  {key:<16} {ST.compare(a.model_a, wa, a.model_b, wb)}")
    print("\n  note  overlapping intervals mean the sample cannot separate them;")
    print("        that is a real answer, not a missing one.")
    return EXIT_OK


# ------------------------------------------------------------------ parser
def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="evalkit",
        description="Is this model any good ON THIS DEVICE?",
        epilog="examples:\n"
               "  evalkit run qwen3-coder:30b --full\n"
               "  evalkit run some-new-model:tag -k 3\n"
               "  evalkit board\n"
               "  evalkit diag dolphin3:8b\n"
               "  evalkit compare qwen3-coder:30b dolphin3:8b\n",
        formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--version", action="version", version="evalkit 0.1.0")
    sub = p.add_subparsers(dest="cmd", metavar="<command>")

    def common(sp):
        sp.add_argument("--display", default="auto",
                        choices=["auto", "rich", "plain", "json", "none"],
                        help="output mode (default: auto — rich on a TTY, plain otherwise)")
        sp.add_argument("-q", "--quiet", action="count", default=0,
                        help="-q hides chrome, -qq silences everything")
        sp.add_argument("-v", "--verbose", action="count", default=0,
                        help="more detail; also hides the progress bar")
        sp.add_argument("--no-progress", action="store_true")
        return sp

    r = common(sub.add_parser("run", help="measure a model on this device"))
    r.add_argument("model")
    r.add_argument("--quick", action="store_true", help="speed+memory+coding+tools (~4 min)")
    r.add_argument("--full", action="store_true", help="adds the 20-turn agentic task (~15 min)")
    r.add_argument("-k", "--repeats", type=int, default=None,
                   help="repeats per noisy task (default 3; 1 with --quick). "
                        "A single run is not a measurement.")
    r.add_argument("--profile", default=None, help="device profile (default: auto-match)")
    r.set_defaults(fn=cmd_run)

    b = common(sub.add_parser("board", help="compare everything measured so far"))
    b.add_argument("--current", action="store_true",
                   help="only rows matching this machine's current config")
    b.set_defaults(fn=cmd_board)

    d = common(sub.add_parser("diag", help="tool-use plumbing diagnostic"))
    d.add_argument("model")
    d.set_defaults(fn=cmd_diag)

    dv = common(sub.add_parser("device", help="show this machine's fingerprint"))
    dv.add_argument("--profile", default=None)
    dv.set_defaults(fn=cmd_device)

    pf = common(sub.add_parser("profiles", help="list device profiles"))
    pf.set_defaults(fn=cmd_profiles)

    c = common(sub.add_parser("compare", help="paired comparison of two stored results"))
    c.add_argument("model_a")
    c.add_argument("model_b")
    c.set_defaults(fn=cmd_compare)
    return p


def main(argv=None) -> int:
    parser = build_parser()
    a = parser.parse_args(argv)
    if not getattr(a, "fn", None):
        parser.print_help()
        return EXIT_USAGE
    try:
        return a.fn(a)
    except KeyboardInterrupt:
        print("\ninterrupted", file=sys.stderr)
        return EXIT_INTERRUPT
    except SystemExit as e:
        return int(e.code or 0)


if __name__ == "__main__":
    sys.exit(main())
