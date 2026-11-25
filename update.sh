#!/usr/bin/env bash
# ARTEX update script: ① Docker update (pull the new image and recreate)  ② local compile update (rebuild the binary)
# Pairs with install.sh: install handles the first-time installation, update handles upgrading to a new version.
# There is no need to run DB migrations yourself. artex re-runs schema.sql idempotently on every start (including ADD COLUMN/CREATE
# INDEX IF NOT EXISTS), so "a restart is the migration". Data (the pgdata volume, ./data, ./skills) is not affected.
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){  local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }

# ── Optional: sync the repository to the latest code (this refreshes compose, the scripts and the local compile sources) ───────
sync_repo(){
  [ -d .git ] && command -v git >/dev/null 2>&1 || { warn "git 작업 사본이 아니라 git pull 을 건너뜁니다"; return; }
  [ "$(ask '최신 코드를 받을까요 (git pull --ff-only)? (y/n)' y)" = y ] || return
  if ! git pull --ff-only; then
    warn "git pull 을 빨리 감기로 진행하지 못했습니다(로컬 변경이 있거나 브랜치가 갈라졌습니다). 직접 처리한 뒤 다시 시도하세요. 이번에는 현재 코드를 그대로 사용합니다"
  fi
}

# ── ① Docker update ───────────────────────────────
update_docker(){
  command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1 \
    || die "docker / docker compose 를 찾을 수 없습니다. 먼저 ./install.sh 로 설치·배포하세요"
  [ -f .env ] || die ".env 를 찾을 수 없습니다. 먼저 ./install.sh 로 최초 배포를 완료하세요"

  # Optional: upgrade to a specific version tag (if left empty, follows ARTEX_TAG in .env; the default is latest)
  local tag; tag="$(ask '대상 이미지 tag(엔터를 누르면 .env / latest 사용)' '')"
  if [ -n "$tag" ]; then
    if grep -q '^ARTEX_TAG=' .env; then
      sed -i.bak "s|^ARTEX_TAG=.*|ARTEX_TAG=${tag}|" .env && rm -f .env.bak
    else
      printf '\nARTEX_TAG=%s\n' "$tag" >> .env
    fi
    ok "ARTEX_TAG 를 ${tag} 로 설정했습니다"
  fi

  # Only artex is touched. postgres is pinned to 16-alpine, so there is no need to bump it along (pulling it would only waste
  # bandwidth, and a major-version change carries compatibility risk). artex declares depends_on postgres, so running up with the
  # service name starts pg automatically if it is not running, and leaves it as is without recreating it if it already is.
  info "새 이미지를 받습니다(artex 만)…"
  if ! docker compose pull artex; then
    warn "이미지 ghcr.io/jiwoochris/artex-global 를 받지 못했습니다(지정한 tag 가 발행된 릴리스 태그인지, 네트워크가 되는지 확인하세요)"
    die "이미지 업데이트에 실패했습니다. \"2) 로컬 업데이트(go 로 다시 컴파일)\" 로도 업데이트할 수 있습니다"
  fi
  info "재구성 후 시작합니다(artex 는 재시작 시 schema 를 자동으로 마이그레이션합니다)…"
  docker compose up -d artex
  ok "업데이트 완료 → http://localhost:8787"
  info "로그 보기: docker compose logs -f artex"
  info "오래된 이미지 정리(선택): docker image prune -f"
}

# ── ② local compile update ──────────────────────────────
update_local(){
  command -v go >/dev/null 2>&1 || die "Go 를 찾을 수 없습니다(>=1.26): https://go.dev/dl/"
  [ -f config.json ] || warn "config.json 을 찾을 수 없습니다. 최초 배포라면 ./install.sh 를 사용하세요"
  ok "Go: $(go version)"

  if command -v npm >/dev/null 2>&1; then
    info "프런트엔드 정적 산출물을 다시 빌드합니다…"
    ( cd web && npm ci && npm run build:static )
    rm -rf server/webui/dist && cp -r web/out server/webui/dist
    info "프런트엔드를 내장한 단일 바이너리를 다시 컴파일합니다…"
    CGO_ENABLED=0 go build -tags embedui -trimpath -o artex ./cmd/artex
  else
    warn "npm 을 찾을 수 없습니다. 프런트엔드를 내장하지 않은 백엔드만 컴파일합니다(프런트엔드는 npm run dev 로 따로 실행해야 합니다)"
    CGO_ENABLED=0 go build -o artex ./cmd/artex
  fi
  ok "컴파일 완료 → ./artex"
  warn "변경을 적용하려면 실행 중인 artex 프로세스를 재시작하세요(재시작 시 schema 를 자동으로 마이그레이션합니다)"
}

echo "=============================="
echo "  ARTEX 업데이트"
echo "  1) Docker 업데이트(새 이미지를 받아 재구성)"
echo "  2) 로컬 업데이트(go 로 다시 컴파일)"
echo "=============================="
case "$(ask '선택' 1)" in
  1) sync_repo; update_docker ;;
  2) sync_repo; update_local ;;
  *) die "잘못된 선택입니다" ;;
esac
