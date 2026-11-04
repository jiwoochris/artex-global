# ARTEX — developer entry points.
#
# Everything the repository needs to be built, tested, audited and run lives here, so a
# contributor does not have to remember which script or toolchain flag goes first. `make`
# on its own prints the target list.
#
# Override any tool with a variable, for example when go is not on PATH:
#   make test-go GO=/usr/local/go/bin/go
#   make lint-shell SHELLCHECK="uvx --from shellcheck-py shellcheck"
#   make test-go-db ARTEX_PG_DSN=postgres://artex:artex@127.0.0.1:5432/artex?sslmode=disable

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

GO ?= go
# gofmt ships next to the go binary; deriving it keeps `make GO=/path/to/go` working.
GOFMT ?= $(if $(filter %/go,$(GO)),$(patsubst %go,%gofmt,$(GO)),gofmt)
PNPM ?= pnpm
COMPOSE ?= docker compose
SHELLCHECK ?= shellcheck
PYTHON ?= python3
UVX ?= uvx

WEB := web
EMBED_DIR := server/webui/dist
# Docker BuildKit expects the Go architecture name; map the host machine onto it.
TARGETARCH ?= $(shell uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GO_VULNCHECK_VERSION ?= v1.9.0
ACTIONLINT_VERSION ?= v1.7.12
# Mirrors the matrix in .github/workflows/ci.yml.
PG_TEST_PKGS ?= agent config db evidence llmrec server

# These write the same directories (web/out, server/webui/dist, dist/) and must not race
# under `make -j`.
.NOTPARALLEL: embed binary release dist-binaries docker-image docker-build

##@ General

.PHONY: help
help: ## Show this target list
	@awk 'BEGIN {FS = ":.*## "; printf "\nUsage: make <target> [VAR=value]\n"} \
		/^##@/ {printf "\n\033[1m%s\033[0m\n", substr($$0, 5); next} \
		/^[a-zA-Z0-9_.-]+:.*?## / {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@printf "\nVariables: GO=%s GOFMT=%s PNPM=%s COMPOSE=%s VERSION=%s TARGETARCH=%s\n\n" \
		"$(GO)" "$(GOFMT)" "$(PNPM)" "$(COMPOSE)" "$(VERSION)" "$(TARGETARCH)"

.PHONY: check-tools
check-tools: ## Verify the required toolchains are installed
	@command -v $(GO) >/dev/null || { echo "go not found; install Go 1.26+ (https://go.dev/dl/) or set GO=<path>" >&2; exit 1; }
	@command -v $(PNPM) >/dev/null || { echo "pnpm not found; corepack enable pnpm, or set PNPM=<path>" >&2; exit 1; }
	@echo "go $(shell $(GO) env GOVERSION | sed 's/go//') · pnpm $(shell $(PNPM) -v)"

##@ Setup

.PHONY: setup
setup: check-tools ## Install dependencies (go modules + pnpm store from the lockfile)
	$(GO) mod download
	cd $(WEB) && $(PNPM) install --frozen-lockfile

##@ Format and lint

.PHONY: fmt
fmt: ## Format Go and web sources in place
	$(GO) fmt ./...
	$(GOFMT) -w $$(git ls-files '*.go' | grep -v '^$(WEB)/')
	cd $(WEB) && $(PNPM) run format

.PHONY: fmt-check
fmt-check: ## Fail if any Go file is unformatted (what CI checks)
	@out="$$($(GOFMT) -l $$(git ls-files '*.go' | grep -v '^$(WEB)/'))"; \
	if [ -n "$$out" ]; then echo "unformatted: $$out" >&2; exit 1; fi; \
	echo "gofmt: clean"

.PHONY: vet
vet: check-tools ## Go static analysis (go vet)
	$(GO) vet ./...

.PHONY: lint-web
lint-web: ## Biome lint, format and a11y check for the web UI (errors fail)
	cd $(WEB) && $(PNPM) run check

.PHONY: lint-shell
lint-shell: ## Shellcheck every shell script (skips when shellcheck is absent)
	@if command -v $(SHELLCHECK) >/dev/null 2>&1; then \
		$(SHELLCHECK) -S info -f gcc *.sh; echo "shellcheck: clean"; \
	else \
		echo "shellcheck not installed, skipping (try: $(UVX) --from shellcheck-py shellcheck)"; \
	fi

.PHONY: lint-workflows
lint-workflows: check-tools ## Lint .github/workflows with actionlint
	$(GO) run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

.PHONY: lint
lint: vet lint-web lint-shell lint-workflows ## All linters (go vet, biome, shellcheck, actionlint)

.PHONY: i18n
i18n: ## Verify the en/es message catalogues have identical key sets
	cd $(WEB) && $(PNPM) run check:i18n

.PHONY: docs-check
docs-check: ## Check internal doc links, anchors and the language gate
	$(PYTHON) -I scripts/check-doc-links.py
	$(PYTHON) -I scripts/check-non-english.py

.PHONY: posture
posture: check-tools ## Assert the Docker/compose posture and the Linux-only platform policy
	$(GO) test ./deploy/ -count=1 -v

##@ Test

.PHONY: test
test: test-go test-web i18n ## Go tests, web unit tests and the i18n gate (what CI runs)

.PHONY: test-go
test-go: check-tools ## Run the Go test suite (integration tests skip without a database)
	$(GO) test ./... -count=1

.PHONY: test-go-db
test-go-db: check-tools ## Run the DB integration tests per package (needs ARTEX_PG_DSN)
	@test -n "$${ARTEX_PG_DSN:-}" || { echo "ARTEX_PG_DSN is not set (see the header of this Makefile)" >&2; exit 1; }
	@for pkg in $(PG_TEST_PKGS); do \
		echo "== $$pkg"; \
		$(GO) test ./$$pkg/ -count=1 || exit 1; \
	done

.PHONY: test-race
test-race: check-tools ## Go tests with the race detector
	$(GO) test ./... -count=1 -race

.PHONY: test-cover
test-cover: check-tools ## Go tests with a coverage summary
	$(GO) test ./... -count=1 -coverprofile=coverage.out
	$(GO) tool cover -func=coverage.out | tail -1

.PHONY: test-web
test-web: ## Web unit tests (vitest, single run)
	cd $(WEB) && $(PNPM) run test --run

.PHONY: test-detections
test-detections: ## Run the detection rule suites (needs Docker; the CI detections job)
	detections/tests/check-harness-sync.sh
	detections/tests/triage-selftest.sh
	detections/tests/check-defense-template.sh
	detections/tests/sigma/run.sh
	detections/tests/sigma_match/run.sh
	detections/tests/sigma_lint/run.sh
	detections/tests/sigma_backends/run.sh
	detections/tests/suricata/run.sh
	detections/tests/attack/run.sh
	detections/tests/indicators/run.sh
	detections/tests/misp/run.sh

.PHONY: check
check: fmt-check lint test ## Local merge gate: format, lint, test

.PHONY: ci
ci: check docs-check posture ## Everything the push/PR workflows verify.

##@ Audit

.PHONY: audit
audit: check-tools ## Dependency vulnerabilities (govulncheck reachable + pnpm audit high)
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GO_VULNCHECK_VERSION) ./...
	cd $(WEB) && $(PNPM) audit --prod --audit-level=high

##@ Build

.PHONY: web-build
web-build: check-tools ## Static export of the frontend into web/out
	cd $(WEB) && $(PNPM) run build:static

.PHONY: embed
embed: web-build ## Copy the export into server/webui/dist (what //go:embed reads)
	rm -rf $(EMBED_DIR)
	mkdir -p $(EMBED_DIR)
	cp -R $(WEB)/out/. $(EMBED_DIR)/

.PHONY: binary
binary: embed ## Build ./artex with the frontend embedded
	CGO_ENABLED=0 $(GO) build -tags embedui -trimpath \
		-ldflags "-s -w -X main.version=$(VERSION)" -o artex ./cmd/artex
	@echo "built ./artex ($(VERSION))"

.PHONY: binary-no-ui
binary-no-ui: check-tools ## Build ./artex without the frontend (backend only)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-X main.version=$(VERSION)" -o artex ./cmd/artex

.PHONY: release-target
release-target: check-tools ## Build+package one target: make release-target TARGET=linux/arm64
	./build.sh --target $(TARGET)

.PHONY: release
release: ## Build and package both Linux targets into dist/ (zips + SHA256SUMS)
	./build.sh --release

##@ Run

.PHONY: dev
dev: ## Backend :8787 + recording proxy :8788 + frontend :5173
	./dev.sh

.PHONY: start
start: ## Run the daemon supervisor (./start.sh), which restarts on self-update
	./start.sh

.PHONY: install
install: ## Interactive first deployment (Docker or local build)
	./install.sh

.PHONY: update
update: ## Interactive update (pull the image or rebuild locally)
	./update.sh

.PHONY: password
password: ## Reset the admin password (local psql or the postgres container)
	./reset-password.sh

##@ Docker

.PHONY: docker-prep
docker-prep: ## Create ./data and ./skills owned by the invoking user
	mkdir -p data skills
	@echo "run the stack with: ARTEX_UID=$$(id -u) ARTEX_GID=$$(id -g) make docker-up"

.PHONY: dist-binaries
dist-binaries: embed ## Pre-build the binary the image copies in (dist/<arch>/artex)
	mkdir -p dist/$(TARGETARCH)
	CGO_ENABLED=0 $(GO) build -tags embedui -trimpath \
		-ldflags "-s -w -X main.version=$(VERSION)" -o dist/$(TARGETARCH)/artex ./cmd/artex
	@echo "staged dist/$(TARGETARCH)/artex for the image"


.PHONY: docker-image
docker-image: dist-binaries ## Build the local image as artex:local from the run-only Dockerfile
	docker build --build-arg TARGETARCH=$(TARGETARCH) -t artex:local .

.PHONY: docker-build
docker-build: dist-binaries ## Build the image through compose
	$(COMPOSE) build

.PHONY: docker-up
docker-up: ## Start the stack (published image, loopback ports)
	$(COMPOSE) up -d
	@echo "UI: http://127.0.0.1:8787 (first visit: /setup)"

.PHONY: docker-up-local
docker-up-local: docker-build ## Build from source and start the stack
	$(COMPOSE) up -d

.PHONY: docker-up-volumes
docker-up-volumes: ## Start with named volumes instead of the ./data + ./skills bind mounts
	$(COMPOSE) -f docker-compose.yml -f docker-compose.volumes.yml up -d

.PHONY: docker-up-readonly
docker-up-readonly: ## Start with a read-only root filesystem (upgrade by pulling an image)
	$(COMPOSE) -f docker-compose.yml -f docker-compose.readonly.yml up -d

.PHONY: docker-down
docker-down: ## Stop the stack, keeping data and the database volume
	$(COMPOSE) down

.PHONY: docker-reset
docker-reset: ## DESTROYS the database volume and named volumes, then stops
	@read -rp "This deletes pgdata and the named volumes. Continue? [y/N] " ans; \
	[ "$$ans" = "y" ] || { echo "cancelled"; exit 1; }
	$(COMPOSE) down -v

.PHONY: docker-logs
docker-logs: ## Follow the application log
	$(COMPOSE) logs -f artex

.PHONY: docker-ps
docker-ps: ## Show service status and health
	$(COMPOSE) ps

.PHONY: docker-shell
docker-shell: ## Open a shell inside the running artex container
	$(COMPOSE) exec artex /bin/bash

.PHONY: docker-config
docker-config: ## Render the resolved compose configuration
	$(COMPOSE) config

.PHONY: docker-pull
docker-pull: ## Pull the published image (ARTEX_TAG in .env selects the tag)
	$(COMPOSE) pull

.PHONY: docker-audit
docker-audit: ## Inspect the published image attestations (SBOM + provenance)
	@echo "run: docker buildx imagetools inspect ghcr.io/yukiteruamano/artex-global:<tag>"

##@ Housekeeping

.PHONY: clean
clean: ## Remove build outputs (binary, dist/, web/out, embed dir, coverage)
	rm -rf artex dist $(WEB)/out $(EMBED_DIR) coverage.out
	$(GO) clean -testcache 2>/dev/null || true

.PHONY: clean-all
clean-all: clean ## Also remove web/node_modules
	rm -rf $(WEB)/node_modules
