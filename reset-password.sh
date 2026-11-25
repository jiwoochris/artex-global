#!/usr/bin/env bash
# =============================================================================
# ARTEX admin password reset script
#
# The login username is fixed as ARTEX. The password is stored as a bcrypt hash under the auth.password_hash key
# of the database's settings table. This script connects to the database, generates the bcrypt hash inside the DB with
# pgcrypto, and writes it back to that key. It is fully compatible with the backend's login check (golang.org/x/crypto/bcrypt).
#
# Two deployment modes:
#   local (default) -- connects to the database directly from the host with psql. Connection details are taken in this order of precedence:
#                    command-line arguments > --dsn/$ARTEX_PG_DSN > database.* in config.json
#   docker        -- runs psql inside the postgres container via `docker compose exec` (or `docker exec`)
#                    (compose does not expose 5432 to the host by default, so it runs inside the container).
#
# Examples:
#   ./reset-password.sh                          # local, reads config.json/the environment automatically and prompts for the new password
#   ./reset-password.sh -p 'NewPass!'            # local, specify the new password directly
#   ./reset-password.sh --dsn postgres://u:p@h:5432/artex
#   ./reset-password.sh -H 127.0.0.1 -P 5433 -U autopentest -W pass -d artex
#   ./reset-password.sh -m docker                # docker deployment (reads POSTGRES_* from .env)
#   ./reset-password.sh -m docker -c <pg-container-name> --exec docker
#
# Security: the new password is passed via an environment variable + psql \getenv (it never appears in the process argv) and is
# escaped automatically with :'var' (prevents SQL injection). The database password is passed via PGPASSWORD, so it also stays out of argv.
# =============================================================================
set -euo pipefail

PASS_KEY="auth.password_hash"
BCRYPT_COST=10

MODE=""            # local | docker (auto-detected if empty)
DSN=""
HOST="" PORT="" USER="" DBPASS="" DBNAME="" SSLMODE=""
CONFIG=""
CONTAINER=""       # postgres service/container name in docker mode (default: postgres)
EXEC_KIND=""       # compose | docker (which exec to use in docker mode; auto if empty)
NEWPASS=""
ASSUME_YES=0

die() { echo "오류: $*" >&2; exit 1; }
info() { echo "· $*" >&2; }

usage() { sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'; exit 0; }

# ---- argument parsing -------------------------------------------------------------
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
    *) die "알 수 없는 인자입니다: $1(-h 로 사용법 확인)" ;;
  esac
done

# ---- read database.* from config.json (only in local mode when no connection details were given explicitly) -----
# Prefer parsing with python3 (robust). If python3 is missing, fall back to grep (config.json uses tidy, separate fields).
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
# either a dsn can be given directly, or the fields separately
if d.get("dsn"):
    print("DSN\t" + d["dsn"]); sys.exit(0)
for k in ("host","port","user","password","dbname","sslmode"):
    if d.get(k) is not None:
        print(k.upper() + "\t" + str(d[k]))
PY
  else
    # very simple fallback: grep per key (the value is a string or a number)
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

# ---- automatic mode detection ---------------------------------------------------------
if [[ -z "$MODE" ]]; then
  if [[ -n "$DSN$HOST$USER$DBNAME" || -n "${ARTEX_PG_DSN:-}" || -f "${CONFIG:-config.json}" ]]; then
    MODE="local"
  elif command -v docker >/dev/null 2>&1 && [[ -f docker-compose.yml ]]; then
    MODE="docker"
  else
    MODE="local"
  fi
fi
info "배포 모드: $MODE"

# ---- collect the new password -----------------------------------------------------------
if [[ -z "$NEWPASS" ]]; then
  read -r -s -p "새 비밀번호 입력(사용자 이름은 ARTEX 로 고정):" NEWPASS; echo >&2
  [[ -n "$NEWPASS" ]] || die "비밀번호는 비워 둘 수 없습니다"
  read -r -s -p "확인을 위해 다시 입력:" NEWPASS2; echo >&2
  [[ "$NEWPASS" == "$NEWPASS2" ]] || die "두 번 입력한 값이 일치하지 않습니다"
fi
[[ -n "$NEWPASS" ]] || die "비밀번호는 비워 둘 수 없습니다"

# Pass the password to psql via an environment variable (read with \getenv, so it never appears in argv/ps)
export ARTEX_RESET_NEWPASS="$NEWPASS"

# Generate the bcrypt hash inside the DB and upsert it. The password is escaped automatically with :'newpw'. CREATE EXTENSION is idempotent;
# if the database role lacks permission to create extensions, this fails here (see the failure branch of run below for guidance).
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
  # connection-detail precedence: command line > --dsn/$ARTEX_PG_DSN > config.json
  if [[ -z "$DSN" && -z "$HOST$USER$DBNAME" ]]; then
    [[ -n "${ARTEX_PG_DSN:-}" ]] && DSN="$ARTEX_PG_DSN"
  fi
  if [[ -z "$DSN" && -z "$HOST$USER$DBNAME" ]]; then
    cfg="${CONFIG:-config.json}"
    if [[ -f "$cfg" ]]; then
      info "$cfg 에서 데이터베이스 설정을 읽습니다"
      apply_config_fields < <(read_config_json "$cfg")
    fi
  fi

  command -v psql >/dev/null 2>&1 || die "이 컴퓨터에서 psql 을 찾을 수 없습니다(postgresql-client 를 설치하거나 -m docker 를 사용하세요)"

  declare -a PSQL_ARGS=()
  if [[ -n "$DSN" ]]; then
    PSQL_ARGS=("$DSN")
    target="$DSN"
  else
    [[ -n "$USER"   ]] || die "데이터베이스 사용자(-U)가 없거나 유효한 config.json/DSN 이 없습니다"
    [[ -n "$DBNAME" ]] || die "데이터베이스 이름(-d)이 없거나 유효한 config.json/DSN 이 없습니다"
    HOST="${HOST:-127.0.0.1}"; PORT="${PORT:-5432}"; SSLMODE="${SSLMODE:-disable}"
    PSQL_ARGS=(-h "$HOST" -p "$PORT" -U "$USER" -d "$DBNAME")
    [[ -n "$SSLMODE" ]] && export PGSSLMODE="$SSLMODE"
    [[ -n "$DBPASS" ]] && export PGPASSWORD="$DBPASS"
    target="$USER@$HOST:$PORT/$DBNAME"
  fi

  info "대상 데이터베이스: $target"
  if [[ "$ASSUME_YES" -ne 1 ]]; then
    read -r -p "이 데이터베이스에서 ARTEX 비밀번호를 재설정할까요? [y/N]" ans
    [[ "$ans" == "y" || "$ans" == "Y" ]] || die "취소했습니다"
  fi

  if ! printf '%s\n' "$SQL" | psql "${PSQL_ARGS[@]}" -v ON_ERROR_STOP=1 -q >/dev/null; then
    die "쓰기에 실패했습니다. pgcrypto 권한 부족·누락 오류가 나면 확장 생성 권한이 있는 역할을 쓰거나 먼저 CREATE EXTENSION pgcrypto 를 직접 실행하세요."
  fi

else
  # ---- docker ----
  command -v docker >/dev/null 2>&1 || die "docker 를 찾을 수 없습니다"
  CONTAINER="${CONTAINER:-postgres}"

  # choose the exec method: prefer docker compose exec (service name), otherwise docker exec (container name)
  if [[ -z "$EXEC_KIND" ]]; then
    if docker compose version >/dev/null 2>&1 && [[ -f docker-compose.yml ]]; then
      EXEC_KIND="compose"
    else
      EXEC_KIND="docker"
    fi
  fi

  # psql credentials inside the container: command line first, then POSTGRES_* from .env, finally the compose default (artex)
  if [[ -f .env ]]; then
    # shellcheck disable=SC1091
    set -a; . ./.env; set +a
  fi
  DUSER="${USER:-${POSTGRES_USER:-artex}}"
  DNAME="${DBNAME:-${POSTGRES_DB:-artex}}"
  [[ -n "$DBPASS" ]] && export PGPASSWORD="$DBPASS"
  [[ -z "${PGPASSWORD:-}" && -n "${POSTGRES_PASSWORD:-}" ]] && export PGPASSWORD="$POSTGRES_PASSWORD"

  info "대상: 컨테이너 $CONTAINER 안 psql -U $DUSER -d $DNAME(exec=$EXEC_KIND)"
  if [[ "$ASSUME_YES" -ne 1 ]]; then
    read -r -p "이 컨테이너 데이터베이스에서 ARTEX 비밀번호를 재설정할까요? [y/N]" ans
    [[ "$ans" == "y" || "$ans" == "Y" ]] || die "취소했습니다"
  fi

  # -e gives only the name, not the value -> it is inherited from the current environment, so the password never appears in the docker command's argv.
  declare -a EXEC_CMD
  if [[ "$EXEC_KIND" == "compose" ]]; then
    EXEC_CMD=(docker compose exec -T -e ARTEX_RESET_NEWPASS -e PGPASSWORD "$CONTAINER"
              psql -U "$DUSER" -d "$DNAME" -v ON_ERROR_STOP=1 -q)
  else
    EXEC_CMD=(docker exec -i -e ARTEX_RESET_NEWPASS -e PGPASSWORD "$CONTAINER"
              psql -U "$DUSER" -d "$DNAME" -v ON_ERROR_STOP=1 -q)
  fi

  if ! printf '%s\n' "$SQL" | "${EXEC_CMD[@]}" >/dev/null; then
    die "쓰기에 실패했습니다. 컨테이너명(-c), 데이터베이스 계정(.env 의 POSTGRES_*), 역할의 pgcrypto 권한을 확인하세요."
  fi
fi

unset ARTEX_RESET_NEWPASS
echo "✓ ARTEX 관리자 비밀번호를 재설정했습니다. 사용자 이름 ARTEX + 새 비밀번호로 로그인하세요(서비스를 재시작할 필요가 없습니다)."
echo "  기존에 발급된 로그인 세션(토큰)은 몇 초 안에 모두 무효화됩니다. 계정이 선점·유출된 뒤의 복구라면 커스텀 도구·MCP·LLM·알림 설정도 함께 점검하세요."
