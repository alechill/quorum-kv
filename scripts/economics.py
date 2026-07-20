#!/usr/bin/env python3
"""Aggregate token usage from a Claude Code session transcript (JSONL).

Sums per-message `usage` fields (input_tokens, output_tokens,
cache_creation_input_tokens, cache_read_input_tokens), segments the session
into chronological phases from a supplied phase map, and tallies tool calls
with the token weight of their results.

Usage:
  economics.py <transcript.jsonl> [--phases phases.json] [--json out.json]

phases.json: [{"name": "...", "until": "<ISO timestamp>"}, ...] — each phase
covers messages up to `until`; the last phase takes the remainder.
"""
import json
import sys
from collections import defaultdict
from datetime import datetime

USAGE_KEYS = [
    "input_tokens",
    "output_tokens",
    "cache_creation_input_tokens",
    "cache_read_input_tokens",
]


def parse_ts(s):
    return datetime.fromisoformat(s.replace("Z", "+00:00"))


def approx_tokens(obj):
    """Rough token weight of a tool result payload: chars/4."""
    try:
        text = json.dumps(obj) if not isinstance(obj, str) else obj
    except Exception:
        text = str(obj)
    return len(text) // 4


def main():
    args = sys.argv[1:]
    phases_file = None
    json_out = None
    if "--phases" in args:
        i = args.index("--phases")
        phases_file = args[i + 1]
        del args[i : i + 2]
    if "--json" in args:
        i = args.index("--json")
        json_out = args[i + 1]
        del args[i : i + 2]
    path = args[0]

    phases = []
    if phases_file:
        phases = json.load(open(phases_file))

    totals = dict.fromkeys(USAGE_KEYS, 0)
    phase_stats = defaultdict(lambda: {
        **dict.fromkeys(USAGE_KEYS, 0), "assistant_turns": 0,
        "first_ts": None, "last_ts": None,
    })
    tool_stats = defaultdict(lambda: {"calls": 0, "result_tokens_approx": 0})
    pending_tools = {}  # tool_use_id -> tool name

    first_ts = last_ts = None
    assistant_msgs = 0
    lines = 0
    model_ids = set()

    for line in open(path):
        line = line.strip()
        if not line:
            continue
        lines += 1
        try:
            rec = json.loads(line)
        except json.JSONDecodeError:
            continue
        ts = rec.get("timestamp")
        if ts:
            t = parse_ts(ts)
            first_ts = first_ts or t
            last_ts = t

        msg = rec.get("message") or {}
        usage = msg.get("usage") or {}
        if msg.get("model"):
            model_ids.add(msg["model"])

        phase_name = None
        if phases and ts:
            for ph in phases:
                if "until" not in ph or t <= parse_ts(ph["until"]):
                    phase_name = ph["name"]
                    break
            if phase_name is None:
                phase_name = phases[-1]["name"]

        if usage:
            assistant_msgs += 1
            for k in USAGE_KEYS:
                v = usage.get(k) or 0
                totals[k] += v
                if phase_name:
                    phase_stats[phase_name][k] += v
            if phase_name:
                ps = phase_stats[phase_name]
                ps["assistant_turns"] += 1
                ps["first_ts"] = ps["first_ts"] or ts
                ps["last_ts"] = ts

        content = msg.get("content")
        if isinstance(content, list):
            for block in content:
                if not isinstance(block, dict):
                    continue
                if block.get("type") == "tool_use":
                    name = block.get("name", "?")
                    tool_stats[name]["calls"] += 1
                    pending_tools[block.get("id")] = name
                elif block.get("type") == "tool_result":
                    name = pending_tools.get(block.get("tool_use_id"), "?")
                    tool_stats[name]["result_tokens_approx"] += approx_tokens(
                        block.get("content", ""))

    out = {
        "transcript": path,
        "lines": lines,
        "assistant_messages_with_usage": assistant_msgs,
        "model_ids": sorted(model_ids),
        "session_start": first_ts.isoformat() if first_ts else None,
        "session_end": last_ts.isoformat() if last_ts else None,
        "totals": totals,
        "phases": {k: dict(v) for k, v in phase_stats.items()},
        "tools": {k: dict(v) for k, v in sorted(
            tool_stats.items(), key=lambda kv: -kv[1]["result_tokens_approx"])},
    }
    print(json.dumps(out, indent=2))
    if json_out:
        json.dump(out, open(json_out, "w"), indent=2)


if __name__ == "__main__":
    main()
