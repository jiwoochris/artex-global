#!/usr/bin/env bash
set -euo pipefail

# 1. Install the frontend (Next.js) dependencies and build the static export
echo "[*] Building Frontend..."
cd web
pnpm install --frozen-lockfile
pnpm run build:static
cd ..

# 2. Copy the built frontend output where Go can embed it
echo "[*] Copying static assets..."
mkdir -p server/webui/dist
cp -r web/out/* server/webui/dist/

# 3. Compile the single Go binary (the embedui tag is required)
echo "[*] Compiling Go Backend..."
CGO_ENABLED=0 go build -tags embedui -o artex ./cmd/artex

echo "[+] Build Completed successfully!"
