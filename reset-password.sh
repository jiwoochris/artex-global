#!/usr/bin/env bash
# =============================================================================
# ARTEX admin password reset script
#
# The login user name is fixed to ARTEX. The password is stored as a bcrypt hash in the
# database settings table under the auth.password_hash key. This script connects to the
# database, builds the bcrypt hash inside the database with pgcrypto, and writes it back
# to that key. This is fully compatible with the backend's login verification
# (golang.org/x/crypto/bcrypt).
#
# Two deployment modes:
#   local (default) -- connect to the database from the host with psql. The connection
#                     details come from, in priority order:
#                     command-line arguments > --dsn/$ARTEX_PG_DSN > database.* in config.json
#   docker        -- run psql inside the postgres container via `docker compose exec` (or
#                    `docker exec`); compose does not publish 5432 on the host by default,
#                    so the work happens inside the container.
#
# Examples:
#   ./reset-password.sh                     # local; reads config.json/env and prompts for the new password
#   ./reset-password.sh -p 'NewPass!'       # local; pass the new password directly
#   ./reset-password.sh --dsn postgres://u:p@h:5432/artex
#   ./reset-password.sh -H 127.0.0.1 -P 5433 -U autopentest -W pass -d artex
#   ./reset-password.sh -m docker           # docker deployment (reads POSTGRES_* from .env)
#   ./reset-password.sh -m docker -c pgname --exec docker
#
# Security: the new password is handed to psql through an environment variable and read
# with \getenv, so it never appears in the process argv, and :'var' escapes it
# automatically (which prevents SQL injection). The database password goes through
# PGPASSWORD and never appears in argv either.
# =============================================================================
set -euo pipefail

PASS_KEY="auth.password_hash"
BCRYPT_COST=10

MODE=""            # local | docker (empty means auto-detect)
DSN=""
HOST="" PORT="" USER="" DBPASS="" DBNAME="" SSLMODE=""
CONFIG=""
CONTAINER=""       # postgres service/container name in docker mode (default: postgres)
EXEC_KIND=""       # compose | docker (which exec to use in docker mode; empty means auto)
NEWPASS=""
ASSUME_YES=0

die() { echo "error: $*" >&2; exit 1; }
info() { echo "· $*" >&2; }

usage() { sed -n '2,/^# =====/p' "$0" | sed 's/^# \{0,1\}//'; exit 0; }

# ---- argument parsing ------------------------------------------------------
while [[ $# -gt 0 ]]; do
  case "$1" in
    -m|--mode)        MODE="${2:-}"; shift 2 ;;
    --dsn)            DSN="${2:-}"; shift 2 ;;
    -H|--host)        HOST="${2:-}"; shift 2 ;;
    -P|--port)        PORT="${2:-}"; shift 2 ;;
    -U|--user)        USER="${2:-}"; shift 2 ;;
    -W|--db-password) DBPASS="${2:-}"; shift 2 ;;
    -d|--dbname)      DBNAME="${2:-}"; shift 2 ;;
    --sslmode)        SSLMODE="${2:-}"; shift 2 ;;
    --config)         CONFIG="${2:-}"; shift 2 ;;
    -c|--container)   CONTAINER="${2:-}"; shift 2 ;;
    --exec)           EXEC_KIND="${2:-}"; shift 2 ;;
    -p|--new-password) NEWPASS="${2:-}"; shift 2 ;;
    -y|--yes)         ASSUME_YES=1; shift ;;
    -h|--help)        usage ;;
    *) die "unknown argument: $1 (see -h for usage)" ;;
  esac
done

# ---- read database.* from config.json (local mode with no explicit connection details only) -----
# Parsing with python3 is preferred (robust). Without python3, grep is the fallback (config.json
# keeps these fields tidy and separate).
read_config_json() {
  local path="$1"
  [[ -f "$path" ]] || return 1
  if command -v python3 >/dev/null 2>&1; then
    python3 - "$path" <<'PY'
import json, sys
try:
    d = json.load(open(sys.argv[1])).get("database", {})
except Exception:
    sys.exit(1)
# either a single dsn or the individual fields
if d.get("dsn"):
    print("DSN\t" + d["dsn"]); sys.exit(0)
for k in ("host","port","user","password","dbname","sslmode"):
    if d.get(k) is not None:
        print(k.upper() + "\t" + str(d[k]))
PY
  else
    # Very simple fallback: one grep per key (values are either strings or numbers)
    local k
    for k in host port user password dbname sslmode; do
      local v
      v=$(grep -oE "\"$k\"[[:space:]]*:[[:space:]]*(\"[^\"]*\"|[0-9]+)" "$path" 2>/dev/null \
            | head -1 | sed -E "s/.*:[[:space:]]*//; s/^\"//; s/\"$//") || true
      [[ -n "$v" ]] && echo -e "${k^^}\t$v"
    done
  fi
}

apply_config_fields() {
  local line key val
  while IFS=$'\t' read -r key val; do
    [[ -z "$key" ]] && continue
    case "$key" in
      DSN)      [[ -z "$DSN" ]] && DSN="$val" ;;
      HOST)     [[ -z "$HOST" ]] && HOST="$val" ;;
      PORT)     [[ -z "$PORT" ]] && PORT="$val" ;;
      USER)     [[ -z "$USER" ]] && USER="$val" ;;
      PASSWORD) [[ -z "$DBPASS" ]] && DBPASS="$val" ;;
      DBNAME)   [[ -z "$DBNAME" ]] && DBNAME="$val" ;;
      SSLMODE)  [[ -z "$SSLMODE" ]] && SSLMODE="$val" ;;
    esac
  done
}

# ---- auto-detect the mode ---------------------------------------------------
if [[ -z "$MODE" ]]; then
  if [[ -n "$DSN$HOST$USER$DBNAME" || -n "${ARTEX_PG_DSN:-}" || -f "${CONFIG:-config.json}" ]]; then
    MODE="local"
  elif command -v docker >/dev/null 2>&1 && [[ -f docker-compose.yml ]]; then
    MODE="docker"
  else
    MODE="local"
  fi
fi
info "deployment mode: $MODE"

# ---- collect the new password ------------------------------------------------
if [[ -z "$NEWPASS" ]]; then
  read -r -s -p "New password (the user name is fixed to ARTEX):" NEWPASS; echo >&2
  [[ -n "$NEWPASS" ]] || die "the password may not be empty"
  read -r -s -p "Repeat to confirm:" NEWPASS2; echo >&2
  [[ "$NEWPASS" == "$NEWPASS2" ]] || die "the two entries do not match"
fi
[[ -n "$NEWPASS" ]] || die "the password may not be empty"

# Hand the password to psql through an environment variable (read with \getenv, so it never
# appears in argv or ps)
export ARTEX_RESET_NEWPASS="$NEWPASS"

# Generate the bcrypt hash inside the database and upsert it. The password is escaped
# automatically through :'newpw'. CREATE EXTENSION is idempotent; if the database role may not
# create extensions this fails here (see the failure branch of the run below for guidance).
SQL=$(cat <<SQL
\\set ON_ERROR_STOP on
\\getenv newpw ARTEX_RESET_NEWPASS
CREATE EXTENSION IF NOT EXISTS pgcrypto;
INSERT INTO settings(key, value)
VALUES ('$PASS_KEY', crypt(:'newpw', gen_salt('bf', $BCRYPT_COST)))
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();
SQL
)

# ---- run -----------------------------------------------------------------
if [[ "$MODE" == "local" ]]; then
  # Connection-detail priority: command line > --dsn/$ARTEX_PG_DSN > config.json
  if [[ -z "$DSN" && -z "$HOST$USER$DBNAME" ]]; then
    [[ -n "${ARTEX_PG_DSN:-}" ]] && DSN="$ARTEX_PG_DSN"
  fi
  if [[ -z "$DSN" && -z "$HOST$USER$DBNAME" ]]; then
    cfg="${CONFIG:-config.json}"
    if [[ -f "$cfg" ]]; then
      info "reading the database settings from $cfg"
      apply_config_fields < <(read_config_json "$cfg")
    fi
  fi

  command -v psql >/dev/null 2>&1 || die "psql not found on this machine (install postgresql-client or use -m docker)"

  declare -a PSQL_ARGS=()
  if [[ -n "$DSN" ]]; then
    PSQL_ARGS=("$DSN")
    target="$DSN"
  else
    [[ -n "$USER"   ]] || die "no database user (-U) and no usable config.json/DSN"
    [[ -n "$DBNAME" ]] || die "no database name (-d) and no usable config.json/DSN"
    HOST="${HOST:-127.0.0.1}"; PORT="${PORT:-5432}"; SSLMODE="${SSLMODE:-disable}"
    PSQL_ARGS=(-h "$HOST" -p "$PORT" -U "$USER" -d "$DBNAME")
    [[ -n "$SSLMODE" ]] && export PGSSLMODE="$SSLMODE"
    [[ -n "$DBPASS" ]] && export PGPASSWORD="$DBPASS"
    target="$USER@$HOST:$PORT/$DBNAME"
  fi

  info "target database: $target"
  if [[ "$ASSUME_YES" -ne 1 ]]; then
    read -r -p "Reset the ARTEX password in this database? [y/N]" ans
    [[ "$ans" == "y" || "$ans" == "Y" ]] || die "cancelled"
  fi

  if ! printf '%s\n' "$SQL" | psql "${PSQL_ARGS[@]}" -v ON_ERROR_STOP=1 -q >/dev/null; then
    die "the write failed. If the error is a missing pgcrypto or insufficient privilege, use a role that may create extensions or run CREATE EXTENSION pgcrypto yourself first."
  fi

else
  # ---- docker ----
  command -v docker >/dev/null 2>&1 || die "docker not found"
  CONTAINER="${CONTAINER:-postgres}"

  # Pick the exec flavour: prefer docker compose exec (service name), otherwise docker exec (container name)
  if [[ -z "$EXEC_KIND" ]]; then
    if docker compose version >/dev/null 2>&1 && [[ -f docker-compose.yml ]]; then
      EXEC_KIND="compose"
    else
      EXEC_KIND="docker"
    fi
  fi

  # psql credentials inside the container: command line first, then POSTGRES_* from .env, then the compose default (artex)
  if [[ -f .env ]]; then
    # shellcheck disable=SC1091
    set -a; . ./.env; set +a
  fi
  DUSER="${USER:-${POSTGRES_USER:-artex}}"
  DNAME="${DBNAME:-${POSTGRES_DB:-artex}}"
  [[ -n "$DBPASS" ]] && export PGPASSWORD="$DBPASS"
  [[ -z "${PGPASSWORD:-}" && -n "${POSTGRES_PASSWORD:-}" ]] && export PGPASSWORD="$POSTGRES_PASSWORD"

  info "target: psql -U $DUSER -d $DNAME inside container $CONTAINER (exec=$EXEC_KIND)"
  if [[ "$ASSUME_YES" -ne 1 ]]; then
    read -r -p "Reset the ARTEX password in this container's database? [y/N]" ans
    [[ "$ans" == "y" || "$ans" == "Y" ]] || die "cancelled"
  fi

  # -e passes the name only, not the value, so the variable is inherited from the current
  # environment and the password never shows up in the docker command's argv.
  declare -a EXEC_CMD
  if [[ "$EXEC_KIND" == "compose" ]]; then
    EXEC_CMD=(docker compose exec -T -e ARTEX_RESET_NEWPASS -e PGPASSWORD "$CONTAINER"
              psql -U "$DUSER" -d "$DNAME" -v ON_ERROR_STOP=1 -q)
  else
    EXEC_CMD=(docker exec -i -e ARTEX_RESET_NEWPASS -e PGPASSWORD "$CONTAINER"
              psql -U "$DUSER" -d "$DNAME" -v ON_ERROR_STOP=1 -q)
  fi

  if ! printf '%s\n' "$SQL" | "${EXEC_CMD[@]}" >/dev/null; then
    die "the write failed. Check the container name (-c), the database account (POSTGRES_* in .env) and the role's pgcrypto privileges."
  fi
fi

unset ARTEX_RESET_NEWPASS
echo "✓ ARTEX admin password reset. Log in with the user name ARTEX and the new password (no service restart needed)."
echo "  Login sessions (tokens) issued earlier are all invalidated within a few seconds. If this is a recovery after an account takeover or leak, also review the custom tools, MCP, LLM and notification settings."
