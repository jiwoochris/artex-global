# Upstream ARTEX changelog

This is the translated upstream ARTEX history from baseline `160fe13`, preserving its original dates, contributors and technical history. These entries are not ScopeWeaver releases. ScopeWeaver's 2026-10-02 modifications are recorded separately in [provenance](docs/PROVENANCE.md). Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). [한국어](CHANGELOG.ko.md).

## [Unreleased]

### Authentication

#### Added

- Added a first-run setup token. Setting the admin password on `/setup` now requires the token, which is either the `ARTEX_SETUP_TOKEN` environment value (never logged) or a random 32-character value the server prints once per start in an `[auth]` log line. `GET /api/auth/status` returns only `initialized` and `setup_token_required`, so the token is not obtainable through any API. `ARTEX_ADMIN_PASSWORD` initializes the password at boot for unattended deploys. Passwords must be at least 8 characters and at most 72 bytes, matching the bcrypt limit.
- `POST /api/auth/change-password` now returns a fresh token. Because the JWT signing key is derived from the password hash, changing the password invalidates every existing session, so the client must replace its stored token with the returned one.
- Added login throttling shared between `/auth/login` and `/auth/init`: five failures in ten minutes trigger an escalating cooldown, capped at ten minutes. The client key comes from the real TCP peer rather than `X-Forwarded-For`, which a client can forge. `reset-password.sh` writes the hash directly, and the server notices within its two-second hash cache, so no restart is needed.

#### Changed

- The server binds to `127.0.0.1:8787` by default instead of all interfaces, so the setup screen is not reachable from the network before a password exists. The proxy defaults to `127.0.0.1:8788`. Pass `-addr :8787` for other hosts; the server logs a warning while no admin password is set. Docker Compose publishes to `${ARTEX_BIND:-127.0.0.1}` and the container still listens on all interfaces internally.
- CORS no longer allows every origin. The defaults are `http://localhost:5173` and `http://127.0.0.1:5173`; `ARTEX_CORS_ORIGINS` sets a comma-separated allowlist and `*` restores the permissive behavior. Allowed responses now carry `Vary: Origin`.
- First-run initialization inserts the password hash atomically and refuses to overwrite an existing password, so concurrent instances cannot reset a live credential. `ARTEX_ADMIN_PASSWORD` is read only while no password is set.

#### Fixed

- Authentication now fails closed. Previously a database error while reading the password state was indistinguishable from "not initialized", so an unreachable database could trigger a fresh first-run setup. A read failure now skips initialization and surfaces as HTTP 503 on `/auth/status`.
- Dynamic authentication errors are translated with `locale.Errorf` instead of being pre-formatted, so the exact error text reaches the message catalog in English, Korean and Simplified Chinese.

### Interception

#### Added

- Added a built-in delete-path deny rule. Existing destructive HTTP rules recognized DELETE methods (`curl -X DELETE`, `requests.delete(`, `method:'DELETE'`), while path vocabulary covered only `/clear /wipe /flush /purge /truncate /drop /destroy /factory-reset /reset-all`. GET/POST deletion such as `curl 'http://t/api/user/delete?id=1'` could therefore delete real data without matching. The new rule covers `/delete /del /remove /unlink /erase /destroy`, including suffix forms `/deleteAll`, `/delete_user`, `/delete-user`, with boundaries avoiding `/delivery`, `/details`, `/delta`, `/delegate`. Its independent seed marker also installs it on upgraded instances; users can disable/delete it under System → Command interception.
### Finding notifications

#### Added

- Added System → Notifications with DingTalk, Feishu, WeCom, generic Webhook, Telegram and email delivery. Each type supports multiple instances, such as separate emergency/daily bots, with independent enablement, rate limits and filters.
- Notifications support immediate per-finding delivery or digests on a global interval, default 30 minutes. Digests begin with new-finding counts/time range and severity distribution. Separate channels can implement high-severity immediate delivery and other-severity digests without hardcoded policy.
- Four filter dimensions: minimum severity, task/asset scope, finding-type keyword inclusion/exclusion (exclusion wins), and disposition changes (off by default because notifications usually mean new findings). Individual messages link to details using the global backlink URL; blank URL omits the button.
- Delivery history lists status, attempts, failure reason and channel with channel/status filters. Manual resend resets retry counts, assuming the operator addressed the cause.
- Channels declare secrets via `SecretKeys()` (webhooks, signing keys, bot tokens, SMTP passwords). APIs return masks with trailing hints; submitting the unchanged mask preserves the value, while clearing removes it.

#### Fixed

> This section records an upstream security audit after the initial feature implementation. Each issue was reproduced, fixed and covered by a regression test.

- Fixed a critical mask bypass through changing destinations while retaining credentials. Separate destination/credential fields and merge-preservation of omitted keys let a destination-only edit send stored Webhook Authorization, Telegram URL bot tokens and SMTP passwords after STARTTLS to arbitrary hosts, silently and without redirects. The path was reproduced across four channels. Destination changes now require an explicit new value or explicit empty value for every secret; unchanged masks, meaning reuse, are rejected too. Secrets are not silently discarded because optional fields such as headers would otherwise lose authentication while the API reports success.
- Escaped untrusted Markdown in DingTalk, WeCom and Feishu, which previously lacked the protection already present in Telegram/email. Model-derived titles/summaries and target-controlled asset query strings could render attacker links or image beacons, revealing that a finding was read and the reader's IP. Content now becomes a single line with Markdown metacharacters escaped at each renderer. Escaping is not done in the shared title function, because Markdown backslashes leaked visibly into Telegram HTML.
- Hardened notification SSRF at dial time. Scheme/host-only validation previously allowed cloud metadata `169.254.169.254`, loopback and internal destinations; failed-response prefixes of 200 bytes entered `last_error` and history, exposing a partial internal-response read. Dial checks cover DNS rebinding and same-host redirects, while cross-host redirects are rejected because URL credentials would transfer. Loopback/link-local need `ARTEX_NOTIFY_ALLOW_LOCAL=1` for legitimate local SMTP relays. RFC1918 remains intentionally allowed for self-hosted Mattermost/SMTP, avoiding breaking normal private deployments.
- Redacted notification secrets from errors. `http.Client.Do` returns `*url.Error` containing full URLs with DingTalk access_token, WeCom key, Feishu hook IDs or Telegram `/bot<token>/`. These leaked to plaintext `notification_deliveries.last_error`, history bypassing masks, logs and test-send UI errors. Errors now retain only scheme://host and the underlying DNS/connectivity/certificate cause, dropping paths/queries. URL-parse validation failures get the same treatment. The preceding fix missed parsing errors and its tests only hit scheme validation; genuine coverage now exercises the missed branch.
- Fixed silently lost digest entries after truncation. Channel limits (WeCom's 4096 bytes being tightest) previously truncated a message but marked its whole batch delivered; omitted findings appeared neither in messages nor failures. Packing now keeps whole entries, marks only included ones delivered, requeues the remainder, and states “first N shown, M continue next”. Deferral refunds the optimistic attempt increment, preventing a 500-entry backlog's tail from exhausting retries by the third segment without an actual failure.
- Validate `min_severity` on writes. Typos such as `hgih` previously ranked as 0, making rank >= 0 always true and silently sending every finding despite an apparent high-only filter. Errors list valid values; reads tolerate legacy invalid values so channels remain readable.
- Restored explicit `rate_per_min=0` as unlimited. Storage had replaced <=0 with defaults 20 (DingTalk/WeCom/Telegram) or 100 (Feishu), contradicting docs/UI/token buckets. Defaults now apply at the API only when omitted, distinguishing absent from explicit zero.
- Digest delivery now honors token buckets: `takeTokens` had deducted `allow` without using it, making rate_per_min ineffective. Claim counts now obey both remaining allowance and memory bounds.
- Failure handling now evaluates each delivery's attempts, not the batch maximum. Previously an older twice-retried item could permanently fail new entries with no retries used. Permanent errors fail immediately, exhausted entries fail individually, and others requeue with their own backoff.
- Unparseable digest snapshots now fail explicitly with a history reason. Previously rendering skipped them but batch success marked them delivered.
- Bounded per-channel round size by lease duration divided by per-send timeout. A three-minute lease could expire during a long serial batch, letting another instance reclaim and duplicate sends/attempts. An assertion locks the constants' relationship; it exposed an old limit of 6 using the entire lease without margin, changed to 5.
- Telegram truncation avoids incomplete HTML entities as well as tags; an `&amp` fragment could otherwise make the parser reject an entire long digest.
- SMTP 4xx such as greylisting 450 now retries, 5xx fails permanently, and absent codes default to retry. Previously every temporary rejection was permanent, causing greylisted servers to fail each notification on its first attempt.
- Reject nested mask sentinels inside objects such as webhook.headers. These fields must be wholly masked or wholly submitted; nested sentinels previously persisted as real values and silently broke later authentication rather than preserving secrets.

#### Design notes

- `RecordFindingTx` writes `notification_events` with one blind INSERT in the finding transaction, pairing finding/event persistence on successful insertion. It deliberately does not read channels or execute user filters, so bad filter configuration cannot disrupt finding storage. The INSERT is wrapped in SAVEPOINT because a PostgreSQL statement error otherwise aborts the transaction, including COMMIT; notification insertion failure is logged without blocking the finding.
- Delivery claims use `FOR UPDATE SKIP LOCKED`, set sending and a future next_attempt_at lease, then commit before network work. No DB lock is held during delivery; sending rows left by crashes are reclaimed after expiry without unbounded retries.
- Rate limiting does not consume retry budgets. The engine computes token allowance before claiming exactly that many rows; claiming then discarding would burn the three attempts merely waiting. Excess work moves to the next tick without loss.

### Test infrastructure

#### Fixed

- Fixed permanent test-data residue in the company ICP ownership test. Cleanup used t.Cleanup but defer d.Close ran first, so cleanup hit a closed connection and discarded errors via `_, _ =`. Its fake TaskID MAX(companies.id)+1 could collide with other tests, breaking exact asset counts. Closing now uses an earlier-registered t.Cleanup so LIFO cleanup precedes it, and failures surface. More than ten similar patterns remain in db; this change fixed only the empirically triggered case.

### Traffic

#### Added

- Traffic Clear all ignores filters, deletes all records and orphaned legacy host directories, then runs optimize/VACUUM/wal_checkpoint(TRUNCATE) and reports freed space. Independently stored finding evidence remains intact. VACUUM on the empty DB is cheap and converts existing indexes to incremental reclamation, enabling later host deletions to reclaim space automatically.

#### Fixed

- Fixed traffic deletion retaining disk space. SQLite placed pages on freelists without auto_vacuum, while contentless_delete ex_fts wrote tombstones without reclaiming postings, so deletion could grow the index. Bodies below 256KB are inline and trigram indexes roughly double body size: a measured 6MB capture left a 16MB index even after deletion. New indexes enable auto_vacuum=incremental. After deletes, background chunks merge full-text indexes, incremental_vacuum and truncate WAL; the same case fell to 104KB. Chunks release write locks between work, avoid blocking capture, yield on shutdown and resume on the next deletion.

> Upgrade note: existing indexes retain their old auto_vacuum mode, so incremental_vacuum is a no-op and startup logs a notice. Full-text merges already stop tombstone growth. Clear all reclaims old space and converts the empty index once; subsequent ordinary deletions reclaim incrementally.

### LLM

#### Fixed

- Fixed custom session headers on one-shot LLM calls. agentcore only placed session IDs in context with a transcript store; round-zero goal decomposition and cold compaction body calls had none. Gateways requiring x-opencode-session returned 400 MissingSessionID while later planner turns worked, and compaction failures were hard to connect to a lone log. Both now use exploration-stable `exp<N>-goals` / `exp<N>-compactor` IDs, restoring headers and previously missing llmrec token attribution.

### Findings

#### Fixed

- Fixed missing scrolling in By asset. The outer card had only max-height, leaving height:auto so the inner 100% scroll viewport could not resolve; large trees overflowed or clipped. The native tree scroll container now receives responsive max-h with overflow-y-auto, shrinking for small lists and scrolling at the cap for large ones.

## [0.3.14] - 2026-09-24

### Task list

#### Added

- Added a findings column to the task list, showing critical/high/medium/low counts. Nonzero counts use severity colors to show each task's finding volume and distribution.

#### Changed

- Narrowed description/objective columns. Long content truncates with full text on hover, reducing horizontal crowding.

### Exploration graph and planning overview

#### Changed

- Reduced `graph_overview` size and capped lists. Recent facts, completed/pending intents, cold summaries and confirmed findings now show a recent window plus total counts; omitted entries remain queryable. This reduces per-turn LLM context and growth in long tasks, including cold sections of associated-task overviews.

#### Fixed

- Removed reverse duplicate edges between digest and finding nodes after cold-node folding.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.13] - 2026-09-19

### Asset interception

#### Added

- Added global asset deny rules for exact/fuzzy domain, IP and URL matches and CIDR ranges. System → Asset interception supports create/read/update/delete and enable/disable. Built-in fuzzy rules block government (`.gov` / `.gov.cn`) and education (`.edu` / `.edu.cn`) sites by default.
- Integrated asset interception into execution. Before `add_intent` or `insert_assets`, the agent's target is checked. Blocked intents are not dispatched and blocked assets are not inserted; asset details and the reason return to the agent.
- Added task-local deny/allow rules independent of global rules. Deny is evaluated first. If no deny matches but configured allow rules also do not match, testing is disallowed. With no allow rules, allowlisting is inactive. Configure at task creation or manage and toggle in task Overview.
- Task templates can store a category and task-level deny/allow rules, applying both to new-task forms.

### Operation review

#### Fixed

- Tightened model-review output: reduced `comment` from 500 to 120 Chinese characters and required JSON only, without introductions or code fences. This reduces `MaxTokens` truncation, parse failures and unintended fail-open decisions under the configured model-failure policy.

### Task archives

#### Fixed

- Archives now skip and log symlinks instead of failing the entire task archive. The format supports regular files/directories only; other files still archive normally without following links or leaving the directory tree. Previously any symlink caused complete failure.

### Accounts and compliance

#### Added

- Added a pre-login usage notice/disclaimer dialog requiring agreement before sign-in.

### License and dependencies

#### Changed

- Adopted AGPL-3.0 and expanded README license/disclaimer information.
- Upgraded norma to v0.4.1.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.12] - 2026-09-17

### Exploration timeline

#### Added

- Added an exploration timeline to task details (#144), displaying start/objective/intent/fact/finding/hint/digest nodes with type filters, keyword search, ascending/descending order, pagination and refresh. The first newest-first page updates live; elsewhere only unread counts increase, preserving the reading position, with an “N new updates · Return to latest” action. Daily grouping keeps dates clear in long tasks.
- Timeline rows show node IDs (#145) for graph comparison and precise lookup.
- Expanded timeline details show upstream/downstream relations and anchored assets (#147). Hovering related nodes opens type/status/source/time/summary/payload previews; assets show type labels and readable text. Data arrives with the timeline page, so expansion needs no extra request.
- Timeline search also matches node IDs (#150): plain digits or displayed forms such as `#41` locate exact nodes in addition to content/source search.

### Intent management

#### Added

- Intent deletion supports soft and hard modes (#149), selected in confirmation. Pending, running and paused intents can be deleted with a required reason.
  - Soft deletion (default) marks the intent deleted, stores the reason separately and retains the node, outputs and lineage.
  - Hard deletion physically removes the intent and exclusive descendants supported only by it, cascading output/intent chains to leaves without orphaned data. Token accounting remains archived on original dates. Shared nodes, objectives and task-root facts are retained; confirmation shows the expected cascade count.

  Both modes notify the planner that the user deleted the intent, including the reason, and trigger replanning.

### Operation review

#### Added

- Approval history supports status and decision-source filters (#139).

#### Fixed

- Pending approvals load independently of history pagination (#133), remaining fully visible while browsing older records.

### Agent

#### Changed

- Updated the Worker role description to general cybersecurity-platform wording (#138).

#### Fixed

- Connected Compactor to task planners, restoring cold-digest compaction that previously never ran because the integration was absent.

### Chat

#### Added

- Chat supports @ references to multiple record types, with scrolling pagination for candidates (#135).

#### Fixed

- Constrained long message bubbles to the conversation panel (#137).
- Fixed missing-file uploads before a new conversation exists. Composer snapshots `FileList` into an array before clearing the input and calling back. Previously asynchronous conversation creation resumed after the live input-bound `FileList` had emptied, omitting the file field and returning HTTP 400.

### Traffic

#### Fixed

- Traffic capture proxy now binds only to 127.0.0.1 by default (#129, #130), avoiding an open proxy on other interfaces.

### Web

#### Fixed

- Restored the global header on the statically exported task-list page.
- Added missing traffic mocks to demo finding details, fixing a blank page.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@RuoJi6](https://github.com/RuoJi6)
- [@dingpotian](https://github.com/dingpotian)

## [0.3.11] - 2026-09-15

### Operation review

#### Added

- Approval records can locate their originating tool execution (#125). Clicking Source opens the complete original conversation, pages to the target, expands commands/results and centers a highlight while keeping surrounding messages readable. Manual scrolling stops automatic repositioning. Persisted `tool_use_id` and task mappings, with a new scoped call-ID index, locate ordinary chat, Workers, planners and segmented MainAgent conversations. Missing, duplicate or ambiguous links produce explicit messages instead of navigating to another execution; deleted conversations, archived tasks and absent records are also explained.
- Added approval-history pagination (#115).

#### Changed

- Reduced model-review input (#124, #125) to versioned JSON containing the current complete tool call, explicitly selected brief context and local working directory. Context includes only real current user messages from chat/task MainAgent. Workers omit intent summaries and inherited parent context; planner/automatic sessions do not invent user messages. Task descriptions, objectives, constraints, global exploration state, historical calls and complete Worker intents still serve execution and separate session audit, but are excluded from operation review. Decisions must return JSON `decision`/`comment`, explaining the actual action, successful consequences and matching rule. Instructions embedded in arguments/context cannot alter review policy. Actual submitted input snapshots and fingerprints are saved; old snapshots retain version labels and are never reconstructed from current data.

#### Fixed

- Fenced model verdicts no longer silently fail open (#126). Code fences around returned JSON are stripped before parsing; only a remaining parse failure invokes the configured model-failure policy. Previously fenced JSON failed parsing and could be allowed silently.

### Agent

#### Added

- Added experimental noa context compaction with norma v0.4.0, off by default in system settings. When enabled, model-driven noa replaces built-in compaction for MainAgent, planner, Worker and chat. Original compacted content is archived centrally under `<workDir>/noa/<sessionID>/`, globally unique per session rather than scattered across tasks. Integration failure falls back without interrupting real tasks. The toggle is read once per run and affects only later starts.

#### Fixed

- Exploration tools return an explicit error without task context instead of panicking on a nil store.

### MCP

#### Added

- Added legacy SSE-only MCP server support (#117).

### Traffic

#### Fixed

- `traffic_search` host matching now includes ports, preventing records for the same host on different ports from mixing (#114).
- Isolated `traffic_search` description-migration failures so subsequent reporter migrations continue.

### Web

#### Changed

- Added separators between task finding-retest options for clearer display (#122).

#### Fixed

- Fixed a complete demo task-details crash. The conversation tab requested `GET /api/tasks/<id>/side-questions`, absent from mocks; a plural-path fallback returned `[]`, leaving `data.items` undefined. The side hook's `merge()` threw `TypeError: t is not iterable` in a `setItems` updater, rethrown during React render outside the caller's catch, triggering “This page couldn't load”. Mocks now return explicit empty side history, and `sideAPI.history` normalizes non-array `items` to `[]` as a second defense.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@RuoJi6](https://github.com/RuoJi6)

## [0.3.10] - 2026-09-13

### Network

#### Added

- Added official DeepSeek web search using the active LLM profile. Unlike the other three sources, DeepSeek exposes search only as its Anthropic-compatible server tool `web_search_20250305`, executed remotely. It requires an official DeepSeek endpoint and Anthropic protocol; OpenAI-format endpoints reject server tools. Each search consumes another model call, bypasses the search egress proxy and traffic capture, and returns only titles/links, with WebFetch needed for body text. Settings explain but do not enforce these constraints; users can verify with Test search.

### Agent

#### Added

- Added cross-work review for Workers: `search_all_worker_traces` searches matching steps across this task without knowing intent_id; `get_worker_trace` lists a selected work's steps, searches within it and retrieves full content by step_id. This reuses observations absent from facts and avoids repeated work.
- Added `node_detail` to Workers so intent/node IDs from trace tools can resolve to complete details.
- `add_hint` now explicitly announces the planning turn it triggers. Rather than only folding hints into overview, each call records one “User added N strategic hints” trigger, including batch contents without one event per hint. The planner sees both the cause and content.

#### Changed

- Relaxed global-overview wording to encourage exploratory breadth and prompt cross-intent clues instead of premature convergence.
- Simplified Worker boundaries: an initial obstacle does not establish exhaustion; complete reasonable methods within the intent before concluding.
- Removed per-asset `related` from `insert_assets`. It only controlled task-scope inclusion and was never stored, so re-registration erased its effect and the UI could not show exclusions. It required an extra model judgment that could not be retained.
- `task_scope` no longer depends on the asset-coverage toggle. `insert_assets` always adds automatic scope (`source='auto'`), and `add_task_scope` remains available to planner, task MainAgent and objective decomposition. Scope defines authorization and asset-query filtering; coverage only decides whether to use it as a metric denominator. Previously disabling coverage stopped both auto/agent writes, leaving only manual UI rows. `list_untested_assets` still hides with coverage because it is purely a coverage view.

#### Fixed

- Fixed cross-task asset leakage (#59). `list_assets` had hardcoded task ID 0 and queried the shared database without scope filters the model could supply, misdirecting it toward other tasks' assets, especially IPs. It now returns only this task's and directly associated tasks' `task_scope`, matching ownership rather than literal values: root domains include child domains/services/endpoints; networks include hosts/services. Direct IDs outside scope also fail. Non-task Auto/pentest contexts retain whole-database fallback. Fixed direct-IP URLs such as `http://1.2.3.4/api` missing CIDR ownership. The producer-filtered UI Test assets view is unchanged.
- Fixed an old tool-reduction migration removing newly added Worker trace tools. Removed `search_all_worker_traces`, `get_worker_trace` and `node_detail` from the unbind list and added a one-time restoration for databases that already ran it.
- Fixed intermittent `/btw` checkpoint storage failures by stripping JSONB-unsupported NUL (`\u0000`) escapes before persistence.

### Triggers

#### Fixed

- Deduplicated descriptions/objectives by task in merged trigger sessions so repeated long objectives do not inflate context.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.9] - 2026-09-11

### Agent

#### Added

- Task MainAgent supports multiple interactive conversations with creation, switching and independent context reset.
- Added persistent `/btw` side questions without interrupting the main flow; exchanges survive restart.
- `spawn_task.source_task_ids` lets new tasks inherit source assets/findings read only.

#### Changed

- Disabled cross-engagement Worker memory and narrowed default tools. Context reading and cross-work review belong to planning; Workers execute and record one intent.
- Simplified Worker prompts and asset-tool descriptions; removed `terminated` and `worker_name` from `get_worker_output`.

#### Fixed

- Fixed premature completion after thinking-only turns. A model could emit reasoning without text/tools, yielding natural `end_turn` without `tool_use`; the harness marked an unfinished intent completed with an empty summary. None of five retry layers handled it: no error for stream retry, `err == nil` satisfied the breaker, intent retry required `model_error`, and SDK emptiness counted reasoning events as output. The work Stop hook now recognizes this and injects a continuation instruction, preserving reasoning for the next action. It intentionally does not resend the same request, because this often follows stable prompt/context structure and would merely repeat the thinking.
- This continuation reuses the LLM Retry/backoff empty-response retry count: default 2, `-1` disables it and restores completion on an empty turn. It caps the total per intent, not consecutive turns. The harness already permits only one push per consecutive idle stretch and refreshes that allowance after a real tool turn; the total cap prevents tool → idle → push cycles exhausting the intent budget. Logs record the Worker/intent and continuation count `(n/N)`. The upstream design reference was `docs/LLM重试设计.md` §1.1 (not included in this baseline's tracked documentation).
- Fixed `/btw` context budgets/input layout for long conversations and added a request-ID fallback in insecure, non-HTTPS contexts.

### Traffic

#### Added

- Findings support multiple traffic evidence records with ordering, notes and roles (request/response/supporting evidence).
- Report agents automatically attach relevant traffic before writing reports.
- Added an agent traffic-binding toggle and completed evidence handoffs between agents.

### Tasks

#### Added

- Added conversation-level finding retests in separate agent sessions, with running state in lists/details.

### Interception

#### Added

- Approval records include details and execution audits: tool context, initial model/rule verdict, output and argument fingerprints.

### Assets

#### Added

- Task test assets support DSL search.

### LLM

#### Fixed

- Connection tests now include custom session headers, fixing opencode zen HTTP 400 responses.

### Network

#### Added

- MCP HTTP transport can skip TLS certificate verification for self-signed services.

### UI

#### Added

- Conversation lists group by agent with collapse/expand and independent pinning; chat supports agent filters.
- Attack graphs render digest nodes and fold their members.

#### Fixed

- Fixed blank screens caused by unsynchronized login credentials.
- Interception messages explicitly identify platform controls so they are not mistaken for target defenses.

### Deployment and updates

#### Added

- Added in-page updates: a Version and updates card and top-bar notice. The flow downloads a release, verifies `SHA256SUMS`, smoke-tests, stages, exits and lets the supervisor restart/apply it; the page refreshes automatically.
- Added `start.sh` / `start.bat` supervisors as official upstream entry points, included in release archives/images without changing `install.sh`. They restart by exit code and forward SIGTERM to artex; verification/replacement remain in Go.
- Failed verification/smoke tests discard the update and keep the current version. Three consecutive new-version startup failures roll back. Settings also allow manual rollback; database schemas do not roll back.
- Updates accept only GitHub domains over HTTPS, with a fixed release source; development builds disable them. GitHub results cache for 30 minutes to protect API quota.

#### Known limits

- In upstream Docker deployments, updates replace the program only. Toolchains stay unchanged and recreating a container restores its image version; the historical command was `docker compose pull artex`. ScopeWeaver's current source-build instructions are in README.
- Updates do not synchronize release `skills/`; new bundled skills do not appear automatically.
- Updates restart the process and interrupt running tasks.

### Dependencies

#### Changed

- Upgraded norma to v0.3.7.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@RuoJi6](https://github.com/RuoJi6)

## [0.3.8] - 2026-09-09

### LLM

#### Added

- Added Retry and backoff settings for five nested layers: SDK connection retry before streaming (reset/timeout/429/5xx), SDK empty-content retry (OpenAI format), same-provider replay before any output reaches the caller, round-robin circuit-breaker cooldown after consecutive failures, and whole-intent retry after Worker `model_error`. Inner layers exhaust before outer ones. Each has count/interval controls: blank retains defaults/exponential backoff, count sets attempts, interval selects fixed delay, `-1` disables. The first three support per-profile field overrides of global defaults, so setting only delay still inherits count; breaker/intent retry are process-global. Saves apply live. Blank settings preserve old behavior byte-for-byte (new columns 0; absent settings use defaults). Connection tests omit retries because their hard 30-second timeout could otherwise reject working endpoints. The upstream design reference was `docs/LLM重试设计.md`, absent from this baseline's tracked docs.
- Added per-profile `session_header_key`. When nonempty, every request sends the current session ID (`conv-<id>`, `exp<x>-worker-i<intent>`, etc.) under that header for gateways using prompt caching/sticky routing. IDs come from request context without modifying norma; shared providers can send different session values. Includes old-database migration and a configuration input.

#### Fixed

- Fixed `23502` when saving empty session headers. The `NOT NULL DEFAULT ''` column had incorrectly used `NULLIF($n,'')`; it now receives empty strings directly.

### Agent

#### Added

- Wall-clock timeouts now finalize in place with norma v0.3.6: `MaxDuration` interrupts active tools, then runs configured wrap-up turns on a live context to save findings and summarize, ending as timeout. Worker/planner no longer rely on external `maxDur+90s` contexts that killed stuck runs as `aborted_tools`. Chat inherits the harness behavior; MainAgent without `MaxDuration` is unaffected.
- Added stall recovery: heartbeat/no-change planner wakeups with no open/running intents begin with an explicit no-worker/no-queue warning and must produce at least one nonduplicate intent.

#### Changed

- Moved Worker intent, start instructions and anchored-asset raw JSON into the system prompt, rebuilt each turn and retained through compaction/resume independently of the first transcript message. The initial user message now only contains a degradable, possibly stale global overview. Per-intent system data sacrifices cross-intent cache reuse to preserve intent.
- Simplified planner/Worker defaults. Planner distinguishes `recent_done` states, checks traces before treating blocked/exhausted as dead ends or retrying, treats negative results as tentative observations and checks evidence, requires output when goals remain with no open/running intents, and prioritizes depth over coverage. Workers record negative observations with tentative interpretation; planners decide. Cross-intent clues go into fact summaries rather than being pursued by the Worker. Reseeding appends and activates a new version while retaining custom/old versions for rollback.
- Narrowed Worker tools to single-intent execution/recording. Removed `list_facts`, `node_detail`, `list_companies`, `search_all_worker_traces`, `list_worker_traces`, `get_worker_trace`; retained `list_findings` for deduplication, `add_finding`/`record_fact`, `insert_assets`/`list_assets`. Removed stale `type=tech`/`on_url`/`props` guidance conflicting with schema. A one-time migration removes Worker bindings only, leaving planner/MainAgent bindings.

### Exploration graph

#### Added

- Added cold-digest graph compaction. Old inactive intents/facts fold into overview digest nodes; originals remain permanently addressable by ID, making presentation folding reversible and storage lossless. Reverse reachability and any-live-branch rules classify heat, with R=6-turn debounce and connected components. Background minor compaction covers new cold blocks; major compaction rereads originals and merges fragments. Activity rechecks and cooldown exclusion keep it off active paths and prevent overwriting revived nodes. Overviews expose `cold_digests` and asset-indexed `cold_index`; planner/MainAgent-only `expand_digest`/`expand_index` restore them. Associated/inherited tasks reuse their own folded views, including cross-task read-only expansion. Includes old-database migrations.
- `graph_overview` now includes the full `finding_list`, unlike windowed facts, because confirmed findings are valuable and usually few. Planner/Workers see all each turn without `list_findings`. Entries are `{id, summary, evidence?, from_intent?, assets?}`, with readable URL/domain/ip:port assets instead of bare IDs.

#### Changed

- Removed flat `hosts` from `graph_overview.coverage`, retaining `host_count` and on-demand `list_assets`; up to 500 host strings per turn offered little planning value. Added `done_intents_total` beside the ≤15-entry `recent_done_intents` window so deduplication can detect omitted history.

### Tools

#### Changed

- Moved default `add_company_scope` binding from Worker to planner: company scope is a planner/MainAgent/Auto responsibility. New databases seed mainagent/planner/auto; existing databases receive a one-time migration.
- Planners now default to both `list_assets` (DSL whole-database search) and `list_untested_assets` (untested scope). A one-time backfill preserves user removals.

### Tasks

#### Added

- Added Running Workers to task lists, counting `state='running'` intent nodes exactly as task Overview does, including 0 when none run.

### Network

#### Added

- Added an independent global egress proxy (http/https/socks5, optional `user:pass`). With capture enabled it is the MITM proxy's upstream for both intercepted and passthrough traffic, retaining capture without leaking the source IP. With capture disabled it enters agent Bash/WebFetch settings; `proxyEnv.ALL_PROXY` supports socks5. Stored in settings KV without migration, it has a System Global proxy card separate from search and LLM proxies.

### UI

#### Fixed

- Corrected intent labels: `exhausted` means budget exhausted, not a fully explored direction; step/time limits can leave partial results. `blocked` means execution error after model/API/network retries, not target/WAF blocking. Added missing `stopped` for user-stopped work.

### Dependencies

#### Changed

- Upgraded norma to v0.3.4 for MCP output truncation/disk storage (`651b961`); later v0.3.6 supports in-place timeout wrap-up.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.7] - 2026-08-31

### LLM

#### Added

- Added per-profile output limits and selectable request field names. The limit caps generated tokens per response; 0 omits the field for server defaults. Context window is separate, used locally for compaction thresholds and not sent. Only OpenAI Chat Completions allows a field choice: blank sends `max_tokens`, common in compatible gateways; official o-series/GPT-5 reasoning endpoints require `max_completion_tokens` and reject the former with `unsupported_parameter`. Anthropic fixes `max_tokens`, Responses fixes `max_output_tokens`, so other formats disable/clear the selector. Connected limits across planner, Worker, chat, MainAgent and goal decomposition; previously OpenAI omitted them and Anthropic used SDK 8192 regardless of settings. Like streaming, values resolve each turn and follow failover on the next turn. Defaults 0/empty preserve old behavior.
- Connection tests log every HTTP attempt's status and raw gateway body, capped at 4K, independently of LLM recording. This exposes 401/quota messages, empty frames and HTML responses beyond the UI's compressed ok/error.

#### Changed

- Task LLM chains are editable in every state, including done/failed/timeout, because MainAgent conversations still use them. Removed terminal-state restrictions from HTTP/DB transactions and enabled UI editing. Saving a terminal task no longer reopens quota-blocked intents into unexecuted, non-retryable open states. To continue execution, retry intents or add goals, which return the task to running.

### Agent

#### Changed

- Worker steering messages now reuse pause/resume and transcript resume like MainAgent, replacing a custom persistent intervention protocol that affected scheduler barriers, recovery and many activity filters. Messages enter the next input turn; intents run in a dedicated goroutine outside the three-slot pool. The UI keeps messaging and restores Direct continue, sent over SSE. No schema change. Messages are memory-only without crash recovery, and a steering message can briefly add one concurrent Worker; this was accepted for infrequent use.

### Tasks

#### Fixed

- Fixed large-task archive OOMs by streaming snapshots instead of loading entire tasks. Closed three cold-archive recovery gaps: modern traffic exists only in SQLite, and a crash between PostgreSQL/SQLite commits could strand archived traffic in hot storage. Staging journals now write regardless of legacy directories, and missing `journal.json` is treated as disposable.
- Reduced slow chat/UI loading with many tasks by optimizing task-list/context/exploration queries and duplicate requests in dashboards, conversations and task details.

### Assets

#### Changed

- Removed the 256-rule company-scope cap from backend, frontend and demo mocks. Individually listed IPs/domains reached it unnecessarily and forced company splitting; scope performance did not justify it. Retained 1024 characters per rule and a 2 MiB request cap, roughly 40–50K rules.

### Skills

#### Fixed

- Fixed skill ZIP uploads using bzip2/Zstandard, adding pure-Go decompressors beyond the standard library's Store/Deflate without new external dependencies. Unsupported Deflate64/LZMA/XZ/PPMd and encrypted archives now fail before extraction with a readable Chinese message naming the file/method and repacking instructions rather than a raw English ZIP error.
- Replaced ASCII-only skill path validation with a Unicode denylist, allowing multilingual filenames and spaces. Still rejects controls, invalid UTF-8, zero-width/bidirectional controls (including RLO disguise), `\ % # ? * : " < > |`, `..`, absolute paths and empty segments, preserving traversal protection. Previously one Chinese filename rejected the entire ZIP. Skill names now allow non-ASCII letters; ASCII remains lowercase letters/digits/hyphens per agentskills.io, with no spaces, dots or separators.
- Fixed Chinese ZIP names from Windows tools lacking the UTF-8 flag by decoding GBK before validation. Quoted non-ASCII frontmatter names now parse correctly too.

### UI

#### Added

- Added default All findings flat view alongside task grouping via tabs. A cross-task table supports 10/20/50/100 rows, selection/export, inline evidence/report expansion, editing name/type/severity/disposition, deepening and deletion just like grouped view. Both share stats/filters; switching preserves filters and saves view/filter preferences locally. Polling targets only the active view; inline edits update both caches to avoid stale switches.
- Added By asset view: a left tree of company → root domain/IP → subdomain → service → endpoint, showing company only when owned, and findings for the selected subtree on the right using shared filters/table. Only assets with findings appear; ancestors are reconstructed, counts aggregate/deduplicate findings, and missing/unlinked assets go into Unassociated. Unlike other views it queries only on entry, filter changes, finding edits or manual refresh, avoiding five-second navigation-tree rebuilds. Labels show relative increments (subdomain without root suffix, `https :443`, endpoint path); full values appear in tooltips/breadcrumbs. Excess nodes drop whole endpoint/service levels with a notice while preserving counts in ancestors.

#### Fixed

- Collapsed the conversation list on narrow screens so mobile conversation content is no longer squeezed.

### Dependencies

#### Changed

- Upgraded norma v0.3.2 → v0.3.3 with `Config.MaxTokensField`, allowing Chat Completions to send `max_completion_tokens` for reasoning models. It sends exactly one of the two fields; default remains `max_tokens`.
- Upgraded `golang.org/x/mod` v0.37.0 → v0.40.0, resolving two dependency security alerts.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@neouks](https://github.com/neouks)
- [@begininvoke](https://github.com/begininvoke)

## [0.3.6] - 2026-08-27

### LLM

#### Fixed

- Fixed non-streaming settings appearing to revert (#69). Storage was correct, but the profile-list DTO omitted `streaming`, so undefined became the frontend's streaming default. Restored the field without `omitempty` so false is returned.
- Connection tests now require actual model content and use the configured streaming mode (#65). Previously HTTP success with zero content (reasoning budget exhaustion, safety removal or compatibility-layer loss) passed, and non-streaming profiles were always tested through streaming. Empty replies now fail; successful tests show the reply and reflect the same channel used by conversations.

### Agent

#### Changed

- `list_facts` now paginates and filters keywords (#74): latest 20 by default, `limit` up to 100, `before` cursor, `q` summary search, returning `{facts,total,has_more,next_before}`. Long summaries truncate by character count; `node_detail` retains full text. Worker/planner prompts match pagination. A one-time schema refresh fixes existing tool catalogs because `SeedTool` only inserts initially, otherwise showing no parameters.

### Tools

#### Added

- Tool execution adds statistics (#72): a dialog shows per-tool count, share and failures sorted by count. It uses task/keyword filters over the complete result set, fetching only when opened.

### Dependencies

#### Changed

- Upgraded norma v0.3.1 → v0.3.2: preserved dropped OpenAI reasoning/refusal (including Responses `reasoning_text`), deduplicated three reasoning field names, bounded zero-content retries, and fixed compaction-split tool pairs causing gateway 400s, including existing transcript orphans.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.5] - 2026-08-25

### LLM

#### Added

- Added per-profile streaming toggles, default true. Off uses real `stream:false` complete JSON to avoid faulty SSE empty/reasoning frames, sacrificing live progress/token counts. Worker/planner/MainAgent/chat/goals resolve the active profile dynamically. llmpool, llmrec and task-runtime provider wrappers support non-streaming. Added/migrated `llm_profiles.streaming` default true without changing old profiles.
- Added `openai-responses` profiles alongside Chat Completions/Anthropic, using `POST /v1/responses`, normalized BaseURL and default `gpt-5`. Updated format constraints idempotently and added the UI format option. Upgraded norma to v0.3.1, including `reasoning_content` round-trip repair.
- Captured raw LLM HTTP bodies at transport level, retaining tool schemas, `tool_use` blocks and SSE frames absent from normalized views, including each norma retry attempt. Added/migrated `llm_records.raw_request/raw_response`; details now switch to raw view and copy either body, with `execCommand` fallback in insecure contexts.

### Conversations

#### Changed

- Conversation selection is now a mode: default rows hide checkboxes, the header shows total plus Select, selection mode offers checks/select-all/bulk delete, Done clears and exits. Successful bulk deletion exits automatically; failures retain selection for retry. Individual rename/pin/delete remain in row menus.

### UI

#### Changed

- Improved task/conversation/traffic interactions (#57): persisted task-column sorting, direct rename icons instead of menus, and refined sheets and traffic viewing.

#### Fixed

- Worker asset labels show only domains/IPs and handle empty labels correctly.

### Agent

#### Added

- Added quantitative goal checks before `prove_goal`: compare measured `graph_overview` values (`coverage.pct`, finding counts, etc.) against coverage/flag/privilege requirements. If unmet, dispatch intents to close gaps instead of marking “mostly done” as met. Fixed a 100%-coverage goal accepted at 40%.

#### Changed

- Rewrote zero-intent planning guidance. Previously calling zero intents the most common/important rule encouraged early stopping with unmet goals and untested scope. Zero is now appropriate only when all directions are covered by open/running/recent_done, or the next step awaits current work output and a later graph update. Uncovered independent directions or unmet goals with untested scope must not stop merely because zero intents are common.
- Strengthened evidence requirements for negative Worker conclusions such as no injection, closed port or no login. Exhaust reasonable methods within the intent (encoding/parameters/path/method) first; incomplete methods or weak evidence use `confidence=inferred`, avoiding premature observed negatives that block whole directions, especially early in a task.
- Applied planner/Worker prompt changes through guarded one-time `reseedPlannerPrompt` / `reseedWorkerPrompt` migrations, appending and activating new versions while retaining old/custom history for restoration.

### Operations

#### Added

- Added `reset-password.sh` for the fixed `ARTEX` administrator in local/docker deployments. Connection options are explicit or read from `--dsn`, `$ARTEX_PG_DSN`, config.json. pgcrypto generates compatible bcrypt in the database and writes `settings.auth.password_hash` without restart. Passwords use environment variables rather than argv, with injection-safe quoting.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@neouks](https://github.com/neouks)

## [0.3.4] - 2026-08-24

### UI

#### Added

- Task selections can bulk-change category, including Uncategorized to remove current grouping. One transaction writes the batch; only tasks deleted since selection can fail.

#### Changed

- New-task category selection is searchable. Typing filters existing names; Enter/Create adds missing names immediately. The selected category is a removable tag; only one is allowed.

### Conversations

#### Fixed

- Fixed chat rejection when a conversation has an LLM profile but no global active profile. Preflight now matches execution resolution: conversation/agent binding first, global fallback.
- Configuration errors now distinguish no profiles (“add one”) from inactive profiles (“activate or assign one to this conversation”) rather than always saying unconfigured.

### Agent

#### Added

- When all goals are met, MainAgent `add_intent` work can run directly in Workers without Planner re-proving goals and cancelling it. Once the frontier drains, the task returns to done. Before dispatch, MainAgent asks whether to register the work as a formal goal; doing so resumes ordinary autonomous planning.

#### Changed

- Trace requests with more than five `step_ids` now return the first five rather than error, with `returned_step_ids`, `omitted_step_ids`, and `notice` indicating omissions and that further reads are unnecessary if sufficient. Duplicate/invalid IDs are removed before counting.

#### Fixed

- Task lists sort by descending ID instead of timestamps, avoiding equal-time tasks swapping during ten-second polling and random map iteration.

### Assets

#### Fixed

- Company-ownership recalculation uses safe `try_inet()` instead of `a.ip::inet`. A hostname in `assets.ip` previously raised `22P02`, rolling back company scope edits/deletion; invalid rows now skip safely.
- Invalid IPs are explicitly reported after company-scope saves with counts, IDs and values, explaining exclusion from IP/CIDR ownership. Server logs cover paths without UI responses, including company deletion, ScopeSentry sync and agent writes.
- Task IP/CIDR scope matching also uses `try_inet()`, replacing a character-only regex that let hexadecimal-letter hostnames such as `abc.def` reach the failing cast.

#### Changed

- `ip`, `service` and `endpoint` assets reject hostnames in `ip`. `insert_assets` and asset APIs return indexed per-item errors recommending `type=subdomain` with `domain`, or resolving A/AAAA first. Agents can correct/reinsert; other batch assets still save.

### Contributors

- [@neouks](https://github.com/neouks)

## [0.3.3] - 2026-08-23

### Worker

#### Added

- Running Workers support individual pause/resume/cancel. Pause retains intents/facts/findings; cancel transactionally removes the intent and direct outputs after execution stops.
- Added paused intent state, execution fences and named termination reasons to prevent late blackboard writes after pause/cancel/task deletion.
- Added optional task concurrency limits; creation, recovery, finding deepening and queue refill share persistent FIFO admission.

#### Changed

- Increased default Worker wall-clock run time from 600 to 1200 seconds; revived timeout tasks start a new clock at their next actual execution.
- Real Worker/planner/MainAgent execution now drives task badges, fixing idle labels while Workers run.
- Worker conversations keep individual controls and current-session token totals; model names move beside the current title.

#### Removed

- Removed Worker multiselect/select-all/bulk pause/resume UI and its bulk-control API/mock contracts.
- Removed Worker row token badges/tooltips while retaining the complete ledger and task aggregation APIs.

### LLM

#### Added

- Added ordered task LLM chains with automatic next-profile switching on explicit provider quota exhaustion, persisting current profile, exhausted state and error summary.
- Running/paused tasks can edit/reorder the full chain and manually switch profiles, effective on the next LLM call.
- Automatic/manual switches and full exhaustion produce structured system activity and deduplicated task-stream notices.
- Added task-role model resolution for the next MainAgent/planner/Worker call's profile and model.
- Added optional global LLM Pool with call order, designated fallback, health, cooldown recovery and manual reset.
- Added an always-on usage ledger aggregating input/output/cache tokens by task, conversation, model and profile.

#### Changed

- Goal decomposition, planner, Worker and MainAgent share task LLM runtime; resolution order is described below.
- Failover responds only to explicit quota/balance/billing errors, not ordinary throttling, auth, network, server or context errors.
- LLM settings use cards and a right drawer with Pool order, priority, exclusions, health and recovery controls.
- Chain-exhaustion messages wrap on narrow screens; current model is a title-adjacent text label with full tooltip instead of a list icon.
- Role resolution is Agent binding → task chain → global: explicit role bindings always win, unbound roles use the task chain, and empty chains fall back globally.
- Empty egress proxy means direct connection, no HTTP_PROXY/HTTPS_PROXY fallback; explicit ARTEX_LLM_PROXY is unchanged. Authenticated `socks5://user:pass@host:port` is supported.

#### Fixed

- Transient stream failures before any output delivery safely back off/retry on the same provider, reducing short runs ending model_error without summaries. Quota exhaustion, excessive context and deterministic 4xx errors do not retry here and remain assigned to failover/compaction handling.

#### Removed

- Removed LLM icons from conversation lists to avoid confusion with Worker state icons.

### UI

#### Added

- Tasks support global category creation/rename/delete/filter, with direct selection during creation.
- Added task-template CRUD and a right management drawer; new tasks load preset descriptions/goals or save current content as a template.
- New tasks can link multiple source tasks/company scopes. One multiline field recognizes domains, URLs, IPs, CIDRs, ICP and company keywords.
- Task test assets support live add/delete with manual/company/inherited/agent source records; Worker titles show current assets and source summaries.
- Findings group by task with independent pagination; descriptions can create high-priority deepening intents.
- Conversations support rename/pin/unpin/delete; task lists support current-page bulk pause/resume.
- Traffic/tool details use right drawers. HTTP messages include Host and highlight request lines, status, headers, JSON and markup bodies.
- Task actions add Details and Pause/resume. System settings configure send keys (Enter/Cmd+Enter etc.); web search adds a proxy field.
- Conversation badges show profile names, with model IDs in tooltips. Tasks gain optional names, a name column and search, falling back to descriptions when empty.
- Skills show use counts, last use and missing dependencies to identify inactive/uninstalled skills.

#### Changed

- Task titles are focusable detail links. Task/company assets and finding groups/items use true server pagination, stable order and exact totals.
- Company creation uses a right drawer and multiline scope input with live recognition, validation and type preview.
- MainAgent input auto-grows, sends with Enter, inserts lines with Shift+Enter and avoids sending during Chinese IME composition.
- Agent previews, task reports and details share Markdown rendering; standardized deletion confirmation, long errors and mobile drawer widths.
- Template selectors display/search names but submit IDs. Restored original font sizing and unified displayed version 0.3.3.
- Task/current-session token totals highlight only input, cache-read and output values; send uses a simpler up-arrow icon.
- Task Overview scope displays company names rather than IDs.

#### Fixed

- Ordinary text containing dots is no longer mistaken for an ICP registration number.
- Fixed duplicate editor keys when variable catalogs collide with global runtime names such as `{{.Now}}`.

#### Removed

- Removed separate task-card Enter buttons; titles open details.
- Removed dashboard New task, company Logo URL input, and finding-count badges from headers/groups.
- Reverted the 10% global font increase and removed Worker checkboxes, model icons and row token totals from conversation lists.

### Agent

#### Added

- New tasks can link multiple existing tasks and inherit direct-source goals, facts, findings, completed intents, scope and blackboard context live and read only.
- Blackboard tools query source nodes/facts/findings/traces on demand; inherited nodes show provenance and all write tools reject changes.
- Company scope tools accept domains, URLs, IPs, CIDRs, ICP and company keywords; keywords guide agents but do not assign ownership automatically.
- Finding deepening creates a high-priority manual Worker intent in the original task, anchored to assets with a `derived_from` edge.
- Added goal management in Overview with view/add/edit/delete. Add/edit notifies Planner and revives tasks; deletion hard-removes goal nodes with edges/anchors but does not revive.
- MainAgent defaults to `steer_work` for live Worker corrections without interruption or lost progress, validating intent ownership.

#### Changed

- Source intents do not enter the new frontier; new tasks retain separate explorations, queues, work directories and histories.
- Deleting running intents now stops them as `stopped` without destroying data, requires a reason stored as a linked fact and payload, and notifies Planner via cancelled with intent/reason retained.
- MainAgent chat uses server activity streams, restores input after send failure, and scrolls to the last reply after opening/lazy loading.
- Task pause terminates current MainAgent/planner/Worker calls but allows new user-initiated MainAgent orchestration while paused.
- Cancel/shutdown/stream interruption retain actual reasons, generated content, rounds, duration, tokens and outstanding tool calls.

#### Removed

- Removed optimistic MainAgent client echoes to prevent duplicate/cross-session messages during pause, failure or concurrent activity.

### Constraints

#### Added

- Goal decomposition extracts allow/deny constraints before goals; MainAgent can add them later. They enter planner/Worker system prompts at highest priority, and diverse/broader exploration explicitly remains subject to them.
- Added constraint-management UI/APIs and separate planner/Worker injection toggles, both on by default and read each turn. `task_constraints` uses idempotent creation for existing databases.

### Interception

#### Added

- Added model fallback review when no regex/string command rule matches, returning ALLOW/ASK/DENY with configurable failure/timeout actions. Interception has Rules/Model tabs and model-prefixed decisions with reasons.

#### Fixed

- Hardened verdict parsing so correct DENY decisions are not misread as allow.

### Traffic

#### Changed

- Traffic exchanges now live in SQLite, with large bodies in hash-deduplicated blobs and trigram text indexes for arbitrary substring/Chinese search. Deletion becomes a single SQL transaction, dropping from hours to milliseconds without stopping capture. Added streaming large-body downloads and `traffic_search` full-text parameters. Legacy file trees remain readable/searchable/deletable without migration.

### Tasks and assets

#### Added

- Task creation adds asset coverage, on by default. Off disables calculation/display, shows only assets in the overview graph, stops automatic scope accumulation and removes related planner/MainAgent tools; company links remain unaffected.
- Added per-asset `insert_assets.related`, default true and effective only with coverage. False stores shared assets without adding task coverage, for incidental/unrelated discoveries.
- Company/task scopes share domain/URL/IP/CIDR/ICP/keyword recognition. Domains/IPs create or reuse global assets; CIDR/ICP/keywords remain scope context.

### Build

- `build.sh --release` builds Linux/macOS amd64/arm64 and Windows amd64 together, packaging skills, example config and README in ZIPs.
- Go linker stripping and ZIP compression are standard; UPX is explicitly optional because self-extracting ELF can segfault on some Linux systems.
- Release workflow smoke-tests Linux amd64 startup and includes SHA256SUMS.

### Contributors

- [@neouks](https://github.com/neouks)

## [0.3.2] - 2026-08-20

### Added

- New tasks link multiple direct sources and inherit facts/findings/completed intents/scope/blackboard live and read only, retaining independent queues, directories and histories.
- Ordered task LLM chains fail over on explicit provider quota exhaustion and persist profile, exhausted state and structured audit activity.
- Running/paused tasks can edit/reorder chains or switch profiles manually; automatic/manual/exhaustion notices appear in details.
- Workers support pause/resume/cancel. Pause retains blackboard data; cancel waits for writes to stop then transactionally removes the intent and direct facts/findings/traces.
- Task deletion can also remove linked assets, traffic, findings and files, with deletion barriers, concurrency protection and auditable counts.
- Optional task concurrency limits queue new tasks FIFO and start them when slots free.
- Added task/company asset pagination, category counts and paginated intent mock contracts.
- Added build.sh for single binaries with static frontend export, embedding, cross-platform targets and version injection.

### Changed

- Explicit task chains coexist with global Pool, retaining strict quota failover; without a chain, existing agent bindings/global rules apply.
- MainAgent input auto-grows with Enter send/Shift+Enter newline and Chinese IME composition protection.
- All task roles share LLM runtime; chains use the smallest candidate context window for safe compaction.
- Details show running/idle/paused from actual LLM calls; inherited facts/findings/intents/assets/graph nodes are source-labeled and read only.
- Agent previews, reports and details use shared Markdown rendering.
- LLM profiles use cards/drawers, with independent model-list scrolling inside the drawer.
- Paused tasks allow new MainAgent messages; pause only ends the current turn and does not block independent orchestration conversations.
- Increased default Worker run wall-clock time from 600 to 1200 seconds.

### Fixed

- Replaced optimistic MainAgent console echoes with server-only rendering, fixing mixed/cross-session messages on pause/send failure.
- MainAgent conversations open at the bottom and stay there after the last reply's full lazy-loaded content expands.
- Fixed MainAgent continuing after task pause and orchestration pause failing to stop it.
- Fixed task badges/actions out of sync at completion or during actual planner/Worker/MainAgent execution.
- Fixed late blackboard writes/residual files racing Worker cancellation, task deletion and concurrent writes.
- Fixed Markdown previews, long deletion text overflow, mobile widths and task-detail button alignment.
- Named every agent cancellation reason; activity details show initiator, terminal state, rounds, duration, usage and outstanding tools.
- Fixed parent context racing to overwrite named shutdown reasons and byte-truncated Chinese activity summaries becoming corrupt.
- Cancelled partial streams retain their true termination reason and already generated content.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@neouks](https://github.com/neouks)

[Unreleased]: https://github.com/Autumn-27/ARTEX/compare/v0.3.10...HEAD
[0.3.10]: https://github.com/Autumn-27/ARTEX/compare/v0.3.9...v0.3.10
[0.3.9]: https://github.com/Autumn-27/ARTEX/compare/v0.3.8...v0.3.9
[0.3.8]: https://github.com/Autumn-27/ARTEX/compare/v0.3.7...v0.3.8
[0.3.7]: https://github.com/Autumn-27/ARTEX/compare/v0.3.6...v0.3.7
[0.3.6]: https://github.com/Autumn-27/ARTEX/compare/v0.3.5...v0.3.6
[0.3.5]: https://github.com/Autumn-27/ARTEX/compare/v0.3.4...v0.3.5
[0.3.4]: https://github.com/Autumn-27/ARTEX/compare/v0.3.3...v0.3.4
[0.3.3]: https://github.com/Autumn-27/ARTEX/compare/v0.3.2...v0.3.3
[0.3.2]: https://github.com/Autumn-27/ARTEX/compare/v0.3.1...v0.3.2
