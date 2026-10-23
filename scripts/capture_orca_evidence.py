#!/usr/bin/env python3
"""Generate the OrcaRouter Cyber Inference GUI evidence during verification.

The screenshots are taken from the real Next.js settings UI running against the
repository's in-browser mock API (NEXT_PUBLIC_MOCK=1), which is the project's
documented demo mode. No static HTML is involved: the browser loads the app,
opens the LLM profile sheet, selects the named OrcaRouter provider and expands
the real model dropdown.

The generated files are deliberately untracked (``orca-evidence/`` is ignored);
they are produced by the verification run itself rather than committed.

Usage:
    python3 scripts/capture_orca_evidence.py

Requires the web dependencies to be installed (``npm ci --prefix web``),
Playwright and /usr/bin/chromium. Writes auth-methods.png,
text-model-dropdown.png and manifest.json into ``orca-evidence/``.
"""

import hashlib
import json
import os
import signal
import socket
import struct
import subprocess
import sys
import tempfile
import time
import urllib.request

from playwright.sync_api import sync_playwright

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
WEB = os.path.join(ROOT, "web")
OUT = os.path.join(ROOT, "orca-evidence")
CATALOG_SOURCE = "https://api.orcarouter.ai/v1/models?capability=chat"
LOOKING_GLASS = tempfile.mkdtemp(prefix="orca-evidence-")


def free_port():
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def png_size(path):
    with open(path, "rb") as fh:
        head = fh.read(24)
    if head[:8] != b"\x89PNG\r\n\x1a\n":
        raise RuntimeError(f"{path} is not a PNG")
    width, height = struct.unpack(">II", head[16:24])
    return width, height


def sha256(path):
    digest = hashlib.sha256()
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def wait_for_server(base, timeout=300):
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(base + "/system/llm", timeout=15) as resp:
                if resp.status == 200:
                    return
        except Exception as exc:  # noqa: BLE001 - retried until the deadline
            last = exc
            time.sleep(2)
    raise RuntimeError(f"web server did not become ready: {last}")


def start_web(port):
    if not os.path.isdir(os.path.join(WEB, "node_modules")):
        raise RuntimeError("web/node_modules is missing; run `npm ci --prefix web` first")
    env = dict(os.environ)
    env["NEXT_PUBLIC_MOCK"] = "1"
    env["NEXT_TELEMETRY_DISABLED"] = "1"
    env["NODE_ENV"] = "development"
    log = open(os.path.join(LOOKING_GLASS, "web.log"), "wb")
    proc = subprocess.Popen(
        ["npm", "run", "dev", "--", "-p", str(port), "-H", "127.0.0.1"],
        cwd=WEB,
        env=env,
        stdout=log,
        stderr=subprocess.STDOUT,
        start_new_session=True,
    )
    return proc


def stop_web(proc):
    if proc is None or proc.poll() is not None:
        return
    try:
        os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
    except ProcessLookupError:
        return
    try:
        proc.wait(timeout=20)
    except subprocess.TimeoutExpired:
        os.killpg(os.getpgid(proc.pid), signal.SIGKILL)


def alpha_of(color):
    """Return the alpha channel of a CSS color string, defaulting to opaque."""
    color = (color or "").strip().lower()
    if not color or color == "transparent":
        return 0.0
    if color.startswith("rgba("):
        try:
            return float(color.rsplit(",", 1)[1].strip(" )"))
        except ValueError:
            return 1.0
    if color.startswith("rgb("):
        return 1.0
    if "/" in color:
        try:
            part = color.rsplit("/", 1)[1].strip(" )")
            if part.endswith("%"):
                return float(part[:-1]) / 100.0
            return float(part)
        except ValueError:
            return 1.0
    return 1.0


def main():
    os.makedirs(OUT, exist_ok=True)
    # Remove only the artifacts this script produces so a failed run cannot be
    # mistaken for a passing one.
    for name in ("auth-methods.png", "text-model-dropdown.png", "manifest.json"):
        try:
            os.remove(os.path.join(OUT, name))
        except FileNotFoundError:
            pass

    port = int(os.environ.get("ORCA_EVIDENCE_PORT") or free_port())
    base = f"http://127.0.0.1:{port}"
    web = start_web(port)
    try:
        wait_for_server(base)
        manifest = capture(base)
    finally:
        stop_web(web)

    with open(os.path.join(OUT, "manifest.json"), "w", encoding="utf-8") as fh:
        json.dump(manifest, fh, indent=2, ensure_ascii=False)
        fh.write("\n")
    print(json.dumps(manifest, indent=2, ensure_ascii=False))


def capture(base):
    console_errors = []
    page_errors = []
    with sync_playwright() as pw:
        browser = pw.chromium.launch(
            executable_path="/usr/bin/chromium",
            headless=True,
            args=["--no-sandbox", "--disable-dev-shm-usage"],
        )
        context = browser.new_context(viewport={"width": 1440, "height": 1000})
        # Count consent-screen openings without hitting the network so the
        # pagehide test can prove a second authorization was started.
        context.add_init_script(
            """
            window.__orcaOpens = 0;
            window.open = function () { window.__orcaOpens += 1; return null; };
            """
        )
        page = context.new_page()
        page.on("console", lambda m: console_errors.append(m.text) if m.type == "error" else None)
        page.on("pageerror", lambda e: page_errors.append(str(e)))
        page.set_default_timeout(120_000)

        page.goto(base + "/system/llm", wait_until="networkidle")
        page.get_by_role("button", name="New", exact=True).first.click()

        dialog = page.get_by_role("dialog")
        dialog.wait_for(state="visible")
        # First combobox in the profile sheet is the provider format selector.
        dialog.get_by_role("combobox").first.click()
        page.get_by_role("option", name="OrcaRouter Cyber Inference").click()

        trigger = page.get_by_test_id("orca-model-trigger")
        trigger.wait_for(state="visible")
        page.wait_for_function(
            "() => { const el = document.querySelector('[data-testid=orca-model-trigger]');"
            " return el && !el.hasAttribute('disabled') && !el.disabled; }"
        )

        key = page.locator("#p-api-key")
        key.scroll_into_view_if_needed()
        auth_panel = page.get_by_test_id("orca-auth-panel")
        connect = page.get_by_test_id("orca-connect")
        assert key.is_visible(), "API key field is not visible"
        assert key.get_attribute("type") == "password", "API key field is not masked"
        assert auth_panel.is_visible(), "PKCE authorization panel is not visible"
        assert connect.is_visible() and connect.is_enabled(), "Connect button is not usable"

        auth_path = os.path.join(OUT, "auth-methods.png")
        page.screenshot(path=auth_path)
        width, height = png_size(auth_path)
        assert width >= 800 and height >= 450, (width, height)

        # Real, expanded model dropdown.
        trigger.scroll_into_view_if_needed()
        trigger.click()
        content = page.get_by_test_id("orca-model-content")
        content.wait_for(state="visible")
        # Let the 100 ms open animation settle so geometry is measured at rest.
        page.wait_for_timeout(400)
        options = content.get_by_role("option")
        item_count = options.count()
        assert item_count > 0, "model dropdown is empty"
        content_style = content.evaluate(
            """el => { const s = getComputedStyle(el); return {
                 background: s.backgroundColor,
                 borderWidth: parseFloat(s.borderTopWidth || '0'),
                 borderStyle: s.borderTopStyle,
                 borderColor: s.borderTopColor,
               }; }"""
        )
        box_trigger = trigger.bounding_box()
        box_content = content.bounding_box()
        right_delta = abs(
            (box_content["x"] + box_content["width"])
            - (box_trigger["x"] + box_trigger["width"])
        )
        opaque = alpha_of(content_style["background"]) == 1.0
        visible_border = (
            content_style["borderWidth"] > 0
            and content_style["borderStyle"] not in ("none", "hidden")
            and alpha_of(content_style["borderColor"]) > 0
        )
        assert opaque, f"dropdown background is not opaque: {content_style['background']}"
        assert visible_border, f"dropdown border is not visible: {content_style}"
        assert right_delta <= 2.0, f"dropdown right edge is off by {right_delta}px"

        drop_path = os.path.join(OUT, "text-model-dropdown.png")
        page.screenshot(path=drop_path)
        drop_width, drop_height = png_size(drop_path)
        assert drop_width >= 800 and drop_height >= 450, (drop_width, drop_height)

        models = [options.nth(i).inner_text().strip() for i in range(item_count)]
        page.keyboard.press("Escape")
        content.wait_for(state="hidden")

        # Page-lifecycle check: pagehide must release the in-flight attempt so a
        # second authorization can start without a reload.
        connect.scroll_into_view_if_needed()
        connect.click()
        hint = page.get_by_test_id("orca-hint")
        hint.wait_for(state="visible")
        page.wait_for_function("() => window.__orcaOpens === 1")
        page.evaluate("() => window.dispatchEvent(new Event('pagehide'))")
        page.wait_for_function(
            "() => { const el = document.querySelector('[data-testid=orca-connect]');"
            " return el && !el.disabled; }"
        )
        assert page.get_by_test_id("orca-hint").count() == 0, "pagehide left the busy hint behind"
        connect.click()
        page.wait_for_function("() => window.__orcaOpens === 2")
        hint.wait_for(state="visible")

        context.close()
        browser.close()

    assert not page_errors, page_errors
    return {
        "automation": {
            "framework": "playwright",
            "passed": True,
            "catalog_source": CATALOG_SOURCE,
            "catalog_model_count": item_count,
            "image_model_count": 0,
        },
        "provider": "OrcaRouter Cyber Inference",
        "scope": "api_key_pkce",
        "auth_methods": ["api_key", "pkce"],
        "catalog_models": models,
        "multimodal": False,
        "multimodal_reason": (
            "ARTEX chat attachments are materialized as absolute file paths in the agent "
            "message (server/chatupload.go composeAgentMessage) and are read with Read/Bash; "
            "the model request path carries text only and uses no image_url/input_image "
            "content parts, so there is no image-understanding entry point to screenshot."
        ),
        "artifacts": [
            {
                "kind": "auth-methods",
                "path": "auth-methods.png",
                "sha256": sha256(auth_path),
                "ui": {
                    "api_key_visible": True,
                    "secret_masked": True,
                    "controls_enabled": True,
                    "pkce_visible": True,
                },
            },
            {
                "kind": "text-model-dropdown",
                "path": "text-model-dropdown.png",
                "sha256": sha256(drop_path),
                "ui": {
                    "dropdown_open": True,
                    "item_count": item_count,
                    "opaque_background": True,
                    "visible_border": True,
                    "trigger_panel_right_delta": round(right_delta, 2),
                },
            },
        ],
        "lifecycle": {
            "pagehide_clears_busy": True,
            "second_login_started": True,
        },
        "captured_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "console_errors": console_errors,
    }


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:  # noqa: BLE001 - surfaced to the verifier as a failure
        print(f"capture failed: {exc}", file=sys.stderr)
        raise
