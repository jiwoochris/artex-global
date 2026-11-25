# syntax=docker/dockerfile:1
#
# 실행 전용 이미지(이미지 안에서 컴파일하지 않음): 상용 도구만 설치하고, **미리 컴파일한 Linux 단일 바이너리**를 넣는다.
# 바이너리는 CI 의 binaries job 이 크로스 컴파일하며(순수 Go, QEMU 없음), 목표 아키텍처별로
# 빌드 컨텍스트의 dist/<TARGETARCH>/artex 에 둔다. 이렇게 하면 다중 아키텍처 빌드에서 arm64 는 apt 계층만
# 에뮬레이션하면 되고, Next/Go 컴파일은 더 이상 에뮬레이션하지 않아 훨씬 빠르다.
#
# 로컬에서 이미지를 수동으로 빌드할 때는 먼저 바이너리를 직접 준비한다:
#   cd web && npm run build:static && cd ..
#   mkdir -p server/webui && cp -r web/out server/webui/dist
#   CGO_ENABLED=0 GOARCH=amd64 go build -tags embedui -o dist/amd64/artex ./cmd/artex
#   docker build -t artex:local .
FROM python:3.12-slim-bookworm
ARG TARGETARCH
# 브라우저를 /root 가 아닌 공용 경로에 설치한다: 아래에서 root 권한을 버리고 실행하므로 비 root 사용자가 읽을 수 있어야 한다.
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright
# 상용 도구: ripgrep / curl / vim 에 더해 recon 에 자주 쓰는 도구 묶음(필요에 따라 추가·삭제).
# Node 는 NodeSource 에서 20.x 를 설치한다: bookworm 기본 apt nodejs 는 18 이고, Playwright 는 >=20 을 요구한다.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates ripgrep curl wget vim git jq unzip \
      dnsutils iputils-ping netcat-openbsd inetutils-telnet whois nmap \
    && curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*
# Playwright MCP 와 CLI 를 전역으로 미리 설치한다(런타임에 npx 로 네트워크 다운로드하지 않도록).
# @playwright/mcp: browser MCP 는 `npx @playwright/mcp` 로 바로 실행한다(전역 설치 완료, -y/@latest 불필요).
# @playwright/cli: playwright-cli 를 제공하며, 설치 후 --help 로 실행 가능 여부를 확인한다.
# 다음으로 playwright(브라우저 관리 제공)를 설치하고, --with-deps 로 chromium 과 그 시스템 의존성을 미리 깔아 둔다.
# 이렇게 하면 컨테이너 안의 MCP/CLI 가 최초 기동 시 바로 쓸 수 있고, 브라우저를 네트워크로 내려받지 않는다.
RUN npm install -g @playwright/mcp@latest @playwright/cli@latest playwright@latest \
    && playwright-cli --help \
    && playwright install --with-deps chromium \
    && chmod -R a+rX /ms-playwright \
    && rm -rf /var/lib/apt/lists/*
# 비 root 실행 사용자. LLM 이 고른 명령(bash 도구·MCP·사용자 정의 도구)이 전부 이 사용자 권한으로 돈다.
# 컨테이너가 침해돼도 root 권한(패키지 설치, 시스템 파일 변경, 커널 공격면)을 쥐지 못하게 한다.
RUN groupadd --system --gid 10001 artex \
    && useradd --system --uid 10001 --gid artex --create-home --home-dir /home/artex --shell /usr/sbin/nologin artex
WORKDIR /app
# 미리 컴파일한 해당 아키텍처 바이너리(dist/amd64/artex 또는 dist/arm64/artex)
COPY dist/${TARGETARCH}/artex /app/artex
# 감시 기동 스크립트: 프로세스 종료 후 종료 코드에 따라 다시 띄울지 결정하며, 화면의 원클릭 업데이트가 이 스크립트로 교체를 완료한다.
# 이 스크립트는 동시에 SIGTERM 을 artex 로 전달하는 일도 맡는다: docker stop 은 신호를 PID 1 에만 보내므로,
# 전달하지 않으면 artex 가 신호를 받지 못해 우아한 종료를 못 하고 10 초 뒤 SIGKILL 로 강제 종료된다.
COPY start.sh /app/start.sh
# 시작 시 root 로 data/ 소유권만 맞춘 뒤 artex 사용자로 권한을 내려 start.sh 를 실행한다(비 root 로 기동).
COPY docker-entrypoint.sh /app/docker-entrypoint.sh
RUN chmod 0755 /app/artex /app/start.sh /app/docker-entrypoint.sh
COPY skills/ /app/skills/
# /app(실행 파일 포함)은 root 소유로 둔다: 같은 사용자로 도는 에이전트가 artex 바이너리를 덮어써 재시작 뒤에도 남는 것을 막는다.
# 쓰기가 필요한 곳은 두 군데뿐이다. data/ (SQLite·작업 공간) 와 state/ (jwt.key: 서버가 실행 파일 옆에 쓰므로 심볼릭 링크로 state/ 에 둔다).
# 작업 공간(data/)에서 내려받을 수 있는 파일과 서명 키가 섞이지 않도록 state/ 는 data/ 와 분리한다.
RUN mkdir -p /app/data /app/state \
    && chown artex:artex /app/data /app/state \
    && chmod 0700 /app/state \
    && ln -s /app/state/jwt.key /app/jwt.key
# data/(SQLite + 작업 공간) 와 state/(jwt.key) 영속화 지점
VOLUME ["/app/data", "/app/state"]
EXPOSE 8787 8788
ENTRYPOINT ["/app/docker-entrypoint.sh"]
CMD ["-addr", ":8787", "-proxy", ":8788"]
