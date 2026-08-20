"""Tests for the parts that must not silently rot.

Every test here exists because the corresponding bug actually happened during
development, not because the function looked testable.

No Ollama required - these are pure-logic tests.
"""
import json
import os
import sys

import pytest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import display  # noqa: E402
import score  # noqa: E402
import stats  # noqa: E402


# --------------------------------------------------------------- statistics
def test_wilson_single_sample_is_barely_informative():
    """One pass must NOT read as certainty. This is the whole argument for -k 3."""
    point, lo, hi = stats.wilson(1, 1)
    assert point == 1.0
    assert lo < 0.25, "a single pass should leave a very wide interval"
    assert hi == 1.0


def test_wilson_matches_known_value():
    point, lo, hi = stats.wilson(8, 10)
    assert point == 0.8
    assert 0.48 < lo < 0.50
    assert 0.94 < hi < 0.95


def test_wilson_handles_zero_and_full():
    assert stats.wilson(0, 5)[1] == 0.0
    assert stats.wilson(5, 5)[2] == 1.0
    assert stats.wilson(0, 0) == (0.0, 0.0, 1.0)


def test_overlapping_intervals_are_reported_as_indistinguishable():
    a, b = stats.wilson(3, 3), stats.wilson(2, 3)
    assert stats.intervals_overlap(a, b)
    assert "INDISTINGUISHABLE" in stats.compare("A", a, "B", b)


def test_clearly_separated_intervals_are_ranked():
    a, b = stats.wilson(20, 20), stats.wilson(2, 20)
    assert not stats.intervals_overlap(a, b)
    assert "A > B" in stats.compare("A", a, "B", b)


def test_flakiness_detects_a_flipping_task():
    assert stats.flakiness([True, False, True])["flaky"] is True
    assert stats.flakiness([True, True, True])["flaky"] is False
    assert stats.flakiness([False, False])["flaky"] is False


def test_mean_sd_single_observation_has_zero_sd_and_n1():
    st = stats.mean_sd([5.0])
    assert st["n"] == 1 and st["sd"] == 0.0


# --------------------------------------------------------------- degeneracy
LOOPING = ("| Source | Claim | Evidence |\n|---|---|---|\n| A | cycle | yes |\n\n") * 11
CLEAN = ("The market split in two this August. Vintage cards climbed while "
         "overprinted modern sets slid.\n\n"
         "Grading remains the liquidity gate, and PSA still clears fastest.\n\n"
         "Reprints are the risk nobody prices in until it lands, which is late.\n\n"
         "Sealed product is where the scarcity actually bites hardest now.\n")


def test_degeneracy_catches_a_looping_table():
    """The bug that motivated all of this: a 104 KB report passed every
    structural check while 25% of its paragraphs were duplicates."""
    m = score.repetition_metrics(LOOPING)
    assert m["dup_line_ratio"] > 0.5
    assert m["gzip_compression_ratio"] > 4.0
    assert m["repetition_score"] > 0.5


def test_single_metric_would_have_missed_it():
    """dup_paragraph_ratio scores the looping sample at 0.0 because its
    120-char paragraph filter skips short repeated blocks. This test pins the
    reason the gate uses MULTIPLE signals."""
    m = score.repetition_metrics(LOOPING)
    assert m["dup_paragraph_ratio"] == 0.0
    assert m["dup_line_ratio"] > 0.5, "the other signals must still catch it"


def test_clean_prose_passes_every_signal():
    m = score.repetition_metrics(CLEAN)
    assert m["dup_paragraph_ratio"] == 0.0
    assert m["dup_line_ratio"] == 0.0
    assert m["gzip_compression_ratio"] < 4.0


def test_short_text_is_not_judged():
    m = score.repetition_metrics("too short")
    assert m["words"] == 0


# --------------------------------------------------------------- scoring
def _profile():
    return json.load(open(os.path.join(
        os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
        "profiles", "lappy.json"), encoding="utf-8"))


def _result(**over):
    base = {
        "model": "test:1b", "repeats": 3,
        "model_meta": {"capabilities": ["completion", "tools"],
                       "parameter_size": "1B", "quantization": "Q4"},
        "speed": {"gen200": {"decode_tps": 30.0, "ttft_s": 0.5},
                  "ctx2k": {"prefill_tps": 200.0}},
        "memory": {"resident_gb_at_32k": 5.0},
        "code_write": {"pass": True}, "code_fix": {"pass": True},
        "tools": {"pass": True, "malformed_calls": 0},
        "repetition": score.repetition_metrics(CLEAN),
    }
    base.update(over)
    return base


def test_vision_absence_is_na_not_failure():
    """A text-only model is not 'bad at vision'. Calling that a FAIL was a real
    bug that made a keeper model look broken."""
    sc = score.scorecard(_result(), _profile())
    assert sc["needs"]["vision"]["state"] == score.NA
    assert sc["fails"] == 0


def test_unmeasured_need_is_skip_not_failure():
    sc = score.scorecard(_result(), _profile())
    assert sc["needs"]["uncensored"]["state"] == score.SKIP
    assert "not run" in sc["needs"]["uncensored"]["why"]


def test_broken_plumbing_blocks_rather_than_fails_agentic():
    """~4 in 5 'model can't do tools' results are the template/parser. A
    capability we could not fairly test must not be recorded as a failure."""
    r = _result(plumbing={"verdict": "template/parser problem",
                          "rungs": {"1_capability": {"pass": True},
                                    "2_emits_tool_call": {"pass": False},
                                    "3_valid_args": {"pass": False},
                                    "4_roundtrip": {"pass": False}}})
    sc = score.scorecard(r, _profile())
    assert sc["needs"]["unattended_agentic"]["state"] == score.BLOCKED


def test_tool_restraint_flags_firing_on_irrelevant_questions():
    r = _result(plumbing={"rungs": {
        "1_capability": {"pass": True}, "2_emits_tool_call": {"pass": True},
        "3_valid_args": {"pass": True}, "4_roundtrip": {"pass": True},
        "5_irrelevance": {"pass": False, "spurious_calls": 1}}})
    sc = score.scorecard(r, _profile())
    assert sc["needs"]["tool_restraint"]["state"] == score.FAIL


def test_never_says_not_recommended():
    """The tool must never render a bare dismissal; if nothing passed it should
    say how much went unmeasured."""
    r = _result(speed={"gen200": {"decode_tps": 1.0, "ttft_s": 9.0},
                       "ctx2k": {"prefill_tps": 5.0}},
                code_write={"pass": False}, code_fix={"pass": False},
                memory={"resident_gb_at_32k": 99.0})
    sc = score.scorecard(r, _profile())
    assert "not recommended" not in sc["use_it_for"].lower()


def test_missing_gate_in_profile_skips_rather_than_fails():
    sc = score.scorecard(_result(), {"name": "empty", "gates": {}})
    assert sc["needs"]["fast_and_decent"]["state"] == score.SKIP
    assert sc["fails"] == 0


# --------------------------------------------------------------- display
def test_ascii_fallback_when_console_cannot_render_unicode(monkeypatch):
    """cp1252 CAN encode the typographic glyphs, but a cp437 console renders
    different characters for those bytes. Encodability is not renderability."""
    monkeypatch.delenv("EVALKIT_UNICODE", raising=False)
    monkeypatch.delenv("EVALKIT_ASCII", raising=False)
    assert display._unicode_ok("cp1252") is False
    assert display._unicode_ok("cp437") is False


def test_utf8_console_gets_unicode(monkeypatch):
    monkeypatch.delenv("EVALKIT_ASCII", raising=False)
    assert display._unicode_ok("utf-8") is True
    assert display._unicode_ok("UTF8") is True


def test_no_color_env_is_honored_but_empty_means_unset(monkeypatch):
    """no-color.org: present AND NOT EMPTY disables colour. `NO_COLOR=""` must
    NOT disable it -- the classic off-by-one in this spec."""
    monkeypatch.setenv("NO_COLOR", "1")
    assert display._no_color() is True
    monkeypatch.setenv("NO_COLOR", "")
    monkeypatch.setenv("FORCE_COLOR", "1")
    assert display._no_color() is False


def test_non_tty_resolves_to_plain(monkeypatch):
    monkeypatch.setattr(display.sys.stdout, "isatty", lambda: False)
    assert display.resolve_mode("auto") == "plain"


def test_explicit_mode_wins():
    assert display.resolve_mode("json") == "json"
    assert display.resolve_mode("none") == "none"


def test_model_output_cannot_inject_terminal_escapes():
    """Model output is untrusted input to the terminal; it can spoof prompts or
    hide text with ANSI."""
    nasty = "hello\x1b[2J\x1b[1;1Hspoofed\x07"
    assert display.sanitize(nasty) == "hellospoofed"


def test_truncation_snaps_to_word_boundary():
    out = display.fit("daily driver coding and agents plus more", 24)
    assert not out.rstrip(display.GLYPH["ell"]).endswith(" ")
    assert len(out) <= 24


# --------------------------------------------------------------- snapshot
def test_scorecard_renders_without_crashing_in_all_modes():
    r = _result()
    sc = score.scorecard(r, _profile())
    for mode in ("plain", "json", "none"):
        d = display.make_display(mode)
        d.result(sc, r, score.NEED_LABEL)
        d.close()


def test_scorecard_snapshot_is_stable():
    """The UI is text, so a diff IS the test."""
    from rich.console import Console
    r = _result()
    sc = score.scorecard(r, _profile())
    con = Console(record=True, width=100, no_color=True, legacy_windows=False)
    con.print(display.scorecard_renderable(sc, r, score.NEED_LABEL))
    out = con.export_text(clear=False)
    assert "test:1b" in out
    assert "[PASS]" in out and "[n/a]" in out and "[SKIP]" in out
    assert "+/- 0.0" not in out, "never print a fabricated zero stddev"


def _spec_dir():
    """The spec is canonical at the REPO ROOT, shared with the Go
    implementation. A second copy under this directory would silently drift,
    which is exactly the failure mode the generated spec exists to prevent."""
    import os
    here = os.path.dirname(os.path.abspath(__file__))
    for _ in range(4):
        here = os.path.dirname(here)
        cand = os.path.join(here, "spec", "tasks")
        if os.path.isdir(cand):
            return cand
    raise AssertionError("cannot locate spec/tasks from the repo root")


# --------------------------------------------------------------- sync with Go
def test_stop_all_ignores_already_expired_models():
    """Ollama lists a model briefly after its expires_at passes. Counting those
    as resident makes stop_all spin and then fail on a model already gone."""
    import datetime as dt
    import tasks_memory as M
    past = (dt.datetime.now(dt.timezone.utc) - dt.timedelta(minutes=5)).isoformat()
    future = (dt.datetime.now(dt.timezone.utc) + dt.timedelta(minutes=5)).isoformat()
    live = M._still_resident([
        {"name": "gone", "expires_at": past},
        {"name": "here", "expires_at": future},
        {"name": "no-expiry-field"},
    ])
    names = [m["name"] for m in live]
    assert "gone" not in names, "an expired model must not count as resident"
    assert "here" in names
    assert "no-expiry-field" in names, "absent expiry must be treated as live"


def test_spec_prompts_have_no_unresolved_placeholders():
    """The extracted spec once shipped the raw template with {d}/{c}/{t}, so
    models were asked to fix code they could not see and were scored as
    failing. Pin it here too, not just in the Go port."""
    import json, os
    base = _spec_dir()
    for name in ("code_write", "code_fix"):
        spec = json.load(open(os.path.join(base, name + ".json"), encoding="utf-8"))
        prompt, files = spec["prompt"], spec.get("files", {})
        for token in ("{d}", "{c}", "{t}"):
            assert token not in prompt, f"{name} still has bare {token}"
        # {{file:NAME}} placeholders must all resolve against the task's files
        import re
        for ref in re.findall(r"\{\{file:([^}]+)\}\}", prompt):
            assert ref in files, f"{name} references {ref} which it does not ship"


def test_code_fix_prompt_actually_contains_the_source():
    import json, os, re
    base = _spec_dir()
    spec = json.load(open(os.path.join(base, "code_fix.json"), encoding="utf-8"))
    rendered = spec["prompt"]
    for name, body in spec["files"].items():
        rendered = rendered.replace("{{file:%s}}" % name, body)
    for want in ("def percent_off", "def total", "CART_OK"):
        assert want in rendered, f"rendered prompt missing {want!r}; model would be guessing"
