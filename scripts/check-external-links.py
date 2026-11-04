#!/usr/bin/env python3
"""Check whether the external links (http/https) referenced by tracked Markdown are still alive.

The sibling script `check-doc-links.py` only looks at internal links and anchors inside the
repository and deliberately skips external URLs (they depend on the network and are flaky, so
the merge gate does not run them). This script owns that "separate check". The external links
that the top of the README, the defence guide and the detections README point visitors and
defenders at (incident reporting portals boho.or.kr, privacy.go.kr, pipc.go.kr, fsec.or.kr,
CISA KEV, OWASP, SigmaHQ, Suricata, MITRE, MISP, the original demo
artex-demo.vercel.app, GitHub badges, ...) would otherwise rot, move or shut down silently, so
this catches that periodically and by hand.

Rules for choosing the URLs to check
- Walk every tracked `.md`, but skip URLs inside fenced code blocks (``` or ~~~) and inline code
  spans (`` `...` ``). A URL there is a command example, a config value or quoted third-party
  material (e.g. `id.redhaze.top` in the side-question verification doc), not a reference link a
  reader follows.
- Collect http/https addresses from Markdown links and images (`[text](url)`, `![alt](url)`),
  autolinks (`<url>`), and bare URLs left in prose.
- Skip reserved and placeholder hosts: localhost, private/loopback IPs (127., 10., 192.168.,
  169.254., 172.16-31., 0.0.0.0, ::1), RFC 2606/6761 reservations (example.com/org/net/edu,
  `*.example.*`, `.test`, `.invalid`, `.local`, `.tld`), and dotless names that are not FQDNs
  (e.g. `target`).

How liveness is checked (the lesson of MAINTAINING.md 8.2 turned into code)
Public and security-agency sites often reject HEAD requests and the default User-Agent or
redirect several times, so a naive check mistakes healthy links for broken ones. This script
therefore checks **with a browser User-Agent, using GET, following redirects**. It sorts the
state into three classes.
- OK: the final status is 2xx/3xx. The link is valid.
- RESTRICTED: 401, 403, 405, 429. The host is alive but the check is blocked by the server's access
  policy (bot blocking, method rejection, rate limiting); the link is not broken. Reported, but not
  counted as a failure.
- DOWN: 404, 410, 5xx (even after retries), DNS/connect/timeout/SSL errors (even after retries).
  Most likely genuinely broken.

Network errors, 5xx and 429 are retried with a small delay to separate transient blips from real
outages. Only the standard library is used. This script sends GETs only to reference URLs that the
repository's **public documents already point at**, to verify liveness; it scans or probes no
target.

Known exceptions (allowlist)
A URL listed in `scripts/external-links-allowlist.txt` that comes back DOWN is classified as
"ALLOWED" separately and does not affect the strict exit code. Recording inherited dead links
from upstream original-preservation files we do not own and cannot fix (e.g. an upstream
CHANGELOG.zh.md) transparently keeps the periodic strict check from being permanently red because
of that one link, so it only goes red **when a newly broken link appears**.

Usage
- Default (report, exit code 0): `python3 -I scripts/check-external-links.py`
- Periodic/release check outside the merge gate (red when a newly broken link exists): `--strict`
  (exit code 1 if any DOWN is not in the allowlist). RESTRICTED and ALLOWED are never failures,
  even under strict.
- Preview the extracted set without network access: `--list` (prints the URLs to check and their
  origins, making no calls).
"""
import argparse
import http.client
import os
import re
import socket
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.request
from collections import defaultdict
from urllib.parse import urlsplit

# [text](url) inline link (not an image) and ![alt](url) image. The path runs until whitespace or a closing paren.
MD_LINK = re.compile(r"(?<!\!)\[[^\]]*\]\(([^)\s]+)")
MD_IMAGE = re.compile(r"!\[[^\]]*\]\(([^)\s]+)")
# <https://...> autolink.
AUTOLINK = re.compile(r"<(https?://[^>\s]+)>")
# Bare URL in prose. Trailing punctuation is stripped afterwards.
BARE_URL = re.compile(r"https?://[^\s)>\]\"'`]+")
# Inline code span `...` (single backtick). Blanked out before extraction.
INLINE_CODE = re.compile(r"`[^`]*`")
# Punctuation commonly glued to the end of a prose URL.
TRAILING_PUNCT = ".,;:!?\"'»)]}>"

# IPv4 private/loopback/link-local ranges (172.16-31. has its own pattern).
PRIVATE_IPV4 = re.compile(r"^(127\.|10\.|192\.168\.|169\.254\.|0\.0\.0\.0$)")
PRIVATE_IPV4_172 = re.compile(r"^172\.(1[6-9]|2\d|3[01])\.")

BROWSER_UA = (
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) "
    "AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36"
)

# Status codes where the host is alive but the check is blocked by server policy (not broken).
RESTRICTED_CODES = {401, 403, 405, 429}

ALLOWLIST_PATH = os.path.join("scripts", "external-links-allowlist.txt")


def load_allowlist(root: str) -> set:
    """Read the set of known DOWN URLs we cannot fix (empty when the file is absent).

    One URL per line. Anything after `#` is a comment; blank and comment-only lines are ignored.
    """
    path = os.path.join(root, ALLOWLIST_PATH)
    allow = set()
    if not os.path.exists(path):
        return allow
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            line = line.split("#", 1)[0].strip()
            if line:
                allow.add(line)
    return allow


def strip_code(line: str) -> str:
    """Blank out inline code spans on one line outside a fence (URLs inside them are not references)."""
    return INLINE_CODE.sub(" ", line)


def is_checkable(url: str) -> bool:
    """Filter out reserved and placeholder hosts, keeping only external URLs worth checking."""
    parts = urlsplit(url)
    if parts.scheme not in ("http", "https"):
        return False
    host = parts.hostname
    if not host:
        return False
    host = host.lower()
    if host == "localhost" or host.endswith(".localhost"):
        return False
    if host == "::1":
        return False
    if PRIVATE_IPV4.match(host) or PRIVATE_IPV4_172.match(host):
        return False
    if host in ("example.com", "example.org", "example.net", "example.edu"):
        return False
    if host.endswith((".example.com", ".example.org", ".example.net", ".example")):
        return False
    if host.endswith((".test", ".invalid", ".local", ".localdomain", ".tld")):
        return False
    if "." not in host:  # dotless name that is not an FQDN (e.g. target)
        return False
    return True


def extract(root: str, md_files: list) -> dict:
    """Build the map of external URL to check → [(file, line number), ...] from tracked .md files."""
    found = defaultdict(list)
    for md in md_files:
        with open(os.path.join(root, md), encoding="utf-8", errors="ignore") as fh:
            lines = fh.readlines()
        in_fence = False
        for lineno, raw in enumerate(lines, 1):
            stripped = raw.lstrip()
            if stripped.startswith("```") or stripped.startswith("~~~"):
                in_fence = not in_fence
                continue
            if in_fence:
                continue
            line = strip_code(raw)
            candidates = []
            for pattern in (MD_LINK, MD_IMAGE, AUTOLINK):
                candidates.extend(m.group(1) for m in pattern.finditer(line))
            for m in BARE_URL.finditer(line):
                candidates.append(m.group(0).rstrip(TRAILING_PUNCT))
            for url in candidates:
                url = url.strip().rstrip(TRAILING_PUNCT)
                if is_checkable(url):
                    where = (md, lineno)
                    if where not in found[url]:
                        found[url].append(where)
    return found


def probe(url: str, timeout: float, retries: int, delay: float):
    """GET the URL with a browser User-Agent and return (verdict, detail).

    The verdict is "OK", "RESTRICTED" or "DOWN". Network errors, 5xx and 429 are retried.
    """
    req = urllib.request.Request(
        url,
        method="GET",
        headers={
            "User-Agent": BROWSER_UA,
            "Accept": "*/*",
            "Accept-Language": "en,es;q=0.8",
        },
    )
    last = ""
    for attempt in range(retries + 1):
        try:
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                code = resp.getcode()
                if code in RESTRICTED_CODES:
                    return "RESTRICTED", f"HTTP {code}"
                return "OK", f"HTTP {code}"
        except urllib.error.HTTPError as exc:
            code = exc.code
            if code in RESTRICTED_CODES:
                return "RESTRICTED", f"HTTP {code}"
            if code >= 500 or code == 408:
                last = f"HTTP {code}"  # may be transient, so retry
            else:
                return "DOWN", f"HTTP {code}"  # 404/410 and friends are final, no retry needed
        except (
            urllib.error.URLError,
            http.client.HTTPException,
            ssl.SSLError,
            socket.timeout,
            ConnectionError,
            TimeoutError,
            OSError,
        ) as exc:
            reason = getattr(exc, "reason", exc)
            last = f"{type(exc).__name__}: {reason}"
        if attempt < retries:
            time.sleep(delay * (attempt + 1))
    return "DOWN", last


def main() -> int:
    ap = argparse.ArgumentParser(description="Liveness check for external links in tracked Markdown")
    ap.add_argument("--strict", action="store_true",
                    help="exit code 1 if any link is DOWN (for periodic/release checks)")
    ap.add_argument("--list", action="store_true",
                    help="print the URLs to check and their origins without making network calls")
    ap.add_argument("--timeout", type=float, default=15.0, help="request timeout (seconds)")
    ap.add_argument("--retries", type=int, default=2, help="retry count for network errors, 5xx and 429")
    ap.add_argument("--delay", type=float, default=0.5, help="base delay between calls and retries (seconds)")
    args = ap.parse_args()

    root = subprocess.check_output(
        ["git", "rev-parse", "--show-toplevel"], text=True
    ).strip()
    tracked = subprocess.check_output(["git", "ls-files"], cwd=root, text=True).splitlines()
    md_files = [f for f in tracked if f.endswith(".md")]

    allow = load_allowlist(root)
    found = extract(root, md_files)
    urls = sorted(found)
    print(f"collected {len(urls)} external URLs to check from {len(md_files)} tracked Markdown files")

    if args.list:
        for url in urls:
            where = ", ".join(f"{f}:{ln}" for f, ln in found[url])
            tag = " [allowlist]" if url in allow else ""
            print(f"  {url}{tag}  ({where})")
        return 0

    results = {"OK": [], "RESTRICTED": [], "ALLOWED": [], "DOWN": []}
    for i, url in enumerate(urls):
        if i:
            time.sleep(args.delay)  # small delay between calls (stays clear of sandbox throttling)
        verdict, detail = probe(url, args.timeout, args.retries, args.delay)
        if verdict == "DOWN" and url in allow:
            verdict = "ALLOWED"  # known upstream-inherited DOWN that we cannot fix
        results[verdict].append((url, detail))
        print(f"  [{verdict:10}] {url} — {detail}")

    print(
        f"\nSummary: OK {len(results['OK'])} · RESTRICTED {len(results['RESTRICTED'])} · "
        f"ALLOWED {len(results['ALLOWED'])} · DOWN {len(results['DOWN'])}"
    )
    if results["RESTRICTED"]:
        print("RESTRICTED (host alive, check blocked; not a broken link):")
        for url, detail in results["RESTRICTED"]:
            print(f"  {url} — {detail}")
    if results["ALLOWED"]:
        print("ALLOWED (known DOWN listed in the allowlist; excluded from strict because we cannot fix it):")
        for url, detail in results["ALLOWED"]:
            where = ", ".join(f"{f}:{ln}" for f, ln in found[url])
            print(f"  {url} — {detail}  (referenced by: {where})")
    if results["DOWN"]:
        print("DOWN (most likely broken; needs a look):")
        for url, detail in results["DOWN"]:
            where = ", ".join(f"{f}:{ln}" for f, ln in found[url])
            print(f"  {url} — {detail}  (referenced by: {where})")

    if args.strict and results["DOWN"]:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
