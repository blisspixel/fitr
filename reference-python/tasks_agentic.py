#!/usr/bin/env python3
"""Long-horizon unattended-agentic test.

The 4-call tool test proves a model can emit valid JSON. This proves (or
disproves) that it can run unsupervised: explore a repo, edit several files,
verify with a test runner, iterate on failures, and STOP when done.

Scored on what actually breaks overnight runs: malformed calls, looping on
the same action, and failing to terminate.

Usage: python agentic.py <model> <outdir>
"""
import json, os, re, shutil, sys, tempfile, time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import tasks_core as E

MAX_TURNS = int(os.environ.get('AGENTIC_TURNS', '40'))
TIME_BUDGET = 2400

VALIDATORS = '''"""Validation helpers for the project. Currently unimplemented."""
'''

USERS = '''from validators import *


def create_user(name, age):
    return {"name": name, "age": age}


def rename_user(user, new_name):
    user["name"] = new_name
    return user
'''

ORDERS = '''from validators import *


def create_order(user, total):
    return {"user": user, "total": total}
'''

TEST_ALL = '''from users import create_user, rename_user
from orders import create_order

u = create_user("ada", 36)
assert u == {"name": "ada", "age": 36}, u
assert rename_user(dict(u), "Ada")["name"] == "Ada"
assert create_order(u, 10.5)["total"] == 10.5

for bad in [("", 36), ("ada", -1), ("ada", "x"), (None, 5)]:
    try:
        create_user(*bad)
    except ValueError:
        pass
    else:
        raise AssertionError("create_user%r must raise ValueError" % (bad,))

try:
    rename_user(dict(u), "")
except ValueError:
    pass
else:
    raise AssertionError("rename_user with empty name must raise ValueError")

for bad_total in [-5, "x", None]:
    try:
        create_order(u, bad_total)
    except ValueError:
        pass
    else:
        raise AssertionError("create_order total=%r must raise ValueError" % (bad_total,))

print("AGENT_OK")
'''

TASK = """You are working unsupervised in a small Python project.

Goal: make `python test_all.py` pass.

Right now the public functions in users.py and orders.py accept anything.
The tests require them to raise ValueError on invalid input. Read the tests
to learn the exact rules, implement whatever helpers you need in
validators.py, and update users.py and orders.py to use them.

Use the tools. Call run_tests to check your work and iterate on failures.
When run_tests reports success, reply with the single word DONE and stop."""

TOOLS = [
    {"type": "function", "function": {
        "name": "list_files", "description": "List files in the project.",
        "parameters": {"type": "object", "properties": {}, "required": []}}},
    {"type": "function", "function": {
        "name": "read_file", "description": "Read a file's full contents.",
        "parameters": {"type": "object", "properties": {
            "path": {"type": "string"}}, "required": ["path"]}}},
    {"type": "function", "function": {
        "name": "write_file", "description": "Overwrite a file with new contents.",
        "parameters": {"type": "object", "properties": {
            "path": {"type": "string"}, "content": {"type": "string"}},
            "required": ["path", "content"]}}},
    {"type": "function", "function": {
        "name": "run_tests", "description": "Run python test_all.py and return its output.",
        "parameters": {"type": "object", "properties": {}, "required": []}}},
]


def main():
    MODEL = sys.argv[1]
    OUT = sys.argv[2] if len(sys.argv) > 2 else "."
    d = tempfile.mkdtemp(prefix="agentic_")
    for fn, body in (("validators.py", VALIDATORS), ("users.py", USERS),
                     ("orders.py", ORDERS), ("test_all.py", TEST_ALL)):
        open(os.path.join(d, fn), "w", encoding="utf-8").write(body)

    written, calls, malformed, sig_counts = set(), [], 0, {}

    def do_tool(name, args):
        if name == "list_files":
            return "\n".join(sorted(os.listdir(d)))
        if name == "read_file":
            p = os.path.join(d, os.path.basename(str(args.get("path", ""))))
            try:
                return open(p, encoding="utf-8").read()
            except Exception as e:
                return f"ERROR: {e}"
        if name == "write_file":
            p = os.path.join(d, os.path.basename(str(args.get("path", ""))))
            c = args.get("content", "")
            if not isinstance(c, str):
                c = json.dumps(c)
            try:
                open(p, "w", encoding="utf-8").write(c)
                written.add(os.path.basename(p))
                return f"wrote {len(c)} bytes to {os.path.basename(p)}"
            except Exception as e:
                return f"ERROR: {e}"
        if name == "run_tests":
            ok, out = E.run_py("test_all.py", d)
            return ("PASS\n" + out) if ok else ("FAIL\n" + out)
        return f"ERROR: unknown tool {name}"

    msgs = [{"role": "user", "content": TASK}]
    t0 = time.perf_counter()
    turns = 0
    ended = "turn_cap"
    for turn in range(MAX_TURNS):
        turns = turn + 1
        if time.perf_counter() - t0 > TIME_BUDGET:
            ended = "time_budget"
            break
        try:
            r = E.chat(MODEL, msgs, tools=TOOLS, num_predict=1200)
        except Exception as e:
            ended = f"error: {type(e).__name__}: {str(e)[:120]}"
            break
        msg = r.get("message", {}) or {}
        tcs = msg.get("tool_calls") or []
        content = msg.get("content") or ""
        msgs.append({"role": "assistant", "content": content,
                     **({"tool_calls": tcs} if tcs else {})})
        if not tcs:
            ended = "clean_stop" if "DONE" in content.upper() else "stopped_without_done"
            break
        for c in tcs:
            fn = c.get("function", {}) or {}
            name = fn.get("name", "")
            args = fn.get("arguments", {})
            if isinstance(args, str):
                try:
                    args = json.loads(args)
                except Exception:
                    malformed += 1
                    args = {}
            if not isinstance(args, dict):
                malformed += 1
                args = {}
            if name not in ("list_files", "read_file", "write_file", "run_tests"):
                malformed += 1
            calls.append(name)
            sig = name + "|" + str(args.get("path", ""))[:40] + "|" + str(hash(str(args.get("content", ""))[:200]))
            sig_counts[sig] = sig_counts.get(sig, 0) + 1
            out = do_tool(name, args)
            msgs.append({"role": "tool", "content": str(out)[:4000],
                         **({"tool_name": name} if name else {})})

    wall = round(time.perf_counter() - t0, 1)
    ok, out = E.run_py("test_all.py", d)
    passed = bool(ok and "AGENT_OK" in out)
    repeats = sum(v - 1 for v in sig_counts.values() if v > 1)

    res = {
        "model": MODEL, "passed": passed, "turns": turns, "wall_s": wall,
        "tool_calls": len(calls), "malformed_calls": malformed,
        "repeated_identical_calls": repeats,
        "looped": repeats >= 3,
        "ended": ended,
        "files_written": sorted(written),
        "ran_tests_n": calls.count("run_tests"),
        "call_sequence": "".join({"list_files": "L", "read_file": "R",
                                  "write_file": "W", "run_tests": "T"}.get(c, "?")
                                 for c in calls),
        "final_test_output": out[-400:],
    }
    E.unload(MODEL)
    shutil.rmtree(d, ignore_errors=True)
    safe = re.sub(r"[^A-Za-z0-9_.-]", "_", MODEL)
    p = os.path.join(OUT, f"result_{safe}__agentic.json")
    json.dump(res, open(p, "w", encoding="utf-8"), indent=2)
    print(json.dumps({k: v for k, v in res.items() if k != "final_test_output"}, indent=2))
    print("WROTE " + p)


if __name__ == "__main__":
    main()
