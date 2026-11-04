# Changelog

This document records the changes that the ARTEX global edition (this fork) adds on top of the upstream repository. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

The upstream ARTEX project's per-version release history (0.3.x and earlier) and contributor list live in the upstream repository, [Autumn-27/ARTEX](https://github.com/Autumn-27/ARTEX) (currently unreachable, checked 2026-10), and can be compared there. The detailed content and rationale for each change in this fork can be found in the repository's commit history.

## [Unreleased]

### Documentation (English only, two UI languages)

- **Made English the canonical document language.** One canonical English file per subject: `README.md`, `CHANGELOG.md`, `CONTRIBUTING.md`, `MAINTAINING.md`, `SECURITY.md`, `CODE_OF_CONDUCT.md`, `docs/defense.md`, `docs/finding-traffic-evidence.md`. The translated copies (`README.{es,ja,ko,zh}.md`, `CHANGELOG.zh.md`, `*.en.md`, `detections/**/README.ko.md`, `sidequestion/*.zh.md`, `docs/defense-ko.md`, `docs/finding-traffic-evidence-{ko,zh}.md`) were removed, and every link that pointed at them was rewired. The sidequestion notes and the `skills/` documents were translated to English; the issue templates and the pull-request template are English only.
- **Documented the container deployment.** [`docs/docker.md`](docs/docker.md) covers the single stack: the posture and why each setting exists, the verification commands, API keys, the opt-in read-only and named-volume overlays, LAN blocking, known limitations and operations (translated from the Spanish original and rewritten for the consolidated stack).
- **The product supports exactly two languages.** The README and the contributing/maintainer policy pages now state that the UI and agent output are English (default) or Spanish, instead of five languages.

### Platform support

- **Removed the native Windows and macOS builds.** ARTEX runs on Linux only: the runtime is a Go binary plus the Debian Trixie container image, and Docker on Windows/macOS is already a Linux VM. `start.bat`, the `darwin/amd64`, `darwin/arm64` and `windows/amd64` entries in the release matrix, the assets they produced, and the docs/UI strings that pointed at them (README, CONTRIBUTING, MAINTAINING, the runtime architecture document, the Playwright skill, the bug-report template) are gone; `build.sh` refuses any other `goos`.
- **Added the explicit Linux-only platform policy.** REQ-PLAT-1 (SPEC §5.1) makes it normative and `deploy/platform_test.go` guards it: release artifacts are `linux/amd64` and `linux/arm64` only, and no Windows or macOS native build, supervisor script or release asset is produced. Anything about *running* ARTEX outside Linux is out of scope; anything about *attacking or defending* Windows/macOS targets is in scope.
- **Installations of the old native Windows/macOS builds stay frozen on the last multiplatform release.** The container path on those hosts is unaffected — `docker compose up -d` behaves the same there as on Linux. All detection and defence material about Windows and macOS targets is retained: `detections/` (Sigma, Suricata, MISP, ATT&CK and the Windows event-log rules), the agent prompts, `docs/defense.md`, and the input hardening that tolerates Windows-produced artefacts.

### Container deployment

- **Consolidated the Docker deployment into one secure stack.** `Dockerfile.hardened`, `docker-compose.hardened.yml` and `docs/docker-hardened.md` were removed. Their useful posture is now the default in `docker-compose.yml`: unprivileged user `10001:10001`, `cap_drop: ALL` with only `NET_RAW` (raw-socket port scanning), `no-new-privileges`, tini as PID 1, a capped `/tmp` tmpfs, `pids_limit`, file-descriptor and core-dump limits, log rotation, a healthcheck, a 60-second stop grace period, loopback-only published ports, and PostgreSQL on an internal network with no published port. What stays optional is only what genuinely conflicts with daily use: `docker-compose.readonly.yml` (read-only rootfs, upgrade by image) and `docker-compose.volumes.yml` (named volumes instead of the `./data` + `./skills` bind mounts). `deploy/posture_test.go` asserts every one of those invariants in the existing `go test ./...` gate, including that no `*-hardened` stack comes back.
- **Fixed the Spanish rule-mode command help.** The Spanish help text told the operator to type `intención …` / `pista …`, but the parser only understood English, Korean and Chinese keywords, so a Spanish operator silently received the status summary. The parser now accepts exactly the English and Spanish keywords the catalogue advertises, and the pre-en/es keywords are gone.

### Scripts

- **`install.sh` no longer installs Docker for you.** It used to run a remote installer through a shell and then suggest adding the user to the `docker` group (which is equivalent to root on the host). It now prints the official installation and rootless-Docker links and exits, so nothing is fetched and executed on the operator's behalf.
- **`install.sh` makes the first start work without privileges.** It creates `./data` and `./skills` and sets `ARTEX_UID`/`ARTEX_GID` to the invoking user, so the bind mounts are writable without a manual ownership change (the compose `user:` is now `${ARTEX_UID:-10001}:${ARTEX_GID:-10001}`). A mismatched owner is reported with the exact fix.
- **`install.sh` hardening:** the local PostgreSQL container publishes on `127.0.0.1` only, is digest-pinned like the compose stack, restarts with the host and is reused instead of failing when it already exists; `config.json` is written through `python3` so a password containing a quote or backslash cannot produce invalid JSON (and values no longer appear in `argv`); secrets are read with `read -s`; `rand()` no longer aborts under `pipefail`.
- **`update.sh`, `build-cloud.sh`, `install.sh`** replace the embed directory (`rm -rf` + copy) instead of copying over it, so a file deleted from the frontend cannot survive in the binary; `update.sh` also creates `server/webui` before copying.
- **`dev.sh`** kills its two children instead of the whole process group (`kill 0` could signal the invoking shell under CI or make) and pins the frontend to port 5173, which is the port the README and the default CORS allowlist already named — `next dev` defaults to 3000, so the documented URL was wrong.
- **`reset-password.sh`** reads `POSTGRES_*` from `.env` with `sed` instead of sourcing it, because sourcing executes whatever the file contains.
- **`build.sh`** no longer aborts when no release archive matched the checksum glob.

### Tooling

- **pnpm 12.17.0.** `packageManager` in `web/package.json` and the `corepack prepare` pin in the `Dockerfile` move from 12.16.1 to 12.17.0, so CI (which reads `packageManager`), the image and local development all resolve the same release. The lockfile was regenerated with 12.17.0 and `pnpm install --frozen-lockfile`, the type check, the unit tests, the i18n gate, the production audit and the static export were re-run against it.

### Developer tooling

- **`Makefile` as the single entry point.** `make` lists every target with a description; the targets wrap the same commands CI runs (`make check` = format, lint, tests, i18n; `make ci` adds doc links, the language gate and the posture tests), plus `make binary`, `make dev`, `make docker-*` (up, overlays, logs, reset), `make release`, `make audit`, `make test-detections` and `make clean`. Toolchains are overridable (`make test-go GO=/path/to/go`), which matters when `go` is not on `PATH`. CONTRIBUTING and the README now point at it instead of repeating the commands.

### CI

- **New `security.yml` gate.** `govulncheck` on every dependency change and weekly, failing only on vulnerabilities the code actually reaches, plus `pnpm audit --prod` failing at high/critical. `SPEC.md` REQ-SEC-11 asked for this; until now the check only happened when a maintainer ran it by hand. The minimatch advisory is fixed by an override in `web/pnpm-workspace.yaml`; the remaining one is a transitive `deepmerge` advisory with no patched release.
- **Publishing is attributable.** The release workflow attests build provenance for the zips and `SHA256SUMS`, and the container image now carries an SBOM and max-mode provenance. This does not replace the missing checksum signature, but a downloader can verify origin with `gh attestation verify` without any key held by the maintainer.
- **The sprint's CI gaps are closed:** `go.mod` is the single source of truth for the Go version (three copies of `"1.26"` were three places to forget), `ci.yml` and `web.yml` cancel superseded runs, every job has a timeout, the release matrix runs both Linux binaries (arm64 under QEMU), `actionlint` lints the workflows themselves, and the release workflow no longer restores package caches, which is the cache-poisoning path into signed artifacts.

### Fixed

- Distinguish task report loading failures from empty reports and offer a retry button. Late responses from a previous task cannot replace the current task report.

## [0.3.15-global.1] · 2026-10-14 · ARTEX-global transition release

The release that turns the Korean edition (`0.3.15-ko.1`) into ARTEX-global, a multilingual edition for defenders and researchers worldwide. Based on upstream ARTEX 0.3.15; no agent features were changed.

### Identity · multilingual (ARTEX-global rename)

- **Rebranded the ARTEX Korean edition as the ARTEX global edition.** The identity shifts from a region-specific edition to a multilingual edition that defenders and researchers worldwide can reach. No agent features were changed; only the multilingual layer and the docs were touched.
  - **Switched the default language from Korean to English.** User-facing output now defaults to English (bootstrap layer `config.DefaultLanguage`, web `DEFAULT_LOCALE`, `config.example.json`, and the `.env.example` comment); Korean, Chinese, and Spanish are selectable in Settings. The agent-package standalone fallback (`agent.defaultOutputLang`) and the Korean golden tests were left in place to avoid regressions.
  - **Reworded the identity strings to the global edition.** Replaced the "Korean edition" wording in the READMEs (Korean, English, Chinese), CONTRIBUTING, SECURITY, the maintainer runbook, and the issue/PR templates, and renamed the English README's "Why a Korean edition" section and its anchor (`#why-a-korean-edition` → `#why-a-global-edition`). The repository slug and image path are renamed to `artex-global` together in the 'Repository rename' section below in this release.

### English-first README and screenshots

- **Promoted the English README to the repository landing page.** The English document is now `README.md` (the first screen), the Korean body moved to `README.ko.md`, and the links/anchors from the root docs to the top-level README were rewired to English=`README.md`, Korean=`README.ko.md`. The README links inside the detections tree were left as-is.
- **Replaced the preview screenshots with English ones.** Recaptured the mock UI in the default English and placed them under `screenshots/en/` (dashboard, findings, chat); the old Korean captures (`screenshots/ko/`) were removed.
- **Made on-screen dates follow the app language.** Replaced 18 hard-coded `ko-KR` locales with `getDateLocale()` in `web/src/i18n/datetime.ts` (display language → BCP47), so dates render in English by default.
- **Translated the demo data to English.** Translated ~220 user-facing Korean strings in the mock data (`web/src/lib/mock/{data,handler}.ts`) to English, preserving commands, payloads, URLs, and identifiers verbatim.

### Repository rename (artex-ko → artex-global)

- **Renamed the repository to `artex-global`.** Changed the GitHub slug to `yukiteruamano/artex-global` (old URLs kept via GitHub redirects) and the container image path to `ghcr.io/yukiteruamano/artex-global`. Replaced 126 occurrences across 37 files in the repo — badges, clone URLs, `docker-compose.yml`, `release.yml`, `install.sh`, docs, edition labels, detection-rule authors, the MISP event name. The upstream Go module (`github.com/Autumn-27/artex`) and the historical version tag (`0.3.15-ko.1`) were left unchanged.

### Localization (i18n)

- **Show built-in agent names and descriptions per language.** The built-in agents (goal decomposition, planning, main, execution, Auto, penetration test, vulnerability retest) are DB seed data (`builtinAgents` in `db/db.go` and the retest agent in `server/finding_retests.go`), so their names, descriptions, and prompt-variable labels used to appear only in Chinese. They are now looked up by agent `key` in `web/messages/` and shown in the current language (falling back to the DB value). User-created agents keep showing their DB values. Applied to the agent list, the editor drawer, and the chat screen; approval/intercept records that come from a chat now show the agent name instead of its internal key (e.g. `pentest`).

### Upstream sync

- **Model judge (fallback approval) token usage is reported separately (upstream 0.3.15, #191).** This upstream change was missing from 0.3.15-ko.1. Judge calls are metered as `worker=judge`, served by the new `GET /api/intercept/judge/usage`. Under the model judge switch in "System → Command interception", a card shows cumulative calls, input, output and cache read/write plus a 30-day daily bar chart.

### Security

- **Closed the remaining gaps in the #22 auth hardening.** A failed password-hash read was still folded into "no password" in token verification and in startup first-run handling, caching an empty hash. Token verification now refuses on a failed read, and startup skips env-var initialisation and setup-token issuance. `ARTEX_ADMIN_PASSWORD` initialisation is also stored once via `InsertSettingIfAbsent` and checked against the 72-byte limit. The `/setup` screen shows a retry notice instead of the form when it cannot confirm the state (the UI part of upstream #197).

## [0.3.15-ko.1] · 2026-10-12 · First Korean-edition release

The first release of the Korean edition, based on upstream ARTEX 0.3.15.

### Distribution

- **The Korean-edition Docker image is now published.** Upstream's `autumn27/artex` image was taken down from Docker Hub, which broke the `docker compose up -d` quick start. Each release tag now pushes a multi-architecture (amd64/arm64) image to `ghcr.io/yukiteruamano/artex-global`, and `docker-compose.yml` pulls that image.

### Security

- **Blocked remote takeover of the first-run admin account.** An instance without a password accepted `POST /api/auth/init` unauthenticated, so whoever reached the port first could set the admin password and obtain a JWT (privately reported by Dangel). Initialisation now requires the setup token (`setup_token`) printed only on the server console at startup, and wrong attempts are rate-limited. Added `ARTEX_SETUP_TOKEN` and `ARTEX_ADMIN_PASSWORD` for unattended deployments and a token field on the `/setup` screen.
- **Default bind address is now 127.0.0.1.** The `artex -addr` default moves from `:8787` to `127.0.0.1:8787`, and `docker-compose.yml` publishes 8787 as `${ARTEX_BIND:-127.0.0.1}`. Deployments reached from other machines must pass `-addr :8787` or set `ARTEX_BIND=0.0.0.0` in `.env`. Starting without a password on a non-loopback address logs a warning.
- **Changing the password invalidates every existing token.** The JWT signing key is derived from the password hash, so a change through the UI, `reset-password.sh`, or a direct DB edit ends earlier sessions. Users must log in once after upgrading. The change-password response carries a fresh token so the current browser session continues.
- **Replaced wildcard CORS with an allowlist.** Instead of `Access-Control-Allow-Origin: *`, headers are only sent for origins in `ARTEX_CORS_ORIGINS` (default: `next dev` at `http://localhost:5173`). Also added server-side minimum password length (8) and audit logs for failed initialisation/login attempts (with client address).
- **Ported the authentication hardening from upstream ARTEX 0.3.15.** Password-setting reads now fail closed with 503 instead of reporting `initialized:false` (`authStatus`, `authInit`, `authLogin`, change-password). First-time setup is atomic via `INSERT ... ON CONFLICT DO NOTHING` ([`db.InsertSettingIfAbsent`](db/settings.go)), so the primary-key constraint protects the existing value even when the read check is unreliable. Passwords over 72 bytes are rejected with 400 instead of 500, and a database connection-pool cap (32) reduces the path where a `too many clients` error can masquerade as a read failure. Ported to match upstream commit `a951e4a` (0.3.15); only the parts not already covered by PR #20 were added.

### Localization (i18n)

- **Added switching to English, Chinese and Spanish.** Korean remains the default. Picking a language under "Display language" in Settings switches the web UI (`web/messages/{ko,en,zh,es}.json`), agent-written reports, summaries and chat replies, and server error messages and status labels together. Operator console logs and the server default follow `ARTEX_LANG` (or `language` in the config file). The agent's behavioural-instruction body is still left in its original language.
- **Forced user-facing output into Korean.** The benchmarked agent's behavioral-instruction body (the "brain") is left in its original language to preserve performance, and a code-level fixed segment (`langDirective`) instructs the agent to write only the user-facing output (vulnerability reports, fact summaries, final summaries, chat replies) in Korean. Commands, payloads, code, and raw logs are kept in their original form.
- **Translated the web UI into Korean.** Introduced `next-intl` into the Next App Router and split the strings into `web/messages/ko.json` and `web/messages/zh.json`. The original Chinese is preserved in `zh.json` so that it can be compared against upstream updates. Screen strings for the dashboard, vulnerabilities, chat, notification delivery, interception, LLM settings, and more were translated into Korean.
- **Translated the server API's user-facing errors and responses into Korean.** HTTP error and response text that is returned to the browser was replaced with Korean. Text that feeds back into the agent brain as input, however, was kept in its original language to prevent benchmark drift, and the reasoning behind each such decision is recorded in the repository's working documents.
- **Reorganized the documentation in Korean.** Created a Korean `README.md`, kept an English `README.en.md` alongside it, and preserved the original Chinese as `README.zh.md`.

### Defense and detection resources

- **Added a defense and detection guide.** A guide (`docs/defense-ko.md`, later merged into the English `docs/defense.md`) that covers how an autonomous AI attack differs from a traditional scanner, the fingerprints (IoCs) a defender can observe, entry points and hardening, detection rules, and incident response.
- **Provides deployable detection rules.** The guide's fingerprints were turned into rules you can use directly. The host and log layer is covered by [Sigma](https://sigmahq.io) atomic and correlation rules ([`detections/sigma/`](detections/sigma/)), and the network layer by [Suricata](https://suricata.io) rules ([`detections/suricata/`](detections/suricata/)) that target the enrich prober and norma SDK WebFetch User-Agents.
- **Visualized ATT&CK coverage.** The techniques that the rules tag were organized into a MITRE ATT&CK Navigator layer ([`detections/attack/`](detections/attack/)).
- **Provides machine-readable indicators of compromise (IoCs) in standard formats.** The unique fingerprints that ARTEX emits were collected into a single CSV ([`detections/indicators/artex_indicators.csv`](detections/indicators/artex_indicators.csv)), along with a MISP event ([`detections/indicators/artex_indicators.misp.json`](detections/indicators/artex_indicators.misp.json)) carrying the same indicators that can be imported straight into a threat-intelligence platform. Indicators that a rule backs are marked with `to_ids`, while host-forensic ports are marked separately as triage clues.
- **Attached reproducible detection tests.** Eight test suites prove the rules by actually running them (Sigma structure and compilation checks, Sigma live event-matching, backend portability, SigmaHQ convention lint, Suricata load and firing, ATT&CK layer consistency, indicator-to-source matching, and MISP export ↔ CSV synchronization). A batch runner that runs them all at once and a pre-commit example were added and wired into the CI merge gate. Sigma live event-matching confirms that the rules not only compile but actually fire on malicious sample events and stay silent on benign ones, for both the atomic and the correlation rules.

### Repository hardening

- **Added security and misuse warnings and Korean legal notices.** The scope of use, notices under the Network Act and the Personal Information Protection Act, and a misuse-prohibition warning were added at the top of the README.
- **Replaced the screen preview with Korean UI screenshots.**
- **Set up a maintainer runbook and a contributing guide.** Added a runbook (`MAINTAINING.md`) for preventing upstream-sync and translation drift, and a detection-rule contribution contract (`CONTRIBUTING.md`). The runbook also documents the release pipeline's build assumptions and how to verify them locally without a tag.
- **Added push/PR merge-gate CI.** The upstream repository ran CI only on tag releases, but this fork runs — on every push and pull request — a Go build, static analysis (`go vet`), and unit tests ([`ci.yml`](.github/workflows/ci.yml)); a Korean UI static build ([`web.yml`](.github/workflows/web.yml)); and integrity checks for the documentation's repository-internal links and image references ([`docs.yml`](.github/workflows/docs.yml)), catching regressions introduced during localization before they merge. Integration tests that require a database are verified alongside a PostgreSQL service isolated per package. The documentation link check runs as a deterministic script ([`scripts/check-doc-links.py`](scripts/check-doc-links.py)) that does not depend on an external network, so the many relative links the multilingual documents point at one another — and the screen-preview images — cannot merge while broken. Documentation anchor (`#heading`) links are also checked against headings using the same slug rules as GitHub, catching table-of-contents and cross-reference links that silently break when heading text changes. The detection-rule suite is handled by the merge gate described in the "Defense and detection resources" section above.
- **Periodically checks external-link liveness.** External links the documents point at — the defense guide's incident-reporting channels and standards references — depend on remote server state and are flaky, so they are kept out of the merge gate; instead a non-blocking workflow ([`external-links`](.github/workflows/external-links.yml)) runs a browser-User-Agent, GET, redirect-following check ([`scripts/check-external-links.py`](scripts/check-external-links.py)) every Monday and on manual dispatch. A host that is alive but blocks the check method (bot protection, rate limiting) and upstream-inherited dead links we cannot fix (an allowlist) are not counted as failures, so it turns red only when an external link our documents curate newly breaks.
- **Established contribution and governance infrastructure.** Added bug, feature, and translation issue templates ([`.github/ISSUE_TEMPLATE/`](.github/ISSUE_TEMPLATE/)) and a pull-request template ([`PULL_REQUEST_TEMPLATE.md`](.github/PULL_REQUEST_TEMPLATE.md)), a security-vulnerability reporting policy (`SECURITY.md`), and a code of conduct (`CODE_OF_CONDUCT.md`), so that external contributors submit issues, PRs, and security reports in a consistent format.
- **Completed an English documentation layer for overseas contributors.** This repository is Korean-first, but English versions of the core documents are provided so that contributors, security researchers, and defenders who do not read Korean can reach the same information. In addition to the English `README.md` and the defense guide (`docs/defense-en.md`, since merged into `docs/defense.md`), there were English versions of the changelog (`CHANGELOG.en.md`), the security reporting policy (`SECURITY.en.md`), the code of conduct (`CODE_OF_CONDUCT.en.md`), the contributing guide (`CONTRIBUTING.en.md`), the maintainer runbook (`MAINTAINING.en.md`), the traffic-evidence design document (`docs/finding-traffic-evidence-en.md`), and the bug, feature, and translation issue templates. (The pull-request template was Korean-only at that point; it is English now.)

---

The upstream ARTEX project's per-version release history and contributor list can be viewed in the upstream repository, [Autumn-27/ARTEX](https://github.com/Autumn-27/ARTEX).
