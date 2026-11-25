#!/usr/bin/env bash
set -euo pipefail

# Render assigns the PORT environment variable arbitrarily, so pass an argument to make the app listen on that port
# Keeps the default execution and the skill/data paths
: "${PORT:?PORT 환경변수가 설정되어 있어야 합니다}"
echo "[*] Starting ARTEX Server on port :$PORT"
exec ./artex -addr ":$PORT"
