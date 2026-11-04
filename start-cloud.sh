#!/usr/bin/env bash
set -euo pipefail

# Render assigns the PORT environment variable itself, so pass it as an argument to make
# the app listen on that port.
# Keep the default behaviour and the skills/data paths unchanged.
: "${PORT:?the PORT environment variable must be set}"
echo "[*] Starting ARTEX Server on port :$PORT"
exec ./artex -addr ":$PORT"
