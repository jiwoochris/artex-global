# SPEC — ARTEX Global Edition (specification of record)

This is the entry point for spec-driven development on this repository. Every
change starts here: state the requirement, find the owning specification section,
update the spec, then implement with a test that proves the behavior.

| Document | Purpose |
| --- | --- |
| `SPEC.md` (this file) | Normative specification: scope, contracts, requirements, quality gates |
| [`PLAN.md`](PLAN.md) | Execution plan for the current change set, wave by wave, with the test for every change |
| [`docs/architecture.md`](docs/architecture.md) | Architecture index: system overview and a map of the eight per-area documents under [`docs/architecture/`](docs/architecture/) (file:line evidence) |
| [`README.md`](README.md) | Orientation, quick start, configuration |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | Contribution workflow |
| [`SECURITY.md`](SECURITY.md) | Vulnerability reporting |
| [`docs/defense.md`](docs/defense.md) | Defence/detection material for autonomous AI attacks |
| [`docs/docker.md`](docs/docker.md) | The single Docker stack: posture, verification, opt-in overlays, limitations |
| [`docs/finding-traffic-evidence.md`](docs/finding-traffic-evidence.md) | How a finding binds to captured traffic evidence |
| [`CHANGELOG.md`](CHANGELOG.md) | Released behavior history |
| [`MAINTAINING.md`](MAINTAINING.md) | Maintainer runbook (release, i18n, CI) |

Repository-wide language rule: **all documents, comments, skill files and
identifiers are English.** The only non-English content allowed anywhere is
(a) the Spanish UI message catalogue `web/messages/es.json`, (b) the Spanish
values in the backend message catalogue `server/errmsg_catalog.go`, and (c) test
data that deliberately exercises non-ASCII input. See §3.

---

## 1. Scope

ARTEX is an autonomous penetration-testing system driven by LLM multi-agents: a
task run decomposes goals, executes real tooling, and accumulates discovered
assets, intents and vulnerabilities into a graph. Distribution shape:

- **One Go binary** (`cmd/artex`) that serves the HTTP API and the embedded
  Next.js static export (build tag `embedui`).
- **PostgreSQL** holds all persistent state (40 tables, `db/schema.sql` + migrations).
- **A Next.js 16 / React 19 frontend** (`web/`) exported statically and embedded.

Normative requirements in this document use MUST / SHOULD / MAY in the RFC 2119
sense.

### 1.1 Non-goals

- Multi-tenant SaaS operation, billing, or a hosted control plane.
- Any behavior against systems the operator is not explicitly authorized to test.
  Authorization is a hard precondition, not a feature (see §7.1).
- Running ARTEX natively on Windows or macOS (REQ-PLAT-1). Attacking and defending
  Windows/macOS *targets* stays in scope and is not a non-goal.

---

## 2. Product-level specifications

### 2.1 Authorization boundary (REQ-SEC-1)

The product MUST only be used against targets the operator owns or has written
authorization to test. This is stated in `README.md` and enforced socially, not
technically; no code path may advertise or automate testing outside a declared
task scope, and scope constraints (`task_scope`, `task_constraints`) MUST be
honored by the agent tool layer.

### 2.2 Configuration resolution (REQ-CFG-1)

Precedence MUST be: **environment variable > config file > built-in default.**

| Setting | Environment | Config file key |
| --- | --- | --- |
| Config path | `ARTEX_CONFIG` | — |
| Database DSN | `ARTEX_PG_DSN` | `database.dsn` (or `host`/`user`/`dbname`/…) |
| Skill directory | `ARTEX_SKILL_DIR` | `skill_dir` |
| Output language | `ARTEX_LANG` | `language` |

Startup aborts with an actionable message when the database configuration is
missing or unreachable. Startup messages are English and MUST NOT be translated
(operator surface, §3.4).

### 2.3 Task lifecycle (REQ-TASK-1)

A task MUST be creatable from a template (`task_templates`) or ad hoc, MUST carry
a scope, MUST be cancellable, and MUST reach exactly one terminal state that is
recorded (`terminalreason.go`). Cancellation MUST propagate to every child agent
and tool invocation. Archive/export of a task MUST be reproducible from stored
data only.

### 2.4 Agent engine (REQ-AGENT-1)

- Every LLM call MUST be routed through the pool/recording layer (`llmpool/`,
  `llmrec/`) so token usage is metered per task and per model profile.
- Tool execution MUST be recorded (`tool_usage`) and MUST respect scope.
- Agent prompts are versioned in the database (`agent_prompts`) with the code
  default as fallback; a seeded prompt MUST NOT overwrite an edited one.
- The human-in-the-loop chat MUST be able to inject guidance into a running task
  without restarting it.

### 2.5 Findings (REQ-FIND-1)

A finding MUST have severity, status, owning task, asset reference and evidence
attachment. The finding lifecycle (open → confirmed/rejected, retest) MUST be
auditable (`finding_retests`, `finding_traffic_bindings`). Export MUST include the
same fields as the UI list.

### 2.6 API contract (REQ-API-1)

- All application endpoints live under `/api/`.
- Every request handler MUST validate input at the boundary and fail closed.
- Error responses MUST use the shared envelope and a user-facing message that is
  looked up in the message catalogue (§3.3).
- Authentication is JWT-based; the setup flow is one-shot and MUST be closed
  after the admin password is set.
- Unknown routes MUST return 404 without leaking internals.

### 2.7 Frontend contract (REQ-UI-1)

- The UI is a static export: **no runtime Node server**. All data comes from
  `/api/*` of the Go binary (or the configured reverse proxy in dev).
- Locale state is client-side (`localStorage` + cookie) so switching language
  re-renders without a rebuild, a reload or a network round trip.
- `<html lang>` MUST track the active locale.
- Every user-visible string MUST come from the message catalogue; no literal UI
  text in components.

---

## 3. Language contract (REQ-I18N-1) — normative

### 3.1 Supported locales

The product supports exactly two locales.

| Locale | Role | Default |
| --- | --- | --- |
| `en` | Source language: every string originates here | Yes |
| `es` | Translation overlay of `en` | No |

`ko`, `zh`, `ja` MUST be rejected: `isLocale()` returns false, the API rejects
them with a 400, `NEXT_PUBLIC_LOCALE` falls back to `en`, and stale
`localStorage`/cookie values fall back to `en`.

### 3.2 Frontend catalogue

- Files: `web/messages/en.json`, `web/messages/es.json`. Both are complete,
  identically keyed trees.
- `web/src/i18n/config.ts` is the single source of truth for the locale list,
  labels, default and the type guard.
- `web/src/i18n/request.ts` and `web/src/i18n/intl-runtime-provider.tsx` MUST
  bundle exactly the two catalogues.
- Both catalogues MUST be checked by `pnpm run check:i18n` (key-set equality,
  baseline `en`) in CI.
- Missing key at runtime: `next-intl` fallback applies; a missing key is a bug
  and MUST be fixed rather than silenced.

### 3.3 Backend catalogue

- Source language is English: user-facing server messages are English literals in
  Go source.
- `server/errmsg_catalog.go` maps the exact English literal → `{"es": "…"}`.
  `"en"` is the identity mapping and is not stored.
- `trMsg` is total: an unknown key returns the input unchanged (never empty).
- Format strings (`%s`, `%d`, `%w`) MUST be translated *before* `Sprintf` so the
  verb count and order stay aligned (`trMsg(template)` at the call site).
- Adding a user-facing Spanish string without a catalogue entry is a defect; the
  catalogue is regenerated from the English literals, never hand-typed.

### 3.4 Surfaces that are English-only by design

Operator-facing output is not part of the bilingual UI and MUST stay English:

- server/agent console logs and log stream (`/api/logs`, `logsink.go`),
- startup/configuration diagnostics,
- code comments, godoc, error strings returned to the *operator* console,
- CLI/scripts output, documentation, skills.

Rationale: they are diagnostic artifacts reviewed by engineers and grep-filtered;
translating them adds a maintenance surface without user value.

### 3.5 Agent output language

`agent/outputlang.go` holds the output-language contract: supported set `{en, es}`,
default `en`, and the directive injected into the agent's output instruction. The
selected language applies to the language the agent uses when writing findings,
summaries, reports and chat replies. Tool commands, payloads, code, URLs, and raw
logs are never translated.

### 3.5.1 Documented non-English sentinels (the only allowed residue)

Some non-English byte sequences are **data contracts**, not prose. They MUST stay
byte-identical because they match rows already stored in PostgreSQL, tokens
produced by other components, or text emitted by third-party APIs. Translating
them would silently break a matcher. The complete allow-list:

| Sentinel | Where | Why it cannot be translated |
| --- | --- | --- |
| `[模型]` | `intercept/intercept.go` decision-source prefix, `server/errmsg_*`, `web/src/components/approval-records.tsx`, `web/src/lib/mock/*`, `db/schema.sql` (`reason LIKE '[模型]%'`) | Stored in `intercept_rules`/audit rows and stripped before display; the SQL `LIKE` and the UI prefix strip must keep matching historical rows |
| `【ARTEX 平台管控·非目标防御】` | `guard/guard.go` control marker, `detections/**` rules, `docs/defense.md` | Written into audit records and detection signatures; defenders match the literal |
| Chat-mention wire tokens (`漏洞`, `资产`, `企业`, `接口`, `IP`, `应用`, `域名`, `子域名`, `服务`) | `web/src/lib/chat-mentions.ts`, `server/chat_mentions.go` | `@[kind#id label]` is a persisted wire format in stored transcripts; the visible category is localized from `mentionTextarea.kind` |
| Provider quota/balance regexes (e.g. `余额`, `未登录`) | `agent/provider.go`, `skills/api-recon/scripts/preload.js` | Must match the Chinese error text returned by upstream provider APIs and target login pages |
| ICP filing detection (`icp\|备案`) | `web/src/lib/company-scope.ts`, `db/company_scope*.go` | Chinese ICP filing numbers are matched in user-scope text |
| Guard control marker in `intercept/` reason text | `db/schema.sql:1076` | Stored-data marker |
| Legacy reporter trigger message (`reporterToolCallMessageV1`, Chinese) | `server/orchestration.go` | Compared with `trigger.tool_call_message` rows written by earlier versions; translating it would stop the trigger from matching existing installations |
| Byte/rune fixtures (`中`, `가`, `文` × N, quoted third-party page titles, CJK filenames in upload tests, non-ASCII search fixtures) | `*_test.go`, `web/src/lib/chat-mentions.test.mjs`, `sidequestion/VALIDATION.md`, `traffic/traffic_store_test.go`, `notify/render_test.go` | The test asserts multi-byte, rune-width or non-ASCII-search handling; ASCII input cannot exercise it |

Anything not on this list must be English. A new non-English literal requires a
new row here and a test that proves why it is needed.

Two enforcement points keep this honest:

- `scripts/check-non-english.py` counts characters in **every non-Latin script**
  (Cyrillic, Greek, Hebrew, Arabic, Devanagari, Bengali, Tamil, Thai, Kana, Han,
  Hangul, Georgian, Armenian) against `scripts/non-english-baseline.json`, and the
  `docs` workflow fails when a file grows past its baseline or a new file appears.
  Spanish diacritics and typographic punctuation are not counted.
- Rule-mode chat commands (`intent …` / `hint …`) are part of the UI surface, so the
  parser accepts exactly the English and Spanish keywords advertised by the help
  text (`intent`/`intención`, `hint`/`pista`). The pre-en/es Korean and Chinese
  keywords were UI input, never a stored format, and are no longer accepted.

### 3.6 Migration rules

1. Every removed language MUST lose its catalogue entries, tests and UI branch —
   not merely be hidden.
2. Tests that previously asserted `ko`/`zh`/`ja` output are reduced to `en`/`es`
   with translated expectations; they MUST NOT be deleted to make the build pass.
3. No non-English string may survive in code comments, docs, skills or UI
   components.

---

## 4. Security requirements (REQ-SEC-*)

| ID | Requirement |
| --- | --- |
| REQ-SEC-1 | Authorized-use boundary documented and respected by scope enforcement |
| REQ-SEC-2 | Passwords hashed with bcrypt cost ≥ 10; ≥ 8 chars, ≤ 72 bytes; rotation supported (`reset-password.sh`) |
| REQ-SEC-3 | JWT secret never stored inside the workspace; key file moved outside discoverable paths, 0600 |
| REQ-SEC-4 | Login and setup endpoints rate-limited per client; failures counted, not logged with credentials |
| REQ-SEC-5 | Every file path taken from a request (skill zip, upload, archive, traffic evidence) MUST be validated against traversal (`..`, absolute paths, symlinks) before use |
| REQ-SEC-6 | Zip/extract operations MUST enforce entry-count and size limits (zip bomb guard) |
| REQ-SEC-7 | SQL MUST be parameterized; no string-built queries |
| REQ-SEC-8 | No secrets, tokens or credentials in logs, error responses, or the audit log |
| REQ-SEC-9 | Outbound requests made on behalf of the agent MUST respect configured allow/deny and MUST NOT be used to reach internal metadata endpoints unless explicitly scoped |
| REQ-SEC-10 | Container runs as non-root, read-only root filesystem where supported, no privileged capabilities |
| REQ-SEC-11 | Dependencies MUST be free of known reachable vulnerabilities at release time (`govulncheck`, `pnpm audit`) |
| REQ-SEC-12 | Auth-relevant responses MUST NOT differ in timing/shape in a way that discloses whether a user exists |

---

## 5. Dependency and image policy (REQ-DEP-*)

| ID | Requirement |
| --- | --- |
| REQ-DEP-1 | Go modules pinned in `go.mod`/`go.sum`; `go build ./... && go vet ./... && go test ./...` green |
| REQ-DEP-2 | Minor/patch upgrades applied continuously; majors only with an explicit API review recorded in `PLAN.md` |
| REQ-DEP-3 | Node packages managed by **pnpm only**: `web/pnpm-lock.yaml` committed, `packageManager` pinned, CI/Docker use `--frozen-lockfile` |
| REQ-DEP-4 | No `npm`/`yarn` invocation anywhere in scripts, CI or Dockerfiles |
| REQ-DEP-5 | Container base image is `python:3.12-slim-trixie` (Debian Trixie); build stages pinned; no `latest` for the runtime base |
| REQ-DEP-6 | New runtime dependencies require a stated reason and a license check (AGPL-3.0 compatible) |

### 5.1 Platform support (REQ-PLAT-1)

| ID | Requirement |
| --- | --- |
| REQ-PLAT-1 | Release artifacts are Linux only (`linux/amd64` and `linux/arm64`); no Windows or macOS native build, supervisor script or release asset is produced. |

The decision rule behind it: anything about *running* ARTEX outside Linux is out of scope;
anything about *attacking or defending* Windows/macOS targets is in scope and must not be removed.
The container image is the supported way to run ARTEX on a Windows or macOS host, because Docker
there is already a Linux VM.

---

## 6. Architecture

`docs/architecture.md` is the entry point of the authoritative implementation map.
It carries the system overview and a map to one document per area, all under
`docs/architecture/`: startup and runtime, backend packages, the agent engine, the
data model, the HTTP surface, the frontend, cross-cutting services, and the
integration table between frontend, backend, database and agent. Section numbers are
stable across the split (`§4.5` is the tool catalog in every document), so a
cross-reference keeps its meaning.

This specification defines *what* must be true; those documents define *where* and
*how* it is implemented.

Module index (responsibility summary; details and file references in
[`docs/architecture/backend-packages.md`](docs/architecture/backend-packages.md)):

| Package | Responsibility |
| --- | --- |
| `cmd/artex` | Process entry point: config → DB → server → listen |
| `config` | Configuration resolution and validation (`ARTEX_*` > file > default) |
| `db` | PostgreSQL schema, migrations, queries, seed data |
| `server` | HTTP API, auth, orchestration, SSE/websocket broadcast, embedded UI |
| `agent` | Agent engine: goals, planner, main agent, workers, prompts, tool catalog |
| `llmpool`, `llmrec` | LLM provider pool, retry/health, usage metering |
| `intercept` | Traffic interception rules, judge model, pending approvals |
| `traffic` | Traffic capture/storage and host inventory |
| `evidence` | Evidence snapshots bound to findings |
| `notify` | Notification channels (webhook, Telegram, email, DingTalk/Feishu/WeCom) |
| `report` | Report generation from task data |
| `guard` | Scope/guard validation |
| `selfupdate` | In-place binary update |
| `mcphttp`, `enrich`, `llmrec`, `sidequestion` | MCP client, enrichment, side questions |
| `skills/` | Distributed skill definitions (English) |
| `web/` | Next.js static UI (App Router, en/es) |

---

## 7. Quality gates (REQ-QA-*)

Every change MUST pass, in this order:

```bash
export PATH=/tmp/pi/go/bin:$PATH     # local container only; CI uses setup-go
go build ./... && go vet ./... && go test ./... -count=1
cd web && pnpm install --frozen-lockfile && pnpm exec tsc --noEmit \
  && pnpm test --run && pnpm run build:static && pnpm run check:i18n \
  && pnpm run lint
python3 scripts/check-doc-links.py
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

| ID | Requirement |
| --- | --- |
| REQ-QA-1 | A functional change without a test is not done |
| REQ-QA-2 | Tests MUST assert the new contract, not merely "no panic" |
| REQ-QA-3 | A deleted language MUST have a test proving it is rejected |
| REQ-QA-4 | Translation-only changes MUST NOT alter test expectations except language |
| REQ-QA-5 | No skipped/disabled test may be added to make a gate pass |

---

## 8. SDD workflow for this repository

1. **Specify** — edit the relevant section here (and `PLAN.md` for execution
   detail) before writing code. Requirements get an ID (`REQ-<AREA>-<n>`).
2. **Design** — update `docs/architecture/` if the structure changes; record
   alternatives in the PR description.
3. **Test first** — add/extend the test named in the traceability table below.
4. **Implement** — smallest change that satisfies the spec; keep the package's
   existing patterns.
5. **Verify** — run the gate in §7 and paste the output in the PR.
6. **Trace** — keep the table below current.

### 8.1 Traceability

| Requirement | Owner code | Test | Plan wave |
| --- | --- | --- | --- |
| REQ-I18N-1 locales = {en, es} | `web/src/i18n/config.ts`, `agent/outputlang.go` | `web/src/i18n/config.test.ts`, `agent/prompt_test.go` | W2, W3a |
| REQ-I18N-1 removed locales rejected | `web/src/i18n/config.ts`, `server/server.go` | `config.test.ts`, `server/errmsg_i18n_test.go` | W2, W3a |
| REQ-I18N-1 catalogues in sync | `web/messages/*.json` | `web/src/scripts/i18n-keys.test.ts`, `check:i18n` | W2 |
| REQ-I18N-1 backend catalogue total | `server/errmsg_catalog.go`, `errmsg_i18n.go` | `server/errmsg_i18n_test.go` | W3a/W3b |
| REQ-CFG-1 precedence | `config/config.go` | `config/config_localized_test.go` | W3a |
| REQ-TASK-1 terminal states | `agent/terminalreason.go` | `agent/terminalreason_test.go` | unchanged |
| REQ-AGENT-1 usage metering | `llmpool/`, `llmrec/` | `llmpool/pool_localized_test.go`, `server/engine_llm_calls_test.go` | W3b |
| REQ-FIND-1 finding lifecycle | `server/finding_workflow.go` | `server/finding_workflow_test.go` | unchanged |
| REQ-API-1 error envelope | `server/errmsg_i18n.go` | `server/errmsg_i18n_test.go` | W3a |
| REQ-UI-1 static export, no literals | `web/src/**` | `pnpm run build:static`, `check:i18n` | W2 |
| REQ-SEC-2..12 | see §4 | one test per fix | W5 |
| REQ-DEP-1..6 | `go.mod`, `web/package.json`, `Dockerfile*` | `go test ./...`, `pnpm test --run`, Dockerfile review | W0a/W0b |
| REQ-PLAT-1 Linux-only artifacts | `.github/workflows/release.yml`, `build.sh`, `start.sh` | `deploy/platform_test.go` | W6 |
| REQ-QA-1..5 | §7 | the gate itself | all |

### 8.2 Acceptance criteria for the current change set

- [ ] `LOCALES` is exactly `["en","es"]`; `ko`, `zh`, `ja` are gone from code, tests, messages and docs.
- [ ] Every user-visible backend message has an `es` entry; every UI string exists in both catalogues.
- [ ] Zero Korean/Chinese characters outside allowed Spanish catalogues and deliberate non-ASCII test fixtures.
- [ ] `docs/architecture.md` (index) and `docs/architecture/*.md` map every package with file:line evidence; `SPEC.md`, `PLAN.md` and the docs cross-link.
- [ ] Both Dockerfiles use `python:3.12-slim-trixie` and pnpm.
- [ ] `go.mod` upgraded with a recorded delta and a clean `govulncheck` (or documented residual risk).
- [ ] `web/pnpm-lock.yaml` committed, no `npm` invocation left.
- [ ] Security review completed with a fix + test per finding.
- [ ] Full gate (§7) green, output attached to the hand-off.
