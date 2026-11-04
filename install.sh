#!/usr/bin/env bash
# ARTEX installer: 1) everything in Docker  2) build and run locally
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){  local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }
rand(){ head -c 18 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 24; }

# ── docker detection / automatic install ──────────────────
ensure_docker(){
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    ok "docker and docker compose are available"; return
  fi
  warn "docker / docker compose not found"
  case "$(uname -s)" in
    Linux)
      if [ "$(ask 'Install Docker automatically? (y/n)' y)" = y ]; then
        curl -fsSL https://get.docker.com | sh
        sudo usermod -aG docker "$USER" || true
        ok "Docker installed (the group change takes effect after you log in again for sudo-free use)"
      else
        die "install docker yourself, then run this script again"
      fi ;;
    Darwin) die "on macOS install Docker Desktop first: https://www.docker.com/products/docker-desktop/" ;;
    *)      die "install docker yourself, then run this script again" ;;
  esac
}

# ── 1) everything in Docker ──────────────────────────────
install_docker(){
  ensure_docker
  if [ ! -f .env ]; then
    cp .env.example .env 2>/dev/null || true
    local pw key
    pw="$(ask 'Postgres password (press Enter to generate one randomly)' "$(rand)")"
    key="$(ask 'ANTHROPIC_API_KEY (may be left empty; can be set later in the UI)' '')"
    sed -i.bak "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=${pw}|" .env
    sed -i.bak "s|^ANTHROPIC_API_KEY=.*|ANTHROPIC_API_KEY=${key}|" .env
    rm -f .env.bak
    ok "created the .env file (POSTGRES_PASSWORD is set)"
  else
    info "keeping the existing .env file"
  fi
  info "pulling the image and starting up…"
  docker compose pull || true
  if ! docker compose up -d; then
    warn "could not pull or start the image ghcr.io/jiwoochris/artex-global (check that ARTEX_TAG in .env is a published tag and that the network works)"
    warn "rerun this script and pick \"2) local run (go build)\", or build via the README route \"build a single binary from source\""
    die "Docker Compose (everything in Docker) failed to start"
  fi
  ok "started -> http://localhost:8787"
  info "on the first visit you must enter the \"setup token\" on the /setup screen to create the admin password"
  info "find the setup token: docker compose logs artex | grep -F '[auth]'   (the default bind address is 127.0.0.1 and is changed with ARTEX_BIND in .env)"
  info "view the logs: docker compose logs -f artex"
}

# ── 2) local build and run ────────────────────────────
install_local(){
  echo "Database setup:"
  echo "  1) Connect to an existing PostgreSQL"
  echo "  2) Start a PostgreSQL in Docker (requires docker)"
  case "$(ask 'Choice' 1)" in
    2)
      ensure_docker
      local pw; pw="$(ask 'Postgres password (press Enter to generate one randomly)' "$(rand)")"
      docker run -d --name artex-pg -p 5432:5432 \
        -e POSTGRES_USER=artex -e POSTGRES_PASSWORD="$pw" -e POSTGRES_DB=artex \
        -v artex-pg:/var/lib/postgresql/data postgres:16-alpine
      DB_HOST=127.0.0.1 DB_PORT=5432 DB_USER=artex DB_PASS="$pw" DB_NAME=artex DB_SSL=disable ;;
    *)
      DB_HOST="$(ask 'Database host' 127.0.0.1)"
      DB_PORT="$(ask 'Port' 5432)"
      DB_USER="$(ask 'User' artex)"
      DB_PASS="$(ask 'Password' '')"
      DB_NAME="$(ask 'Database name' artex)"
      DB_SSL="$(ask 'sslmode (disable/require)' disable)" ;;
  esac

  # create config.json
  cat > config.json <<JSON
{
  "database": {
    "host": "${DB_HOST}",
    "port": ${DB_PORT},
    "user": "${DB_USER}",
    "password": "${DB_PASS}",
    "dbname": "${DB_NAME}",
    "sslmode": "${DB_SSL}"
  }
}
JSON
  ok "created the config.json file"

  # check the go environment
  command -v go >/dev/null 2>&1 || die "Go not found. Install Go (>=1.26) first: https://go.dev/dl/"
  ok "Go: $(go version)"

  # embedding the frontend requires node to produce the static assets
  if command -v pnpm >/dev/null 2>&1; then
    info "building the frontend static assets…"
    ( cd web && pnpm install --frozen-lockfile && pnpm run build:static )
    rm -rf server/webui/dist && mkdir -p server/webui && cp -r web/out server/webui/dist
    info "compiling the single binary with the frontend embedded…"
    CGO_ENABLED=0 go build -tags embedui -trimpath -o artex ./cmd/artex
  else
    warn "pnpm not found, so pnpm is required to embed the frontend: compiling only the backend without the UI (run the frontend separately with pnpm run dev)"
    CGO_ENABLED=0 go build -o artex ./cmd/artex
  fi
  ok "build finished -> ./artex"

  info "starting… (Ctrl-C to stop). The default bind address is 127.0.0.1:8787; on the first visit enter the [auth] setup token from the log below on the /setup screen"
  ./artex
}

echo "=============================="
echo "  ARTEX install"
echo "  1) Install everything in Docker"
echo "  2) Local run (go build)"
echo "=============================="
case "$(ask 'Choice' 1)" in
  1) install_docker ;;
  2) install_local ;;
  *) die "invalid choice" ;;
esac
