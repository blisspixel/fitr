#!/usr/bin/env python3
"""Tool-use PLUMBING diagnostic -- run before blaming a model for 'bad at tools'.

Field reports converge on this: when a local model appears unable to use tools,
roughly 4 times in 5 the real cause is the chat template, the tool-call parser,
the quant, or the context size -- not the weights. Ollama reports a `tools`
capability, but that flag says the template CLAIMS support, not that the
round-trip works.

Five rungs, cheapest first. Stop at the first failure: everything above it is
uninterpretable.

  1 capability   does Ollama advertise `tools` at all?
  2 emits        does a blunt, unambiguous request produce a tool_calls array?
  3 args         are the arguments valid JSON matching the declared schema?
  4 roundtrip    does it accept a role:"tool" result and use it in the answer?
  5 irrelevance  with tools present, does an unrelated question call NOTHING?

Rung 5 is the one most local models fail, and it needs no ground truth.
"""
import json, sys, urllib.request

OLLAMA = "http://127.0.0.1:11434"

WEATHER = [{"type": "function", "function": {
    "name": "get_weather",
    "description": "Get the current temperature for a city.",
    "parameters": {"type": "object",
                   "properties": {"city": {"type": "string", "description": "City name"}},
                   "required": ["city"]}}}]


def chat(model, messages, tools=None, num_predict=300):
    p = {"model": model, "messages": messages, "stream": False,
         "options": {"num_ctx": 8192, "num_predict": num_predict,
                     "temperature": 0.0, "top_k": 1, "seed": 42},
         "keep_alive": "5m", "think": False}
    if tools:
        p["tools"] = tools
    r = urllib.request.Request(OLLAMA + "/api/chat", data=json.dumps(p).encode(),
                               headers={"Content-Type": "application/json"})
    return json.loads(urllib.request.urlopen(r, timeout=900).read())


def caps(model):
    r = urllib.request.Request(OLLAMA + "/api/show",
                               data=json.dumps({"model": model}).encode(),
                               headers={"Content-Type": "application/json"})
    return json.loads(urllib.request.urlopen(r, timeout=60).read())


def diagnose(model, verbose=True):
    out = {"model": model, "rungs": {}}

    def say(*a):
        if verbose:
            print(*a, flush=True)

    # 1 -- capability flag
    info = caps(model)
    c = info.get("capabilities", []) or []
    tmpl = (info.get("template") or "")
    ok1 = "tools" in c
    out["rungs"]["1_capability"] = {"pass": ok1, "capabilities": c,
                                    "template_mentions_tools": "tool" in tmpl.lower()}
    say(f"  [{'PASS' if ok1 else 'FAIL'}] 1 capability      {c}"
        f"  template_mentions_tools={'tool' in tmpl.lower()}")
    if not ok1:
        out["verdict"] = "no tool support advertised -- not a fair tools test"
        return out

    # 2/3 -- emits a call with valid args
    r = chat(model, [{"role": "user", "content": "What is the temperature in Oslo right now?"}], WEATHER)
    msg = r.get("message", {}) or {}
    tcs = msg.get("tool_calls") or []
    ok2 = bool(tcs)
    args, ok3 = None, False
    if tcs:
        fn = tcs[0].get("function", {}) or {}
        args = fn.get("arguments")
        if isinstance(args, str):
            try:
                args = json.loads(args)
            except Exception:
                args = None
        ok3 = isinstance(args, dict) and "city" in args
    out["rungs"]["2_emits_tool_call"] = {"pass": ok2, "n_calls": len(tcs),
                                         "content_if_none": (msg.get("content") or "")[:160]}
    out["rungs"]["3_valid_args"] = {"pass": ok3, "args": args}
    say(f"  [{'PASS' if ok2 else 'FAIL'}] 2 emits call      n={len(tcs)}"
        + ("" if ok2 else f"  -> said instead: {(msg.get('content') or '')[:90]!r}"))
    say(f"  [{'PASS' if ok3 else 'FAIL'}] 3 valid args      {args}")
    if not ok2:
        out["verdict"] = ("template/parser problem -- the model answered in prose instead of "
                          "emitting a tool_calls array. Check the chat template for this tag.")
        return out

    # 4 -- round-trip a tool result
    msgs = [{"role": "user", "content": "What is the temperature in Oslo right now?"},
            {"role": "assistant", "content": msg.get("content", ""), "tool_calls": tcs},
            {"role": "tool", "tool_name": "get_weather", "content": "-3 degrees Celsius"}]
    r2 = chat(model, msgs, WEATHER)
    final = ((r2.get("message", {}) or {}).get("content") or "")
    ok4 = ("-3" in final or "3 degree" in final.lower() or "minus" in final.lower())
    out["rungs"]["4_roundtrip"] = {"pass": ok4, "answer": final[:200]}
    say(f"  [{'PASS' if ok4 else 'FAIL'}] 4 round-trip      {final[:90]!r}")

    # 5 -- irrelevance: tools present, unrelated question -> call NOTHING
    r3 = chat(model, [{"role": "user", "content": "What is the capital of France?"}], WEATHER)
    m3 = r3.get("message", {}) or {}
    spurious = m3.get("tool_calls") or []
    ok5 = not spurious
    out["rungs"]["5_irrelevance"] = {"pass": ok5, "spurious_calls": len(spurious),
                                     "answer": (m3.get("content") or "")[:160]}
    say(f"  [{'PASS' if ok5 else 'FAIL'}] 5 irrelevance     "
        + ("called nothing (correct)" if ok5
           else f"WRONGLY called {[t.get('function',{}).get('name') for t in spurious]}"))

    rungs = out["rungs"]
    passed = sum(1 for v in rungs.values() if v["pass"])
    out["passed"] = passed
    out["of"] = len(rungs)
    if passed == len(rungs):
        out["verdict"] = "tool plumbing is healthy -- failures above this are the MODEL"
    elif not rungs["4_roundtrip"]["pass"]:
        out["verdict"] = "emits calls but cannot consume results -- usually a template issue"
    elif not rungs["5_irrelevance"]["pass"]:
        out["verdict"] = "over-eager: fires tools on unrelated questions (model behaviour, not plumbing)"
    else:
        out["verdict"] = "partial"
    say(f"  => {out['verdict']}")
    return out


if __name__ == "__main__":
    for m in sys.argv[1:]:
        print(f"\n=== tool plumbing: {m} ===")
        try:
            diagnose(m)
        except Exception as e:
            print("  ERROR", type(e).__name__, str(e)[:160])
