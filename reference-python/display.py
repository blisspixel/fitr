#!/usr/bin/env python3
"""Display layer: one renderer core, several thin drivers.

Structure borrowed from Inspect AI (UK AISI), which is the best-organised eval
TUI going: all renderables live here, and each output mode is a thin driver over
one protocol. Adding a mode is a small function, not a fork of the renderer.

Deliberately Rich-only, no Textual. Inspect spent ~18 months fighting Textual
major-version churn and still carries dual-API compatibility branches; plain
Rich gets most of the value for a fraction of the maintenance.

Modes
  auto   pick rich if stdout is a TTY, else plain          (default)
  rich   live progress + styled panels
  plain  line-oriented, no ANSI, no cursor tricks          (CI, pipes, logs)
  json   NDJSON on stdout, nothing else                    (machine-readable)
  none   silent

Stream discipline (from promptfoo): progress goes to STDERR, results to STDOUT,
so `evalkit run ... > results.txt` stays clean and pipeable.
"""
from __future__ import annotations

import atexit
import json
import os
import re
import signal
import shutil
import sys
import time

from rich.console import Console, Group
from rich.panel import Panel
from rich.progress import (BarColumn, Progress, SpinnerColumn, TextColumn,
                           TimeElapsedColumn)
from rich.table import Table
from rich.text import Text

# ---------------------------------------------------------------- palette
# Semantic names only -- never reference a raw colour outside this block.
THEME = {
    "pass": "green",
    "fail": "red",
    "skip": "bright_black",
    "na": "bright_black",
    "blocked": "yellow",
    "warn": "orange3",
    "metric": "cyan",
    "muted": "bright_black",
    "head": "bold blue",
    "accent": "magenta",
}

STATE_STYLE = {"PASS": "pass", "FAIL": "fail", "SKIP": "skip",
               "n/a": "na", "BLKD": "blocked"}

# Column caps, content-fit within them. promptfoo divides the terminal evenly
# across columns, which is exactly why its tables are unreadable -- do not.
CAP_MODEL, CAP_ROLE, CAP_WHY = 34, 46, 78

# Glyphs: never assume the console can encode them. Windows consoles default to
# cp1252 and will render mojibake for the usual typographic characters. Probe the
# actual stream encoding once and fall back to ASCII rather than emitting junk.
def _unicode_ok(enc=None) -> bool:
    if os.environ.get("EVALKIT_ASCII"):
        return False
    if enc is None:
        enc = getattr(sys.stdout, "encoding", "") or ""
    enc = enc.lower().replace("-", "")
    if not enc:
        return False
    # Encodability is NOT renderability. cp1252 happily encodes the typographic
    # glyphs (0xB7, 0x97, 0xB1, 0x85), but a Windows console on codepage 437
    # draws entirely different characters for those bytes -- mojibake. Only a
    # UTF stream is safe. EVALKIT_UNICODE=1 forces it back on.
    if os.environ.get("EVALKIT_UNICODE"):
        return True
    if not enc.startswith("utf"):
        return False
    try:
        "·-±…".encode(enc)
        return True
    except (UnicodeEncodeError, LookupError):
        return False


_U = _unicode_ok()
GLYPH = {
    "dot": " · " if _U else " | ",
    "dash": "-",
    "pm": "±" if _U else "+/-",
    "ell": "…" if _U else "...",
    "range": "-",
}

# Model output is UNTRUSTED input to your terminal. Inspect added escape
# sanitisation for precisely this; a model can emit ANSI that rewrites your
# screen, spoofs a prompt, or hides text.
_ANSI = re.compile(r"\x1b\[[0-?]*[ -/]*[@-~]|\x1b[@-Z\\-_]|[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]")


def sanitize(s: str) -> str:
    """Strip ANSI/control sequences from model-generated text before display."""
    return _ANSI.sub("", s or "")


def fit(s: str, cap: int) -> str:
    """Truncate on a word boundary where possible -- cutting mid-word reads as a
    rendering bug, not as elision."""
    s = sanitize(str(s or ""))
    if len(s) <= cap:
        return s
    e = GLYPH["ell"]
    keep = cap - len(e)
    cut = s[:keep]
    sp = cut.rfind(" ")
    if sp > keep * 0.6:          # only snap back if we do not lose too much
        cut = cut[:sp]
    return cut.rstrip(" ,;") + e


# ---------------------------------------------------------------- environment
def _no_color() -> bool:
    # NO_COLOR is a standard (no-color.org): any non-empty value disables colour.
    if os.environ.get("NO_COLOR"):
        return True
    if os.environ.get("FORCE_COLOR"):
        return False
    return not sys.stdout.isatty()


def _dumb_terminal_size():
    """Rich falls back to a hardcoded 80x25 for TERM=dumb unless BOTH width and
    height are known. Honour COLUMNS/LINES so CI logs do not wrap at 80."""
    if os.environ.get("TERM") != "dumb":
        return {}
    try:
        cols = int(os.environ.get("COLUMNS", "0"))
    except ValueError:
        cols = 0
    if not cols:
        return {}
    try:
        lines = int(os.environ.get("LINES", "0")) or 25
    except ValueError:
        lines = 25
    return {"width": cols, "height": lines}


def resolve_mode(requested: str = "auto") -> str:
    if requested and requested != "auto":
        return requested
    if not sys.stdout.isatty():
        return "plain"
    if os.environ.get("TERM") == "dumb":
        return "plain"
    return "rich"


# ---------------------------------------------------------------- consoles
def _console(stderr: bool = False) -> Console:
    return Console(
        stderr=stderr,
        no_color=_no_color(),
        soft_wrap=False,
        highlight=False,
        **_dumb_terminal_size(),
    )


# ---------------------------------------------------------------- renderables
def scorecard_renderable(sc: dict, res: dict, labels: dict):
    meta = res.get("model_meta") or {}
    dev = res.get("device") or {}
    reps = res.get("repeats") or 1

    head = Table.grid(padding=(0, 2))
    head.add_column(style=THEME["muted"], no_wrap=True)
    head.add_column(overflow="fold")
    head.add_row("model", Text(fit(sc["model"], CAP_MODEL), style="bold"))
    head.add_row("size", f"{meta.get('parameter_size','?')}  {meta.get('quantization','')}  {meta.get('family','')}")
    head.add_row("use for", Text(sanitize(sc["use_it_for"]), style=THEME["accent"]))
    D_ = GLYPH["dot"]
    head.add_row("device", f"{dev.get('gpu','?')}{D_}driver {dev.get('gpu_driver','?')}{D_}"
                           f"{dev.get('inference_device','?')}{D_}profile {res.get('profile') or 'auto'}")

    needs = Table(box=None, pad_edge=False, show_edge=False, expand=False)
    needs.add_column("", width=6, no_wrap=True)
    needs.add_column("need", width=34, no_wrap=True)
    needs.add_column("evidence", overflow="fold")
    for key, v in sc["needs"].items():
        st = v["state"]
        needs.add_row(
            Text(f"[{st}]", style=THEME[STATE_STYLE.get(st, "muted")]),
            labels.get(key, key),
            Text(fit(v["why"], CAP_WHY * 2), style=THEME["muted"]),
        )

    parts = [head, "", needs]

    sr = res.get("speed_repeats") or {}
    if sr:
        d = sr.get("decode_tps", {}) or {}
        p = sr.get("prefill_tps", {}) or {}

        def stat(label, st):
            """A single observation is not an estimate -- never print '+/- 0.00'."""
            mean, sd, n = st.get("mean"), st.get("sd"), st.get("n") or 0
            if mean is None:
                return f"{label} n/a"
            if n < 2 or not sd:
                return f"{label} {mean} (abs, n=1)"
            return (f"{label} {mean} {GLYPH['pm']}{sd} "
                    f"(min {st.get('min')}, max {st.get('max')})")

        parts += ["", Text(f"over {reps} repeats   " + stat("decode", d)
                           + "   " + stat("prefill", p), style=THEME["metric"])]

    if reps < 3:
        parts += ["", Text(
            f"single-sample run {GLYPH['dash']} identical configs vary "
            f"10{GLYPH['range']}20pp between runs; "
            "re-run with -k 3 before ranking against a close model",
            style=THEME["warn"])]

    return Panel(Group(*parts), title="evalkit", title_align="left",
                 border_style=THEME["muted"], padding=(1, 2))


# Short codes keep the needs column from collapsing in a narrow terminal.
NEED_CODE = {
    "fast_and_decent": "fast", "coding": "code", "uncensored": "unfiltered",
    "unattended_agentic": "agentic", "tool_restraint": "restraint",
    "low_footprint": "small", "vision": "vision",
}


def board_renderable(groups: dict, current_key: str) -> Group:
    """One block per device fingerprint. Never rank across blocks."""
    out = []
    for key, rows in groups.items():
        g = rows[-1]
        is_cur = key == current_key
        head = f"{g.get('gpu','?')}{GLYPH['dot']}driver {g.get('driver','?')}{GLYPH['dot']}KV {g.get('kv','?')}"
        note = ("this machine, current config" if is_cur
                else f"different hardware/config {GLYPH['dash']} not comparable to other blocks")

        t = Table(box=None, pad_edge=False, expand=False, show_edge=False)
        t.add_column("model", width=26, no_wrap=True, style="bold")
        t.add_column("params", width=6, justify="right", no_wrap=True)
        t.add_column("tok/s", width=7, justify="right", no_wrap=True)
        t.add_column("sd", width=6, justify="right", no_wrap=True)
        t.add_column("TTFT", width=6, justify="right", no_wrap=True)
        t.add_column("prefill", width=7, justify="right", no_wrap=True)
        t.add_column("GB@32K", width=6, justify="right", no_wrap=True)
        t.add_column("k", width=3, justify="right", no_wrap=True)
        t.add_column("serves", overflow="fold", style=THEME["muted"])

        def _f(x):
            try:
                return float(x)
            except Exception:
                return -1.0

        for r in sorted(rows, key=lambda r: -_f(r.get("tok_s"))):
            serves = [NEED_CODE.get(n, n) for n in
                      (r.get("needs_served") or "").split(";") if n]
            sd = r.get("tok_s_sd")
            sd_s = "" if not sd or str(sd) in ("0", "0.0") else str(sd)
            t.add_row(
                fit(r.get("model", ""), 26), r.get("params", ""),
                str(r.get("tok_s", ""))[:7], sd_s,
                r.get("ttft_s", ""), r.get("prefill_tps", ""),
                r.get("resident_32k_gb", ""), str(r.get("repeats") or "?"),
                ", ".join(serves) or "-")
        out += [Text(head, style=THEME["head"]),
                Text("  " + note, style=THEME["pass"] if is_cur else THEME["warn"]),
                t, ""]

    out.append(Text("k = repeats. k<3 is a smoke test, not a rankable result.",
                    style=THEME["muted"]))
    if len(groups) > 1:
        out.append(Text("blocks were measured under different hardware/config "
                        + GLYPH["dash"] + " re-measure rather than ranking across them",
                        style=THEME["warn"]))
    return Group(*out)


# ---------------------------------------------------------------- drivers
class Display:
    """Protocol: phase() / note() / done() / result() / emit()."""

    def phase(self, name, detail=""): ...
    def note(self, msg, style="muted"): ...
    def done(self, name, seconds): ...
    def result(self, sc, res, labels): ...
    def emit(self, obj): ...
    def close(self): ...


class RichDisplay(Display):
    """Live progress on stderr; final result on stdout."""

    # Never show 0% on a started task or 100% before it is finished -- a bar
    # that reads 100% while still working destroys trust in the whole tool.
    FLOOR, CEIL = 0.02, 0.98

    def __init__(self, total_phases: int = 6):
        self.err = _console(stderr=True)
        self.out = _console()
        self.progress = Progress(
            SpinnerColumn(style=THEME["accent"]),
            TextColumn("[bold]{task.description}"),
            BarColumn(bar_width=28, complete_style=THEME["pass"],
                      finished_style=THEME["pass"], pulse_style=THEME["accent"]),
            TextColumn("{task.fields[detail]}", style=THEME["muted"]),
            TimeElapsedColumn(),
            console=self.err, transient=True, refresh_per_second=8,
        )
        self.total = total_phases
        self.n = 0
        self.task = None
        self.progress.start()
        # Rich only restores the cursor via Live.stop(). A bare start() plus a
        # Ctrl-C leaves the terminal with a hidden cursor and no live region.
        atexit.register(self.close)
        self._prev_sigint = signal.getsignal(signal.SIGINT)
        try:
            signal.signal(signal.SIGINT, self._on_sigint)
        except (ValueError, OSError):
            self._prev_sigint = None   # not the main thread; nothing to restore

    def _on_sigint(self, signum, frame):
        self.close()
        if callable(self._prev_sigint):
            self._prev_sigint(signum, frame)
        raise KeyboardInterrupt

    def phase(self, name, detail=""):
        frac = min(self.CEIL, max(self.FLOOR, self.n / max(1, self.total)))
        if self.task is None:
            self.task = self.progress.add_task(name, total=1.0, completed=frac,
                                               detail=detail)
        else:
            self.progress.update(self.task, description=name, completed=frac,
                                 detail=detail)

    def note(self, msg, style="muted"):
        self.err.print(Text("  " + sanitize(msg), style=THEME.get(style, style)))

    def done(self, name, seconds):
        self.n += 1
        if self.task is not None:
            self.progress.update(self.task, detail=f"{name} {seconds:.1f}s")

    def result(self, sc, res, labels):
        if self.task is not None:
            self.progress.update(self.task, completed=1.0, detail="")
        self.progress.stop()
        self.out.print(scorecard_renderable(sc, res, labels))

    def emit(self, obj):
        pass

    def close(self):
        try:
            self.progress.stop()
        except Exception:
            pass
        try:
            self.err.show_cursor(True)
        except Exception:
            pass
        if getattr(self, "_prev_sigint", None) is not None:
            try:
                signal.signal(signal.SIGINT, self._prev_sigint)
            except (ValueError, OSError):
                pass
            self._prev_sigint = None


class PlainDisplay(Display):
    """Line-oriented, no ANSI, no cursor control. Safe for CI and pipes."""

    def __init__(self, total_phases: int = 6):
        self.t0 = time.time()

    def phase(self, name, detail=""):
        print(f"[{time.time()-self.t0:7.1f}s] {name} {detail}".rstrip(),
              file=sys.stderr, flush=True)

    def note(self, msg, style="muted"):
        print(f"           {sanitize(msg)}", file=sys.stderr, flush=True)

    def done(self, name, seconds):
        print(f"[{time.time()-self.t0:7.1f}s] {name} done in {seconds:.1f}s",
              file=sys.stderr, flush=True)

    def result(self, sc, res, labels):
        w = 78
        print("-" * w)
        print(f"model    {sc['model']}")
        print(f"use for  {sc['use_it_for']}")
        print(f"device   {(res.get('device') or {}).get('gpu','?')} | "
              f"driver {(res.get('device') or {}).get('gpu_driver','?')} | "
              f"profile {res.get('profile','?')}")
        print("-" * w)
        for key, v in sc["needs"].items():
            print(f"[{v['state']:<4}] {labels.get(key,key):<32} {sanitize(v['why'])}")
        if (res.get("repeats") or 1) < 3:
            print("\n! single-sample run; re-run with -k 3 before ranking close models")
        print("-" * w)

    def emit(self, obj):
        pass

    def close(self):
        pass


class JsonDisplay(Display):
    """NDJSON on stdout and nothing else. Progress notes go to stderr."""

    def phase(self, name, detail=""):
        print(json.dumps({"event": "phase", "name": name, "detail": detail}),
              flush=True)

    def note(self, msg, style="muted"):
        print(sanitize(msg), file=sys.stderr, flush=True)

    def done(self, name, seconds):
        print(json.dumps({"event": "phase_done", "name": name,
                          "seconds": round(seconds, 2)}), flush=True)

    def result(self, sc, res, labels):
        print(json.dumps({"event": "result", "model": sc["model"],
                          "use_for": sc["use_it_for"],
                          "needs": {k: v["state"] for k, v in sc["needs"].items()},
                          "serves": sc["serves"]}), flush=True)

    def emit(self, obj):
        print(json.dumps(obj), flush=True)

    def close(self):
        pass


class NoneDisplay(Display):
    def phase(self, name, detail=""): ...
    def note(self, msg, style="muted"): ...
    def done(self, name, seconds): ...
    def result(self, sc, res, labels): ...
    def emit(self, obj): ...
    def close(self): ...


def make_display(mode: str = "auto", total_phases: int = 6) -> Display:
    m = resolve_mode(mode)
    if m == "json":
        return JsonDisplay()
    if m == "none":
        return NoneDisplay()
    if m == "plain":
        return PlainDisplay(total_phases)
    try:
        return RichDisplay(total_phases)
    except Exception:
        # Never fail because the display could not start.
        return PlainDisplay(total_phases)
