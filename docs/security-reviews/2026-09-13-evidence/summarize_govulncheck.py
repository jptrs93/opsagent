#!/usr/bin/env python3
"""Summarise a govulncheck -json stream into {symbol_findings, module_findings}.

Usage: summarize_govulncheck.py <govulncheck.json> <summary.json>
A symbol finding has a trace whose frames name a function; a module finding is
any advisory matched at module level. One entry per advisory id.
"""
import json, sys

def stream(text):
    dec = json.JSONDecoder(); i = 0; n = len(text)
    while i < n:
        while i < n and text[i].isspace(): i += 1
        if i >= n: break
        obj, i = dec.raw_decode(text, i)
        yield obj

osv, sym, mod = {}, {}, {}
for msg in stream(open(sys.argv[1]).read()):
    if "osv" in msg:
        osv[msg["osv"]["id"]] = msg["osv"]
    f = msg.get("finding")
    if not f:
        continue
    trace = f.get("trace") or []
    first = trace[0] if trace else {}
    entry = {"id": f["osv"], "module": first.get("module"), "version": first.get("version"),
             "fixed_version": f.get("fixed_version"), "summary": osv.get(f["osv"], {}).get("summary", "")}
    mod.setdefault(f["osv"], entry)
    if any(fr.get("function") for fr in trace) and f["osv"] not in sym:
        sym[f["osv"]] = {"id": f["osv"], "summary": entry["summary"],
                         "example_trace": {"trace": trace[:1], "fixed_version": f.get("fixed_version")}}
out = {"symbol_findings": list(sym.values()), "module_findings": list(mod.values())}
json.dump(out, open(sys.argv[2], "w"), indent=1); open(sys.argv[2], "a").write("\n")
print(sys.argv[2], "symbol", sorted(sym), "module", len(mod))
