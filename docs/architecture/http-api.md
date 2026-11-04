# §6 HTTP and API surface

> Part 6 of the architecture document · [back to the architecture index](../architecture.md)

Handler assembly, the route table, authentication, the realtime channel and the error contract.

#### Contents

- [6.1 Handler assembly and middleware](#61-handler-assembly-and-middleware)
- [6.2 Route table](#62-route-table)
- [6.3 Authentication model](#63-authentication-model)
- [6.4 Realtime channel](#64-realtime-channel)
- [6.5 Error and message contract](#65-error-and-message-contract)

---

### 6.1 Handler assembly and middleware

`Server.Handler` builds one `http.ServeMux` for the API, wraps it, and mounts it under `/api/` on a second
root mux whose fallback serves the embedded UI (`server/server.go:776-1071`).

| Layer | Behaviour | Reference |
| --- | --- | --- |
| Root mux | `"/api/"` → the API handler, `"/"` → `webuiHandler()` (the embedded export, or a 404 stub without the `embedui` build tag) | `server/server.go:1067-1070`, `server/webui_embed.go:16-19`, `server/webui_stub.go:16-18` |
| CORS | Only allowlisted origins get `Access-Control-Allow-*`; the default list is the `dev.sh` frontend (`localhost:5173`, `127.0.0.1:5173`), overridden by `ARTEX_CORS_ORIGINS`; `OPTIONS` answers 204 | `server/server.go:4080-4127` |
| JWT | `requireAuth` validates the token for every path except `/api/auth/*` and `/api/health` | `server/auth.go:458-476` |
| Body decoding | Handlers call the shared `decode` helper and answer 400 on a malformed body | `server/server.go:4129-4140` |
| Route registration | All routes are declared in `Handler` in area blocks; three helpers register their own sub-trees: finding traffic (`server/finding_traffic.go:96-104` — called from `server/server.go:888`), side questions (`server/side_questions.go:292-305`) and, inside `Handler`, task/asset intercept rules | `server/server.go:776-1071` |

`webuiHandler` is where the "one binary" claim is enforced: with `-tags embedui` the compiled static export
is served from `/`; without it every non-API path 404s and the operator is expected to run `next dev`
(`server/webui_embed.go:16-19`, `server/webui_stub.go:16-18`).

### 6.2 Route table

261 routes are registered: 243 `mux.HandleFunc` calls in `server/server.go`, 7 in
`server/finding_traffic.go:96-104`, 9 side-question routes generated for three parents
(`server/side_questions.go:292-302`) and 2 more for the event stream and cancel
(`server/side_questions.go:303-304`). Go 1.22 method patterns (`"GET /api/tasks/{id}"`) mean the method is
part of the pattern, so a wrong method is a 405 from the mux, not a 400 from the handler.

The rows below are contiguous blocks in file order, so the counts up to and including the intercept rows add
up to the 243 in `server/server.go` and no route is counted twice. The two rows after them come from the
helper registrations and are added on top.

| Area | Routes | Representative paths | Handler location |
| --- | --- | --- | --- |
| Auth | 4 | `GET /api/auth/status`, `POST /api/auth/init`, `POST /api/auth/login`, `POST /api/auth/change-password` | `server/server.go:781-784`, `server/auth.go:481`, `:505`, `:590`, `:660` |
| Health, stats, logs | 5 | `GET /api/health`, `/api/stats`, `/api/logs`, `/api/logs/history`, `/api/logs/stream` (SSE) | `server/server.go:786-790`, `server/server.go:3073` |
| Self-update | 4 | `GET /api/update/check`, `POST /api/update/apply`, `POST /api/update/rollback`, `GET /api/update/stream` (SSE) | `server/server.go:795-798`, `server/update.go:239`, `:303`, `:350`, `:368` |
| Tasks and their sub-resources | 60 | `GET\|POST /api/tasks`, `GET\|PATCH /api/tasks/{id}`, task categories and templates, task archives, workspace, scope, goals, constraints, task intercept rules, task LLM binding, intent control/messages/rerun, batch operations, `/api/active` | `server/server.go:800-862` |
| LLM provider (global) | 3 | `GET\|POST /api/llm`, `POST /api/llm/test` | `server/server.go:864-866` |
| Assets and companies | 10 | `GET\|POST\|DELETE /api/assets`, `/api/assets/counts`, `/api/companies…`, `/api/companies/reattribute` | `server/server.go:869-880` |
| Exploration graph and findings | 24 | `/api/exploration/{frontier,intents,graph,nodes,activity,activity/history,activity/stream}`, `/api/exploration/main-sessions`, `…/main-session/new`, `/api/exploration/findings…` (list, groups, asset-tree, stats, export, detail, lineage, deepen, retests, patch, delete) | `server/server.go:882-906` |
| Token and usage stats | 3 | `GET /api/tokens/{daily,conversations,usage}` | `server/server.go:907-909` |
| Audit and GC | 2 | `GET /api/audit`, `POST /api/gc` (a no-op stub) | `server/server.go:911-912`, `server/server.go:4073-4076` |
| Traffic | 7 | `GET\|DELETE /api/traffic`, `/api/traffic/hosts`, `/api/traffic/all`, `/api/traffic/exchange`, `/api/traffic/blob` | `server/server.go:913-919` |
| Tool runs and LLM records | 7 | `GET /api/commands`, `/api/commands/stats`, `/api/llm/records…`, `/api/llm/records/by-model` | `server/server.go:920-926` |
| Settings, notifications, report | 12 | `GET\|PUT /api/settings`, `POST /api/settings/web-search/test`, `/api/notify/{meta,channels,deliveries}`, `…/channels/{id}/test`, `…/deliveries/{id}/retry`, `GET /api/report` | `server/server.go:927-941` |
| Chat and conversations | 16 | `POST /api/chat`, `/api/chat/upload`, `/api/chat/mentions`, `/api/tasks/{id}/chat/{status,stop}`, `/api/conversations…` (CRUD, messages, stop) | `server/server.go:942-961` |
| Agents, prompts, triggers | 21 | `/api/agents…` (CRUD, config, prompt, wrapup, variables, preview, visibility), `/api/triggers/{id}` | `server/server.go:963-983` |
| Tools, MCP, Python detection | 13 | `/api/tools`, `/api/tools/custom…`, `/api/mcp…`, `/api/settings/python/detect` | `server/server.go:985-998` |
| ScopeSentry sync | 5 | `/api/sync/scopesentry/{status,datasource,projects,tasks,sync}` | `server/server.go:1000-1004` |
| Skills | 12 | `/api/skills`, `/api/skills/upload`, `/api/skills/missing`, `/{name}/usage\|meta\|dirs\|files…` | `server/server.go:1006-1018` |
| Visibility | 4 | `/api/visibility/{kind}/{id}`, `/api/visibility/toggle`, `/api/visibility/skill/{name}`, `/api/visibility/skill/toggle` | `server/server.go:1020-1024` |
| LLM profiles, retry policy, pool | 9 | `/api/llm/profiles…`, `/api/llm/{retry-policy,pool,pool/reset,models}` | `server/server.go:1026-1034` |
| Intercept rules | 5 | `/api/intercept/rules…` | `server/server.go:1037-1041` |
| Asset intercept rules | 5 | `/api/asset-intercept/rules…` | `server/server.go:1044-1048` |
| Intercept pending, history, judge | 12 | `/api/intercept/pending…`, `/api/intercept/history…`, `/api/intercept/task/{taskID}`, `/api/intercept/{tool-config,judge,judge/usage}` | `server/server.go:1050-1061` |
| Finding traffic evidence | 7 | `/api/exploration/findings/{id}/traffic…` (read, bind, edit, order, body) | `server/finding_traffic.go:96-104` |
| Side questions | 11 | `…/side-questions` on conversation, task-chat and intent parents; `GET /api/side-questions/{requestID}/events` (SSE); `POST …/cancel` | `server/side_questions.go:292-305` |

### 6.3 Authentication model

There is one account. The username is the fixed string `ARTEX`, and the only credential is a bcrypt hash
stored in `settings` under `auth.password_hash` (`server/auth.go:25`, `server/auth.go:670-690`).

| Concern | Implementation | Reference |
| --- | --- | --- |
| Token | HS256 JWT, `sub: "ARTEX"`, 7-day TTL | `server/auth.go:27`, `server/auth.go:122-131` |
| Transport | `Authorization: Bearer`, the `artex_token` cookie, or `?token=` for SSE (EventSource cannot set headers) | `server/auth.go:446-457` |
| Signing key | 32 random bytes in `<baseDir>/jwt.key`, migrated out of `<data>/` on first boot, mode 0600 | `server/auth.go:79-118` |
| Key rotation | The effective key is `HMAC-SHA256(jwt.key, password_hash)`, so any password change invalidates every earlier token | `server/auth.go:141-156` |
| Hash cache | The hash is re-read from the database every 2 s, and once more on a failed verification, so `reset-password.sh` revokes sessions without a restart | `server/auth.go:166-222` |
| Password rules | At least 8 characters, at most 72 bytes (bcrypt's hard limit), `bcrypt.DefaultCost` | `server/auth.go:37-41`, `server/auth.go:547-560` |
| One-shot setup | While no hash exists, `POST /api/auth/init` also requires the setup token: `ARTEX_SETUP_TOKEN` if set, otherwise 32 random characters printed once on the console `[auth]` line, compared in constant time and cleared after use | `server/auth.go:282-320`, `server/auth.go:505-588` |
| "Set exactly once" | The check is a fast-fail; the guarantee is `INSERT … ON CONFLICT DO NOTHING` (`InsertSettingIfAbsent`, `db/settings.go:32`) inside `initMu` | `server/auth.go:527-566` |
| Headless bootstrap | `ARTEX_ADMIN_PASSWORD` initialises the hash at startup with the same first-insert-only semantics | `server/auth.go:235-278` |
| Login | Always runs the bcrypt comparison, so a wrong username costs the same as a wrong password | `server/auth.go:660-690` |
| Shared limiter | Per-client cooldown: 5 free failures, then a doubling 30 s → 10 min cooldown answering 429; keyed on the TCP peer only, because `X-Forwarded-For` is client-controlled | `server/auth.go:322-390`, `server/auth.go:393-419` |
| Exposure warning | Startup logs a warning when the password is unset and the listen address is not loopback | `server/auth.go:434-444`, `cmd/artex/main.go:116` |
| Read-failure handling | A settings read error is a 503, never "not initialised", so a database outage cannot reopen the setup path | `server/auth.go:481-502`, `server/auth.go:56-63` |

### 6.4 Realtime channel

`server/broadcast.go` implements an in-process, per-task pub/sub of `db.Activity` values — nothing more. It
is not a message broker: it does not persist, it does not survive a restart, and it assumes one process.

| Property | Implementation | Reference |
| --- | --- | --- |
| Topology | `map[taskID]set(chan db.Activity)`, one buffered channel (256) per subscriber | `server/broadcast.go:13-31` |
| Subscribe | Returns the channel plus an idempotent unsubscribe that deletes the subscriber and closes the channel | `server/broadcast.go:24-51` |
| Publish | Fans out to every subscriber of that task; when a buffer is full the event is dropped, so a slow client can never stall the engine | `server/broadcast.go:53-64` |
| Single emit point | `Engine.emitActivity` persists the row first and then publishes it, so the stored transcript and the live stream cannot diverge | `server/engine.go:663-712` |
| Consumer | `GET /api/exploration/activity/stream?task=&intent=` subscribes **before** replaying, so no event falls into the gap | `server/server.go:3124-3160` |
| Replay and cursor | `since=` seeds the replay and `Last-Event-ID` overrides it on a browser auto-reconnect; the backlog is replayed from `activity` in batches of 500 | `server/server.go:3141-3192` |
| Keep-alive | `: ping` comment every 20 s | `server/server.go:3204-3214` |
| Second producer | Finding-workflow audit rows are published the same way | `server/findings_groups.go:177` |

Three other SSE endpoints each have their own in-process hub, because their payloads are not activities:
`GET /api/logs/stream` from the log sink (`server/server.go:3073-3122`), `GET /api/update/stream` from
`updateHub` (`server/update.go:151-236`, `:368`) and `GET /api/side-questions/{requestID}/events`
(`server/side_questions.go:547-600`). All four use the same shape: `text/event-stream`,
`X-Accel-Buffering: no`, a 20 s ping, and a client that resumes from a cursor.

### 6.5 Error and message contract

Every JSON error body is exactly `{"error": "<message>"}` — `writeErr` wraps `writeJSON` and passes the
message through `trMsg`, so the text is in the active output language and the HTTP status carries the
machine-readable meaning (`server/server.go:4129-4139`). Success bodies are the domain shape or `{}`; 204
means "no body" and the client returns `undefined` (`web/src/lib/api.ts:157-161`).

`trMsg` is total: an English source string with no catalogue entry, an unwired hook or an unknown language
returns the English input unchanged, and formatting is applied after translation so a `%d` template can
never miss its lookup (`server/errmsg_i18n.go:1-33`). Evidence errors are mapped to 409/404/422 by
`evidenceError` rather than flattened to 500 (`server/finding_traffic.go:106-115`).

---

← [architecture index](../architecture.md) · section numbers are stable across the split (§6.x).
