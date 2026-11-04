# syntax=docker/dockerfile:1
#
# Run-only image (nothing is compiled inside it): it installs only the operational tools
# and drops in a **pre-compiled single Linux binary**. CI's `binaries` job cross-compiles
# that binary (pure Go, no QEMU) and places it per target architecture at
# dist/<TARGETARCH>/artex in the build context. Multi-arch builds then only have to
# emulate the apt layer for arm64; Next/Go are no longer compiled under emulation, which
# is far faster.
#
# The process runs as the unprivileged user `artex` (uid/gid 10001); only the build steps
# in this file run as root. The root filesystem stays writable so the UI's one-click
# self-update keeps working. The isolation that makes that safe lives in docker-compose.yml
# (capability drop, no-new-privileges, capped tmpfs, process and log limits, loopback-only
# ports); docs/docker.md explains each decision, including the read-only overlay for
# deployments that upgrade only by pulling a new image.
#
# To build the image by hand locally, prepare the binary yourself first:
#   cd web && pnpm install --frozen-lockfile && pnpm run build:static && cd ..
#   mkdir -p server/webui && cp -r web/out server/webui/dist
#   CGO_ENABLED=0 GOARCH=amd64 go build -tags embedui -o dist/amd64/artex ./cmd/artex
#   docker build -t artex:local .
FROM python:3.12-slim-trixie@sha256:b18b261e1b4974b4f321451ad5251ded85abb22c2b8b262da527dda1d031ab30
ARG TARGETARCH
ARG ARTEX_UID=10001
ARG ARTEX_GID=10001
# Browsers outside any home directory so the unprivileged user can read them; pnpm's
# global bin dir and corepack's cache outside /root and outside the runtime $HOME for the
# same reason (both are written here as root). Without COREPACK_HOME the build-time pnpm
# would live in /root/.cache and the `pnpm` shim would try to re-download itself at runtime.
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
    PNPM_HOME=/usr/local/share/pnpm \
    COREPACK_HOME=/usr/local/share/corepack \
    NPM_CONFIG_UPDATE_NOTIFIER=false \
    COREPACK_ENABLE_DOWNLOAD_PROMPT=0
ENV PATH="$PNPM_HOME:$PATH"
# Operational tools: ripgrep / curl / vim plus the bundle reconnaissance reaches for most
# often (add or drop as needed). xz-utils is for the Node tarball below.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates ripgrep curl wget vim git jq unzip xz-utils \
      dnsutils iputils-ping netcat-openbsd inetutils-telnet whois nmap \
    && rm -rf /var/lib/apt/lists/*
# Node from the official release tarball, at a pinned version with pinned SHA256 sums,
# instead of `curl -fsSL https://deb.nodesource.com/setup_22.x | bash -`: that script runs
# whatever the remote serves, as root, with nothing to verify. The two hashes are the
# linux-x64 / linux-arm64 lines of
# https://nodejs.org/dist/v<NODE_VERSION>/SHASUMS256.txt — bump version and both hashes
# together. trixie's own nodejs is below Playwright's floor of 20, so apt is not an option.
ARG NODE_VERSION=22.23.4
RUN set -eux; \
    case "$TARGETARCH" in \
      amd64) node_arch=x64;   node_sha=1805bcd35e6815559bd2eb97db438d3fdb0eb3a5e6a977b7c1badca76337e9ce ;; \
      arm64) node_arch=arm64; node_sha=6287aa46b6c375d816e63ae66d024fbbfd70404825aabdfdf898a970871d2f4b ;; \
      *) echo "unsupported TARGETARCH: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    curl -fsSLo /tmp/node.tar.xz \
      "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${node_arch}.tar.xz"; \
    echo "${node_sha}  /tmp/node.tar.xz" | sha256sum -c -; \
    tar -xJf /tmp/node.tar.xz -C /usr/local --strip-components=1 --no-same-owner; \
    rm /tmp/node.tar.xz; \
    node --version; \
    [ "$(node -p 'process.versions.node.split(".")[0]')" -ge 20 ]
# pnpm comes from the Node distribution's corepack, pinned to the version in
# web/package.json ("packageManager").
RUN corepack enable \
    && corepack prepare pnpm@12.17.0 --activate \
    && pnpm --version
# Pre-install the Playwright MCP server and CLI globally, so the runtime never downloads
# them over the network.
# @playwright/mcp: the browser MCP runs directly as `npx @playwright/mcp` (already installed
# globally, so no -y/@latest is needed).
# @playwright/cli: provides playwright-cli; --help confirms it actually runs after install.
# Then playwright (browser management) is installed and chromium, together with its system
# dependencies, is fetched up front: the container's MCP/CLI work on first launch and no
# browser is pulled over the network at runtime. a+rX makes the browser tree readable by
# the unprivileged runtime user.
RUN pnpm add -g @playwright/mcp playwright @playwright/cli \
    && playwright-cli --help \
    && playwright install --with-deps chromium \
    && chmod -R a+rX "$PLAYWRIGHT_BROWSERS_PATH" \
    && rm -rf /var/lib/apt/lists/*
# Unprivileged runtime user. A real home directory is created because this image's root
# filesystem is writable: tools that cache in $HOME (Chromium profiles, pnpm) write there.
RUN groupadd -g ${ARTEX_GID} artex \
    && useradd -u ${ARTEX_UID} -g ${ARTEX_GID} -m -d /home/artex -s /usr/sbin/nologin artex
ENV HOME=/home/artex
WORKDIR /app
# The pre-compiled binary for this architecture (dist/amd64/artex or dist/arm64/artex)
COPY dist/${TARGETARCH}/artex /app/artex
# Supervisor script: after the process exits it decides from the exit code whether to bring
# it back, and the one-click update in the UI finishes the swap through this script.
# The script also forwards SIGTERM to artex: `docker stop` signals only PID 1, so without
# the forward artex never sees the signal, cannot shut down gracefully, and is SIGKILLed
# 10 seconds later.
COPY start.sh /app/start.sh
RUN chmod +x /app/artex /app/start.sh
COPY skills/ /app/skills/
# /app belongs to the runtime user: it holds data/ (when no volume is mounted, and the
# image's own VOLUME is seeded from here) and the skill editor writes into skills/.
# data/ has to exist before the chown, otherwise the anonymous volume Docker creates for
# VOLUME below is root-owned and the unprivileged process cannot write to it.
RUN mkdir -p /app/data && chown -R ${ARTEX_UID}:${ARTEX_GID} /app
# Persistence point for data/ (SQLite + jwt.key)
VOLUME ["/app/data"]
USER ${ARTEX_UID}:${ARTEX_GID}
EXPOSE 8787 8788
# `docker stop` sends SIGTERM to PID 1 only; start.sh forwards it to artex so the process
# shuts down gracefully instead of being SIGKILLed after the grace period.
STOPSIGNAL SIGTERM
# Same probe docker-compose.yml uses, so `docker run` without compose still reports health.
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 \
    CMD curl -fsS -o /dev/null http://127.0.0.1:8787/api/health || exit 1
ENTRYPOINT ["/app/start.sh"]
# 8788 is the traffic-recording MITM proxy, which has **no authentication**: it is pinned
# to container loopback, so nothing on the Docker network (or the LAN) can use it as an
# open, TLS-terminating forward proxy. Publishing it
# is not enough to reach it — routing a browser through the proxy needs a deliberate CMD
# override to `-proxy 0.0.0.0:8788` (docker-compose.yml does that through
# ARTEX_PROXY_ADDR); only do that on a host you control.
CMD ["-addr", ":8787", "-proxy", "127.0.0.1:8788"]
