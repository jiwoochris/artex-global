#!/usr/bin/env python3
"""Language gate: keep non-English text from coming back.

The language contract (SPEC.md §3): English is the source language for every
document, comment, skill and code string, and the only supported UI/output
languages are `en` and `es`. A small set of byte-exact sentinels and non-ASCII
test fixtures must stay non-ASCII because they match rows already stored in
PostgreSQL, tokens produced by other components, or text returned by third-party
APIs; SPEC.md §3.5.1 lists them.

The gate works against a checked-in baseline (`scripts/non-english-baseline.json`):
a file that is absent from the baseline, or whose non-English character count grows,
fails the check. Deleting non-English text or shrinking a count passes and the
baseline can be refreshed with `--update`.

Usage:
  python3 scripts/check-non-english.py            # verify
  python3 scripts/check-non-english.py --update   # rewrite the baseline
  python3 scripts/check-non-english.py -v         # print offending lines
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys

HANGUL = "[\uac00-\ud7af]"
HAN = "[\u4e00-\u9fff]"
KANA = "[\u3040-\u30ff]"

SKIP_DIRS = {".git", "node_modules", ".next", "out", "dist", "data"}
TEXT_EXTS = {".go", ".ts", ".tsx", ".js", ".mjs", ".py", ".sh", ".sql", ".yml", ".yaml", ".md", ".json"}
BASELINE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "non-english-baseline.json")


def count(text: str) -> int:
    return len(re.findall(HANGUL, text)) + len(re.findall(HAN, text)) + len(re.findall(KANA, text))


def walk(roots: list[str]):
    for root in roots:
        if os.path.isfile(root):
            yield root
            continue
        for base, dirs, files in os.walk(root):
            dirs[:] = [d for d in dirs if d not in SKIP_DIRS]
            for name in files:
                if os.path.splitext(name)[1] in TEXT_EXTS:
                    yield os.path.join(base, name)


def scan(roots: list[str]) -> dict[str, int]:
    found: dict[str, int] = {}
    for path in walk(roots):
        rel = os.path.relpath(path).replace(os.sep, "/")
        if rel.startswith(".git/"):
            continue
        try:
            with open(path, encoding="utf-8", errors="ignore") as handle:
                total = count(handle.read())
        except OSError as exc:
            print(f"cannot read {rel}: {exc}", file=sys.stderr)
            continue
        if total:
            found[rel] = total
    return found


def offending_lines(path: str) -> list[str]:
    try:
        with open(path, encoding="utf-8", errors="ignore") as handle:
            content = handle.read()
    except OSError:
        return []
    lines = []
    for number, line in enumerate(content.split("\n"), 1):
        if count(line):
            lines.append(f"    {number}: {line.strip()[:120]}")
    return lines[:20]


def main() -> int:
    parser = argparse.ArgumentParser(description="Keep non-English text from coming back to the tree.")
    parser.add_argument("paths", nargs="*", default=["."])
    parser.add_argument("--update", action="store_true", help="rewrite the baseline from the current tree")
    parser.add_argument("-v", "--verbose", action="store_true", help="print offending lines for new files")
    args = parser.parse_args()

    found = scan(args.paths or ["."])

    if args.update:
        try:
            with open(BASELINE, "w", encoding="utf-8") as handle:
                json.dump(found, handle, indent=1, sort_keys=True, ensure_ascii=False)
                handle.write("\n")
        except OSError as exc:
            print(f"cannot write baseline {BASELINE}: {exc}", file=sys.stderr)
            return 1
        print(f"baseline updated: {len(found)} files, {sum(found.values())} characters -> {BASELINE}")
        return 0

    try:
        with open(BASELINE, encoding="utf-8") as handle:
            baseline: dict[str, int] = json.load(handle)
    except OSError:
        print(f"missing baseline {BASELINE}; run with --update", file=sys.stderr)
        return 1

    failures = 0
    for path, total in sorted(found.items()):
        allowed = baseline.get(path, 0)
        if total > allowed:
            failures += 1
            print(f"{total:6} (baseline {allowed:6}) {path}")
            if args.verbose:
                for line in offending_lines(path):
                    print(line)
    removed = [path for path in baseline if path not in found]
    if removed:
        print(f"{len(removed)} file(s) are clean now and can leave the baseline: {', '.join(sorted(removed)[:5])}"
              + (" ..." if len(removed) > 5 else ""))

    print(f"files over baseline: {failures}; total non-English characters: {sum(found.values())}")
    return 1 if failures else 0


if __name__ == "__main__":
    raise SystemExit(main())
