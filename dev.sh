#!/usr/bin/env bash
# Development mode: runs the backend (127.0.0.1:8787) and the traffic proxy (:8788)
# together with the frontend `next dev` server (:5173).
# The frontend's /api is reverse-proxied to the backend. Ctrl-C stops both.
#
# The single-binary (frontend embedded) setup is described in the "single binary" section
# of the README; it does not use this script.
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

command -v go >/dev/null 2>&1 || { echo "[x] Go not found (>=1.26): https://go.dev/dl/" >&2; exit 1; }
command -v pnpm >/dev/null 2>&1 || { echo "[x] pnpm not found: the frontend dev server needs pnpm" >&2; exit 1; }

# Track the two children explicitly. `kill 0` would signal the whole process group, which
# includes the invoking shell when this script is not started in its own group (CI, make).
backend=0
frontend=0
cleanup() {
  trap - EXIT INT TERM
  [ "$backend" -ne 0 ] && kill -TERM "$backend" 2>/dev/null || true
  [ "$frontend" -ne 0 ] && kill -TERM "$frontend" 2>/dev/null || true
  wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

# Backend (plain go run, no embedded frontend). The number of concurrent work agents is
# configured in "System settings".
go run ./cmd/artex -addr 127.0.0.1:8787 -proxy 127.0.0.1:8788 &
backend=$!

# Frontend hot reload on the port the README and the default CORS allowlist name (Next's
# own default is 3000, so the port is set here explicitly). /api is reverse-proxied to :8787.
( cd web && pnpm run dev -- -p 5173 ) &
frontend=$!

echo "[dev] backend :8787 / proxy :8788 / frontend http://localhost:5173  (Ctrl-C to stop)"
wait
