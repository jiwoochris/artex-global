#!/usr/bin/env bash
# ARTEX installer: 1) everything in Docker  2) build and run locally
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){  local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }
ask_secret(){ local p="$1" a; read -rsp "$p: " a; echo >&2; printf '%s' "$a"; }
# 24 alphanumeric characters. The 4 KiB read keeps the pipe from closing early: an early
# close would SIGPIPE tr and, with pipefail, abort the script.
rand(){
  local raw
  raw="$(head -c 4096 /dev/urandom | LC_ALL=C tr -dc 'A-Za-z0-9')"
  printf '%s' "${raw:0:24}"
}

# ── docker detection ──────────────────────────────────
# Deliberately no automatic installation: piping a remote script into a shell runs whatever
# the network serves, as the invoking user (the installer needs root for that), and adding a
# user to the docker group is equivalent to giving it root on the host. Print the official
# path instead and let the operator decide.
ensure_docker(){
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    ok "docker and docker compose are available"; return
  fi
  warn "docker / docker compose not found"
  cat >&2 <<'EOF'
Install Docker Engine with the official instructions for your distribution:
  https://docs.docker.com/engine/install/
and the Compose plugin (usually the docker-compose-plugin package):
  https://docs.docker.com/compose/install/linux/

If you would rather not give your user access to the Docker daemon (membership in the
"docker" group is equivalent to root on this host), use rootless Docker:
  https://docs.docker.com/engine/security/rootless/

Then run this script again.
EOF
  exit 1
}

# ── 1) everything in Docker ──────────────────────────────
install_docker(){
  ensure_docker
  if [ ! -f .env ]; then
    cp .env.example .env 2>/dev/null || true
    local pw key
    pw="$(ask_secret 'Postgres password (press Enter to generate one randomly)')"
    [ -n "$pw" ] || pw="$(rand)"
    key="$(ask 'ANTHROPIC_API_KEY (may be left empty; can be set later in the UI)' '')"
    sed -i.bak "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=${pw}|" .env
    sed -i.bak "s|^ANTHROPIC_API_KEY=.*|ANTHROPIC_API_KEY=${key}|" .env
    rm -f .env.bak
    ok "created the .env file (POSTGRES_PASSWORD is set)"
  else
    info "keeping the existing .env file"
  fi

  # The stack bind-mounts ./data and ./skills, and the container writes as the uid in
  # ARTEX_UID. Docker creates a missing bind-mount source as root, which that user cannot
  # write, so create the directories here and run the container as whoever owns them. That
  # keeps the whole install free of privileged steps.
  mkdir -p data skills
  env_uid="$(sed -n 's/^ARTEX_UID=//p' .env | tail -1)"
  env_gid="$(sed -n 's/^ARTEX_GID=//p' .env | tail -1)"
  if [ -z "$env_uid" ] || [ -z "$env_gid" ]; then
    # .env.example ships both keys empty; fill them with the invoking user's ids.
    if grep -q '^ARTEX_UID=' .env; then
      sed -i.bak "s|^ARTEX_UID=.*|ARTEX_UID=$(id -u)|" .env
      sed -i.bak "s|^ARTEX_GID=.*|ARTEX_GID=$(id -g)|" .env
      rm -f .env.bak
    else
      {
        printf '\n# Run the container as the invoking user, so the ./data and ./skills bind mounts\n'
        printf '# need no ownership change. Set these to 10001 to use the image user instead.\n'
        printf 'ARTEX_UID=%s\nARTEX_GID=%s\n' "$(id -u)" "$(id -g)"
      } >> .env
    fi
    ok "set ARTEX_UID/ARTEX_GID ($(id -u):$(id -g)) so the bind mounts are writable"
  elif [ "$env_uid" != "$(id -u)" ] && [ -z "$(find data -maxdepth 0 -uid "$env_uid" -print -quit 2>/dev/null)" ]; then
    warn "data/ is not owned by ARTEX_UID=$env_uid; the container may fail to write it"
    warn "fix it once as root: chown -R ${env_uid}:${env_gid} data skills   (or set ARTEX_UID=$(id -u))"
  fi

  info "pulling the image and starting up…"
  docker compose pull || true
  if ! docker compose up -d; then
    warn "could not pull or start the image ghcr.io/yukiteruamano/artex-global (check that ARTEX_TAG in .env is a published tag and that the network works)"
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
      local pw; pw="$(ask_secret 'Postgres password (press Enter to generate one randomly)')"
      [ -n "$pw" ] || pw="$(rand)"
      # A leftover container with the same name makes `docker run` fail; keep its data and
      # reuse it instead of silently creating a second database nobody points at.
      if docker container inspect artex-pg >/dev/null 2>&1; then
        if ! docker container start artex-pg >/dev/null 2>&1; then
          die "container artex-pg exists but could not be started; inspect it with: docker logs artex-pg"
        fi
        info "reusing the existing artex-pg container (its password is the one it was created with)"
      else
        # Published on loopback only: this database is for a local run, not for the network.
        # Digest-pinned like the compose stack, so a retag cannot change what runs.
        docker run -d --name artex-pg --restart unless-stopped \
          -p 127.0.0.1:5432:5432 \
          -e POSTGRES_USER=artex -e POSTGRES_PASSWORD="$pw" -e POSTGRES_DB=artex \
          -v artex-pg:/var/lib/postgresql/data \
          postgres:16-alpine@sha256:0cefaabd882e42bcb2a2f3dc56d3ce4770560ba62fd7f197f1a2e008fd5731ab
      fi
      DB_HOST=127.0.0.1 DB_PORT=5432 DB_USER=artex DB_PASS="$pw" DB_NAME=artex DB_SSL=disable ;;
    *)
      DB_HOST="$(ask 'Database host' 127.0.0.1)"
      DB_PORT="$(ask 'Port' 5432)"
      DB_USER="$(ask 'User' artex)"
      DB_PASS="$(ask_secret 'Password (not echoed)')"
      DB_NAME="$(ask 'Database name' artex)"
      DB_SSL="$(ask 'sslmode (disable/require)' disable)" ;;
  esac

  # Write config.json through python3 so a password containing a quote or a backslash stays
  # valid JSON. The values go through the environment, never argv, so they stay out of `ps`.
  if command -v python3 >/dev/null 2>&1; then
    ARTEX_CFG_HOST="$DB_HOST" ARTEX_CFG_PORT="$DB_PORT" ARTEX_CFG_USER="$DB_USER" \
    ARTEX_CFG_PASS="$DB_PASS" ARTEX_CFG_NAME="$DB_NAME" ARTEX_CFG_SSL="$DB_SSL" \
    python3 -c 'import json, os
cfg = {"database": {
    "host": os.environ["ARTEX_CFG_HOST"],
    "port": int(os.environ["ARTEX_CFG_PORT"]),
    "user": os.environ["ARTEX_CFG_USER"],
    "password": os.environ["ARTEX_CFG_PASS"],
    "dbname": os.environ["ARTEX_CFG_NAME"],
    "sslmode": os.environ["ARTEX_CFG_SSL"],
}}
with open("config.json", "w", encoding="utf-8") as fh:
    json.dump(cfg, fh, indent=2)
    fh.write("\n")' || die "could not write config.json"
  else
    case "$DB_PASS" in
      *'"'* | *\\*) die "the password contains a quote or a backslash and python3 is unavailable to escape it; install python3 or choose another password" ;;
    esac
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
  fi
  ok "created the config.json file (it contains the database password: keep it out of version control)"

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
