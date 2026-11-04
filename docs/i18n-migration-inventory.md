# Code Context — Go backend language/localization inventory (en+es target)

Scope: Go non-`web` tree at `/GIT/artex-global`. All counts are from non-`_test.go` string
literals unless stated. `grep` for Korean/Han inside `[...]` is unreliable in this
shell (byte-wise range matching) — the numbers below come from a literal scanner.

Baseline totals (string literals containing Hangul / Han, non-test):

| package | ko literals | han literals |
|---|---|---|
| server | 592 | 762 |
| notify | 118 | 26 |
| selfupdate | 64 | 12 |
| report | 53 | 0 |
| config | 41 | 73 |
| intercept | 17 | 17 |
| sidequestion | 10 | 11 |
| db | 6 | 195 |
| cmd | 5 | 0 |
| agent | 7 | 420 |
| llmpool | 1 | 6 |
| traffic | 0 | 77 |
| guard | 0 | 5 |
| evidence | 0 | 5 |
| enrich | 0 | 2 |

(212 of `server`'s 592 and 37 of `config`'s 41 are catalog *copies*; see §2.)

---

## 1) Language-code plumbing

### Supported lists / defaults / normalization

| file:line | symbol | role |
|---|---|---|
| `config/config.go:39` | `SupportedLanguages = ["en","ko","zh","es","ja"]` | backend source of truth for valid codes |
| `config/config.go:44` | `const DefaultLanguage = "en"` | out-of-box language |
| `config/config.go:48-56` | `NormalizeLanguage(code) string` | lower/trim, returns `""` if unsupported (callers fall through) |
| `config/config.go:63` | `var CurrentLanguage func() string` | server-injected hook (`settings` table value) |
| `config/config.go:71-79` | `ActiveLanguage() string` | runtime language for *display strings*: hook → else hard-coded `"ko"` fallback |
| `config/config.go:87-95` | `Language() string` | bootstrap language: `ARTEX_LANG` → config file `language` → `DefaultLanguage` |
| `config/config.go:31-33` | `Config.Language string` (`json:"language"`) | config-file field |
| `config.example.json:24-25` | `_comment_language`, `language: "en"` | documented default |
| `agent/outputlang.go:19` | `SupportedOutputLangs` | duplicate of `config.SupportedLanguages` (separate package, cannot import) |
| `agent/outputlang.go:16` | `const defaultOutputLang = "ko"` | fallback **for the agent package only** (tests) |
| `agent/outputlang.go:22-29` | `normalizeLang` | narrows to `defaultOutputLang` when unknown |
| `agent/outputlang.go:11` | `var OutputLanguage func() string` | server-injected hook |
| `agent/outputlang.go:33-39` | `resolveOutputLang()` | hook → `defaultOutputLang` |
| `agent/outputlang.go:44-50` | `var langPhrase` (ko/en/zh/es/ja → `Korean` …) | language name token **inside the directive** |
| `agent/outputlang.go:53-58` | `var langBare` | short form (`Korean`, `English`, `Spanish` …) |
| `notify/notify.go:51` | `var Lang func() string` | leaf-package hook (notify must stay stdlib-only) |
| `notify/notify.go:53-61` | `labelLang()` | whitelists the 5 codes inline, else `"ko"` |
| `intercept/label_i18n.go:16-22` | `judgeActionLang()` | `config.ActiveLanguage()` + map membership, else `"ko"` |
| `server/manager.go:281` | `settingOutputLanguage = "output_language"` | settings-table KV key |
| `server/errmsg_i18n.go:36-45` | `(*Server).loadOutputLanguage()` | settings row → `config.Language()` |
| `server/errmsg_i18n.go:47-55` | `(*Server).outputLanguage()` | cached read (`atomic.Value`) |
| `server/server.go:203-206` | `outputLang atomic.Value` | per-process cache |
| `server/server.go:76` | `errOutputLanguageInvalid = "unsupported language (must be one of en·ko·zh·es·ja)"` | 400 body for bad code; **mirrored as a catalog key** at `server/errmsg_catalog.go:206` |

### Wiring (who sets what)

| file:line | code | effect |
|---|---|---|
| `server/server.go:317-321` | `s.outputLang.Store(s.loadOutputLanguage()); agent.OutputLanguage = s.outputLanguage; config.CurrentLanguage = resolveLang; notify.Lang = resolveLang` | one closure feeds agent directive + backend catalogs + notify labels |
| `server/server.go:3396-3406` | `settingsPayload()` → `"output_language"` | GET /api/settings echo |
| `server/server.go:3474-3686` | `putSettings` | `OutputLanguage *string` at `:3508`, validate+persist at `:3533-3546` (`NormalizeLanguage` → `SetSetting` → `s.outputLang.Store`) |
| `db/settings.go:9` / `:21` | `GetSetting` / `SetSetting` | storage (77 call sites in server/) |
| `agent/prompt.go:80-98` | `langDirective(code)` | Chinese directive tail; two bodies (zh vs non-zh) |
| `agent/prompt.go` callers | `agent/chat.go:87`, `agent/goals.go:82`, `agent/mainagent.go:105`, `agent/planner.go:344`, `agent/worker.go:293` | append `langDirective(resolveOutputLang())` to the rendered system prompt |
| `agent/wrapup.go:109-118` | `withWrapupLang(prompt)` | appends the tail to settlement prompts; **skipped when lang=="ko"** |
| `agent/wrapup.go:96`, `:175`, `:184` | call sites | settlement + task-timeout wrap-up |

Flow: `ARTEX_LANG` / `config.json:language` → `config.Language()` → (overridden by settings row
`output_language`) → `loadOutputLanguage()` → `s.outputLang` cache → three consumers
(1) `agent.OutputLanguage` → `langDirective` tail on every user-facing prompt,
(2) `config.CurrentLanguage` → `trMsg` (`writeErr`) + `intercept.judgeActionLang`,
(3) `notify.Lang` → severity/status labels. UI reads/writes the same row via
`GET /api/settings` (`output_language`) and `PUT /api/settings`.

Also out of scope but must agree: `web/src/i18n/config.ts:3` (`LOCALES`), `web/src/scripts/check-i18n-keys.ts:15`, `web/src/scripts/i18n-keys.test.ts:8`, `web/src/i18n/config.test.ts:7`, `web/src/lib/types.ts:952`.

---

## 2) Translation catalogs (6 total)

| file:line | var | shape | entries | key | lookup |
|---|---|---|---|---|---|
| `server/errmsg_catalog.go:7` | `errCatalog` | `map[ko]map[lang]string` | **212** | byte-exact Korean literal | `trMsg` (`server/errmsg_i18n.go:19-27`) via `config.ActiveLanguage()` |
| `config/msg_i18n.go:8` | `cfgCatalog` | same | **4** | Korean literal | `trCfg` (`:189`) via `config.Language()` |
| `config/msg_i18n.go:34` | `logCatalog` | same | **33** | Korean log template | `config.T` (`:201`); server wrapper `logT` (`server/errmsg_i18n.go:30`) |
| `notify/notify.go:65` | `severityLabels` | `map[lang]map[enum]string` | 5 langs × 4 | enum (`critical`…) | `SeverityLabel` (`:83`) |
| `notify/notify.go:73` | `statusLabels` | same | 5 × 9 | enum (`pending`…) | `StatusLabel` (`:91`) |
| `intercept/label_i18n.go:8` | `judgeActionLabels` | same | 5 × 3 | enum (`allow`/`deny`/`ask`) | `intercept/intercept.go:509` |

`cfgCatalog` + `logCatalog` = **37** entries (4 + 33). Every catalog entry has all four of
`en/zh/es/ja`; nothing is missing and no `en` value equals its key.

### Key contract
- Key = **the exact Go string literal the call site passes** (Korean), including leading/trailing
  spaces and `%` verbs. `ko` is never stored — a `ko` (or unknown, or unwired) lookup returns the key.
- `trMsg` is called **inside `writeErr`** (`server/server.go:4079-4084`), so every
  `writeErr(w, code, <korean literal or const>)` is translated implicitly.
  Only the 43 explicit `trMsg(...)` sites (all `fmt.Sprintf`/`fmt.Errorf`/concat templates) are needed.
- Templates: translate **the template first**, then format — `fmt.Errorf(trMsg(errXxxFmt), a, b)`
  (`server/asset_intercept.go`, `server/engine.go`, `server/notifier.go`…). Verb order is enforced by
  `server/logt_catalog_test.go:89` (`TestErrCatalogFormatVerbsMatch`) and `config/msg_i18n_test.go:76`.
- `logT`/`config.T` wrap the template only: `log.Printf(logT("[auth] … %s"), v)`.
- Coverage verified: **all 212 `errCatalog` keys and all 37 `cfg`/`log` keys appear as literals in
  non-test source; zero keys exist only inside the catalog file.** (No key is a synthetic constant.)

---

## 3) Call-site census

### `writeErr` (34 non-test files, **868** call sites)
Argument shapes: 340 `err.Error()` (untranslatable passthrough), 205 string literal,
164 identifier (const/var), 15 `fmt.*`/`trMsg`.

| file | sites | | file | sites |
|---|---|---|---|---|
| `server/server.go` | 194 | | `server/finding_retests.go` | 17 |
| `server/server_mgmt.go` | 143 | | `server/customtool.go` | 16 |
| `server/intercept.go` | 47 | | `server/findings_groups.go` | 15 |
| `server/assets.go` | 41 | | `server/finding_traffic.go` | 15 |
| `server/auth.go` | 33 | | `server/sync_scopesentry.go` | 14 |
| `server/notify_api.go` | 32 | | `server/task_archives.go` | 13 |
| `server/conversations.go` | 32 | | `server/asset_intercept.go` | 13 |
| `server/side_questions.go` | 31 | | `server/commands.go` | 12 |
| `server/workspace.go` | 27 | | `server/triggers.go` | 11 |
| `server/goals_api.go` | 23 | | `server/task_assets.go` | 11 |
| `server/task_categories.go` | 19 | | `server/task_archive_package.go` | 9 |
| `server/intent_intervention.go` | 19 | | `server/scheduler.go` | 9 |
| `server/constraints_api.go` | 19 | | `server/update.go` | 8 |
| `server/task_templates.go` | 18 | | `server/task_llm.go` | 8 |
| `server/task_intercept.go` | 18 | | others (9 files) | ≤8 each |

### Other translators
| symbol | call sites |
|---|---|
| `trMsg(` | 43 (non-test) — `server/server.go` 13, `server/notifier.go` 8, `server/engine.go` 4, `server/asset_intercept.go` 3, `server/sync_scopesentry.go` 3, `server/auth.go` 2, then 1 each in `intercept.go, mcpdiscover.go, skill_zip.go, task_categories.go, task_control.go, task_metadata.go, task_templates.go, chat_mentions.go` |
| `logT(` / `config.T(` | 34 — `server/auth.go` 18, `server/mcpdiscover.go` 3, `server/manager.go` 3, `cmd/artex/main.go` 3, `server/orchestration.go` 2, 1 each in `server.go, customtool.go, task_control.go` |
| `trCfg(` | 4 (`config/config.go:203,207,210,212`) |

### Non-`server` packages: own Korean user-facing strings, no catalog

| package | ko literals | own catalog? | notes |
|---|---|---|---|
| `notify` | 118 | **no** (only enum labels) | raw Korean in `telegram.go` 16, `email.go` 14, `notify.go` 13, `http.go` 13, `feishu.go` 11, `markdown.go` 11, `dingtalk.go` 10, `html.go` 8, `webhook.go` 7, `mask.go` 6, `wecom.go` 5, `render.go` 2, `event.go` 1, `filter.go` 1. Message titles/batch intros/validation errors are Korean-only |
| `selfupdate` | 64 | no | `stage.go` 31, `bootstrap.go` 23, `github.go` 9, `selfupdate.go` 1 |
| `report` | 53 | no | `findings.go` 41 (markdown/CSV report labels: `Uncategorized`, `Vulnerability Summary Report`, `Critical/High/Medium/Low`…), `report.go` 12 |
| `intercept` | 17 | partial (`judgeActionLabels` only) | `intercept.go` 14 (validation msgs), `label_i18n.go` 3 |
| `sidequestion` | 10 | no | `context.go` 6 (`errors.New(...)` summaries), `service.go` 3, `request.go` 1 |
| `db` | 6 | no | Korean + 77 Chinese `errors.New`/`fmt.Errorf` (e.g. `db/chat_mentions.go:25` `invalid pagination offset…`, `db/company_scope.go:116`) — surfaced to users via `writeErr(w,400,err.Error())` |
| `agent` | 7 | no | incl. `agent/worker.go:154 settleWrapUpPrompt` (Korean settlement prompt) |
| `cmd` | 5 | no | `cmd/artex/main.go:40-41` banner (`AI Autonomous Penetration Testing System`, `version %s`) |
| `llmpool` | 1 | no | `pool.go` exhausted-error text |
| `guard`, `evidence`, `traffic`, `enrich` | 0 ko | no | Chinese strings only (agent-facing) |

Raw, **untranslated** Korean in `server` itself: 380 literals outside the catalog file; 209 are
catalog-key values, **171 are not in any catalog** (returned as Korean regardless of the setting):
`conversations.go` 18, `server_mgmt.go` 14, `notify_api.go` 13, `task_archives.go` 12,
`side_questions.go` 12, `auth.go` 18, `intercept.go` 8, `update.go` 7, `task_resolution.go` 6,
`goals.go` 4, `manager.go` 4, `orchestration.go` 4, `sync_scopesentry.go` 4, `skill_zip.go` 5,
`assets.go` 3, `mcpdiscover.go` 3, `task_control.go` 3, `workspace.go` 3, `engine.go` 2,
`findings_groups.go` 2, + 12 files with 1 each (`engine_timeout.go`, `finding_retests.go`,
`goals_api.go`, `intent_intervention.go`, `task_archive_package.go`, `task_assets.go`,
`task_categories.go`, `task_intercept.go`, `task_metadata.go`, `task_templates.go`,
`webui_stub.go`, `customtool.go`).

---

## 4) Non-comment non-English payloads

### 4a. Chinese agent prompt bodies (raw strings, seeded into DB)
| file:line | symbol | size | audience |
|---|---|---|---|
| `agent/planner.go:298-340` | `plannerDefaultTmpl` | 43 lines | planner system prompt |
| `agent/promptcatalog.go:15-25` | `autoDefaultTmpl` | 11 | Auto agent |
| `agent/promptcatalog.go:31-64` | `pentestDefaultTmpl` | 34 | pentest agent |
| `agent/promptcatalog.go:70` | `DefaultAssistantPrompt` | 1 (long line) | chat assistant — explicitly says “Answer in concise, accurate English” |
| `agent/promptcatalog.go:76-94` + `:107-124` | `ReporterDefaultPrompt` | 19+17 | reporter |
| `agent/mainagent.go:87-101` | `mainAgentDefaultTmpl` | 15 | main agent |
| `agent/worker.go:230-243` | `workerDefaultTmpl` | 14 | worker |
| `agent/goals.go:19-46`, `:53-67` | `goalsDefaultTmpl`, `goalsScopeTail` | 28+15 | goal decomposition |
| `agent/compaction.go:533-548` | `compressionSystemPrompt` | 16 | context compaction |
| `agent/retester.go:4-15` | `RetesterDefaultPrompt` | 12 | retest agent |
| `agent/prompt.go:80-98` | `langDirective` (both branches) | 2 long lines | output-language mandate |
| `intercept/prompt.go:10-18`, `:32-40`, `:50-128` | `JudgeContextBoundary`, `JudgeOutputContract`, `DefaultJudgePrompt` | 97 | LLM judge |
| `agent/tools.go` (140 han literals), `agent/tools_insert.go` (88), `agent/toolcatalog.go` (41), `agent/tools_digest.go` (3) | tool `desc`/schema descriptions passed to `str()/obj()` | — | seeded into `tools.description` |
| `agent/terminalreason.go:31+` | `reasonHint map[TerminalReason]string` | 37 han literals | run-reason text shown to planner |
| `server/platform_tools.go` (52) | platform tool descriptions | — | seeded into `tools` |
| `server/orchestration.go` (87) | orchestration tool descriptions | — | seeded into `tools` |
| `traffic/traffic.go:134` | `TrafficSearchDescription` | 1 long line | seeded tool description |
| `guard/guard.go:124,128,148-150` | `systemBlockMessage(...)` + reason strings | 5 | **deliberately Chinese** (anti-bypass framing, `docs`: see comment `guard/guard.go:111-118`) |
| `sidequestion/context.go` | `summaryInstruction`, `[Previous summary]`/`[New material excerpts]` framing (comment at `:16-19`) | 11 | side-question compaction |
| `db/db.go` 66, `db/company_scope.go` 22, `db/asset_dsl.go` 11, and ~12 other `db/*.go` | error strings (`DSL syntax error`, `unknown field`, …) | 195 | user-visible through `writeErr(w,400,err.Error())` |
| `evidence/store.go:55,79,82,252,320` | hash/length errors (Chinese) | 5 | user-visible via API |

### 4b. DB seeds
| file:line | payload |
|---|---|
| `db/schema.sql:669,687` | `task_asset_links.source_summary` = `'auto-linked during task execution'` / `'migrated from historical task asset links'` (user-visible) |
| `db/schema.sql:1134` | `finding_retests.error = 'retest session deleted'` |
| `db/schema.sql:1076` | `reason LIKE '[模型]%'` — **literal marker**, not translatable (see `intercept/label_i18n.go` comment) |
| `db/schema.sql:859-863` | MCP seed `'ScopeSentry'` (identifier, keep) |
| `server/assembly.go:194-203` | `seedPrompts` → `agent.BuiltinPromptSeeds()` into `agent_prompts` (Chinese bodies) |
| `server/assembly.go:~211+` | `wireTools` → `agent.BuiltinToolSeeds()` + `traffic.SeedToolMetas()` into `tools` |
| `db/config.go:743-753` | `SeedPromptIfEmpty` — **first-insert only** (`current_prompt_id` set ⇒ no-op) |
| `db/tools.go:31-43` | `SeedTool` — `ON CONFLICT (key) DO NOTHING` |

### 4c. Tests pinning language (51 `*_localized_test.go` + 5 others)
All 51 assert **Korean output** (Hangul present, Han absent) via per-package helpers:
`config/config_localized_test.go:30`, `intercept/intercept_localized_test.go:32`,
`llmpool/pool_localized_test.go:82`, `notify/notify_localized_test.go:30`,
`selfupdate/selfupdate_localized_test.go:38`, `server/intercept_archive_localized_test.go:12`,
`sidequestion/sidequestion_localized_test.go:13`. Some pin exact Korean literals
(`notify/notify_localized_test.go` severity/status labels; `server/auth_localized_test.go`
`authErrUnauthorized`; `server/conversations_localized_test.go` default conversation titles).

| file | #tests | what it pins |
|---|---|---|
| `config/config_localized_test.go` | 2 | `PostgresDSN` error + source string are Korean; sets `ARTEX_LANG=ko` (`:50`, `:77`) |
| `db/finding_retests_localized_test.go` | 1 | retest reason labels |
| `db/task_assets_localized_test.go` | 1 | manual task scope summary |
| `intercept/intercept_localized_test.go` | 4 | messages, `judgeActionLabel`, default message, approval summary |
| `llmpool/pool_localized_test.go` | 1 | exhausted error |
| `notify/dingtalk_feishu_wecom_localized_test.go` | 5 | Feishu item lines/card buttons, DingTalk card, China-platform validation/send errors |
| `notify/html_email_localized_test.go` | 3 | HTML item labels, batch intro, email validate |
| `notify/markdown_localized_test.go` | 4 | title, batch intro, item labels, footer |
| `notify/mask_filter_localized_test.go` | 4 | filter validate, unknown-kind update, destination-changed, masked-container reject |
| `notify/notify_localized_test.go` | 4 | `SeverityLabel`, `StatusLabel`, item title placeholder, asset line |
| `notify/webhook_http_telegram_localized_test.go` | 8 | Telegram labels/title, webhook/telegram validate, `validateHTTPURL`, redaction helpers, HTTP status errors |
| `report/report_localized_test.go` | 4 | markdown report, empty report, findings markdown, findings CSV |
| `selfupdate/selfupdate_localized_test.go` | 3 | check-URL, fetch-latest, stage-missing-asset errors |
| `sidequestion/sidequestion_localized_test.go` | 3 | outputs Korean **and** `TestSideQuestionBrainPreserved` pins the Chinese brain prompt |
| `server/asset_intercept_localized_test.go` | 1 | asset-intercept errors |
| `server/assets_localized_test.go` | 3 | error consts, task-asset provenance, handler responses |
| `server/auth_localized_test.go` | 2 | `requireAuth` 401 body via real HTTP, auth consts |
| `server/chat_mentions_localized_test.go` | 1 | mention errors |
| `server/chatupload_localized_test.go` | 2 | upload consts + handler responses |
| `server/constraints_api_localized_test.go` | 2 | consts + handler responses |
| `server/conversations_localized_test.go` | 6 | consts, oversized body, default titles, `isDefaultConversationTitle`, retest reasons, transcript summary |
| `server/customtool_localized_test.go` | 3 | create/test-run validation |
| `server/engine_localized_test.go` | 2 | `controlWork` no-running-work, engine errors |
| `server/finding_retests_localized_test.go` / `finding_traffic_localized_test.go` / `findings_groups_localized_test.go` | 1/1/2 | error consts, body-too-large, follow-up audit summary |
| `server/goals_api_localized_test.go` / `goals_localized_test.go` | 2/1 | goal consts, handlers, resume errors |
| `server/intent_intervention_localized_test.go` | 2 | consts, `sendWorkerMessage` validation |
| `server/intercept_archive_localized_test.go` | 1 | archive errors |
| `server/manager_localized_test.go` | 1 | `SetWorkers` error |
| `server/mcpdiscover_localized_test.go` | 1 | MCP connect errors |
| `server/notifier_localized_test.go` | 3 | delivery reasons, snapshot parse, all-unparseable |
| `server/notify_api_localized_test.go` | 3 | consts, channel lookup, test message |
| `server/reporter_seed_localized_test.go` | 1 | seeded reporter labels |
| `server/runtime_activity_localized_test.go` | 1 | activity summaries |
| `server/server_localized_test.go` | 4 | error consts, handler responses, chat-unavailable reason, command parsing |
| `server/server_mgmt_localized_test.go` | 8 | consts, API format, skill sentinel, PG gate 503, bad task id, list-models no key, template validate, global prompt vars |
| `server/side_questions_localized_test.go` / `skill_zip_localized_test.go` / `webui_stub_localized_test.go` | 1/1/1 | API errors, zip errors, stub error |
| `server/sync_scopesentry_localized_test.go` | 3 | consts, formatted, ingest parse |
| `server/task_archive_package_localized_test.go` | 1 | staged+original coexist error |
| `server/task_categories_localized_test.go` | 2 | consts + handler |
| `server/task_control_localized_test.go` | 4 | consts, apply responses, batch control, intent-state wrap |
| `server/task_metadata_localized_test.go` | 2 | consts + update responses |
| `server/task_resolution_localized_test.go` | 1 | LLM resolution error |
| `server/task_templates_localized_test.go` | 2 | consts + responses |
| `server/triggers_localized_test.go` | 2 | consts + `validateTrigger` |
| `server/update_localized_test.go` | 3 | update consts, progress messages, rollback-in-progress |
| `server/workspace_localized_test.go` | 2 | consts + handler |

**Only these actually enumerate language codes** (they break first when ko/zh/ja are dropped):
- `config/msg_i18n_test.go:47` — `for _, lang := range []string{"zh","es","ja"}`
- `server/errmsg_i18n_test.go:46` — `{"en","zh","es"}` (+ `"fr"` at `:58` asserting ko fallback)
- `server/logt_catalog_test.go:72` and `:129` — `{"en","zh","es"}`
- `config/msg_i18n_test.go:18-33` — default = English, `ARTEX_LANG=ko` = Korean
- `agent/prompt_test.go:90-95` (`langMarker` for 5 codes), `:100-133` (`TestLangDirectiveLocalized`), `:140-175` (`TestLangDirectiveAppendedToUserFacingRoles`)
- `server/output_language_test.go` — cache follows `PUT /api/settings`
- **`server/testmain_test.go:33` sets `os.Setenv("ARTEX_LANG","ko")` for the whole server test binary** — the single most load-bearing line for the existing suite.

---

## 5) Mechanically-safe transformation sequence

Proposed order (each step compiles and keeps tests explainable):

1. **Shrink the code lists.** `config/config.go:39` → `[]string{"en","es"}`; `config/config.go:71-79`
   `ActiveLanguage` fallback `"ko"` → `"en"`; `agent/outputlang.go:19` → `{"en","es"}` and
   `:16 defaultOutputLang` `"ko"` → `"en"`; `notify/notify.go:56` case list → `"en","es"`.
   `NormalizeLanguage` already rejects anything else, so nothing else needs editing to *reject* ko/zh/ja
   — but existing rows may still hold `ko`/`zh`/`ja` (see 6).
2. **Re-key the catalogs to English.** For `errCatalog` (212) / `cfgCatalog`+`logCatalog` (37):
   `catalog[old_en_value] = {"es": <es>}` and rewrite the source literal to the same English string.
   That is mechanically safe **because**:
   - every key is currently a real literal in non-test source (verified, 0 orphans), so `sed`-style
     replacement of `"<ko>"` → `"<en>"` hits exactly the right places;
   - `trMsg` already returns the key unchanged on a miss, so a half-migrated file degrades to
     English, not to Korean.
3. **Then** `trMsg`/`trCfg`/`T` become `map[en]map[lang]string` with only `es`; the `ko` branch and the
   "ko is the key" comment blocks (`server/errmsg_i18n.go:1-17`, `config/msg_i18n.go:1-7`) must be rewritten.
4. **Translate the 171 non-catalog server literals + 118 notify + 64 selfupdate + 53 report + 17
   intercept + 10 sidequestion + 6 db + 7 agent + 5 cmd + 1 llmpool** directly to English (no catalog
   needed if en is the only source).
5. **Enum label maps**: keep `notify.SeverityLabel`/`StatusLabel`/`judgeActionLabel` but drop the
   `ko`/`zh`/`ja` rows and make `"en"` the fallback (`labelLang()`, `judgeActionLang()`).
6. **Normalize persisted language**: a one-time settings migration
   (`UPDATE settings SET value='en' WHERE key='output_language' AND value NOT IN ('en','es')`) plus the
   same for `config.json:language` users — otherwise `loadOutputLanguage` falls back through
   `config.Language()` and silently normalizes to `""`→`en`, which is fine, but the stale DB row stays.

### Where this mechanically breaks (call out before editing)

| hazard | evidence | consequence |
|---|---|---|
| **3 duplicate English values in `errCatalog`** — `"Failed to save: "` ← `Failed to save: ` + `Could not save: `; `"No API Key was provided"` ← `API Key was not provided` + `API Key was not provided`; `"key must start with a lowercase letter and may contain only lowercase letters, digits, and underscores"` ← `key must be …` + `key must be …` | computed over all 212 entries | re-keying by en value **merges two keys into one**; must dedupe to a single source literal per group (they are the same message with different spacing) |
| **23 keys are fragments with edge whitespace**, concatenated with dynamic text | e.g. `Failed to save: ` (`server/auth.go:135,146`), ` (confirm and retry to overwrite)`, ` (64 characters or fewer, …)`, `Tool not found: `, `A skill with this name already exists: ` | English fragment must keep the same leading/trailing space, or messages concatenate without a separator; also `trMsg(prefix)+err.Error()` sites must translate only the prefix |
| **1 key is a prefix of another** — `Invalid skill name` vs `Invalid skill name (taken from the name field in SKILL.md): ` | `errCatalog` | any prefix-matching helper would be wrong; exact-key lookup (current) is required |
| **30 keys contain format verbs** (`%s %d %v %q %w`, incl. multi-verb `%s%s%v`, `%d%s%w`) | `TestErrCatalogFormatVerbsMatch` (`server/logt_catalog_test.go:89`) | when rewriting the source literal to the English template the verbs and their order must be preserved verbatim, and the `fmt.Errorf(trMsg(t), args…)` call sites must keep their arg order |
| **0 entries have `en == key`** (and 0 entries are missing a language) | computed over all 249 entries | no entry is "already English", so every value needs a human translation; no free wins |
| **`errCatalog` and `cfgCatalog`/`logCatalog` are disjoint** (`intersection = 0`) | computed | the same English sentence may appear in both catalogs; a merged catalog needs an explicit dedupe pass, or keep two maps |
| **`server/server.go:76 errOutputLanguageInvalid` is both a Go const and a catalog key** (`errmsg_catalog.go:206`), and its text embeds the language list `en·ko·zh·es·ja` | same file | editing the const without the catalog key silently returns the stale Korean sentence |
| **`agent/prompt.go` zh branch** exists solely because `zh` was a display language | `prompt.go:84-89` | with ko/zh/ja gone, the zh branch and the “never emit Chinese” clause should collapse to one body; `agent/prompt_test.go:113-131` asserts both branches |
| **`config/msg_i18n.go` and `agent/outputlang.go` duplicate the supported list** (`config` cannot be imported by `agent`?) | `agent/outputlang.go:19` | two lists must be edited together; there is no single source |
| **`notify` whitelists codes inline** instead of calling `config.NormalizeLanguage` | `notify/notify.go:56` | third copy of the list |
| **`agent/wrapup.go:113-115` skips the tail when `lang == "ko"`** | benchmark-drift guard | once `defaultOutputLang` becomes `en`, this branch silently drops the directive for English (the new default) — it must become `"en"` or be removed |
| **Seeded DB rows are write-once** | `db/config.go:743-753`, `db/tools.go:31-43` | translating `agent/*.go` prompt bodies and tool descriptions does **not** reach existing installs; a one-time `reseed*` migration per agent is required — copy the existing pattern: `server/orchestration.go:571,601,632,662` (`settings` flag + `ResetPromptToDefault`), `:530-552` (`RefreshToolDefaults`) |
| **`writeErr(w, 400, err.Error())` × 340** | census | lower-layer `db`/`evidence` errors are Chinese/Korean and bypass all catalogs; translating only `server` leaves ~77 Chinese + 6 Korean messages reachable from the API |
| **`db/schema.sql:670,688,1134` seed Chinese `source_summary`/`error` text** | SQL literal | already written into existing DBs; needs an `UPDATE` migration, not a schema edit |

### Do NOT translate (deliberate)
- `guard/guard.go:148-150` `systemBlockMessage` (+ `:124`,`:128` reasons) — documented anti-bypass framing.
- `guard/guard.go:111-118` comment explains one string serves both agent input and audit display (`GET /api/audit → entries[].reason`).
- `intercept/label_i18n.go:5-7` markers: `allow/deny/ask` enum keys and `"[模型]"` source marker.
- `db/schema.sql:1076` `LIKE '[模型]%'`.
- The reasoning bodies themselves (`segment [A]`) — the existing design keeps them and only localizes the *display* surface through `langDirective`; translating prompt bodies to English is a **behavior/benchmark change**, not a mechanical one. Flag as decision required.

---

## Start Here
Open `server/errmsg_i18n.go:19-55` together with `server/errmsg_catalog.go:7` and `config/msg_i18n.go:8`
— that is the whole lookup contract — then `config/config.go:39-95` + `agent/outputlang.go` for the
code lists, then `server/server.go:317-321` for the wiring. Run
`export PATH=/tmp/pi/go/bin:$PATH; go test ./config/ ./server/ -run 'Catalog|Lang|Localized'`
after any catalog edit; `server/errmsg_i18n_test.go`, `config/msg_i18n_test.go` and
`server/logt_catalog_test.go` are the fast canaries.
