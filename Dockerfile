# syntax=docker/dockerfile:1
#
# Runtime-only image (nothing is compiled inside the image): install only the common tools and add a **precompiled Linux single binary**.
# The binary is cross-compiled by the CI binaries job (pure Go, no QEMU) and placed per target architecture at
# dist/<TARGETARCH>/artex in the build context. This way, in a multi-arch build arm64 only has to emulate the apt layers,
# and the Next/Go compilation is no longer emulated, which is much faster.
#
# When building the image manually on a local machine, prepare the binary yourself first:
#   cd web && npm run build:static && cd ..
#   mkdir -p server/webui && cp -r web/out server/webui/dist
#   CGO_ENABLED=0 GOARCH=amd64 go build -tags embedui -o dist/amd64/artex ./cmd/artex
#   docker build -t artex:local .
FROM python:3.12-slim-bookworm
ARG TARGETARCH
# Common tools: ripgrep / curl / vim plus a bundle of tools often used for recon (add or remove as needed).
# Node 20.x is installed from NodeSource: bookworm's default apt nodejs is 18, and Playwright requires >=20.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates ripgrep curl wget vim git jq unzip \
      dnsutils iputils-ping netcat-openbsd inetutils-telnet whois nmap \
    && curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*
# Preinstall the Playwright MCP and CLI globally (so nothing is downloaded over the network via npx at runtime).
# @playwright/mcp: the browser MCP runs directly via `npx @playwright/mcp` (installed globally, no -y/@latest needed).
# @playwright/cli: provides playwright-cli; after installing, run --help to confirm it executes.
# Then install playwright (which provides browser management) and use --with-deps to preinstall chromium and its system dependencies.
# That way the MCP/CLI inside the container is usable right at first start, and no browser is downloaded over the network.
RUN npm install -g @playwright/mcp@latest @playwright/cli@latest playwright@latest \
    && playwright-cli --help \
    && playwright install --with-deps chromium \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
# Precompiled binary for the matching architecture (dist/amd64/artex or dist/arm64/artex)
COPY dist/${TARGETARCH}/artex /app/artex
# Supervisor start script: after the process exits it decides from the exit code whether to restart it, and the UI's one-click update completes the swap through this script.
# The script also forwards SIGTERM to artex: docker stop sends the signal only to PID 1, so
# without forwarding artex never receives the signal, cannot shut down gracefully, and is SIGKILLed after 10 seconds.
COPY start.sh /app/start.sh
RUN chmod +x /app/artex /app/start.sh
COPY skills/ /app/skills/
# Persistence point for data/ (SQLite + jwt.key)
VOLUME ["/app/data"]
EXPOSE 8787 8788
ENTRYPOINT ["/app/start.sh"]
CMD ["-addr", ":8787", "-proxy", ":8788"]
