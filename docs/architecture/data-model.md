# §5 Data model

> Part 5 of the architecture document · [back to the architecture index](../architecture.md)

The PostgreSQL schema, its ownership and migrations, and the state that lives outside the database.

#### Contents

- [5.1 Schema ownership and application](#51-schema-ownership-and-application)
- [5.2 Table inventory](#52-table-inventory)
- [5.3 Core relations](#53-core-relations)
- [5.4 Startup migrations and seeds](#54-startup-migrations-and-seeds)
- [5.5 State that is not in PostgreSQL](#55-state-that-is-not-in-postgresql)

---

PostgreSQL is the only application datastore. The traffic index (SQLite) and the evidence, transcript and
workspace files are file-system state, covered in [5.5](#55-state-that-is-not-in-postgresql).

### 5.1 Schema ownership and application

| Question | Answer | Reference |
| --- | --- | --- |
| Where is the schema defined? | One file, `db/schema.sql` (1407 lines), embedded into the binary with `//go:embed schema.sql` | `db/schema.sql:1`, `db/db.go:20-21` |
| Who applies it? | `db.Open` — the only caller is `server.NewManager` | `db/db.go:134`, `server/manager.go:373` |
| When? | On every process start, before any HTTP listener exists | `cmd/artex/main.go:98`, `server/manager.go:373` |
| Is it a migration chain? | No. The whole file is re-executed top to bottom and must be idempotent (`IF NOT EXISTS`, `CREATE OR REPLACE`, `DROP TRIGGER IF EXISTS`); schema changes are additive `ALTER TABLE … ADD COLUMN IF NOT EXISTS` statements appended to the same file | `db/schema.sql:2`, `db/schema.sql:566`, `db/schema.sql:1241-1243` |
| How many such additive migrations exist? | 57 `ALTER TABLE … ADD COLUMN IF NOT EXISTS` statements | `grep -c 'ALTER TABLE .* ADD COLUMN IF NOT EXISTS' db/schema.sql` |
| Concurrency between instances | Session-level `pg_advisory_lock(7337741001)` held on one pinned connection for DDL **and** seeding; deadlock (`40P01`) retried with backoff | `db/db.go:23`, `db/db.go:63-81`, `db/db.go:47-61` |
| Concurrency with runtime transactions | Long archive transactions take `pg_advisory_xact_lock` on the same key, so an archive cannot interleave with startup DDL | `db/db.go:83-92` |
| Database creation | `ensureDatabase` connects to the `postgres` system database and issues `CREATE DATABASE` when the target is missing | `db/db.go:103-130` |
| Connection pool | `maxOpenConns = 32`, lifetime 30 min, idle 5 min — deliberately below PostgreSQL's default 100 | `db/db.go:25-29`, `db/db.go:148-152` |

Two tables are created outside `schema.sql` by Go code, after the schema has been applied: `llm_records`
(`db/commands.go:191`) and `llm_usage` (`db/llm_usage.go:51`). Both are called from `server.NewManager`
(`server/manager.go:386-391`). They are the only schema that a `psql < db/schema.sql` initialisation does
not create.

### 5.2 Table inventory

51 tables exist: 49 declared in `db/schema.sql` (`grep -c 'CREATE TABLE' db/schema.sql` → 49) plus the two
created from Go. They are grouped below by the banner comments the schema file itself uses, so the group
names and line numbers are the author's own. The "Owner" column names the primary file for that table's
queries, derived from identifier references in `db/*.go` and `server/*.go`; it is one or two files, not every
file that reads the table.

| Group (schema banner) | Table | DDL | Owner |
| --- | --- | --- | --- |
| A. Asset layer (`db/schema.sql:25`) | `companies` | `db/schema.sql:28` | `db/companies.go` |
| | `assets` | `db/schema.sql:41` | `db/assets.go` |
| | `company_scope` | `db/schema.sql:116` | `db/companies.go` |
| B. Reasoning / exploration (`db/schema.sql:152`) | `explorations` | `db/schema.sql:154` | `db/exploration.go` |
| | `exploration_nodes` | `db/schema.sql:171` | `db/exploration.go` |
| | `exploration_edges` | `db/schema.sql:249` | `db/exploration.go` |
| | `exploration_anchors` | `db/schema.sql:277` | `db/exploration.go`, `db/task_archives_restore.go` |
| | `task_constraints` | `db/schema.sql:288` | `db/constraints.go` |
| | `activity` | `db/schema.sql:299` | `db/exploration.go` |
| | `main_sessions` | `db/schema.sql:347` | `db/exploration.go` |
| C. LLM profiles (`db/schema.sql:355`) | `settings` | `db/schema.sql:357` | `db/settings.go` |
| | `llm_profiles` | `db/schema.sql:363` | `db/config.go` |
| | `llm_profile_health` | `db/schema.sql:488` | `db/llmhealth.go` |
| D. Task layer (`db/schema.sql:498`) | `task_categories` | `db/schema.sql:502` | `db/task_categories.go` |
| | `tasks` | `db/schema.sql:514` | `db/tasks.go` |
| | `task_archives` | `db/schema.sql:581` | `db/task_archives.go` |
| | `task_templates` | `db/schema.sql:623` | `db/task_templates.go` |
| | `task_relations` | `db/schema.sql:646` | `db/task_archives.go`, `db/task_context.go` |
| | `task_asset_links` | `db/schema.sql:658` | `db/task_assets.go` |
| | `task_llm_profiles` | `db/schema.sql:706` | `db/task_context.go`, `db/config.go` |
| | `task_scope` | `db/schema.sql:749` | `db/task_scope.go` |
| E. Agents / prompts (`db/schema.sql:775`) | `agents` | `db/schema.sql:777` | `db/tools.go`, `db/db.go` |
| | `agent_prompts` | `db/schema.sql:815` | `db/config.go` |
| | `agent_prompt_vars` | `db/schema.sql:831` | `db/config.go`, `db/db.go` |
| F. MCP servers (`db/schema.sql:842`) | `mcp_servers` | `db/schema.sql:844` | `db/config.go` |
| | `mcp_tools_cache` | `db/schema.sql:876` | `db/config.go` |
| G. Visibility (`db/schema.sql:887`) | `agent_visibility` | `db/schema.sql:889` | `db/config.go` |
| | `agent_skill_visibility` | `db/schema.sql:900` | `db/config.go` |
| | `skill_usage` | `db/schema.sql:912` | `db/skill_usage.go` |
| | `tool_usage` | `db/schema.sql:932` | `db/tool_usage.go` |
| H. Built-in tool catalogue (`db/schema.sql:946`) | `tools` | `db/schema.sql:948` | `db/tools.go` |
| I. Conversations (`db/schema.sql:965`) | `conversations` | `db/schema.sql:967` | `db/conversation.go` |
| | `conversation_activities` | `db/schema.sql:983` | `db/conversation.go` |
| J. Agent triggers (`db/schema.sql:1004`) | `agent_triggers` | `db/schema.sql:1006` | `db/triggers.go` |
| | `scheduler_state` | `db/schema.sql:1038` | `db/triggers.go` |
| K. Intercept rules (`db/schema.sql:1044`) | `intercept_rules` | `db/schema.sql:1046` | `db/intercept.go`, `db/db.go` (seeds) |
| | `intercept_pending` | `db/schema.sql:1066` | `db/intercept.go` |
| L. Finding persistence (`db/schema.sql:1092`) | `findings` | `db/schema.sql:1094` | `db/findings.go` |
| | `finding_retests` | `db/schema.sql:1126` | `db/finding_retests.go` |
| | `traffic_evidence_snapshots` | `db/schema.sql:1160` | `db/finding_traffic.go` |
| | `finding_traffic_bindings` | `db/schema.sql:1177` | `db/finding_traffic.go` |
| M. Backend log persistence (`db/schema.sql:1191`) | `server_logs` | `db/schema.sql:1193` | `db/logs.go` |
| Side questions (unbannered, `db/schema.sql:1201`) | `side_question_sessions` | `db/schema.sql:1203` | `db/side_questions.go` |
| | `side_question_requests` | `db/schema.sql:1221` | `db/side_questions.go` |
| Asset intercept rules (`db/schema.sql:1246`) | `asset_intercept_rules` | `db/schema.sql:1255` | `db/asset_intercept.go` |
| Task-level asset rules (`db/schema.sql:1274`) | `task_intercept_rules` | `db/schema.sql:1282` | `db/task_intercept.go` |
| M. Finding IM push (`db/schema.sql:1304`) | `notification_channels` | `db/schema.sql:1319` | `db/notification.go` |
| | `notification_events` | `db/schema.sql:1360` | `db/notification.go` |
| | `notification_deliveries` | `db/schema.sql:1379` | `db/notification_delivery.go` |
| Created from Go, not in `schema.sql` | `llm_records` | `db/commands.go:157` | `db/commands.go` |
| | `llm_usage` | `db/llm_usage.go:30` | `db/llm_usage.go` |

The schema file reuses the letter `M` for two different banners (log persistence and IM push); that is a
naming slip in the file, not two parts of one area.

Counts of record differ across documents: `SPEC.md:37` says 40 tables and an earlier revision of
[3.4](backend-packages.md#34-db) said 50. The number that reproduces is 51, with the command above.

### 5.3 Core relations

58 `REFERENCES` clauses exist in `db/schema.sql`. They fall into five clusters.

**Asset cluster.** `assets.company_id → companies(id) ON DELETE SET NULL` (`db/schema.sql:46`);
`company_scope.company_id → companies(id) ON DELETE CASCADE` (`db/schema.sql:118`). A company is an
attribution bucket, not an owner: deleting one keeps the assets and clears the attribution.

**Exploration cluster.** Everything in the graph hangs off `explorations`, and everything in an exploration
hangs off `exploration_nodes`:

| Relation | Effect | Reference |
| --- | --- | --- |
| `exploration_nodes.exploration_id → explorations(id)` | `CASCADE` — deleting an exploration deletes its graph | `db/schema.sql:173` |
| `exploration_edges.{src_id,dst_id} → exploration_nodes(id)` | `CASCADE` — an edge cannot outlive a node | `db/schema.sql:251-252` |
| `exploration_anchors.{node_id,asset_id}` | `CASCADE` both ways — the node↔asset join | `db/schema.sql:278-279` |
| `task_constraints.exploration_id → explorations(id)` | `CASCADE` | `db/schema.sql:290` |
| `activity.exploration_id → explorations(id)` | `CASCADE`; `node_id` is nullable and `SET NULL`, so a deleted node keeps its transcript rows | `db/schema.sql:301-302` |
| `main_sessions.exploration_id → explorations(id)` | `CASCADE` | `db/schema.sql:348` |

**Task cluster.** `tasks` is the hub: `category_id → task_categories(id) SET NULL`
(`db/schema.sql:517`), `exploration_id → explorations(id) ON DELETE RESTRICT` (`db/schema.sql:521` — the
restriction is what makes deleting the task the only way to reclaim its graph), `llm_profile_id` and
`active_llm_profile_id → llm_profiles(id) SET NULL` (`db/schema.sql:528-529`), `company_id → companies(id)`
(`db/schema.sql:531`). Children cascade from `tasks`: `task_archives` (one row per task, `UNIQUE`,
`db/schema.sql:583`), `task_relations.{task_id,source_task_id}` (`db/schema.sql:647-648`),
`task_asset_links.{task_id,asset_id}` (`db/schema.sql:659-660`), `task_llm_profiles` (`db/schema.sql:707-708`),
`task_scope` (`db/schema.sql:751`).

**Agent cluster.** `agent_prompts.agent_id → agents(id)`, with the agent pointing back at its published
prompt through `current_prompt_id → agent_prompts(id) SET NULL` (`db/schema.sql:817`, `db/schema.sql:828`),
`agent_prompt_vars.agent_id` (`db/schema.sql:833`), `agents.llm_profile_id → llm_profiles(id) SET NULL`
(`db/schema.sql:785`), `mcp_tools_cache.server_id → mcp_servers(id)` (`db/schema.sql:878`),
`agent_visibility.agent_id` and `agent_skill_visibility.agent_id` (`db/schema.sql:890`, `db/schema.sql:901`).

**Finding / evidence / notify cluster.** `findings.task_id → tasks(id) ON DELETE SET NULL` and
`findings.node_id → exploration_nodes(id) ON DELETE SET NULL` (`db/schema.sql:1096-1097`) — a finding
survives the deletion of the task and of the intent that produced it, deliberately, because it is the
deliverable. `finding_retests.finding_id → findings(id) CASCADE` with
`conversation_id UNIQUE → conversations(id) SET NULL` (`db/schema.sql:1128-1129`),
`finding_traffic_bindings.finding_id → findings(id) CASCADE` and
`finding_traffic_bindings.snapshot_id → traffic_evidence_snapshots(id)` (`db/schema.sql:1179-1180`).
`notification_deliveries.{event_id,channel_id}` cascade from `notification_events` and
`notification_channels` (`db/schema.sql:1381-1382`), which is what makes a channel deletable without
orphaning its queue.

Two links matter when reading the schema because they are deliberately *not* plain cascades:
`intercept_pending.rule_id → intercept_rules(id) SET NULL` (`db/schema.sql:1068`) keeps an approval record
after its rule is deleted, and `side_question_sessions` carries a `CHECK` that pins a session to exactly one
parent kind — one of conversation, or one of task+exploration (+optional intent)
(`db/schema.sql:1214-1216`).

### 5.4 Startup migrations and seeds

`db.Open` applies the schema and then seeds, both under the advisory lock (`db/db.go:158-170`). Seeding is
idempotent and never overwrites a user edit.

| Seed | What it writes | Reference |
| --- | --- | --- |
| Built-in agents and variable catalogue | Six `agents` rows (`goals`, `planner`, `mainagent`, `worker`, `auto`, `pentest`) plus their `agent_prompt_vars`; `ON CONFLICT (key) DO UPDATE` on name/description only | `db/db.go:189-216`, `db/db.go:218-243` |
| Renamed-variable cleanup | Deletes the `EngagementTitle`, `CoverageGaps` and `Now` catalogue rows | `db/db.go:238-242` |
| Interactive-shell default | One-shot `UPDATE agents SET interactive_shell=true` for `planner/worker/mainagent/auto`, gated by the `interactive_shell_default_v1` setting so a later user toggle wins | `db/db.go:247-254` |
| Browser MCP | `INSERT … ON CONFLICT (name) DO NOTHING` for the Playwright MCP, disabled | `db/db.go:259-266` |
| ScopeSentry MCP | Seeded *in the schema file* (`§F`), so `psql < db/schema.sql` also gets it | `db/db.go:267-269` |
| Skill visibility | Default visibility rows per agent | `db/db.go:332-347` |
| Built-in intercept rules | Three successive rule sets (v1 at `:349`, v2 at `:544`, v3 at `:602`), each first-insert-only | `db/db.go:270-280` |
| Built-in asset blocklist | Fuzzy domain rules for government/education sites, first startup only | `db/db.go:293-330` |
| Tool catalogue | `SeedTool` inserts a built-in tool's code-defined defaults once; a second path propagates new parameters to an already-seeded row | `db/tools.go:27-66`, `db/tools.go:96-130` |
| Prompt bodies | `seedPrompts` publishes each agent's code-default prompt | `server/assembly.go:194` |
| Interrupted retests | `RecoverFindingRetests` re-drives retests that were in flight when the process died | `db/finding_retests.go:267`, `server/manager.go:382` |

There is no down-migration and no version table. The applied state of a database is "whatever the current
binary's `schema.sql` produced"; an older binary started against a newer database leaves the extra columns in
place, and every column add is written so that a re-run is a no-op (the task layer alone carries several
such backfills, `db/schema.sql:552-570`).

### 5.5 State that is not in PostgreSQL

| State | Location | Why it is outside the database | Reference |
| --- | --- | --- | --- |
| Traffic index | `<data>/traffic/_index/index.sqlite` | The recorder needs an embedded, high-write-rate index next to the blobs; PostgreSQL would add a round trip per recorded exchange | `traffic/traffic.go:176-248`, `traffic/traffic.go:249-295` |
| Recorded bodies | `<data>/traffic/_blobs/` | Content-addressed spilled bodies, read back by range for the UI | `traffic/traffic.go:526-577`, `traffic/traffic.go:687-750` |
| MITM CA | `<data>/traffic/_ca/mitmproxy-ca-cert.pem` | Generated by go-mitmproxy; its path is injected into worker shells | `traffic/traffic.go:354-357` |
| Evidence bodies | `<data>/evidence/blobs/<hash[:2]>/<hash>.bin` | Immutable and content-addressed (SHA-256) so a snapshot cannot be edited after the fact | `evidence/store.go:29-41` |
| Raw LLM transcripts | `<data>/transcripts/` | Large JSON per session, written once | `server/task_llm.go:562` |
| JWT signing key | `<baseDir>/jwt.key`, mode 0600 | Must not be downloadable through the workspace file manager | `server/auth.go:79-118` |
| Skills | `config.SkillDir()` — repo `skills/`, or `/app/skills` in the image | Plain files, edited and uploaded through the UI | `config/config.go:183-195`, `Dockerfile:59` |
| Task working dirs and uploads | `<data>/tasks/<id>/`, `<data>/uploads/` | Agent-authored files, browsable through `/api/workspace/*` | `server/workspace.go:44` |

---

← [architecture index](../architecture.md) · section numbers are stable across the split (§5.x).
