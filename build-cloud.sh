#!/usr/bin/env bash
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

command -v go >/dev/null 2>&1 || { echo "[x] Go not found (>=1.26): https://go.dev/dl/" >&2; exit 1; }
command -v pnpm >/dev/null 2>&1 || { echo "[x] pnpm not found: the frontend build needs pnpm" >&2; exit 1; }

# 1. Install the frontend (Next.js) dependencies and build the static export
echo "[*] Building Frontend..."
cd web
pnpm install --frozen-lockfile
pnpm run build:static
cd ..

# 2. Copy the built frontend output where Go can embed it. Replace the directory instead of
# copying over it, so a file deleted in the frontend cannot survive in the embed tree.
echo "[*] Copying static assets..."
rm -rf server/webui/dist
mkdir -p server/webui/dist
cp -R web/out/. server/webui/dist/

# 3. Compile the single Go binary (the embedui tag is required)
echo "[*] Compiling Go Backend..."
CGO_ENABLED=0 go build -tags embedui -trimpath -o artex ./cmd/artex

echo "[+] Build Completed successfully!"
