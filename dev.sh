#!/usr/bin/env bash
# Development mode: runs the backend (127.0.0.1:8787) + traffic proxy (:8788) together with the frontend next dev (:5173).
# The frontend's /api is reverse-proxied to the backend. Ctrl-C stops them all together.
#
# For the single-binary approach (frontend embedded), see the "single binary" section of the README. That approach does not use this script.
set -euo pipefail
cd "$(dirname "$0")"

# On exit, kill every child process in this process group (backend + frontend).
cleanup() { kill 0 2>/dev/null || true; }
trap cleanup EXIT INT TERM

# Backend (plain go run, frontend not embedded). The number of concurrent work agents is configured in "System settings".
go run ./cmd/artex -addr 127.0.0.1:8787 -proxy 127.0.0.1:8788 &

# Frontend hot reload (Vite/Next dev server, /api is reverse-proxied to :8787).
( cd web && npm run dev ) &

echo "[dev] 백엔드 :8787 / 프록시 :8788 / 프런트엔드 http://localhost:5173  (Ctrl-C 로 종료)"
wait
