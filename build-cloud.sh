#!/usr/bin/env bash
set -euo pipefail

# 1. Install frontend (Next.js) dependencies and do the static build
echo "[*] Building Frontend..."
cd web
npm ci
npm run build:static
cd ..

# 2. Copy the built frontend output to the location where Go can embed it
echo "[*] Copying static assets..."
mkdir -p server/webui/dist
cp -r web/out/* server/webui/dist/

# 3. Compile the Go single binary (the embedui tag is required)
echo "[*] Compiling Go Backend..."
CGO_ENABLED=0 go build -tags embedui -o artex ./cmd/artex

echo "[+] Build Completed successfully!"
