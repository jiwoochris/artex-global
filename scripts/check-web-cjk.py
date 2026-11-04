#!/usr/bin/env python3
"""Check whether Chinese (Han) characters leak into the statically exported web UI (`web/out`).

The point of this fork is that nothing but English (the source language) and Spanish
appears on screen for the user (the "zero Han characters in `out` HTML" property
established by backlog item B4). Left to manual review alone, a contributor hardcoding a
Chinese string or a missing translation silently breaks that property and stays broken
until the next person happens to notice. This script blocks that regression at the merge
gate.

How it checks
-------------
It scans the **raw contents** of every `.html` file under `web/out` (not just the rendered
markup, but inline scripts and React Server Components flight data too) and exits with
code 1 if even one Han character is present. The raw contents are what matter because
user-visible text is carried into the HTML inside the flight data (JSON-shaped strings);
stripping the tags would miss leaks on that path.

Out of scope (an honest limit)
------------------------------
This gate only sees the static export output. Content the browser renders at runtime in
**MOCK mode (`NEXT_PUBLIC_MOCK=1`, the mode the public demo uses)** is therefore not
covered. `web/src/lib/mock/data.ts`, `handler.ts` and the hardcoded samples in each page
are pulled in by `useEffect` through `mockHandle()` after hydration and rendered as React
state, so they never reach `out` HTML - only the runtime JS bundle. Chinese entering that
way passes this gate (the sample logs on `/system/logs` in the public demo used to leak
Chinese exactly like that; backlog G114 fixed it by translating to Korean).

That surface is protected by source review. Chinese left in render-targeted files (`.tsx`
and client data under `web/src`) must be one of: a code comment, an i18n fallback blocked
by `t.has(...)` and therefore never displayed, a backend wire-format token (chat mention
classification labels, the `[模型]` decision-source sentinel), a branch literal that only
appears when the locale is `zh`, or a mirror of the agent brain prompt (section [A]) that is
not translated in order to preserve performance. Anything that is none of these and is simply
"Chinese visible on screen" gets translated (backlog G115 audited that classification end
to end).

What is allowed and what is detected
------------------------------------
- **Hangul (`가-힣` and friends) is allowed.** Han blocks and Hangul blocks do not overlap
  in Unicode, so detecting Han blocks alone lets Hangul through automatically.
- **Only Han (CJK ideographs) are detected**: unified ideographs plus extension A
  (`㐀`-`鿿`), compatibility ideographs (`豈`-`﫿`), and extension B and above on the
  supplementary planes (`𠀀`-). Kana and fullwidth punctuation are not "Han" and are out of
  scope for this gate (the range is narrowed to Han so that original-text code blocks or
  quoted evidence do not produce false positives).

Deliberate Han that must be preserved upstream
----------------------------------------------
`out` HTML currently contains no Han at all, so the allow list is empty. If preserving
performance later requires leaving the original (Chinese) text on screen on purpose (for
example the `中文` label in the language switcher), add those characters to `ALLOWED_HAN`
below and comment why they stay. That keeps the gate strict while letting only the intended
exceptions through.

Uses the standard library only and makes no network access.
Run: `python3 -I scripts/check-web-cjk.py [out directory]`
(defaults to `web/out` in the repository root; CI invokes it from the `web` working
directory as `python3 -I ../scripts/check-web-cjk.py`).
"""
import os
import re
import subprocess
import sys

# Han blocks only. Hangul (AC00-D7A3), kana and fullwidth punctuation are not included.
# Ranges: extension A + unified ideographs (U+3400-U+9FFF = 㐀-鿿), compatibility
# ideographs (U+F900-U+FAFF = 豈-﫿), and extension B and above on the supplementary
# planes (U+20000-U+2FFFF).
HAN = re.compile(r"[㐀-鿿豈-﫿\U00020000-\U0002ffff]")

# Han characters deliberately left on screen (for example the `中文` label in the language
# switcher). Empty today - an empty set means any Han character trips the gate. When adding
# an exception, record the character together with the reason.
# e.g. ALLOWED_HAN = {"中", "文"}  # language switcher '中文' label (upstream comparison)
ALLOWED_HAN: set = set()


def find_out_dir() -> str:
    """Resolve the `out` directory to check.

    Uses the path given as an argument, else `web/out` in the git repository root.
    """
    if len(sys.argv) > 1:
        return os.path.abspath(sys.argv[1])
    root = subprocess.check_output(
        ["git", "rev-parse", "--show-toplevel"], text=True
    ).strip()
    return os.path.join(root, "web", "out")


def main() -> int:
    out_dir = find_out_dir()
    if not os.path.isdir(out_dir):
        print(
            f"error: the directory to check does not exist - {out_dir}\n"
            "run `npm run build:static` to produce the static export first."
        )
        return 2

    checked = 0
    offenders = []  # (relative path, Han character count, preview)
    for dirpath, _dirnames, filenames in os.walk(out_dir):
        for name in filenames:
            if not name.endswith(".html"):
                continue
            checked += 1
            path = os.path.join(dirpath, name)
            with open(path, encoding="utf-8", errors="ignore") as fh:
                text = fh.read()
            found = [ch for ch in HAN.findall(text) if ch not in ALLOWED_HAN]
            if found:
                rel = os.path.relpath(path, out_dir)
                kinds = "".join(sorted(set(found)))
                offenders.append((rel, len(found), kinds))

    print(f"checked {checked} HTML files (target: {out_dir})")
    if offenders:
        print(f"Chinese (Han) leak in {len(offenders)} file(s) - user-facing HTML contains Han characters:")
        for rel, count, kinds in offenders:
            preview = kinds if len(kinds) <= 30 else kinds[:30] + "…"
            print(f"  out/{rel}: {count} Han character(s) · kinds {preview}")
        print(
            "\nTranslate them to English, or, if the Han is intentional and must be "
            "preserved upstream, add it to ALLOWED_HAN in scripts/check-web-cjk.py together "
            "with the reason."
        )
        return 1
    if checked == 0:
        print("warning: no HTML was checked - verify that the build succeeded.")
        return 2
    print("Chinese (Han) leaks: 0 - user-facing HTML contains no Han characters.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
