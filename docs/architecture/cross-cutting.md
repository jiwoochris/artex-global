# §8 Cross-cutting concerns

> Part 8 of the architecture document · [back to the architecture index](../architecture.md)

Observability, notifications, evidence capture, traffic interception, self-update and skill distribution.

#### Contents

- [8.1 Observability](#81-observability)
- [8.2 Notifications](#82-notifications)
- [8.3 Evidence capture](#83-evidence-capture)
- [8.4 Traffic interception and command guard](#84-traffic-interception-and-command-guard)
- [8.5 Self-update](#85-self-update)
- [8.6 Skill distribution](#86-skill-distribution)
- [8.7 Language handling across the backend](#87-language-handling-across-the-backend)

---

### 8.1 Observability

Two independent channels, both in-process:

| Channel | Implementation | Reference |
| --- | --- | --- |
| Log ring | `logSink` keeps the last 3000 lines, tees to stderr and fans out to SSE subscribers | `server/logsink.go:34-52` |
| Log redirection | `StartLogCapture()` replaces the standard logger output at the first line of `run`, so every `log.Printf` in the process is captured | `server/logsink.go:50`, `cmd/artex/main.go:69` |
| Persistence | `SetDB` restores the last 100 rows into the ring and starts one async writer goroutine that inserts into `server_logs` | `server/logsink.go:56-118`, `db/logs.go` |
| Read path | `GET /api/logs` (ring), `/api/logs/history` (database), `/api/logs/stream` (SSE) | `server/server.go:788-790`, `server/server.go:3073` |
| Transcript | Every agent step is an `activity` row plus a live broadcast; the database is the source of truth and the stream is lossy by design | `server/engine.go:675-712` |
| Audit | `Guard.Audit()` keeps the last 2000 gated tool calls with action/reason/command, exposed at `GET /api/audit` | `guard/guard.go:190-210`, `server/server.go:4064-4071` |
| Failure attribution | `postToolUse` classifies Bash results as blocked/error/ok and counts them per task (Observer / G5) | `guard/guard.go:156-172` |
| Cost | `llm_records` and `llm_usage` aggregate per call and per task; `GET /api/tokens/*` and `/api/llm/records/*` read them | `db/commands.go`, `db/llm_usage.go`, `server/server.go:907-926` |

### 8.2 Notifications

`notify/` is a channel abstraction, not a service; `server.Notifier` owns the queue and the loop.

| Layer | Implementation | Reference |
| --- | --- | --- |
| Channel contract | `Channel` interface, `Get(kind)`, `ValidKind`, `Kinds`, and a `PermanentError` marker for "do not retry" | `notify/channel.go:14-127` |
| Kinds | `dingtalk`, `feishu`, `wecom`, `webhook` (generic), `telegram`, `email` | `db/schema.sql:1320-1325`, `notify/dingtalk.go`, `notify/feishu.go`, `notify/wecom.go`, `notify/webhook.go`, `notify/telegram.go`, `notify/email.go` |
| Rendering | Markdown for IM, HTML for email, with byte/rune-aware truncation and item packing | `notify/render.go:22-165`, `notify/markdown.go`, `notify/html.go` |
| Secrets | Channel config is stored plaintext but masked on echo; a masked value sent back is rejected unless the destination is unchanged | `notify/mask.go:27-260`, `db/schema.sql:1329-1335` |
| SSRF defence | A dialer that refuses internal and link-local addresses, plus error rewriting that strips URLs from messages | `notify/http.go:40-147`, `notify/http.go:231-280` |
| Filtering | Severity/status/keyword/task filters validated at save time and matched per event | `notify/filter.go:14-135` |
| Blast radius | Three separate tables: `notification_channels`, `notification_events` (a blind INSERT inside the finding transaction) and `notification_deliveries` (produced outside it), so a misconfigured webhook can neither abort nor poison a finding write | `db/schema.sql:1304-1313`, `db/notification.go`, `db/notification_delivery.go` |
| Delivery loop | `Notifier.Run` ticks, takes a per-channel token bucket, renders single or batched messages and writes delivery state | `server/notifier.go:140-330`, `server/notifier.go:536-566` |
| API | `/api/notify/{meta,channels,deliveries}`, `…/channels/{id}/test`, `…/deliveries/{id}/retry` | `server/server.go:932-939`, `server/notify_api.go` |

### 8.3 Evidence capture

Evidence exists so that a finding stays provable after the traffic that proved it has been pruned.

| Step | Implementation | Reference |
| --- | --- | --- |
| Capture switch | `settings.traffic_capture`, off by default; `SetTrafficEnabled` persists and applies it | `server/manager.go:253`, `server/manager.go:432`, `server/manager.go:479-500` |
| Injection | When capture is on, `Manager.ProxyAddr()` returns the MITM address and workers get `HTTP_PROXY`/`HTTPS_PROXY` plus CA variables (`SSL_CERT_FILE`, `CURL_CA_BUNDLE` and the Python equivalents) in their shell environment | `server/manager.go:768-775`, `agent/worker.go:199-220` |
| Snapshot | `evidence.Store.Record` copies the referenced exchange (URL, method, status, request/response heads, bodies) into a SHA-256 content-addressed blob and inserts `traffic_evidence_snapshots` | `evidence/store.go:182-196`, `db/finding_traffic.go:342` |
| Binding | `Store.Bind` maps a finding to its snapshots through `finding_traffic_bindings`, with a role and a note | `evidence/store.go:197-214`, `db/schema.sql:1177-1188` |
| Verification | Bodies are re-hashed on read; a mismatch is an error, not a warning | `evidence/store.go:36-58` |
| GC | `RunGC` ticks hourly and collects unreferenced snapshots and blobs; one goroutine per process | `evidence/store.go:347-419`, `server/server.go:339` |
| Export | A findings export stages a private copy of the store that the GC does not touch | `server/finding_traffic_export.go:28` |
| API | `GET\|POST /api/exploration/findings/{id}/traffic`, `…/{binding_id}[/body]`, `PUT …/order` | `server/finding_traffic.go:96-104` |

There is no `server/capture.go`: the capture switch lives in `server/manager.go`, the environment injection
in `agent/worker.go`, and the snapshot logic in `evidence/` plus `server/finding_traffic.go`.

### 8.4 Traffic interception and command guard

Two different layers, often confused:

| Layer | What it matches | Where it lives | Reference |
| --- | --- | --- | --- |
| Traffic recording | Nothing — it records. A local MITM proxy that re-signs TLS with its own CA and stores every exchange | `traffic/` (`Open`, `Page`, `Get`, `Tools`) | `traffic/traffic.go:176-248`, `traffic/traffic.go:859-1000`, `traffic/traffic.go:2000` |
| Command intercept | Tool name or tool input, by string or regex, with action allow/deny/ask and an approval timeout | `intercept/` plus `intercept_rules`/`intercept_pending` | `db/schema.sql:1046-1089`, `intercept/intercept.go:96-135` |
| Asset intercept | The *target* (exact/fuzzy domain, IP, URL, CIDR): global rules plus per-task block/allow rules | `asset_intercept_rules` / `task_intercept_rules`, matched in `db/asset_intercept_match.go` | `db/schema.sql:1255-1301`, `db/schema.sql:1282-1301` |
| Guard hook | Every tool call passes `PreToolUse` before execution and `PostToolUse` after | `guard/guard.go` | `guard/guard.go:46-81` |

The guard is a thin adapter: it extracts the command surface for the audit log, then delegates the decision
to `Interceptor.Match` and, when nothing matches, to the optional LLM judge; `ask` blocks on
`Interceptor.HandleAsk` until the operator decides or the timeout fires (`guard/guard.go:83-135`,
`intercept/intercept.go:445-519`, `intercept/intercept.go:597-680`). Rules are user data, not code:
destructive-command gating ships as ordinary `[built-in]` rows the operator can disable or delete
(`guard/guard.go:1-7`, `db/db.go:349-660`).

A blocked call is recorded twice with different text. History rows keep the raw reason
(`intercept.Interceptor.Log`), while the model-facing tool result is wrapped by `systemBlockMessage`, which
prefixes a fixed framing marker and then the reason (`guard/guard.go:137-151`). The framing is deliberately
kept in Chinese: the source comment states it is steering wording aimed at the agent and at the audit log,
and the detection material in `detections/` matches the exact string. [`docs/defense.md`](../defense.md) quotes
it as the guard control marker and explains the defender-side reading.

### 8.5 Self-update

| Phase | Implementation | Reference |
| --- | --- | --- |
| Boot decision | `selfupdate.Bootstrap()` runs before anything else and returns applied / rolled-back / discarded | `selfupdate/bootstrap.go:52-80`, `cmd/artex/main.go:75-78` |
| Path resolution | `ResolvePaths()` derives the binary, staged file and backup from the executable location | `selfupdate/selfupdate.go:43-80` |
| Check | GitHub releases API with an allowlisted host check; the asset name is built from tag plus GOOS/GOARCH | `selfupdate/github.go:60-140` |
| Stage | Download the zip, verify against `checksums`, then extract the binary | `selfupdate/stage.go:50-134`, `selfupdate/stage.go:135-260` |
| Swap and settle | Marker file, atomic swap, 30 s settle window: if the new build dies inside it, the backup is restored on the next boot | `selfupdate/bootstrap.go:117-168`, `selfupdate/bootstrap.go:215-256`, `cmd/artex/main.go:107` |
| Restart | The API closes a channel that `run` selects on; the process exits and `start.sh` relaunches it | `server/update.go:48-50`, `cmd/artex/main.go:133-138`, `start.sh:12-26` |
| UI | `GET /api/update/check`, `POST /api/update/apply`, `POST /api/update/rollback`, `GET /api/update/stream` (progress hub) | `server/update.go:239-380` |
| Container | `InDocker()` disables in-place replacement, so a containerised install updates by rebuilding the image | `selfupdate/selfupdate.go:165-180`, [`docs/docker-hardened.md`](../docker-hardened.md) |

### 8.6 Skill distribution

A skill is a directory of plain files with a `SKILL.md`; nothing about it is compiled in.

| Concern | Implementation | Reference |
| --- | --- | --- |
| Location | `config.SkillDir()`: `ARTEX_SKILL_DIR` > config file `skill_dir` > `<baseDir>/skills` | `config/config.go:178-195`, `cmd/artex/main.go:110` |
| Shipped skills | `skills/api-recon`, `skills/playwright-cli`, `skills/scopesentry`; mounted over `/app/skills` in compose | `skills/`, `Dockerfile:59`, `docker-compose.yml:52-54` |
| CRUD API | List, create, upload, delete, dirs and files, per-file read/write/delete, usage and missing-skill reports | `server/server.go:1006-1021`, `server/server_mgmt.go:1049`, `server/server_mgmt.go:1666` |
| Upload | `multipart` POST parsed as a zip: 20 MB compressed body, 100 MB uncompressed total, 20 MB per file | `server/server_mgmt.go:1332-1334`, `server/server_mgmt.go:1366-1371` |
| Archive handling | Adds bzip2 and zstd decompressors (Go embeds only store/deflate), decodes GBK entry names, and rejects encrypted or unsupported methods with a readable message | `server/skill_zip.go:23-53`, `server/skill_zip.go:63-130` |
| Discovery rule | The shallowest `SKILL.md` inside the zip defines the skill root, and the name comes from its frontmatter | `server/server_mgmt.go:1387-1420` |
| Visibility | Per-agent visibility rows gate which skills an agent may see; an agent's skill tools are packed into one `Skill` meta-tool | `db/schema.sql:900-911`, `agent/assembly.go:27-79` |
| Usage | `skill_usage` records calls for the UI's usage view | `db/schema.sql:912-931`, `db/skill_usage.go` |

### 8.7 Language handling across the backend

| Surface | Mechanism | Reference |
| --- | --- | --- |
| HTTP error messages | `trMsg(englishLiteral)` over the Spanish catalogue; English is the key, so a missing entry degrades to English | `server/errmsg_i18n.go:26-33`, `server/errmsg_catalog.go` |
| Active language | `config.ActiveLanguage()` — runtime `settings.output_language` over `config.Language()`; only `en`/`es` pass normalisation | `config/config.go:66-96` |
| Console log templates | `config.T(key)` through `server.logT` | `config/msg_i18n.go:76`, `server/errmsg_i18n.go:36` |
| Agent output | `langDirective(code)` appended to each role's system prompt; the reasoning body itself is not translated | `agent/prompt.go:77`, `agent/outputlang.go` |
| Notification labels | Severity and status labels resolve per language in `notify.labelLang` | `notify/notify.go:60-104` |
| Intercept labels | Rule-action labels have their own small catalogue | `intercept/label_i18n.go` |

The rule that keeps this honest: a user-facing string is a constant, and the catalogue key is that same
constant, so the displayed text and the lookup key cannot drift apart (`server/errmsg_i18n.go:9-18`).

---

← [architecture index](../architecture.md) · section numbers are stable across the split (§8.x).
