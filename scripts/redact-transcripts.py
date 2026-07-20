#!/usr/bin/env python3
"""Copy session transcripts into ./transcripts, redacting secrets.

Redacts:
- every VALUE found in .env.claude-box (exact-string match, plus a
  URL-encoded variant), regardless of which variable it came from;
- common token shapes (GitHub gho_/ghp_/github_pat_, Fly tokens, AWS keys,
  JWTs, hex/base64 blobs tied to key-like prefixes).

Usage: redact-transcripts.py <src.jsonl>... --outdir transcripts
"""
import re
import sys
import urllib.parse
from pathlib import Path

ENV_FILE = Path(__file__).resolve().parent.parent / ".env.claude-box"


def env_values():
    vals = []
    if not ENV_FILE.exists():
        return vals
    for line in ENV_FILE.read_text().splitlines():
        line = line.strip().lstrip("#").strip()
        if "=" not in line:
            continue
        _, _, v = line.partition("=")
        v = v.strip().strip('"').strip("'")
        # Ignore trivially short values that would shred the text.
        if len(v) >= 8:
            vals.append(v)
    return vals


TOKEN_PATTERNS = [
    r"gho_[A-Za-z0-9]{20,}",
    r"ghp_[A-Za-z0-9]{20,}",
    r"ghs_[A-Za-z0-9]{20,}",
    r"github_pat_[A-Za-z0-9_]{20,}",
    r"fo1_[A-Za-z0-9_\-\.]{20,}",
    r"fm2_[A-Za-z0-9_\-\.+/=]{40,}",
    r"FlyV1 [A-Za-z0-9_\-\.+/=,]{40,}",
    r"AKIA[0-9A-Z]{16}",
    r"eyJ[A-Za-z0-9_\-]{10,}\.eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}",
    r"sk-[A-Za-z0-9\-_]{20,}",
    r"nvapi-[A-Za-z0-9\-_]{20,}",
    r"AIza[A-Za-z0-9\-_]{30,}",
    r"nfp_[A-Za-z0-9]{20,}",
]


def main():
    args = sys.argv[1:]
    outdir = Path("transcripts")
    if "--outdir" in args:
        i = args.index("--outdir")
        outdir = Path(args[i + 1])
        del args[i : i + 2]
    outdir.mkdir(parents=True, exist_ok=True)

    secrets = env_values()
    replacements = []
    for v in secrets:
        replacements.append(v)
        q = urllib.parse.quote(v, safe="")
        if q != v:
            replacements.append(q)
    # Longest first so substrings don't leave residue.
    replacements.sort(key=len, reverse=True)
    patterns = [re.compile(p) for p in TOKEN_PATTERNS]

    total_hits = 0
    for src in args:
        src = Path(src)
        text = src.read_text(errors="replace")
        hits = 0
        for v in replacements:
            n = text.count(v)
            if n:
                hits += n
                text = text.replace(v, "[REDACTED]")
        for pat in patterns:
            text, n = pat.subn("[REDACTED]", text)
            hits += n
        (outdir / src.name).write_text(text)
        total_hits += hits
        print(f"{src.name}: {hits} redactions")
    print(f"total redactions: {total_hits}")


if __name__ == "__main__":
    main()
