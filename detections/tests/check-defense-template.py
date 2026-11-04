#!/usr/bin/env python3
#
# Documentation-claim consistency check for the inline Sigma correlation template
# in the defense guide. check-defense-template.sh launches this inside a container
# that has a pinned sigma-cli (plus the splunk, elasticsearch, loki, and kusto
# backends) installed, with docs/defense.md mounted read-only under /docs.
#
# Why this is a gate and not one of the eight detection suites. The rules under
# detections/sigma/ are re-run by every CI push, the pre-commit hook, and
# run-all.sh. The behaviour-based base template in the defense guide (section 4.2,
# "same-source multi-stage web attack") is NOT in that tree on purpose: it carries
# no ARTEX fingerprint, so it would violate the tree's "ship only what is grounded
# in ARTEX source, not guessed" rule. It lives as a fenced ```yaml block inside the
# prose instead. The guide then states measured facts about it in the present
# tense — "sigma check passes with 0 errors", "converts on splunk/eql/loki",
# "lucene and kusto reject the correlation with 'Backend does not support
# correlation rules'". Nothing re-ran those facts, so an upstream sigma-cli or
# backend change could make the prose silently false with no red CI. This gate
# turns each of those prose claims into a reproducible assertion. Like
# check-harness-sync.sh and triage-selftest.sh it is a gate, not a suite: it is not
# a SUITES entry, not a `detections/tests/<x>/run.sh` CI step, and not a run.sh
# directory, so the eight detection suites stay eight and check-harness-sync never
# counts it.
# The guide is English-only, so the block is extracted and checked from that one
# file: the leading comment header is prose, while every id, detection, and
# correlation line a SIEM actually compiles is the canonical rule content.
#
# The negative assertion (lucene and kusto reject the correlation) is deliberate,
# and differs on purpose from sigma_backends/check.sh, which avoids negative
# assertions so it will not break when a backend gains a capability. Here the
# defense guide states the rejection as a present-tense measured fact that a reader
# may rely on, so if a backend starts converting Sigma correlations the guide's
# sentence is now false and must be rewritten. The red build is the intended
# signal: this is a documentation-freshness guard, not a backend capability test.
#
# Pure standard library plus the sigma-cli the wrapper installed; it writes the
# extracted YAML only under /tmp (never the repo tree) and exits non-zero on any
# failed assertion.
#
# Usage. check-defense-template.sh runs this inside Docker with the docs mounted at
# /docs. To run it directly on a host that already has the pinned sigma-cli and the
# four backends, point ARTEX_DOCS_DIR at the docs directory:
#
#     ARTEX_DOCS_DIR="$(git rev-parse --show-toplevel)/docs" \
#         python3 detections/tests/check-defense-template.py

import os
import subprocess
import sys
import tempfile

DOCS = os.environ.get("ARTEX_DOCS_DIR", "/docs")
GUIDE = os.path.join(DOCS, "defense.md")

# The auth leg of the template matches `/login`, `/auth`, `/verify`, `/otp`. The
# bare word "login" is alphanumeric, so no backend escapes it; its survival into a
# converted query proves the sub-rule detection actually compiled through, not just
# that the command exited 0 on an empty result.
SURVIVES = "login"

fail = 0


def note(msg):
    print(f"  {msg}")


def ok(msg):
    note(f"PASS  {msg}")


def bad(msg):
    global fail
    note(f"FAIL  {msg}")
    fail = 1


def extract_yaml_block(path):
    """The single ```yaml fenced block in a defense doc, as a list of lines.

    The guide carries exactly one such block (the base template). Finding zero or
    more than one is itself a regression worth failing on: zero means the template
    was removed or its fence renamed out from under this gate, more than one means
    the "the template" the prose claims about is now ambiguous.
    """
    with open(path, encoding="utf-8") as f:
        lines = f.read().splitlines()
    blocks = []
    current = None
    for line in lines:
        if current is None:
            if line.strip() == "```yaml":
                current = []
        else:
            if line.strip() == "```":
                blocks.append(current)
                current = None
            else:
                current.append(line)
    if current is not None:
        raise ValueError(f"{path}: unterminated ```yaml fence")
    if len(blocks) != 1:
        raise ValueError(
            f"{path}: expected exactly one ```yaml block, found {len(blocks)}"
        )
    return blocks[0]


def run_sigma(args):
    """Run `sigma ...`, returning (returncode, combined stdout+stderr)."""
    proc = subprocess.run(
        ["sigma", *args],
        capture_output=True,
        text=True,
    )
    return proc.returncode, proc.stdout + proc.stderr


def main():
    if not os.path.isfile(GUIDE):
        print(f"FAIL: missing expected defense guide: {GUIDE}", file=sys.stderr)
        sys.exit(1)

    try:
        block = extract_yaml_block(GUIDE)
    except ValueError as e:
        print(f"FAIL: {e}", file=sys.stderr)
        sys.exit(1)

    with tempfile.NamedTemporaryFile(
        "w", suffix=".yml", encoding="utf-8", delete=False
    ) as tf:
        tf.write("\n".join(block) + "\n")
        template_path = tf.name

    try:
        print("== 1  sigma check passes with zero errors ==")
        rc, out = run_sigma(["check", template_path])
        if rc == 0 and "Found 0 errors" in out and "0 issues" in out:
            ok("defense.md: sigma check → 0 errors, 0 condition errors, 0 issues")
        else:
            bad(f"defense.md: sigma check did not report a clean pass (exit {rc})")
            for line in out.splitlines():
                if "error" in line.lower() or "issue" in line.lower():
                    note(f"        {line}")

        print(
            "== 2  the correlation template converts on splunk, eql, and loki =="
        )
        for target in ("splunk", "eql", "loki"):
            rc, out = run_sigma(
                ["convert", "-t", target, "--without-pipeline", template_path]
            )
            survived = SURVIVES in out.replace("\\", "")
            if rc == 0 and out.strip() and survived:
                ok(
                    f"-t {target}: whole template (atomic + correlation) converts, "
                    f"'{SURVIVES}' survives into the query"
                )
            else:
                bad(
                    f"-t {target}: conversion failed, produced nothing, or dropped "
                    f"'{SURVIVES}' (exit {rc})"
                )
                for line in out.splitlines():
                    if "error" in line.lower() or "not support" in line.lower():
                        note(f"        {line}")

        print(
            "== 3  lucene and kusto reject the correlation "
            "(the guide documents this; a change means the guide is now stale) =="
        )
        for target in ("lucene", "kusto"):
            rc, out = run_sigma(
                ["convert", "-t", target, "--without-pipeline", template_path]
            )
            rejected = (
                rc != 0 and "does not support correlation" in out.lower()
            )
            if rejected:
                ok(
                    f"-t {target}: rejects the correlation with 'Backend does not "
                    f"support correlation rules', exactly as the guide states"
                )
            else:
                bad(
                    f"-t {target}: no longer rejects the correlation (exit {rc}). "
                    f"If this backend now converts Sigma correlations, update the "
                    f"defense guide's section 4.2 claim and the detections/README "
                    f"portability table to match."
                )
    finally:
        os.unlink(template_path)

    print()
    if fail == 0:
        print("RESULT: PASS")
    else:
        print("RESULT: FAIL")
    sys.exit(fail)


if __name__ == "__main__":
    main()
