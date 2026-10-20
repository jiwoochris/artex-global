<div align="center">

# ScopeWeaver

An LLM multi-agent autonomous penetration-testing system (Go backend + Next.js frontend)

English · [한국어](README.ko.md)

</div>

---

> **About this project.** ScopeWeaver is a derivative of [Autumn-27/ARTEX](https://github.com/Autumn-27/ARTEX),
> rebranded and translated into English and Korean. It is built from upstream commit `160fe13`.

Modification date: **2026-10-02**. See [provenance and changes](docs/PROVENANCE.md); this is not a release tag.

[Verification status and known limitations](docs/VERIFICATION.md).

> The code and architecture originate in ARTEX. This standalone derivative changes the product
> name, English/Korean interface and messages, documentation, and distribution targets. See [Provenance and changes](#provenance-and-changes)
> for the full list of what differs from upstream, and [License and disclaimer](#license-and-disclaimer)
> for the terms, which are unchanged (AGPL-3.0).
>
> **Authorization.** This is a security-testing tool. Use it only against systems you own or are
> explicitly authorized to test. The upstream usage restrictions still apply — read them below.

---

## Language

English is the default. Select English, Korean or Simplified Chinese (`zh-Hans`) in the interface.
Bare `zh`, `zh-CN` and `zh-SG` resolve to Simplified Chinese; explicit Hans tags keep their script even
with a Traditional region. Undeclared Traditional tags (`zh-Hant`, `zh-TW`, `zh-HK`, `zh-MO`) fall
through to the next preference rather than select Simplified Chinese. See the
[contributor locale guide](web/src/i18n/README.md). The server's durable default is
`language` in system settings; `SCOPEWEAVER_LANGUAGE` (or `ARTEX_LANGUAGE`) supplies the environment
fallback. Operating-system `LANG` does not select the application language.

Requests prefer `lang`, then supported `Accept-Language` preferences, then the `scopeweaver_locale`
cookie, then the server default. New HTTP-created tasks retain their language across queues and
restarts; older tasks without saved language use the server default. Built-in agent guidance and
output instructions use the run language; user-edited templates and stored evidence remain intact.
Report/CSV/ZIP labels use the export request's language. [Full documentation](docs/README.md) and
[Korean references](docs/ko/README.md) include the bundled skill guidance.

Standalone penetration-testing conversations save confirmed findings directly to the Findings
page under the unassigned-task group. Each conversation keeps its own finding history across
turns without starting a task. Missing or invalid prompts for Auto, Penetration testing, Report
writer, and Finding retest fall back to their role defaults; render failures appear in backend logs.

## Screenshots

These captures show ScopeWeaver's English interface with the included fictional demo data.
They do not represent scans of live targets. See the [Korean screenshots](README.ko.md#스크린샷)
and [mobile view](screenshots/en/mobile.png).

| Dashboard | Tasks |
| :---: | :---: |
| ![English dashboard](screenshots/en/dashboard.png) | ![English task list](screenshots/en/tasks.png) |
| Findings | Settings |
| ![English findings](screenshots/en/findings.png) | ![English settings](screenshots/en/settings.png) |

Original Chinese screenshots remain in [the screenshots directory](screenshots), credited to
[upstream ARTEX](https://github.com/Autumn-27/ARTEX/tree/160fe13c243be361eeac5408c40a96e824fa842c/screenshots).

---

## Approval records

The global "Approval records" view, the in-task "Interception approval" panel, and the approval
cards shown inside conversations all expand to show full detail. The presentation follows the
[approval-detail component from AegisHook](https://github.com/RuoJi6/AegisHook/blob/main/web/src/components/CallDetail.vue),
reusing the upstream component and theme.

## Asset sync (ScopeSentry)

You can sync asset data directly from [ScopeSentry](https://github.com/Autumn-27/ScopeSentry),
which saves you from collecting the same data twice:

- On the **Asset sync** page, enter your ScopeSentry address and API key to connect the data source.
- Choose what to sync by **project** or by **task**, and pick the asset types (domain / subdomain /
  IP / port / site / endpoint …).
- Import in one click. Assets are merged into the company asset scope and go straight into the
  asset graph for agents to explore.

---

## Installation

Run only one active ScopeWeaver server per PostgreSQL database. Its data directory must also
have a single owner. Stop the previous server before starting a replacement; concurrent replicas
sharing either resource are unsupported. Startup recovery treats unfinished chat work as
interrupted and reconciles unpublished transcript files before resuming queues.

### GLM and other model providers

The **LLM → New** form includes GLM-5.3 configuration templates for Z.ai's General API and a
separately labeled Coding Plan reference. Add your own API key; template selection does not save,
activate, or contact a provider. Coding Plan use is restricted to officially supported tools, and
ScopeWeaver is not listed. See [provider setup and support limits](docs/llm-providers.md).
To reduce cloud token use, see [Controlling token use](docs/llm-providers.md#controlling-token-use).

Optional **native Ollama** profiles run without an API key. Three templates cover Ollama on the same
machine, on the host from Docker, and the opt-in `docker compose --profile local-llm` service. Selecting one reads installed models and prefills one, preferring declared tool support;
it does not download or run a model. They send a 32K context (`num_ctx` 32,768), one request at a time and a 30-minute keep-alive. An opt-in
**local-only LLM network** setting (off by default) permits only loopback and explicitly trusted inference CIDRs; LAN and Docker addresses now require an allowlist entry. It also blocks all agent web search and update checks/downloads, while local version status and rollback remain available. Target tools keep network access; host/remote Ollama must separately use `OLLAMA_NO_CLOUD=1`. Outside strict mode, cloud formats, custom URLs and proxies keep working. Native requests give queued judge/main-agent/side-question calls the next available slot, with a 90-second/ask native judge default and bounded context safeguards. See
[native Ollama](docs/llm-providers.md#native-ollama-local-inference).

> Requires a **PostgreSQL** database. Exploration needs an **LLM** configured
> (`ANTHROPIC_API_KEY` or `OPENAI_API_KEY`, a keyless Ollama profile, or set it in the UI).

### Option 1: one-click install script (recommended)

```bash
git clone https://github.com/cskwork/scopeweaver.git
cd scopeweaver
./install.sh
```

The script detects (and optionally installs) Docker, then lets you choose **① all-in-Docker** or
**② build and run locally**:

- **① All-in-Docker**: enter one Postgres password (press Enter for a random one) → it writes
  `.env` → `docker compose up -d`.
- **② Local**: pick a database (connect to an existing one, or start one with Docker) → it
  generates `config.json` → `go` compiles a single binary with the frontend embedded → start.

Once it is up, open **http://localhost:8787**. The first visit lands on `/setup`, which needs the
**setup token** before it will accept the admin password.

```bash
docker compose logs scopeweaver | grep "Setup token:"   # Docker
# local build: the token is in the console output where the backend was started
```

Paste that token into the `/setup` form, set the password, then sign in as `ARTEX`. No API response
ever returns the token — it exists only in that log line and in what you submit yourself. To skip the
screen, set `ARTEX_ADMIN_PASSWORD` in `.env` before the first start. See
[First-run authentication](#first-run-authentication) for unattended setups and `ARTEX_SETUP_TOKEN`.

The port is published to `127.0.0.1` only, so nothing else on your network can reach the setup
screen. Set `ARTEX_BIND=0.0.0.0` in `.env` to serve other hosts.

> The scripts speak English by default. For Korean prompts, set `SCOPEWEAVER_LANGUAGE=ko` (or
> `ARTEX_LANGUAGE=ko`) before running, for example `SCOPEWEAVER_LANGUAGE=ko ./install.sh`.

### Option 2: Docker Compose (manual)

```bash
git clone https://github.com/cskwork/scopeweaver.git
cd scopeweaver
cp .env.example .env          # set POSTGRES_PASSWORD, optionally ANTHROPIC_API_KEY
docker compose up -d --build  # builds the scopeweaver image locally + postgres
# → http://localhost:8787
```

> The default compose file **builds the image locally** from this source. To use a published
> release image from `ghcr.io/cskwork/scopeweaver`, explicitly select its version in compose.

The image bundles common tools (ripgrep/curl/vim/npm/nmap…); `./skills` and `./data` are
bind-mounted so they persist.

A remote MCP can use `http` (Streamable HTTP) or `sse` (legacy SSE) in system settings. Legacy SSE
servers usually open the event stream with `GET /sse` and then receive JSON-RPC requests on the
`/message?sessionId=...` the server returns; set the URL to `/sse` and the header to
`Authorization=Bearer <token>`.

### Option 3: download a prebuilt binary (Releases)

> Download a platform archive from [Releases](https://github.com/cskwork/scopeweaver/releases).
> The archive for each platform is
> `scopeweaver-<version>-<os>-<arch>.zip`, unpacking to `scopeweaver` + `start.sh`
> (`start.bat` on Windows) + `skills/` + `config.example.json`:

The archive also includes `adapters/agent/`; see the [agent setup instructions](adapters/agent/README.md).

```bash
cp config.example.json config.json   # fill in the database connection
./start.sh                            # → http://localhost:8787
```

> Start with `start.sh` / `start.bat`, not `./scopeweaver` directly. It is a supervisor: after the
> program exits it decides, from the exit code, whether to relaunch, and the
> [in-app one-click update](#option-1-in-app-one-click-update-recommended) relies on it to swap in
> the new build. Running `./scopeweaver` directly means an update will not be relaunched.
> To run in the background: `nohup ./start.sh >scopeweaver.log 2>&1 &`.

### Option 4: build a single binary from source

```bash
# 1) export the frontend as static files
cd web && npm ci && npm run build:static && cd ..
# 2) copy it into the embed directory
mkdir -p server/webui/dist
cp -R web/out/. server/webui/dist/
# 3) build (the embedui tag embeds the frontend)
CGO_ENABLED=0 go build -tags embedui -o scopeweaver ./cmd/artex
./start.sh
```

> The build source path stays `./cmd/artex` and the Go module stays `github.com/Autumn-27/artex`
> for compatibility with upstream. Only the output binary is named `scopeweaver`.

### Option 5: build cross-platform release archives

`build.sh` builds and embeds the frontend, strips debug info with the Go linker, and zips each
release. Release mode builds Linux amd64/arm64, macOS amd64/arm64, and Windows amd64 by default:

Archives include `THIRD_PARTY_NOTICES.txt`; container images retain the same notice at
`/app/THIRD_PARTY_NOTICES.txt` for the vendored shadcn stylesheet.

```bash
./build.sh --release
# output: dist/scopeweaver-0.3.3-*.zip
```

UPX-packed self-extracting binaries can clash with some Linux kernels, virtualization, or security
policies, so UPX is off by default. Set custom targets with `ARTEX_TARGETS`, and pass `--upx` explicitly when you have confirmed the target is compatible:

```bash
ARTEX_TARGETS=linux/amd64,windows/amd64 ./build.sh --release
./build.sh --target linux/amd64 --upx
```

### Reverse proxy deployments

Production builds serve the UI, API, and live SSE streams from the same origin.
You can expose HTTPS on port 443 and keep the backend port private. Disable proxy
buffering and allow long-lived connections so live events reach the browser:

```nginx
location / {
    proxy_pass http://127.0.0.1:8787;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_buffering off;
    proxy_cache off;
    proxy_read_timeout 3600s;
}
```

Set `NEXT_PUBLIC_SSE_BASE` at build time only when SSE uses a different origin.
Changing it at container runtime does not change the exported frontend. During
`next dev`, SSE still connects directly to port 8787 to bypass Next.js buffering;
an explicit empty `NEXT_PUBLIC_SSE_BASE` selects the same origin.

---

## Updating

> Updates swap the program only; your data stays put. The Postgres volume `pgdata`, `./data`
> (jwt.key / SQLite / …), and `./skills` are all preserved. **Database migrations run on their
> own** — `scopeweaver` re-runs `schema.sql` idempotently on every start (including `ADD COLUMN` /
> `CREATE INDEX IF NOT EXISTS`), so "restart is migrate." Still, back up `./data` and the database
> before upgrading.

### Option 1: in-app one-click update (recommended)

On the **System configuration** page (sidebar "System configuration" → `/system/settings`), the
**Version and updates** card checks for and installs new versions without logging into the server.

After you click "Update": it downloads the release for your platform → checks it against the
release's `SHA256SUMS` → smoke-tests the new binary with `-h` → stages it as `scopeweaver.new` →
the program exits and `start.sh` / `start.bat` relaunches it to finish the swap. The page waits for
the new version to come up and refreshes.

- **A failed update leaves no broken program**: if verification or the smoke test fails, the staged
  file is discarded and the current version keeps running. If a swapped-in version fails to start
  three times in a row, it rolls back to `scopeweaver.old` automatically (the failed one is kept as
  `scopeweaver.failed` for inspection).
- **Roll back anytime**: the previous version is kept as `scopeweaver.old`, and the card has a
  "Roll back to previous version" button. Note that the database schema does not roll back.
- **Updating interrupts running tasks** — an update is a restart, so do it when idle.
- **Development builds get no updates**: this is disabled when the version is `dev` or a
  `git describe` string with a suffix, so a release does not overwrite a locally built debug binary.
- **Under Docker, only the program changes, not the image**: the playwright / nmap toolchains in the
  image do not upgrade along with it, and rebuilding the container with `docker compose up -d` reverts
  to the versions baked into the image. To upgrade the image too, use
  `docker compose pull scopeweaver && docker compose up -d scopeweaver` (once a published image exists;
  otherwise `docker compose up -d --build scopeweaver`).
- If reaching GitHub needs a proxy, configure the **global proxy** on the same page and the update
  path uses it. Updates download only from GitHub domains and force HTTPS.

### Option 2: one-click update script

```bash
cd scopeweaver
./update.sh
```

The script optionally runs `git pull` first, then lets you choose **① Docker update** or
**② local build update** (matching `install.sh`):

- **① Docker**: rebuild the current checkout with `docker compose build scopeweaver`, then
  recreate the service with `docker compose up -d scopeweaver`.
- **② Local**: rebuild the frontend static output → recompile `./scopeweaver` (restart the process
  to apply).

### Option 3: Docker Compose (manual)

```bash
cd scopeweaver
git pull                       # update compose / scripts (optional)
# To pin a source version, check out a reviewed tag or commit before building.
docker compose up -d --build scopeweaver   # rebuild from source and restart → auto-migrate schema
docker image prune -f          # clean up old images (optional)
```

> Once a published image exists and compose explicitly selects it, replace the build step with
> `docker compose pull scopeweaver && docker compose up -d scopeweaver`.

### Option 4: prebuilt binary (Releases)

Download the new `scopeweaver-<version>-<os>-<arch>.zip`, stop the old process,
overwrite `scopeweaver` and `skills/` (keep your `config.json` and `data/`), and restart:

```bash
cp -r <unpacked>/skills ./ && cp <unpacked>/scopeweaver ./
./start.sh
```

### Option 5: build from source

```bash
git pull
cd web && npm ci && npm run build:static && cd ..
cp -r web/out server/webui/dist
CGO_ENABLED=0 go build -tags embedui -o scopeweaver ./cmd/artex
# restart ./start.sh
```

---

## Configuration

### Coding-agent integration

Claude Code, Codex and Pi can create tasks, read progress, coverage and findings through the
[agent adapter](adapters/agent/README.md). Claude Code/Codex use local stdio MCP; Pi uses a native
extension. Reads are enabled by default, with task creation and pause/resume explicitly enabled
through configuration. The adapter uses the existing authenticated API and keeps ScopeWeaver's
internal agents and model configuration intact.

**Database** (`config.json`, or override with the environment variable `ARTEX_PG_DSN`):

```json
{
  "database": {
    "host": "127.0.0.1", "port": 5432,
    "user": "artex", "password": "yourpass",
    "dbname": "artex", "sslmode": "disable"
  }
}
```

> The configuration and environment keys keep the `ARTEX_*` prefix and the `artex` database defaults
> for compatibility with upstream. Scripts also accept the `SCOPEWEAVER_*` aliases where noted.

### First-run authentication

The admin password is set once, on `/setup`, and must be at least 8
characters (72 bytes maximum). After that you sign in as `ARTEX`; `POST /api/auth/change-password`
invalidates every existing session and returns a fresh token.

| Variable | Default | Purpose |
|---|---|---|
| `ARTEX_SETUP_TOKEN` | generated | Pins the first-run token instead of generating one. Its value is never written to any log. |
| `ARTEX_ADMIN_PASSWORD` | unset | Initializes the password at boot, skipping `/setup`. Only read while no password exists. |
| `ARTEX_CORS_ORIGINS` | `http://localhost:5173`, `http://127.0.0.1:5173` | Comma-separated browser origins allowed to call the API. The embedded UI is same-origin. `*` restores the permissive legacy behaviour. |

Without `ARTEX_SETUP_TOKEN`, the server prints a random 32-character token once per start in a
`[auth]` log line; `GET /api/auth/status` reports only `initialized` and `setup_token_required`. Read
it from `docker compose logs scopeweaver` (Docker) or the backend console (local build). Set
`ARTEX_ADMIN_PASSWORD` for unattended deploys, or pin `ARTEX_SETUP_TOKEN` when the token must be
known ahead of first boot. If the password state cannot be read at startup, initialization is skipped
rather than allowed to proceed.

Changing the password — including `reset-password.sh`, which writes the hash directly — invalidates
existing sessions within about two seconds, with no restart.

**LLM**: `export ANTHROPIC_API_KEY=sk-...` (or `OPENAI_API_KEY`), or set it on the UI's "LLM
configuration" page. Optional: `ARTEX_LLM_PROVIDER` / `ARTEX_LLM_MODEL` / `ARTEX_LLM_BASE_URL` /
`ARTEX_LLM_PROXY`. Each `SCOPEWEAVER_LLM_*` alias overrides its `ARTEX_LLM_*` counterpart; Ollama
also accepts `CONTEXT_WINDOW_K`, `MAX_CONCURRENT`, `KEEP_ALIVE` and `MAX_TOKENS` suffixes
([full list](docs/llm-providers.md#environment-and-configuration)).

**Concurrency**: the number of work agents per task is set in "System settings" (default 3).

**Common flags**: `./start.sh -addr 127.0.0.1:8787 -proxy 127.0.0.1:8788` (`-addr` is frontend + API,
`-proxy` is the traffic-capture proxy; both default to loopback, pass `-addr :8787` to serve other
hosts). The start script passes flags straight through to `scopeweaver`.

---

## Development

Contributions are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for requirements, checks, and how pull requests are reviewed and merged.

### Manual vulnerability retest

The task-detail "Retest" tab paginates the task's vulnerabilities, shows each one's past
conclusions and evidence, and lets you start a retest by hand. Once started it keeps the current
tab, shows a spinner and "Retesting"; when a fix is confirmed it updates the vulnerability status.

Click "Retest" on a row in the vulnerability list, or "Start retest" in the vulnerability detail's
"Retest" area, and fill in the optional fix version, test conditions, or limits. The system creates
a separate retest agent session and keeps the current page. The flat, grouped-by-task, and asset
views all offer this entry point; a running retest shows a spinner and "Retesting," and you click in
to view the session. When it ends it returns to "Retest." A retest does not restart the original
scan task. Conclusions are "Still reproducible," "Fixed," or "Cannot confirm," and each conclusion,
its evidence, and the session link are saved in the vulnerability detail.

The newer backend pre-creates an editable "vulnerability retest" (`retester`) agent on first start;
you can configure its prompt, LLM, run budget, and tools in agent management. It uses its bound LLM
by default, or the globally active configuration if none is bound. When a retest session completes
successfully with a "Fixed" conclusion, the system sets the vulnerability disposition to "Fixed"
automatically; running, failed, stopped, or other conclusions keep the original status. The original
evidence and report are always kept. You can also set "Fixed" by hand from the status dropdown.

This version's history is viewed through the vulnerability detail and the session; it is not yet in
the vulnerability report export or the task archive, and it is not auto-linked to a traffic capture.
Demo mode only produces clearly labeled simulated records and does not touch real targets.

### Local run and tests

```bash
./dev.sh    # backend(:8787) + traffic proxy(:8788) + frontend next dev(:5173) → http://localhost:5173
```

- Backend: `go run ./cmd/artex` (without `-tags embedui` the frontend is not embedded)
- Frontend: `cd web && npm run dev` (`/api` is reverse-proxied to the backend, with hot reload)
- Tests: `go test ./...`
- Mock preview (no backend): `cd web && NEXT_PUBLIC_MOCK=1 npm run dev`

---

## Architecture

ScopeWeaver (ARTEX under the hood) is an **LLM multi-agent autonomous penetration system**: a single
Go backend (with the Next.js frontend embedded) plus PostgreSQL. Agent capabilities come from the
[`norma`](https://github.com/Autumn-27/norma) SDK (`agentcore` / `tool` / `permission` / `harness` /
`memory` / `transcript`). The core is a **two-graph architecture**, with two autonomy mechanisms
built around it: **process-level information exchange between workers** and a **planner's
multi-round shared todolist that keeps the attack chain stable**.

### Layers

```mermaid
flowchart TB
  subgraph FE["Frontend Next.js (embedded in the single binary via go:embed)"]
    UI["Dashboard · Tasks · Assets · Coverage graph · Traffic · Workspace · System config"]
  end
  subgraph SRV["server (Go net/http)"]
    API["REST /api/*  JWT auth  SSE"]
    ENG["engine scheduling loop"]
    MGR["Manager  task/engine/store lifecycle"]
  end
  subgraph AG["agent (norma SDK)"]
    GO["goals  goal decomposition + scope extraction"]
    PL["planner  (the only intent generator)"]
    WK["worker  executor ×N"]
    MA["mainagent  human-in-the-loop"]
  end
  subgraph DB["PostgreSQL"]
    AGRAPH["asset graph  assets / companies / task_scope"]
    EGRAPH["exploration graph  exploration_nodes / anchors / activity"]
  end
  subgraph SUB["Supporting subsystems"]
    PROXY["traffic-recording proxy  MITM + CA trail"]
    GUARD["guard / intercept  tool approval gate"]
    ENR["enrich  DNS / HTTP async completion"]
    EXT["MCP · skills · memory · report"]
  end

  UI -->|HTTP| API
  API --> MGR --> ENG
  ENG --> PL
  ENG --> WK
  API --> MA
  API --> GO
  PL --> DB
  WK --> DB
  MA --> DB
  GO --> DB
  WK -->|"Bash / HTTP fully traced"| PROXY
  WK --> GUARD
  WK --> ENR
  PL -.-> EXT
  WK -.-> EXT
  MA -.-> EXT
```

| Layer | Responsibility |
| --- | --- |
| **Frontend** | Next.js static export, embedded in the single binary with `go:embed`; visualizes tasks/assets/exploration/coverage and the human-in-the-loop chat |
| **server** | `net/http` routing + JWT auth + SSE; `Manager` owns the lifecycle of tasks, engines, and DB stores |
| **engine** | one `plannerLoop` + N worker goroutines per task; intent claiming, timeout/pause/drain |
| **agent** | goals / planner / worker / mainagent; the `ToolSet` exposes the two graphs as LLM tools |
| **db** | Postgres backing (pgx) for both graphs; schema is embedded via `go:embed` and created idempotently on every start |
| **support** | recording MITM proxy, approval gate, async completion, MCP/skills/memory/report |

### Two-graph architecture: exploration graph + asset graph

The system splits "**what the target is**" from "**how far it has been tested**" into two
independent graphs that connect through anchors:

- **Asset graph (global, shared)**: the single source of truth for assets across tasks. Nodes are
  `root_domain / subdomain / ip / service / app / endpoint`, each owned by a company. The
  domain→subdomain→service→endpoint parent-child relationships and dedup keys are all computed by
  the program; agents only submit raw observations.
- **Exploration graph (per task)**: the "thinking and progress" of one task. Nodes are
  `goal / intent / fact / finding / hint`, connected by `spawns / derived_from / yields / proves`
  edges into a **lineage chain** that answers "which direction derived from which facts, and what it
  produced."
- **The two graphs connect through anchors**: `exploration_anchors(node_id, asset_id)` anchors an
  intent/fact/finding to a specific asset — so you can see which assets a direction is probing, and
  from any asset look back at which intents tested it in this task and what facts they produced. This
  also drives **asset test coverage** and the **asset coverage graph** (in-scope assets + tested
  highlighted).

```mermaid
flowchart LR
  subgraph EG["Exploration graph (per task · progress chain)"]
    direction TB
    G["goal"]
    I1["intent A"]
    F1["fact"]
    I2["intent B"]
    FD["finding"]
    G -->|spawns| I1
    I1 -->|yields| F1
    F1 -->|derived_from| I2
    I2 -->|proves| FD
  end
  subgraph AG["Asset graph (global · source of truth)"]
    direction TB
    RD["root_domain"]
    SD["subdomain"]
    SV["service"]
    EP["endpoint"]
    RD --> SD --> SV --> EP
  end
  I1 -. anchor .-> SD
  F1 -. anchor .-> SV
  I2 -. anchor .-> EP
  FD -. anchor .-> EP
```

> Division of labor: the **planner** reads the exploration graph's state, judges the goal, and only
> sends an **intent** into the frontier when there is an uncovered new direction; a **worker** claims
> **one intent**, runs real tools, writes the new assets/facts/findings back to both graphs, and
> stops. The asset graph is shared fact; the exploration graph is each task's progress chain.

### Engine and intent lifecycle (one exploration loop)

The engine is an **event-driven** loop: a graph change wakes the planner, the planner sends intents,
a worker claims an intent, executes, and writes back, and the write-back triggers the next round —
until the goal is proven (`prove_goal`).

```mermaid
sequenceDiagram
  autonumber
  participant EV as graph-change debounce
  participant P as planner
  participant FR as frontier intent queue
  participant W as worker
  participant PX as recording proxy
  participant DB as two graphs + activity

  EV-->>P: wake
  P->>DB: read state (graph_overview prefetch + coverage/scope)
  P->>FR: send 0..N intents (with asset_ids)
  Note over P,FR: most wakes send 0 — no new direction means done
  W->>FR: claimNext one intent
  W->>DB: load the intent's asset_ids as initial context
  W->>PX: run real tools (Kali / Bash / HTTP)
  PX-->>W: response (fully traced + CA verified)
  W->>DB: write back fact / asset / finding + per-step activity
  DB-->>EV: graph change
  EV-->>P: wake again (loop)
```

### Process-level information exchange between workers

In a deep exploration, many valuable observations (an error, a response, a hidden parameter) show up
in one worker's **execution process** without being written as a formal fact. To avoid duplicate
work and let workers on a chain stand on each other's shoulders, a worker can **search across other
workers' processes**:

- `search_all_worker_traces(q)`: keyword-search the **execution process of other works in this task**
  (automatically excluding this intent's own steps); hits carry an `intent_id`.
- `list_worker_traces` / `get_worker_trace(intent_id, step_ids=[…])`: first see which works have run,
  then pull the full content of specific steps of one work for a detailed exchange.

So even when the exploration graph has no matching fact yet, later workers can reuse others'
in-process observations — **information flows between workers at the granularity of the "execution
process,"** while the boundary holds (each worker still does only the one intent it claimed).

```mermaid
flowchart LR
  WA["worker A (intent #12)"] -->|"per-step activity"| ACT[("exploration graph · activity store")]
  WB["worker B (intent #34)"] -->|"per-step activity"| ACT
  WC["worker C (intent #56)"] ==>|"1) search_all_worker_traces(q)"| ACT
  ACT ==>|"2) hits in A/B's steps (own excluded)"| WC
  WC ==>|"3) get_worker_trace(id, step_ids)"| ACT
  ACT ==>|"4) return full process content"| WC
```

### The planner's shared multi-round todolist → a stable attack chain

A real attack chain is often a **multi-step sequence with dependencies** (find an injection point →
get credentials → move laterally → escalate). Sending all of that out in parallel at once only
causes chaos. So the planner holds a **planning todolist that is kept per task and shared across
wakes**:

- The planner is event-driven — a graph change wakes it, but **each wake is a fresh session**; the
  shared todolist lets it **record a serial exploitation chain once** and then **send intents step by
  step across rounds** according to dependencies, instead of laying the whole chain out up front in
  one round.
- Each round sends an intent only for the next step whose "prerequisites are done and whose depended
  facts exist," and updates the list as it goes (marking steps that facts have satisfied as complete).

```mermaid
flowchart TB
  subgraph TODO["shared todolist (kept per task · persists across wakes)"]
    direction LR
    T1["1 injection point  [done]"]
    T2["2 get credentials  [in progress]"]
    T3["3 lateral  [blocked]"]
    T4["4 escalate  [blocked]"]
    T1 -.prereq met.-> T2 -.-> T3 -.-> T4
  end
  R1["round 1 wake  send intent ①"] --> T1
  R2["round 2 (① produced a fact)  send intent ②"] --> T2
  R3["round 3 (② produced a fact)  send intent ③"] --> T3
```

So the attack chain still **advances steadily, without repeats or reordering** in an "event-driven +
stateless session" setting — the key to how the system walks a multi-step exploitation chain on its
own.

---

## Provenance and changes

ScopeWeaver is derived from [Autumn-27/ARTEX](https://github.com/Autumn-27/ARTEX) at upstream commit
`160fe13`. Changes in this standalone derivative:

- **Product name**: "ARTEX" → "ScopeWeaver" in the user-visible documentation, scripts, and
  distribution. The Go module (`github.com/Autumn-27/artex`), the build source path (`./cmd/artex`),
  the `ARTEX_*` config/env keys, and the `artex` database defaults are **kept** for compatibility.
- **Documentation language**: English is the default; a Korean translation lives in
  [README.ko.md](README.ko.md) and under [docs/ko](docs/ko/README.md).
- **Script language**: setup/build/start/update/reset-password prompts default to English, with
  Korean selectable via `SCOPEWEAVER_LANGUAGE=ko` (`ARTEX_LANGUAGE=ko` also works). No network
  translation is used.
- **Distribution**: the output binary is `scopeweaver`, release archives are
  `scopeweaver-<version>-<os>-<arch>.zip`, the repository is
  `https://github.com/cskwork/scopeweaver`, and container images target this repo's GHCR. Upstream's
  Docker Hub image (`autumn27/artex`) is not used.
- **ScopeWeaver release history**: standalone releases start at `v0.1.0`, with notes under
  [log/](log/changelog-v0.1.0.md). Version tags trigger platform archives and container builds.
  Docker Compose continues to build locally unless a published image is explicitly selected.

The changelog translation in [CHANGELOG.md](CHANGELOG.md) describes the history of the **upstream
ARTEX** project faithfully; it does not attribute those changes to ScopeWeaver.

### Screenshots and localized assets

The localized captures are in `screenshots/en/` and `screenshots/ko/`. They show the included demo
fixtures on desktop and mobile. Original ARTEX screenshots elsewhere in `screenshots/` remain
unchanged and credited to upstream.

---

## Credits

- **Upstream**: [Autumn-27/ARTEX](https://github.com/Autumn-27/ARTEX) — the project this derivative is
  derived from. The original code and architecture are credited to its authors.
- **Agent SDK**: [`norma`](https://github.com/Autumn-27/norma).
- **Asset sync**: [ScopeSentry](https://github.com/Autumn-27/ScopeSentry).
- **Approval-detail UI**: [AegisHook](https://github.com/RuoJi6/AegisHook).
- **Reference**: [Cairn](https://github.com/oritera/Cairn).
- **Upstream community**: the ARTEX authors run the WeChat public account **SecSentry**
  (`screenshots/wx.png` is their QR code, kept as an upstream asset). This is the upstream project's
  channel, not a ScopeWeaver channel.

---

## License and disclaimer

> This section preserves the upstream license and the authors' usage restrictions and disclaimer,
> translated faithfully from ARTEX. The terms are unchanged.

### Open-source license

This project is licensed under the **GNU Affero General Public License v3.0 (AGPL-3.0)**. The full
terms are in the [LICENSE](LICENSE) file at the repository root.

This means anyone may use, modify, and distribute this project freely, but **derivative works must
also be open-sourced under AGPL-3.0**; in particular, **if you modify this project and offer it to
users over a network (for example, as a hosted service), you must also make the corresponding
complete source code available to those users.**

> ⚠️ **Important**: an open-source license does not by itself restrict how the software may be used.
> The "usage restrictions" and "disclaimer" below are additional conditions and a serious statement
> from the authors to users. Please observe them.

**ARTEX is intended only for personal study, source-code research, and local technical validation. It
must not be used to launch real tests against any online system or website.**

### Permitted use

- Only for **reading, studying, and researching this project's source code**, and for validating the
  technical principles in a **locally isolated environment**.
- Suitable for personal study, academic research, code review, and other non-attacking uses.

### Prohibited

- **Do not use this tool to scan, probe, exploit, or attack any website, online service, or
  networked system** (whether or not you are authorized, and whether or not the asset is your own).
- Do not use this tool for any real penetration test, red/blue exercise, or production environment.
- Do not use this tool for illegal intrusion, data theft, extortion, denial of service, or any
  destructive or criminal activity.
- Do not use this tool for anything that violates the laws and regulations of your country or region.

### Compliance responsibility

Users must comply with all laws and regulations on cybersecurity, data protection, and computer crime
in their own country or region (in mainland China, including but not limited to the Cybersecurity
Law, the Data Security Law, the Personal Information Protection Law, and related judicial
interpretations). **All legal responsibility and consequences arising from use of this tool rest with
the user.**

### Disclaimer

This project is provided "AS IS," without any express or implied warranty. The authors and
contributors are not liable for any direct or indirect loss, data loss, system damage, or legal
dispute arising from use of this tool, whether or not the use was appropriate. **By downloading,
installing, or using this project, you confirm that you have read, understood, and agreed to all of
the terms above.**
