# PLAN — English/Spanish-only UI, English-only docs, hardened dependencies

Master execution plan for the change set requested in one go:

1. UI supports **English + Spanish only**; every other language is removed.
2. **Documentation and code comments in English only**, every function documented, exact.
3. **Architecture document** for every part of the project and how the parts connect.
4. Everything linked from **SPEC.md** (spec-driven development entry point).
5. Container base image **`python:3.12-slim-trixie`**.
6. **Go dependencies** reviewed/updated, fixes applied.
7. **npm → pnpm**, dependencies reviewed/updated.
8. **Security review** of the whole project with fixes.
9. Tests for every functional change; no regressions.

Branch: `chore/i18n-en-es-hardening`. Baseline (`fdfb7a1`, before any change):
`go build ./...`, `go vet ./...`, `go test ./... -count=1` all green.

---

## 1. Decisions taken (owner-approved)

| # | Decision | Rationale |
| --- | --- | --- |
| D1 | Non-English documents are **deleted**; the English variant becomes the canonical file name. | "Any other language is prohibited." |
| D2 | **Full English source language**: Chinese agent prompt bodies and the Korean error-message *keys* are rewritten in English. Spanish remains as a translation overlay. | Owner chose "Full inglés: prompts y claves" over the lower-risk hybrid. |
| D3 | Dependency policy: latest **minor/patch** applied; **major** bumps only when the changelog does not break the API and build+tests stay green. | Least risk of behavioural regression in an autonomous pentest agent. |
| D4 | Delivery: commits per area on `chore/i18n-en-es-hardening`. | Reviewable diffs. |
| D5 | Agent *behaviour* must not change beyond language: identical prompt semantics, identical tool contracts, identical test expectations (translated, not relaxed). | Avoid silent capability drift. |

### Consequences of D2 (important)

The backend currently uses **Korean literals as translation keys**: `trMsg("<Korean>")`
looks the literal up in `server/errmsg_catalog.go`. With English as the source
language the contract becomes:

```
source literal (English)  -->  catalog key (same English string)  -->  { "es": "<Spanish>" }
```

i.e. `en` is the identity mapping and is no longer stored; only `es` is a table
entry. Every user-facing Korean literal in Go source is replaced by the *exact*
English string that the catalog already stored under its `"en"` value, which
makes the transformation mechanical and reviewable. Where a literal has no
catalog entry (new/untracked strings), it is translated to English and a new
`es` entry added.

Tests: 51 `*_localized_test.go` files assert per-language output for
`ko/zh/es/ja`. They are reduced to the two supported languages (`en`, `es`) —
assertions are translated, never deleted for convenience.

---

## 2. Language model after this change (target state)

| Layer | Before | After |
| --- | --- | --- |
| UI locales (`web/src/i18n/config.ts`) | `en, ko, zh, es, ja` | `en, es` (default `en`) |
| UI messages (`web/messages/*.json`) | 5 files | `en.json`, `es.json` |
| Agent output language (`agent/outputlang.go`) | `en, ko, zh, es, ja` | `en, es` (default `en`) |
| Backend message catalog | Korean key → `en/zh/es/ja` | English key → `es` |
| Startup/operator catalog (`config/msg_i18n.go`) | Korean key → `en/zh/es/ja` | English key → `es` |
| Agent reasoning prompts | Chinese | English |
| Docs / comments / skills | Korean + Chinese | English |

Fallback rule everywhere: if the active language is `en` (or the lookup misses),
return the source string unchanged.

---

## 3. Waves, ownership and files

File ownership is exclusive per wave so parallel agents never edit the same file.

### W0 — Toolchain and image (parallel)

**W0a — pnpm migration + Trixie base image**
- `web/package-lock.json` → deleted; `web/pnpm-lock.yaml` added; `packageManager` pinned in `web/package.json`.
- `web/.npmrc` / `web/pnpm-workspace.yaml` only if pnpm requires them (build scripts approval).
- `.github/workflows/{ci,web,release,docs,detections}.yml` → `pnpm/action-setup` + `--frozen-lockfile`.
- `Dockerfile`, `Dockerfile.hardened` → `python:3.12-slim-trixie`; npm → pnpm inside the image.
- Shell scripts (`build.sh`, `install.sh`, `start.sh`, `update.sh`, `dev.sh`, `reset-password.sh`, `build-cloud.sh`, `start-cloud.sh`, `start.bat`) — web-related commands only.
- Verification: `pnpm install --frozen-lockfile`, `pnpm exec tsc --noEmit`, `pnpm test --run`, `pnpm run build:static`.

**W0b — Go dependency update**
- `go.mod`, `go.sum` (+ minimal code fixes if an API changed).
- Verification: `go build ./... && go vet ./... && go test ./... -count=1`; `govulncheck ./...` recorded.
- Majors reviewed and deliberately skipped are listed in the report.

### W1 — Documentation (parallel with W0)

**W1a — English-only docs, canonical names**
- Delete `README.{es,ja,ko,zh}.md`, `CHANGELOG.zh.md`; promote `*.en.md` → canonical `CHANGELOG.md`, `CONTRIBUTING.md`, `MAINTAINING.md`, `SECURITY.md`, `CODE_OF_CONDUCT.md`; delete the Korean originals.
- Delete `docs/defense-ko.md`, `docs/finding-traffic-evidence-{ko,zh}.md`, `detections/README.ko.md`, `detections/tests/README.ko.md`; `docs/defense-en.md` → `docs/defense.md`.
- Translate `docs/docker-seguro.es.md` → `docs/docker.md` (the stack was later consolidated), then delete the Spanish file.
- Translate `sidequestion/README.md`, `sidequestion/VALIDATION.md`, `skills/**` to English.
- Fix every reference/link; `python3 scripts/check-doc-links.py` must be clean.

**W1b — `docs/architecture.md` + `docs/architecture/*.md`** (new): an index with the system
overview, plus one document per area — runtime/startup, backend packages, agent engine
(with a one-run sequence diagram), data model, HTTP surface, frontend, cross-cutting
concerns, integration-point table. Section numbers are preserved across the split.

**W1c — `SPEC.md`** (owner-written, links everything): product scope, language contract,
architecture references, functional specs per subsystem, security requirements,
dependency/image policy, test strategy, acceptance criteria.

### W2 — UI i18n purge (web)

- `web/src/i18n/config.ts` → `LOCALES = ["en","es"]`, labels, cookie/storage unchanged.
- Delete `web/messages/{ko,zh,ja}.json`; keep `en.json`, `es.json`.
- `web/src/i18n/request.ts`, `intl-runtime-provider.tsx` → two-bundle map.
- `web/src/scripts/check-i18n-keys.ts` → baseline `en`, compare `es` only.
- `web/src/scripts/extract-i18n.ts`, `i18n-keys.ts` → two locales.
- `web/src/i18n/config.test.ts` → asserts exactly `["en","es"]`.
- Any component/lib that branches on `ko|zh|ja` (settings language picker, `agent-i18n.ts`, `status.ts`, `types.ts`, `chat-mentions.ts`, `chat-send-mode.ts`, `company-scope.ts`, `exploration-graph.tsx`, `status-badge.tsx`, `app-config.ts`, `nav-main.tsx`, chat/tasks pages) → reduced to `en|es`.
- `scripts/check-web-cjk.py` reviewed: it must not fail on legitimate data (agent output may contain any script) and must not require Korean.
- Tests: `config.test.ts`, `i18n-keys.test.ts`, plus `pnpm run check:i18n`, `tsc --noEmit`, `build:static`.

### W3 — Backend language plumbing + English source language

**W3a — plumbing (`en`,`es` only)** — owned files: `agent/outputlang.go`,
`config/config.go` + `config/msg_i18n.go`, `server/manager.go`,
`server/server.go` (language validation/handlers), `server/errmsg_i18n.go`,
`intercept/label_i18n.go`, notify/report language switches.
- `SupportedOutputLangs = {"en","es"}`, `defaultOutputLang = "en"`,
  `langPhrase`/`langBare` keep only `en`,`es`.
- `/api/settings` output-language validation error updated.
- Catalog lookups drop `zh`/`ja`.

**W3b — English source language, package by package** (disjoint dirs):
- `server/` (largest: 871 `writeErr` sites, 212 catalog entries) — split into
  3 agents by file block, catalog regenerated once by the owner script.
- `config/`, `notify/`, `report/`, `intercept/`, `db/`, `guard/`, `selfupdate/`,
  `llmpool/`, `sidequestion/`, `evidence/`, `traffic/`, `agent/`.
- Chinese prompt bodies (`agent/tools.go`, `planner.go`, `promptcatalog.go`,
  `prompt.go`, `worker.go`, `wrapup.go`, `intercept/prompt.go`,
  `db/schema.sql` seeds) → English, semantics preserved (D5).
- Verification per batch: `go build ./... && go vet ./... && go test ./... -count=1`.

### W4 — Comment/docstring translation (Go + web)

- Every Go file: Korean/Chinese comments → English. 287 Go files carry ~43k Korean
  + ~100k Chinese characters today (much of the Chinese is prompt payload, handled in W3b).
- Every `web/src/**` file: Korean/Chinese comments → English (86 files, ~8.5k chars).
- Exported functions/types get a doc comment that states the contract (requirement 2:
  "every function explained"). Missing doc comments are added for exported symbols.
- Verification: `python3 /tmp/pi/scan.py .` must report only allow-listed residual
  non-English content (none expected), plus `go build/vet/test` and `pnpm tsc/test`.

### W5 — Security review and remediation

- `govulncheck ./...`, `gosec ./...` (via `go run`), dependency audit
  (`pnpm audit`, `go mod` graph), Docker image hardening review.
- Fix by severity; each fix gets a test. Focus areas already visible in the code:
  auth/session (JWT, setup token, rate limiting), file/zip handling
  (`server/skill_zip.go`, `task_archive_package.go`, `chatupload.go` — path traversal),
  command execution paths (`agent/tools.go`), SSRF in intercept/traffic, secrets in logs,
  CORS/proxy defaults, dependency supply chain.
- Verification: full Go + web suites, plus targeted tests per fix.

---

## 4. Test plan — every functional change has a test

| Change | Test |
| --- | --- |
| Locale list is `en`,`es`; default `en` | `web/src/i18n/config.test.ts` |
| Message files in sync for 2 locales | `web/src/scripts/i18n-keys.test.ts`, `pnpm run check:i18n` |
| Runtime language switch still re-renders | existing `intl-runtime-provider` consumers + manual build check (`build:static`) |
| Backend accepts only `en`,`es` | `server/errmsg_i18n_test.go`, `config/config_localized_test.go` |
| English key → Spanish overlay lookup | `server/errmsg_i18n_test.go`, `config/msg_i18n_test.go` |
| Per-package localized output | the 51 `*_localized_test.go` files reduced to `en`+`es` |
| Agent output-language directive | `agent/prompt_test.go`, `agent/wrapup_test.go` |
| Prompts now English (semantics unchanged) | `agent/toolcatalog_test.go`, `prompt_test.go`, `intercept/prompt_test.go` |
| Go dependency update | `go build/vet/test` + `govulncheck` |
| pnpm migration | `pnpm install --frozen-lockfile`, `pnpm test --run`, `build:static` |
| Docker Trixie image | Dockerfile reviewed + tag existence check (no docker daemon in this environment) |
| Security fixes | one test per fix (see W5 table in the commit messages) |
| Docs English-only, links valid | `scripts/check-doc-links.py` |

---

## 5. Verification gates (must all pass before hand-off)

```bash
export PATH=/tmp/pi/go/bin:$PATH
go build ./... && go vet ./... && go test ./... -count=1
cd web && pnpm install --frozen-lockfile && pnpm exec tsc --noEmit && pnpm test --run && pnpm run build:static
python3 scripts/check-doc-links.py
python3 /tmp/pi/scan.py .        # residual non-English scan
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

---

## 6. Risks and mitigations

| Risk | Mitigation |
| --- | --- |
| Translating agent prompts degrades the benchmarked reasoning (D2 accepted by owner) | Prompts translated 1:1 with identical structure/ordering/tool vocabulary; `agent/prompt_test.go` assertions updated only where they pin language, not semantics. |
| Korean-keyed catalog rewrite breaks error text silently | Mechanical mapping (old `en` value becomes new key) + test asserting no lookup misses for every call-site literal. |
| Dependency major bump changes behaviour | Policy D3; build+vet+full test suite after each step; deltas reported. |
| pnpm lockfile drift / CI mismatch | `--frozen-lockfile` everywhere; CI updated in the same commit. |
| Doc deletion breaks workflows/links | `check-doc-links.py` + `docs.yml` gate. |
| Parallel agents editing the same file | Exclusive file ownership per wave (section 3). |

---

## 7. Status

| Wave | Item | Status |
| --- | --- | --- |
| W0a | pnpm + Trixie image | done — `pnpm@12.16.1` pinned, `package-lock.json` removed, `web/pnpm-workspace.yaml` carries `allowBuilds`, both Dockerfiles on `python:3.12-slim-trixie` |
| W0b | Go dependency update | done — 34 modules upgraded (x/net 0.62.0, x/crypto 0.58.0, pgx 5.12.0, mitmproxy 1.9.4, dnsx 1.3.1, sqlite 1.61.1 …), `go 1.26.10` directive, 11 govulncheck findings fixed, 1 residual (GO-2026-5932, unreachable) |
| W1a | English-only docs | done — 4 README translations, `CHANGELOG.zh.md`, Korean canonical docs and all `*.ko.md` deleted; `*.en.md` promoted to canonical; `docs/docker-seguro.es.md` → `docs/docker.md`; `check-doc-links.py` clean (34 docs, 284 refs) |
| W1b | `docs/architecture.md` + `docs/architecture/` | done — index (186 lines) plus eight documents (2 023 lines, 2 209 with headers) covering §2–§9 with file:line evidence, Mermaid diagrams, per-document contents lists and an integration table |
| W1c | `SPEC.md` + `PLAN.md` | done |
| W2 | UI en/es purge | done — `LOCALES = ["en","es"]`, `ko/zh/ja` message files and every locale branch removed, `tsc`, vitest, `check:i18n` (2996 keys), biome and `build:static` (31 pages) green |
| W3a | Backend language plumbing | done — `config.SupportedLanguages` / `agent.SupportedOutputLangs` = {en,es}, `ActiveLanguage()` fallback `en`, notify/intercept label maps reduced, `server/testmain_test.go` pins `ARTEX_LANG=en` |
| W3b | English source language | done — every user-facing literal translated to English, `server/errmsg_catalog.go` re-keyed English → `{es}` (354 entries), `config/msg_i18n.go` re-keyed, 151 new Spanish pairs merged, write-once prompt/tool reseed added |
| W4 | Comment/docstring translation | done — `python3 /tmp/pi/scan.py .` reports 23 Korean + 856 Chinese characters left, all on the documented sentinel/fixture allow-list (SPEC §3.5.1) |
| W5 | Security review | done — `docs/security-review.md` (4 High, 9 Medium, 5 Low, with verified file:line evidence); HTTP-layer and infrastructure findings remediated with tests |

### 7.1 Verification snapshot (this machine, no PostgreSQL)

```text
go build ./...                     PASS
go vet ./...                       PASS
go test ./... -count=1             PASS  (all packages; DB-backed tests skip as in CI's no-DB job)
cd web && pnpm exec tsc --noEmit   PASS
pnpm run test --run                PASS  (5 files / 15 tests)
pnpm run check:i18n               PASS  (2996 keys, en == es)
pnpm run lint                      PASS  (0 errors)
pnpm run build:static             PASS  (31/31 pages, 234 files)
python3 scripts/check-doc-links.py PASS  (0 broken references)
```

### 7.2 Deviations from the original plan (review these)

1. **Worksheet pipeline instead of file-by-file editing.** Translating ~124k characters
   through per-edit tool calls exceeded the agent budget; the remainder was converted by
   extracting `file\tline\ttext` worksheets, translating them in one pass, and applying them
   by line number (`/tmp/pi/wsl.py`). Consequence: a few rows dropped trailing commas in
   composite literals (fixed) and one struct field was lost during the earlier phase
   (`Task.TimeoutSeconds`, restored). This is why the whole suite is re-run at the end.
2. **`biome.json` migrated** to the installed Biome 2.6.4 schema and
   `nursery/useSortedClasses` → `nursery/useTailwindSortedClasses` (the old config could not
   even load). No rule disabled, no ignore added.
3. **`firstNonEmpty()` helper added** in `web/src/lib/utils.ts`: 25
   `nursery/useNullishCoalescing` errors could not be fixed with `??` without changing
   behaviour where `""`/`0` must also fall back. 16 call sites use the helper, 9 use `??`.
4. **Chat-mention wire tokens stay Chinese** (`@[漏洞#1 …]`) because they are a persisted
   wire format shared with `server/chat_mentions.go`; the visible category is localized. Listed
   in SPEC §3.5.1 together with the other functional sentinels (`[模型]`, the guard control
   marker, provider quota regexes, ICP detection, rune-limit fixtures).
5. **Logs are English-only, user-facing API text is en+es.** Operator diagnostics are not
   part of the bilingual UI (SPEC §3.4), which is why the Spanish catalogue covers API
   messages and notifications, not console log lines — except the `logT` templates that
   already had a Spanish entry, which were re-keyed rather than dropped.
6. **Accepted risks, not silently fixed** (see `docs/security-review.md`): the agent shell has
   no sandbox (H-1) and scope is a prompt-level construct, the shipped image exposes an
   intercepting proxy (H-3, bind narrowed + opt-in), workspace containment is lexical (H-4),
   and auto-update trusts a checksum delivered over the same channel (L-3, documented).
   Fixing H-1/H-4 is a design change, not a translation task.
7. **DB integration tests were not run here** (no PostgreSQL in this environment, same
   limitation as the CI `go` job without a database); the CI `db` job remains the gate for
   those paths.

