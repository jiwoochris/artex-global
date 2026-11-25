#!/usr/bin/env python3
"""Check whether the external links (http, https) that tracked Markdown documents point to are still alive.

The sibling script `check-doc-links.py` looks only at in-repo internal links and anchors and skips
external URLs by design (they depend on the network and are flaky, so the merge gate does not handle
them). This script takes on that "separate check". External links that the top of the README, the
defense guide and the detections README point visitors and defenders to as "go here" (the
incident-reporting channels boho.or.kr, privacy.go.kr, pipc.go.kr, fsec.or.kr, CISA KEV, OWASP,
SigmaHQ, Suricata, MITRE, MISP, the original demo artex-demo.vercel.app, GitHub badges, etc.) stay
silently broken when they degrade, move or shut down; this catches that, periodically and on demand.

Rules for choosing which URLs to check
- Walk every tracked `.md`, but skip URLs inside fenced code blocks (``` or ~~~) and inline code
  spans (`` `...` ``). URLs in there are command examples, config values or quoted external assets
  (e.g. `id.redhaze.top` in the side-question verification document), not "reference links for the reader to follow".
- Collect http/https addresses from Markdown links and images (`[text](url)`, `![alt](url)`),
  autolinks (`<url>`), and any remaining bare URLs in prose.
- Exclude reserved and placeholder hosts: localhost, private/loopback IPs (127., 10., 192.168.,
  169.254., 172.16-31., 0.0.0.0, ::1), RFC 2606/6761 reserved names (example.com/org/net/edu,
  `*.example.*`, `.test`, `.invalid`, `.local`, `.tld`), and names without a dot that are not FQDNs (e.g. `target`).

How liveness is checked (the lessons of MAINTAINING.md 8.2 turned into code)
Korean public-sector and security-agency sites reject HEAD requests and the default User-Agent, or
redirect several times, so a naive check mistakes healthy links for broken ones. That is why this
script checks **with a browser User-Agent, using GET, following redirects**. It sorts statuses into
three kinds.
- OK: the final status is 2xx or 3xx. The link is valid.
- RESTRICTED: 401, 403, 405, 429. The host is alive; the check was merely blocked by the server's
  access policy (bot blocking, method rejection, rate limiting), so it is not a broken link. Reported but not counted as a failure.
- DOWN: 404, 410, 5xx (even after retries), DNS/connection/timeout/SSL errors (even after retries).
  Very likely actually broken.

Network errors, 5xx and 429 are retried after a short delay to tell transient flakiness from real outages.
Uses only the standard library. This script only sends GET requests to reference URLs that the
repository's **public documents already point to** in order to confirm they are alive; it does not scan or probe any target.

Known exceptions (allowlist)
URLs listed in `scripts/external-links-allowlist.txt` that come back DOWN are classified separately as
"ALLOWED" and are not counted toward the strict exit code. This transparently records dead links
inherited by upstream files preserved verbatim (e.g. upstream's CHANGELOG.zh.md), which we do not own
and cannot fix, so the periodic strict check doesn't stay red forever because of one of them and turns
red **only when a newly broken link appears**.

Running
- Default (report, exit code 0): `python3 -I scripts/check-external-links.py`
- Periodic/release check rather than a merge gate (red if there is a newly broken link): `--strict` (exit
  code 1 if there is even one DOWN not in the allowlist). RESTRICTED and ALLOWED are not failures even under strict.
- Preview just the extracted set without the network: `--list` (prints the URLs to be checked and where they come from, without making requests).
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

# [text](url) body links (not images) and ![alt](url) images. The path runs up to whitespace or a closing parenthesis.
MD_LINK = re.compile(r"(?<!\!)\[[^\]]*\]\(([^)\s]+)")
MD_IMAGE = re.compile(r"!\[[^\]]*\]\(([^)\s]+)")
# <https://...> autolinks.
AUTOLINK = re.compile(r"<(https?://[^>\s]+)>")
# Bare URLs in prose. Trailing punctuation is stripped afterwards.
BARE_URL = re.compile(r"https?://[^\s)>\]\"'`]+")
# Inline code spans `...` (single backtick). Blanked out with spaces before extraction.
INLINE_CODE = re.compile(r"`[^`]*`")
# Punctuation commonly attached to the end of URLs in prose.
TRAILING_PUNCT = ".,;:!?\"'»)]}>"

# IPv4 private/loopback/link-local ranges (172.16-31. is a separate pattern).
PRIVATE_IPV4 = re.compile(r"^(127\.|10\.|192\.168\.|169\.254\.|0\.0\.0\.0$)")
PRIVATE_IPV4_172 = re.compile(r"^172\.(1[6-9]|2\d|3[01])\.")

BROWSER_UA = (
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) "
    "AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36"
)

# Status codes where the host is alive but the check was blocked by server policy (not broken).
RESTRICTED_CODES = {401, 403, 405, 429}

ALLOWLIST_PATH = os.path.join("scripts", "external-links-allowlist.txt")


def load_allowlist(root: str) -> set:
    """Read the set of known DOWN URLs that we cannot fix (an empty set if the file is missing).

    One URL per line. Everything after `#` is a comment; blank and comment-only lines are ignored.
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
    """Blank out inline code spans in one line outside a fence (URLs in them are not references)."""
    return INLINE_CODE.sub(" ", line)


def is_checkable(url: str) -> bool:
    """Filter out reserved/placeholder hosts, keeping only external URLs worth checking."""
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
    if "." not in host:  # a bare name that is not an FQDN (e.g. target)
        return False
    return True


def extract(root: str, md_files: list) -> dict:
    """Build a dict of external URLs to check from tracked .md files → [(file, line number), ...]."""
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
    """Send a GET with a browser UA to the URL and return (classification, detail).

    The classification is "OK", "RESTRICTED" or "DOWN". Network errors, 5xx and 429 are retried.
    """
    req = urllib.request.Request(
        url,
        method="GET",
        headers={
            "User-Agent": BROWSER_UA,
            "Accept": "*/*",
            "Accept-Language": "ko,en;q=0.8",
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
                return "DOWN", f"HTTP {code}"  # 404, 410, etc. are definitive; no retry needed
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
    ap = argparse.ArgumentParser(description="추적 마크다운의 외부 링크 생존 점검")
    ap.add_argument("--strict", action="store_true",
                    help="DOWN 이 하나라도 있으면 종료 코드 1 (주기/릴리스 점검용)")
    ap.add_argument("--list", action="store_true",
                    help="네트워크 호출 없이 점검 대상 URL 과 출처만 출력")
    ap.add_argument("--timeout", type=float, default=15.0, help="요청 타임아웃(초)")
    ap.add_argument("--retries", type=int, default=2, help="네트워크 오류·5xx·429 재시도 횟수")
    ap.add_argument("--delay", type=float, default=0.5, help="호출 간·재시도 간 기본 지연(초)")
    args = ap.parse_args()

    root = subprocess.check_output(
        ["git", "rev-parse", "--show-toplevel"], text=True
    ).strip()
    tracked = subprocess.check_output(["git", "ls-files"], cwd=root, text=True).splitlines()
    md_files = [f for f in tracked if f.endswith(".md")]

    allow = load_allowlist(root)
    found = extract(root, md_files)
    urls = sorted(found)
    print(f"추적 마크다운 {len(md_files)}개에서 점검 대상 외부 URL {len(urls)}개 수집")

    if args.list:
        for url in urls:
            where = ", ".join(f"{f}:{ln}" for f, ln in found[url])
            tag = " [allowlist]" if url in allow else ""
            print(f"  {url}{tag}  ({where})")
        return 0

    results = {"OK": [], "RESTRICTED": [], "ALLOWED": [], "DOWN": []}
    for i, url in enumerate(urls):
        if i:
            time.sleep(args.delay)  # small delay between requests (avoids sandbox throttling)
        verdict, detail = probe(url, args.timeout, args.retries, args.delay)
        if verdict == "DOWN" and url in allow:
            verdict = "ALLOWED"  # known DOWN inherited from upstream that we cannot fix
        results[verdict].append((url, detail))
        print(f"  [{verdict:10}] {url} — {detail}")

    print(
        f"\n요약: OK {len(results['OK'])} · RESTRICTED {len(results['RESTRICTED'])} · "
        f"ALLOWED {len(results['ALLOWED'])} · DOWN {len(results['DOWN'])}"
    )
    if results["RESTRICTED"]:
        print("RESTRICTED(호스트 생존·확인 방법이 막힘, 깨진 링크 아님):")
        for url, detail in results["RESTRICTED"]:
            print(f"  {url} — {detail}")
    if results["ALLOWED"]:
        print("ALLOWED(allowlist 에 적힌 알려진 DOWN — 우리가 고칠 수 없어 strict 제외):")
        for url, detail in results["ALLOWED"]:
            where = ", ".join(f"{f}:{ln}" for f, ln in found[url])
            print(f"  {url} — {detail}  (참조: {where})")
    if results["DOWN"]:
        print("DOWN(깨졌을 가능성 높음 — 확인 필요):")
        for url, detail in results["DOWN"]:
            where = ", ".join(f"{f}:{ln}" for f, ln in found[url])
            print(f"  {url} — {detail}  (참조: {where})")

    if args.strict and results["DOWN"]:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
