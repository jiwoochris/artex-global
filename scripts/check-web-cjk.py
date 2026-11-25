#!/usr/bin/env python3
"""Check whether Chinese (Han characters) has leaked into the HTML of the statically exported web UI (`web/out`).

This fork's core achievement and differentiator is that "there is no Chinese on any user-facing screen"
(the zero-Han-characters-in-`out`-HTML property established by backlog B4). If that property were left
to manual checking alone, a regression where a contributor hard-codes a Chinese string or a translation
goes missing would stay silently broken until the next person happened to spot it by eye. This script
blocks that regression at the merge gate.

How it checks
-------------
It scans the **raw content** of every `.html` file under `web/out` (not just the rendered markup but also
inline scripts and React Server Components flight data), and exits with code 1 if there is even a single
Han character. The raw content is examined as is because user-visible text is carried in the flight data
(JSON-like strings) and shipped down inside the HTML. Merely stripping the tags would miss leaks through
that path.

Out of scope (honest limitations)
-----------------------
This gate only looks at the static export output. So it cannot check **content that the browser draws at
runtime in MOCK mode (`NEXT_PUBLIC_MOCK=1`, the mode the public demo uses)**.
`web/src/lib/mock/data.ts`, `handler.ts` and each page's hard-coded samples are fetched after hydration by
`useEffect` via `mockHandle()` and rendered from React state, so they ship only in the runtime JS bundle,
not in the `out` HTML. In other words, Chinese that comes in through this path passes this gate
(that is how Chinese once sat quietly in the `/system/logs` sample log of the public demo, which was
fixed to Korean in backlog G114).

That surface is guarded by source review. Any Chinese remaining in rendered files (`.tsx` and client data
in `web/src`) must be one of the following: a code comment, an i18n fallback that is guarded by
`t.has(...)` and never actually used, a backend wire-format token (chat mention classification labels, the
`[模型]` decision-source sentinel), a branch literal shown only when the locale is `zh`, or a mirror that
reflects verbatim an agent-brain (段 [A], "section [A]") prompt that is not translated in order to
preserve performance. If it is none of these and is "just Chinese visible on screen", translate it into
Korean (backlog G115 verified this classification exhaustively).

Allowed and detected ranges
-------------
- **Hangul (`가-힣`, etc.) is allowed**. The Han blocks and the Hangul blocks do not overlap in Unicode,
  so detecting only the Han blocks lets Hangul pass automatically.
- **Only Han characters (CJK ideographs) are detected**: Unified Ideographs and Extension A (`㐀`–`鿿`),
  Compatibility Ideographs (`豈`–`﫿`), and Extension B and above in the supplementary planes (`𠀀`–).
  Kana and full-width punctuation are not "Han characters", so they are not targets of this gate (the
  range is narrowed to Han characters to avoid false positives from them slipping in through code
  blocks preserved verbatim or quoted evidence).

Intentional Han characters that must be preserved from upstream
-----------------------------
The `out` HTML currently contains not a single Han character, so the allowlist is empty. If in the future
the original (Chinese) text must deliberately stay on screen to preserve performance (e.g. the `中文`
label in the language switcher), add those characters to `ALLOWED_HAN` below and write a comment explaining
why they stay. That way the gate stays strict while letting only the intended exceptions through narrowly.

Uses only the standard library and makes no network connections.
Run: `python3 -I scripts/check-web-cjk.py [out directory]`
(The default is `web/out` under the repository root. CI calls it from the `web` working directory as
`python3 -I ../scripts/check-web-cjk.py`.)
"""
import os
import re
import subprocess
import sys

# Han blocks only. Hangul (AC00–D7A3), kana and full-width punctuation are not included.
# Ranges: Extension A + Unified Ideographs (U+3400–U+9FFF = 㐀–鿿), Compatibility Ideographs (U+F900–U+FAFF = 豈–﫿),
#         Extension B and above in the supplementary planes (U+20000–U+2FFFF).
HAN = re.compile(r"[㐀-鿿豈-﫿\U00020000-\U0002ffff]")

# Han characters deliberately kept on screen (e.g. the language switcher's `中文`). Currently empty —
# while it is empty, any Han character trips the gate. When adding an exception, record the characters together with the reason.
# e.g.) ALLOWED_HAN = {"中", "文"}  # language switcher '中文' label (for comparison with upstream)
ALLOWED_HAN: set = set()


def find_out_dir() -> str:
    """Decide which `out` directory to check.

    If a path is given as an argument, use it; otherwise use `web/out` under the git repository root.
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
            f"오류: 검사할 디렉터리가 없습니다 — {out_dir}\n"
            "먼저 `npm run build:static` 로 정적 내보내기를 만든 뒤 실행하세요."
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

    print(f"검사한 HTML {checked}개 (대상: {out_dir})")
    if offenders:
        print(f"중국어(한자) 누출 {len(offenders)}개 파일 — 사용자 노출 HTML 에 한자가 있습니다:")
        for rel, count, kinds in offenders:
            preview = kinds if len(kinds) <= 30 else kinds[:30] + "…"
            print(f"  out/{rel}: 한자 {count}개 · 종류 {preview}")
        print(
            "\n한글로 번역하거나, 상류 보존이 꼭 필요한 의도적 한자라면 "
            "scripts/check-web-cjk.py 의 ALLOWED_HAN 에 사유와 함께 추가하세요."
        )
        return 1
    if checked == 0:
        print("경고: 검사한 HTML 이 없습니다 — 빌드가 제대로 되었는지 확인하세요.")
        return 2
    print("중국어(한자) 누출 0 — 사용자 노출 HTML 에 한자가 없습니다.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
