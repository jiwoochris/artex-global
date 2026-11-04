#!/usr/bin/env bash
# Development mode: runs the backend (127.0.0.1:8787) and the traffic proxy (:8788)
# together with the frontend `next dev` server (:5173).
# The frontend's /api is reverse-proxied to the backend. Ctrl-C stops both.
#
# The single-binary (frontend embedded) setup is described in the "single binary" section
# of the README; it does not use this script.
set -euo pipefail
cd "$(dirname "$0")"

# On exit, kill every child process in this process group (backend + frontend).
cleanup() { kill 0 2>/dev/null || true; }
trap cleanup EXIT INT TERM

# Backend (plain go run, no embedded frontend). The number of concurrent work agents is
# configured in "System settings".
go run ./cmd/artex -addr 127.0.0.1:8787 -proxy 127.0.0.1:8788 &

# Frontend hot reload (the Next dev server; /api is reverse-proxied to :8787).
( cd web && pnpm run dev ) &

echo "[dev] backend :8787 / proxy :8788 / frontend http://localhost:5173  (Ctrl-C to stop)"
wait
