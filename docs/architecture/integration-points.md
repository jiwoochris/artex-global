# §9 Integration points

> Part 9 of the architecture document · [back to the architecture index](../architecture.md)

Every coupling between frontend, backend, database, agent runtime and the file system.

#### Contents

- [9.1 Frontend to backend](#91-frontend-to-backend)
- [9.2 Backend to database](#92-backend-to-database)
- [9.3 Backend to agent runtime](#93-backend-to-agent-runtime)
- [9.4 Process boundaries and external systems](#94-process-boundaries-and-external-systems)
- [9.5 File-system artifacts](#95-file-system-artifacts)

---

### 9.1 Frontend to backend

Every call in this table originates in `web/src/lib/api.ts` and lands on a route registered in
`server/server.go` (or in one of the three helper registrations). The grouping matches
[7.4](frontend.md#74-api-client) and [6.2](http-api.md#62-route-table).

| Frontend method | Endpoint | Backend handler | Notes |
| --- | --- | --- | --- |
| `api.health` | `GET /api/health` | `server/server.go:786` | Exempt from JWT |
| `api.authStatus`, `login`, `initPassword`, `changePassword` | `/api/auth/*` | `server/auth.go:481`, `:660`, `:505`, `:590` | Exempt from JWT; `init` also needs the setup token |
| `api.listTasks`, `createTask`, `getTask`, `updateTaskMetadata` | `/api/tasks…` | `server/server.go:800-812` | DTO shapes in `server/dto.go` |
| Task control, goals, constraints, scope, intents | `/api/tasks/{id}/…` | `server/server.go:844-861` | Lifecycle events also wake `reconcileConcurrency` |
| Categories, templates, archives | `/api/task-categories`, `/api/task-templates`, `/api/task-archives…` | `server/server.go:802-828` | Archive work is queued and processed by a worker |
| Assets, companies | `/api/assets…`, `/api/companies…` | `server/server.go:869-880` | Company reattribution is a background pass |
| Graph and transcript | `/api/exploration/…` | `server/server.go:882-907` | `activity/stream` is SSE; `activity/{seq}` is lazy detail |
| Findings | `/api/exploration/findings…` | `server/server.go:883-896`, `server/finding_traffic.go:96-104` | Detail, lineage, retest and evidence all hang off `findings` |
| Token usage | `/api/tokens/…`, `/api/llm/records…` | `server/server.go:907-926` | Aggregates over `llm_usage`/`llm_records` |
| Traffic | `/api/traffic…` | `server/server.go:913-919` | `{enabled:false}` when the proxy is off |
| Intercept rules, approvals, judge | `/api/intercept/…`, `/api/asset-intercept/rules` | `server/server.go:1031-1064` | An `ask` decision blocks the agent thread until answered |
| Notify | `/api/notify/…` | `server/server.go:932-939` | Channel secrets are masked on read |
| Chat, conversations, side questions | `/api/chat…`, `/api/conversations…`, `…/side-questions` | `server/server.go:942-961`, `server/side_questions.go:292-305` | Chat uploads land in the task working directory |
| Agents, prompts, triggers, tools, MCP, skills, visibility | `/api/agents…`, `/api/tools…`, `/api/mcp…`, `/api/skills…`, `/api/visibility…` | `server/server.go:963-1029` | Prompt edits are database overrides over the code defaults |
| Settings, update, logs | `/api/settings`, `/api/update/…`, `/api/logs…` | `server/server.go:786-790`, `server/server.go:927-928`, `server/update.go:239-380` | Update and log streams are SSE |
| Workspace | `/api/workspace/…` | `server/server.go:837-843` | Paths are resolved and confined by `wsResolve` |

Streams are the only calls that do not go through `http<T>`: the client builds the URL with `sseUrl` and
attaches an `EventSource`.

### 9.2 Backend to database

| Backend unit | Tables it touches | Reference |
| --- | --- | --- |
| `db.ExplorationStore` | `explorations`, `exploration_nodes`, `exploration_edges`, `exploration_anchors`, `activity`, `main_sessions` | `db/exploration.go` |
| `db.AssetStore` | `assets`, `companies`, `company_scope`, `exploration_anchors`, `task_asset_links`, `task_scope` | `db/assets.go`, `db/companies.go`, `db/task_scope.go` |
| `db.DB` (config and settings) | `settings`, `llm_profiles`, `llm_profile_health`, `agent_prompts`, `agent_prompt_vars`, `mcp_servers`, `mcp_tools_cache`, `agent_visibility`, `agent_skill_visibility` | `db/config.go`, `db/settings.go`, `db/llmhealth.go` |
| Task manager | `tasks`, `task_categories`, `task_templates`, `task_archives`, `task_relations`, `task_llm_profiles`, `task_scope`, `task_intercept_rules` | `db/tasks.go`, `db/task_archives.go`, `db/task_templates.go`, `db/task_intercept.go` |
| Agent runtime seeders | `agents`, `agent_prompt_vars`, `tools`, `tool_usage`, `skill_usage`, `agent_triggers`, `scheduler_state` | `db/db.go`, `db/tools.go`, `db/triggers.go`, `server/assembly.go` |
| Finding pipeline | `findings`, `finding_retests`, `traffic_evidence_snapshots`, `finding_traffic_bindings` | `db/findings.go`, `db/finding_retests.go`, `db/finding_traffic.go`, `evidence/store.go` |
| Intercept | `intercept_rules`, `intercept_pending`, `asset_intercept_rules`, `task_intercept_rules` | `db/intercept.go`, `db/asset_intercept.go`, `db/task_intercept.go` |
| Conversations | `conversations`, `conversation_activities`, `side_question_sessions`, `side_question_requests` | `db/conversation.go`, `db/side_questions.go` |
| Notifier | `notification_channels`, `notification_events`, `notification_deliveries` | `db/notification.go`, `db/notification_delivery.go`, `server/notifier.go` |
| Log sink and metering | `server_logs`, `llm_records`, `llm_usage` | `db/logs.go`, `db/commands.go`, `db/llm_usage.go`, `server/logsink.go` |

Every one of these goes through `*db.DB`; no handler holds a `*sql.DB` of its own, which is what makes the
advisory-lock and pool settings in [5.1](data-model.md#51-schema-ownership-and-application) effective process-wide.

### 9.3 Backend to agent runtime

The seam is the `agent` package's option structs plus the process-wide hooks the server installs at boot
(`server/server.go:248-348`).

| Direction | Contract | Reference |
| --- | --- | --- |
| Server → engine | `Engine.SetAgentResolver` decides which agent config a task runs with | `server/server.go:248-261` |
| Server → agent | `wireAgentAugment` installs the tool augmenter (skills, MCP, host tools) and the skill unlocker | `server/assembly.go:25-190` |
| Server → agent | `wireTools` resolves the enabled tool set from the `tools` table and the domain registry | `server/assembly.go:212` |
| Server → prompts | Prompt bodies are database overrides layered over the code defaults; `seedPrompts` publishes the defaults | `server/assembly.go:194`, `db/config.go` |
| Agent → server | Every step is emitted as a `db.Activity` into the engine's single emit point, which persists and broadcasts it | `agent/capture.go:31-47`, `server/engine.go:675-712` |
| Agent → server | Findings are written through the finding recorder into `RecordFindingTx`, which also creates the notify event row | `db/finding_traffic.go:342`, `agent/finding_recorder.go` |
| Agent → guard | `Guard.Hooks()` returns a `hook.Registry` attached to the session; `PreToolUse` can block | `guard/guard.go:55-81` |
| Agent → intercept | Task/agent/conversation identity travels in the context (`WithTaskContext`, `WithConvID`) so an approval can be attributed | `intercept/intercept.go:52-94` |
| Agent → LLM | `llmpool.Registry` plus the provider cache resolve a profile chain per agent/task, with failover and a circuit breaker | `server/llmpool.go`, `llmpool/pool.go` |
| Agent → traffic | `Manager.HostTools()` exposes `traffic_search`/`traffic_get` only while capture is on | `server/manager.go:745-755`, `traffic/traffic.go:2000` |

### 9.4 Process boundaries and external systems

| Boundary | Direction | Protocol | Reference |
| --- | --- | --- | --- |
| Browser ↔ `artex` | in | HTTP + JSON, SSE for streams | `server/server.go:776-1071` |
| `artex` ↔ PostgreSQL | in/out | `pgx` over the DSN (`ARTEX_PG_DSN` > config file) | `db/db.go:23`, `config/config.go:201-214` |
| `artex` ↔ target hosts | out | Whatever the agent runs in its shell, optionally through the recording proxy | `agent/worker.go:199-220`, `traffic/traffic.go:176-248` |
| Worker shell ↔ `artex` proxy | in | HTTP CONNECT/forward proxy on `127.0.0.1:8788` by default | `cmd/artex/main.go:57`, `server/manager.go:394-421` |
| `artex` ↔ LLM providers | out | HTTPS, Anthropic/OpenAI/compatible wire formats, optional per-profile proxy | `agent/provider.go`, `llmpool/pool.go` |
| `artex` ↔ MCP servers | out | stdio child processes or HTTP/SSE | `mcphttp/client.go`, `server/mcpdiscover.go` |
| `artex` ↔ IM and SMTP | out | DingTalk, Feishu, WeCom, generic webhook, Telegram, email | `notify/`, `server/notifier.go` |
| `artex` ↔ GitHub | out | Releases API and asset download with checksum verification | `selfupdate/github.go`, `selfupdate/stage.go` |
| `artex` ↔ ScopeSentry | out | MCP | `server/sync_scopesentry.go` |
| `start.sh` ↔ `artex` | supervisor | restart based on the process exit code | `start.sh:12-26` |
| Operator ↔ `artex` | in | console setup token; `reset-password.sh` writes the same database | `server/auth.go:282-320`, `reset-password.sh` |

### 9.5 File-system artifacts

| Artifact | Written by | Read by |
| --- | --- | --- |
| `<data>/traffic/_index/index.sqlite`, `_blobs/`, `_ca/` | the traffic recorder | traffic API, evidence store, worker shells (CA path) |
| `<data>/evidence/blobs/` | `evidence.Store` | finding traffic API, findings export |
| `<data>/transcripts/` | `server/task_llm.go` | LLM record detail |
| `<data>/tasks/<id>/` | agent tools, chat uploads | workspace API |
| `<data>/uploads/` | `POST /api/chat/upload` | chat and agents |
| `<baseDir>/jwt.key` | `server/auth.go` | `requireAuth` |
| `<baseDir>/skills/` | skill API, operator, compose mount | agent tool catalogue, skill API |
| Staged binary, backup and settle marker next to the executable | `selfupdate` | `selfupdate.Bootstrap` |

Two of these are deliberately outside the browsable workspace: the JWT key (migrated out of `<data>` on
first boot, `server/auth.go:79-101`) and the binary/backup pair used by the updater.

---

← [architecture index](../architecture.md) · section numbers are stable across the split (§9.x).
