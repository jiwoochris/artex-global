#!/usr/bin/env bash
# OCI's disposable Linux build runner only; no deployment or registry credentials.
set -euo pipefail

# publish: OCI DevOps "publish" stage only. Resolves the digest for IMAGE_TAG and
# writes deploy/<svc>/<DEPLOY_ENV>/image.yaml on the environment's branch
# (development -> dev, production -> release). In production, when the release
# source equals the commit the development image was built from, the tested
# development image is promoted instead of the fresh build (no untested bits in
# prod); otherwise (hotfix) the fresh build is used. Never echoes GITHUB_TOKEN.
publish_image() {
  : "${OCIR_REPOSITORY:?OCIR_REPOSITORY is required}"
  : "${IMAGE_TAG:?IMAGE_TAG is required}"
  : "${BUILDRUN_HASH:?BUILDRUN_HASH is required}"
  : "${OCI_COMPARTMENT_ID:?OCI_COMPARTMENT_ID is required}"
  : "${GITHUB_TOKEN:?GITHUB_TOKEN is required}"
  : "${DEPLOY_SVC:?DEPLOY_SVC is required}"
  : "${DEPLOY_REPO_URL:?DEPLOY_REPO_URL is required}"
  : "${DEPLOY_IMAGE_REPOSITORY:?DEPLOY_IMAGE_REPOSITORY is required}"
  # artex is an internal tool: one scope (tools), one branch (infra-platform docs/guide/naming.md).
  local env="${DEPLOY_ENV:-tools}" branch
  case "$env" in
    tools) branch=main ;;
    *) echo "unknown DEPLOY_ENV ${env}" >&2; exit 1 ;;
  esac

  local digest
  digest="$(oci artifacts container image list --compartment-id "$OCI_COMPARTMENT_ID" \
    --repository-name "$OCIR_REPOSITORY" --display-name "${OCIR_REPOSITORY}:${IMAGE_TAG}" \
    --query 'data.items[0].digest' --raw-output)"
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "no digest resolved for ${OCIR_REPOSITORY}:${IMAGE_TAG}" >&2; exit 1; }

  local clone_dir authed_url image_path
  clone_dir="$(mktemp -d)"
  trap 'rm -rf "$clone_dir"' RETURN
  # authed_url is never logged or echoed.
  authed_url="$(printf '%s' "$DEPLOY_REPO_URL" | sed "s#https://#https://x-access-token:${GITHUB_TOKEN}@#")"
  git clone --quiet --branch "$branch" --filter=blob:none "$authed_url" "$clone_dir" 2>/dev/null

  image_path="deploy/${DEPLOY_SVC}/${env}/image.yaml"
  if [[ -f "$clone_dir/$image_path" ]]; then
    local recorded
    recorded="$(sed -n 's/^# sourceCommit: //p' "$clone_dir/$image_path" | head -n1)"
    if [[ -n "$recorded" && "$recorded" != TODO ]]; then
      if ! git -C "$clone_dir" merge-base --is-ancestor "$recorded" "$BUILDRUN_HASH" 2>/dev/null; then
        echo "recorded sourceCommit ${recorded} is not an ancestor of ${BUILDRUN_HASH}; skipping publish" >&2
        return 0
      fi
    fi
  fi

  local source_commit="$BUILDRUN_HASH" tag="$IMAGE_TAG" origin="build"
  local dev_path="$clone_dir/deploy/${DEPLOY_SVC}/development/image.yaml"
  if [[ "$env" == production && -f "$dev_path" ]]; then
    local dev_source
    dev_source="$(sed -n 's/^# sourceCommit: //p' "$dev_path" | head -n1)"
    if [[ "$dev_source" =~ ^[0-9a-f]{40}$ ]] \
      && git -C "$clone_dir" diff --quiet "$dev_source" "$BUILDRUN_HASH" -- . ':(exclude)deploy' 2>/dev/null; then
      source_commit="$dev_source"
      tag="$(sed -n 's/^  tag: //p' "$dev_path" | head -n1)"
      digest="$(sed -n 's/^  digest: //p' "$dev_path" | head -n1)"
      origin="development"
    fi
  fi

  mkdir -p "$(dirname "$clone_dir/$image_path")"
  cat >"$clone_dir/$image_path" <<EOF
# sourceCommit: ${source_commit}
image:
  repository: ${DEPLOY_IMAGE_REPOSITORY}
  tag: ${tag}
  digest: ${digest}
EOF

  git -C "$clone_dir" -c user.name=noraneko-ci -c user.email=ci@noraneko.cc add "$image_path"
  if git -C "$clone_dir" diff --cached --quiet; then
    echo "${image_path} already points at ${tag}; nothing to publish"
    return 0
  fi
  git -C "$clone_dir" -c user.name=noraneko-ci -c user.email=ci@noraneko.cc \
    commit --quiet -m "chore(deploy): ${DEPLOY_SVC} ${env} image ${tag} (from ${origin})"

  local attempt
  for attempt in 1 2 3; do
    git -C "$clone_dir" pull --quiet --rebase origin "$branch" 2>/dev/null || true
    if git -C "$clone_dir" push --quiet origin "HEAD:${branch}" 2>/dev/null; then
      return 0
    fi
    sleep $((attempt * 2))
  done
  echo "push failed after 3 attempts" >&2
  exit 1
}

if [[ "${1:-}" == "publish" ]]; then
  shift
  publish_image "$@"
  exit $?
fi

if [[ -n "${CONTAINERS_CONF_OVERRIDE:-}" ]]; then
  echo 'CONTAINERS_CONF_OVERRIDE must be unset before OCI CI starts' >&2
  exit 1
fi

APP_VERSION="$(cat VERSION)"
BUILDRUN_HASH="${OCI_PRIMARY_SOURCE_COMMIT_HASH:?OCI source commit is required}"
[[ "$APP_VERSION" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || {
  echo 'VERSION must contain MAJOR.MINOR.PATCH' >&2; exit 1;
}
[[ "$BUILDRUN_HASH" =~ ^[a-f0-9]{40}$ && "$BUILDRUN_HASH" != 0000000000000000000000000000000000000000 ]] || {
  echo 'OCI source commit must be a full nonzero SHA' >&2; exit 1;
}
export APP_VERSION BUILDRUN_HASH

# ── artex(D-023) 빌드 ──────────────────────────────────────────────────────
# Next.js 정적 export 를 server/webui/dist 에 넣어 embedui 로 Go 바이너리에 임베드하고,
# arm64 단일 바이너리를 만든 뒤 루트 Dockerfile 로 런타임 이미지를 만든다. OCI 러너에는
# node·go 가 없어 컨테이너 안에서 빌드한다(oshilog 와 같은 podman 패턴). 결과는 service-image.
ci_directory="$(mktemp -d)"
cleanup() { local status=$?; trap - EXIT; rm -rf "$ci_directory"; exit "$status"; }
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# 1) web 정적 빌드 → web/out (output: "export")
podman run --rm --network host --user 0 \
  --volume "$PWD:$PWD:rw" --workdir "$PWD/web" \
  docker.io/library/node:20-bookworm sh -eu -c \
  'if [ -f package-lock.json ]; then npm ci; else npm install; fi; npm run build:static'

# 2) embedui 소스 트리로 복사(루트 Dockerfile 주석의 경로)
rm -rf server/webui/dist
mkdir -p server/webui
cp -r web/out server/webui/dist

# 3) Go arm64 단일 바이너리(embedui)
mkdir -p dist/arm64
podman run --rm --network host --user 0 \
  --volume "$PWD:$PWD:rw" --workdir "$PWD" \
  --env CGO_ENABLED=0 --env GOOS=linux --env GOARCH=arm64 --env GOFLAGS=-buildvcs=false \
  docker.io/library/golang:1.26-bookworm sh -eu -c \
  'go build -tags embedui -trimpath -o dist/arm64/artex ./cmd/artex'
[[ -x dist/arm64/artex ]] || { echo 'go build produced no binary' >&2; exit 1; }

# 4) 런타임 이미지(arm64). .noraneko/Dockerfile 은 operator 가 올린 arm64 base 를 FROM 해
# COPY 만 한다(RUN 없음) — x86 러너에서 에뮬레이션 없이 빌드된다. (OCI 러너는 binfmt_misc
# 마운트를 막아 QEMU 로 arm64 RUN 을 에뮬레이션할 수 없으므로, 무거운 레이어는 base 에 미리 담는다.)
podman build --platform linux/arm64 --format docker --tag service-image \
  --file "$PWD/.noraneko/Dockerfile" "$PWD"
[[ "$(podman image inspect service-image --format '{{.Architecture}}')" == arm64 ]] || {
  echo 'Built image is not arm64' >&2; exit 1;
}
