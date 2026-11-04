#!/usr/bin/env bash
# ARTEX update script: 1) Docker update (pull a new image and recreate)  2) local build update (rebuild the binary)
# It pairs with install.sh: install handles the first deployment, update handles upgrading to a new version.
# Database migrations never have to be run by hand. artex reruns schema.sql idempotently on
# every start (including ADD COLUMN/CREATE INDEX IF NOT EXISTS), so "a restart is the
# migration". Data (the pgdata volume, ./data, ./skills) is not affected.
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){  local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }

# ── Optional: sync the repository to the latest code (this refreshes compose, the scripts and the local build sources) ───────
sync_repo(){
  if [ ! -d .git ] || ! command -v git >/dev/null 2>&1; then
    warn "not a git checkout, skipping git pull"
    return
  fi
  [ "$(ask 'Fetch the latest code (git pull --ff-only)? (y/n)' y)" = y ] || return
  if ! git pull --ff-only; then
    warn "git pull could not fast-forward (local changes or a diverged branch). Resolve it yourself and retry; this run continues with the current code"
  fi
}

# ── 1) Docker update ───────────────────────────────
update_docker(){
  if ! command -v docker >/dev/null 2>&1 || ! docker compose version >/dev/null 2>&1; then
    die "docker / docker compose not found. Install and deploy with ./install.sh first"
  fi
  [ -f .env ] || die ".env not found. Complete the first deployment with ./install.sh first"

  # Optional: upgrade to a specific version tag (empty means follow ARTEX_TAG from .env, which defaults to latest)
  local tag; tag="$(ask 'Target image tag (press Enter to use .env / latest)' '')"
  if [ -n "$tag" ]; then
    if grep -q '^ARTEX_TAG=' .env; then
      sed -i.bak "s|^ARTEX_TAG=.*|ARTEX_TAG=${tag}|" .env
      rm -f .env.bak
    else
      printf '\nARTEX_TAG=%s\n' "$tag" >> .env
    fi
    ok "set ARTEX_TAG to ${tag}"
  fi

  # Touch only artex. postgres is pinned to 16-alpine and does not need to follow (pulling it
  # would waste bandwidth, and a major version change risks compatibility). artex declares
  # depends_on postgres, so bringing up the service by name starts pg when it is not running
  # and leaves it untouched when it already is.
  info "pulling the new image (artex only)…"
  if ! docker compose pull artex; then
    warn "could not pull the image ghcr.io/yukiteruamano/artex-global (check that the tag is a published release tag and that the network works)"
    die "image update failed. \"2) local update (rebuild with go)\" can update too"
  fi
  info "recreating and starting (artex migrates the schema automatically on restart)…"
  docker compose up -d artex
  ok "update finished -> http://localhost:8787"
  info "view the logs: docker compose logs -f artex"
  info "prune old images (optional): docker image prune -f"
}

# ── 2) local build update ──────────────────────────────
update_local(){
  command -v go >/dev/null 2>&1 || die "Go not found (>=1.26): https://go.dev/dl/"
  [ -f config.json ] || warn "config.json not found. For a first deployment use ./install.sh"
  ok "Go: $(go version)"

  if command -v pnpm >/dev/null 2>&1; then
    info "rebuilding the frontend static assets…"
    ( cd web && pnpm install --frozen-lockfile && pnpm run build:static )
    # Replace the embed directory instead of copying over it: a file removed from the
    # frontend would otherwise survive here and be embedded again.
    rm -rf server/webui/dist
    mkdir -p server/webui/dist
    cp -R web/out/. server/webui/dist/
    info "recompiling the single binary with the frontend embedded…"
    CGO_ENABLED=0 go build -tags embedui -trimpath -o artex ./cmd/artex
  else
    warn "pnpm not found, but pnpm is required to embed the frontend: compiling only the backend without the UI (run the frontend separately with pnpm run dev)"
    CGO_ENABLED=0 go build -o artex ./cmd/artex
  fi
  ok "build finished -> ./artex"
  warn "restart the running artex process to apply the change (the schema is migrated automatically on restart)"
}

echo "=============================="
echo "  ARTEX update"
echo "  1) Docker update (pull a new image and recreate)"
echo "  2) Local update (rebuild with go)"
echo "=============================="
case "$(ask 'Choice' 1)" in
  1) sync_repo; update_docker ;;
  2) sync_repo; update_local ;;
  *) die "invalid choice" ;;
esac
