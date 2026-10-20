#!/usr/bin/env bash
set -euo pipefail
LANG_SEL="${SCOPEWEAVER_LANGUAGE:-${ARTEX_LANGUAGE:-en}}"
case "$LANG_SEL" in ko|ko_*|ko-*|KO) LANG_SEL=ko ;; *) LANG_SEL=en ;; esac
msg(){ if [ "$LANG_SEL" = ko ]; then printf '%s' "$2"; else printf '%s' "$1"; fi; }

cd "$(dirname "$0")"

cleanup() { kill 0 2>/dev/null || true; }
trap cleanup EXIT INT TERM

# Local dev binds the backend to loopback only; the browser reaches it through
# the frontend dev server. Use -addr :8787 only when another device must connect.
go run ./cmd/artex -addr "${ARTEX_BIND:-127.0.0.1}:8787" -proxy 127.0.0.1:8788 &

( cd web && npm run dev ) &

echo "[dev] $(msg "Backend :8787 / proxy :8788 / frontend http://localhost:5173 (Ctrl-C to stop)" "백엔드 :8787 / 프록시 :8788 / 프런트엔드 http://localhost:5173 (Ctrl-C로 종료)")"
wait
