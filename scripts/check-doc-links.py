#!/usr/bin/env python3
"""Check internal link, image and anchor reference integrity in repo Markdown docs.

It walks every tracked `.md` document and verifies two things.

1. Relative links to other files in the repo (`[text](path)`) and image
   references (`![alt](path)`, `<img src="path">`) point at files that exist.
2. Document anchor links (`[text](#heading)`, `[text](other.md#heading)`) point
   at headings that actually exist in the target `.md` document. Anchor slugs are
   produced by reproducing GitHub's rule in the standard library (lowercase →
   keep only `\\p{Word}`, hyphens and spaces → spaces become hyphens, and when
   the same slug repeats append `-1`/`-2` in order of appearance).

A single broken reference exits with code 1, so the script drops straight into
the CI merge gate (`.github/workflows/docs.yml`) and a contributor's local check.

What is not checked:
- External URLs (`http://`, `https://`, `mailto:`, `tel:`, `data:`): they depend
  on the network and are flaky, so this deterministic gate ignores them. External
  link status is checked separately.
- The fragment after `#` in links to non-`.md` files (e.g. a source line number):
  it is not a heading anchor, so only file existence is checked.
- The `#` fragment of image references: heading anchors are meaningless there.
- Example links and heading-like comments inside fenced code blocks (``` or ~~~):
  they are code samples, not real references or headings, so they are skipped.

Standard library only, no network access. Run: `python3 -I scripts/check-doc-links.py`
"""
import os
import re
import subprocess
import sys
import unicodedata
from collections import defaultdict

# [text](path) links that are not images (`![...]`). Path runs until whitespace (excludes the "..." title).
MD_LINK = re.compile(r"(?<!\!)\[[^\]]*\]\(([^)\s]+)")
# ![alt](path) image links.
MD_IMAGE = re.compile(r"!\[[^\]]*\]\(([^)\s]+)")
# <img ... src="path" ...> HTML images.
HTML_IMAGE = re.compile(r"<img[^>]*\bsrc=[\"']([^\"']+)[\"']", re.IGNORECASE)

# Network / non-file schemes. An anchor (`#`) refers to the same document, so it is not listed here.
EXTERNAL_PREFIXES = ("http://", "https://", "mailto:", "tel:", "data:")

ATX_HEADING = re.compile(r"^(#{1,6})\s+(.*?)\s*#*\s*$")
# Strip inline Markdown from heading text so only plain text remains.
MD_INLINE_LINK = re.compile(r"\[([^\]]*)\]\([^)]*\)")


def is_external(target: str) -> bool:
    return target.startswith(EXTERNAL_PREFIXES)


def _is_word_char(ch: str) -> bool:
    """Report whether `\\p{Word}` keeps this character under GitHub's anchor slug rule.

    Ruby regex `\\p{Word}` = Letter(L*) + Mark(M*) + Decimal_Number(Nd)
    + Connector_Punctuation(Pc) + Join_Control(U+200C, U+200D). Hangul, Han,
    underscores and emoji variation selectors (Mn) are all preserved.
    """
    if ch == "_":
        return True
    if ch in ("‌", "‍"):
        return True
    category = unicodedata.category(ch)
    if category[0] in ("L", "M"):
        return True
    return category in ("Nd", "Pc")


def slugify(text: str) -> str:
    """Convert heading text to a GitHub anchor slug (without the duplicate suffix)."""
    text = text.strip().lower()
    out = []
    for ch in text:
        if ch == " ":
            out.append("-")
        elif ch == "-" or _is_word_char(ch):
            out.append(ch)
    return "".join(out)


def heading_slugs(root: str, md: str) -> set:
    """Build the set of GitHub anchor slugs from every ATX heading in a file.

    Repeated slugs get `-1`/`-2` suffixes in order of appearance, like GitHub.
    """
    slugs = set()
    counts = defaultdict(int)
    in_fence = False
    with open(os.path.join(root, md), encoding="utf-8", errors="ignore") as fh:
        for line in fh:
            stripped = line.lstrip()
            if stripped.startswith("```") or stripped.startswith("~~~"):
                in_fence = not in_fence
                continue
            if in_fence:
                continue
            match = ATX_HEADING.match(line.rstrip("\n"))
            if not match:
                continue
            raw = match.group(2)
            raw = MD_INLINE_LINK.sub(r"\1", raw)
            raw = raw.replace("`", "").replace("*", "").replace("_", "")
            base = slugify(raw)
            seen = counts[base]
            counts[base] += 1
            slugs.add(base if seen == 0 else f"{base}-{seen}")
    return slugs


def main() -> int:
    root = subprocess.check_output(
        ["git", "rev-parse", "--show-toplevel"], text=True
    ).strip()
    tracked = subprocess.check_output(
        ["git", "ls-files"], cwd=root, text=True
    ).splitlines()
    md_files = [f for f in tracked if f.endswith(".md")]

    slug_cache: dict = {}

    def slugs_of(md_path: str) -> set:
        if md_path not in slug_cache:
            slug_cache[md_path] = heading_slugs(root, md_path)
        return slug_cache[md_path]

    checked_files = 0
    checked_anchors = 0
    broken_files = []
    broken_anchors = []
    for md in md_files:
        with open(os.path.join(root, md), encoding="utf-8", errors="ignore") as fh:
            lines = fh.readlines()
        base_dir = os.path.dirname(md)
        in_fence = False
        for lineno, line in enumerate(lines, 1):
            stripped = line.lstrip()
            if stripped.startswith("```") or stripped.startswith("~~~"):
                in_fence = not in_fence
                continue
            if in_fence:
                continue
            for pattern in (MD_LINK, MD_IMAGE, HTML_IMAGE):
                for match in pattern.finditer(line):
                    target = match.group(1).strip()
                    if is_external(target):
                        continue

                    if target.startswith("#"):
                        file_part, anchor = "", target[1:]
                    elif "#" in target:
                        file_part, anchor = target.split("#", 1)
                    else:
                        file_part, anchor = target, ""
                    file_part = file_part.split("?", 1)[0]
                    anchor = anchor.strip()

                    # The .md document being pointed at (anchor check target). For a link to another file it is that file,
                    # for a bare `#anchor` with no file part it is the current document.
                    target_md = None
                    if file_part:
                        checked_files += 1
                        resolved = os.path.normpath(
                            os.path.join(root, base_dir, file_part)
                        )
                        if not os.path.exists(resolved):
                            broken_files.append((md, lineno, target))
                            continue
                        if file_part.endswith(".md"):
                            target_md = os.path.normpath(
                                os.path.join(base_dir, file_part)
                            )
                    else:
                        target_md = md

                    # Anchors are only meaningful for body links (MD_LINK). The
                    # `#` fragment of an image reference is skipped.
                    if anchor and target_md is not None and pattern is MD_LINK:
                        checked_anchors += 1
                        if anchor not in slugs_of(target_md):
                            broken_anchors.append((md, lineno, target, target_md))

    print(
        f"tracked markdown {len(md_files)} · file references {checked_files} · "
        f"anchor references {checked_anchors} checked"
    )
    if broken_files:
        print(f"broken file references {len(broken_files)} — target file missing from the repo:")
        for md, lineno, target in broken_files:
            print(f"  {md}:{lineno} -> {target}")
    if broken_anchors:
        print(f"broken anchor references {len(broken_anchors)} — target document has no such heading:")
        for md, lineno, target, target_md in broken_anchors:
            print(f"  {md}:{lineno} -> {target}  (target: {target_md})")
    if broken_files or broken_anchors:
        return 1
    print("0 broken references — every internal link, image and anchor points at a real target.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
