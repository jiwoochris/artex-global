# LLM provider templates

ScopeWeaver supports four profile formats: **Anthropic**, **OpenAI Chat Completions**, **OpenAI Responses** and native **Ollama**. Every format keeps its custom base URL, proxy, session header, retry, rate and concurrency settings, so cloud providers, gateways and self-hosted OpenAI-compatible servers keep working unchanged. Ollama is optional, and cloud providers remain the default-compatible path.

A template fills a **new profile's form** without saving credentials, activating a profile, or modifying existing profiles. Ollama templates also read installed models from the selected server; cloud templates make no requests. Format, base URL and model stay editable after any template.

## Z.ai GLM-5.3

![GLM-5.3 new-profile template](../screenshots/en/llm-templates.png)

Template preview with no API key entered; this is not a live provider connection.

Open **LLM → New → Configuration template**. Choose **General API (recommended)** for API billing, or **Coding Plan (reference)** only after obtaining Z.ai authorization for ScopeWeaver.

| Field | General API | Coding Plan reference |
| --- | --- | --- |
| Format | OpenAI Chat Completions | OpenAI Chat Completions |
| Base URL | `https://api.z.ai/api/paas/v4` | `https://api.z.ai/api/coding/paas/v4` |
| Model | `glm-5.3` | `glm-5.3` |
| Thinking | `enabled` | `enabled` |
| Reasoning effort | `low` | `low` |
| Context window (K) | `0` (200K default) | `0` (200K default) |
| Output limit | `16384` | `16384` |

The endpoints and billing are distinct: General API usage is separate from Coding Plan subscription quota. See [Z.ai connection configuration](https://zcode.z.ai/en/docs/configuration). Confirm your account's model access and billing before testing.

GLM-5.3 accepts text, has a 1M-token context window, and requires reasoning to remain enabled. Supported effort values are `low`, `high`, and `max`; this template uses `low`, which [measured](#measured-effect-glm-proxy) well under half the tokens of `max` on a small task with the same finding. Raise it for harder tasks. The model field remains editable, so review reasoning and context settings when choosing another model. See the [official GLM-5.3 guide](https://docs.z.ai/guides/llm/glm-5.3).

**Coding Plan support boundary:** Z.ai limits Coding Plan to officially supported tools. ScopeWeaver is not on that list; an endpoint template does not establish permission or support. Obtain Z.ai authorization before using it here. See [tool integration](https://docs.z.ai/devpack/tool/others) and [usage policy](https://docs.z.ai/devpack/usage-policy). ScopeWeaver does not impersonate supported tools or switch billing endpoints automatically.

Enter a profile name and your own API key. Applying either template preserves any name, key, proxy, session-header setting, and other unrelated preferences already entered. Save when ready; a new profile must be activated separately. The existing **Test connection** and **Load models** controls make real provider requests only when clicked; this change does not perform a live provider test.

Verified against official documentation on **2026-10-03**. Provider availability, account entitlement, and policies may change.

## OpenCode Go (opencode subscription gateway)

Open **LLM → New → Configuration template** and choose **OpenCode Go · glm-5.3-flash**.

| Field | Value |
| --- | --- |
| Format | OpenAI Chat Completions |
| Base URL | `https://opencode.ai/zen/go/v1` |
| Model | `glm-5.3-flash` |
| Thinking | omitted (model default) |
| Reasoning effort | `max` |
| Context window (K) | `0` (200K default) |
| Output limit | `16384`, sent as `max_tokens` |
| Session header | `x-opencode-session` (required by the gateway; the template fills it) |

The gateway is OpenAI-compatible: the opencode 1.18.5 provider definition (`opencode-go`) uses `@ai-sdk/openai-compatible` at `https://opencode.ai/zen/go/v1` with the `OPENCODE_API_KEY` environment key (checked 2026-10-20). GLM-class models accept effort `low`, `high` and `max`; this template deliberately fills `max` at the operator's request, so expect the highest reasoning-token use. **Load models** reads the gateway's model list; the model field stays editable for other ids such as `qwen3.7-plus`. Every request must carry an `x-opencode-session` header (the gateway answers `400 MissingSessionID` without it); the template fills the profile's **Session header** field with that name, and ScopeWeaver sends its own session ids as the value.

**Credential and terms:** the key belongs to your opencode subscription; opencode stores it in its own auth file, which ScopeWeaver never reads. Paste the key into the profile or set `OPENCODE_API_KEY`. The key is governed by opencode's subscription terms — confirm that using it from another tool is permitted before saving. As with the Z.ai Coding Plan boundary, a template here does not establish permission.

Environment configuration:

```bash
SCOPEWEAVER_LLM_PROVIDER=opencode-go
SCOPEWEAVER_LLM_MODEL=glm-5.3-flash
# Key: OPENCODE_API_KEY is used when no generic key is set; SCOPEWEAVER_LLM_API_KEY overrides.
```

`SCOPEWEAVER_LLM_PROVIDER=opencode-go` resolves to the OpenAI Chat Completions wire format; an unset base URL and model default to the values in the table above.

## Controlling token use

Every agent turn resends the system prompt, the tool schemas and the conversation so far. Input therefore grows turn by turn until compaction shortens the history, and provider prompt caching decides how much of the repeated input is billed at the cheaper cached rate. This section answers [issue #24](https://github.com/cskwork/scopeweaver/issues/24) for cloud profiles. Native Ollama has its own safeguards, described [below](#runtime-safeguards).

**Evidence status:** measurements use Z.ai GLM-5.3 on the Coding Plan as a proxy for DeepSeek. GLM has a different tokenizer, cache accounting and price, so **DeepSeek numbers are unverified**. DeepSeek, Claude, OpenAI, Qwen, Kimi, OpenRouter and OpenCode Go support is verified with test fixtures built from each provider's documented responses, not with live calls. This includes all Claude 5.5 request and replay behavior below; no Claude API key was used.

### Economy templates

Open **LLM → New → Configuration template** and choose an *(economy)* entry. All six set output limit `16384` and context window `0` (200K compaction default). Like the GLM templates, they keep an entered API key and do not save or activate anything.

| Template | Format | Base URL | Model | Thinking | Effort | Output field |
| --- | --- | --- | --- | --- | --- | --- |
| DeepSeek | OpenAI Chat Completions | `https://api.deepseek.com` | `deepseek-flash` | omitted (DeepSeek default: on) | `low` | `max_tokens` |
| Anthropic Claude | Anthropic | `https://api.anthropic.com` | `claude-haiku-5-5` | `disabled` | `low` | `max_tokens` |
| OpenAI | OpenAI Responses | `https://api.openai.com/v1` | `gpt-6-luna` | omitted | `low` | `max_output_tokens` |
| Alibaba Qwen (international) | OpenAI Chat Completions | `https://dashscope-intl.aliyuncs.com/compatible-mode/v1` | `qwen3.8-flash` | omitted | `low` | `max_completion_tokens` |
| Moonshot Kimi | OpenAI Chat Completions | `https://api.moonshot.ai/v1` | `kimi-k3` | omitted | `low` | `max_completion_tokens` |
| OpenRouter | OpenAI Chat Completions | `https://openrouter.ai/api/v1` | `deepseek/deepseek-v4.1-flash` | omitted | `low` | `max_tokens` |

- **Effort `low` is an untested economy default** on every provider above. If results are cut off or too shallow, raise effort toward the provider default (DeepSeek `high`, GPT-6 Luna `medium`, Qwen `xhigh`, Kimi K3 `max`), then the output limit.
- Claude: the template disables thinking on Haiku to save output tokens. Haiku accepts disabled thinking only at effort `low`, `medium` or `high`. Sonnet and Opus 5.5 reject disabled thinking, so they have no economy template; use them with thinking **Omit** or **Adaptive** (see [Claude 5.5 thinking](#claude-55-thinking)).
- OpenAI: the template uses the Responses format because GPT-6 Luna accepts tools on Chat Completions only at effort `none`.
- OpenRouter: the model id and its `reasoning_effort` support were checked in OpenRouter's public model list on 2026-10-16. Claude models through OpenRouter need cache markers that the Chat Completions format cannot send, so the template uses DeepSeek Flash, which caches prefixes automatically.
- Z.ai GLM-5.3: the General and Coding Plan templates now fill effort `low`, output `16384` and context `0`, the economy row measured below. Earlier versions filled effort `max`, context `1000` and output `0`. Thinking stays on because GLM-5.3 requires it.

### Claude 5.5 thinking

These rules apply only to a model id of the form `claude-…-5-5` (or its dated snapshot `claude-…-5-5-YYYYMMDD`) on the official host (`https://api.anthropic.com`, or an empty base URL).

- Thinking left empty (**Omit**) now sends `thinking: {type: adaptive, display: summarized}`. The new **Adaptive** option sends the same; **On** (`enabled`) also gets `display: summarized`.
- In tool loops, thinking blocks are replayed with their tool calls, including blocks with empty text and `redacted_thinking` blocks. ScopeWeaver keeps them in memory for the latest 128 tool calls (at most 4 MiB) per provider; blocks with summarized text are also saved in the session history, but empty or redacted blocks do not survive a restart in the middle of a tool loop.
- Summary and approval-judge calls no longer send `temperature`. They also drop their `disabled` thinking for Sonnet, Opus, and Haiku at effort `xhigh` or `max`, which reject it; the profile's thinking setting applies instead.
- A profile set to **Off** (`disabled`) still sends `disabled` on agent turns, so Sonnet and Opus 5.5 need **Omit** or **Adaptive**.
- Third-party Anthropic-compatible endpoints (DeepSeek, Kimi, Z.ai) and other model ids are unchanged: empty thinking is still omitted.
- **Verified with request and stream fixtures only**; live Claude behavior and billing are unverified.

### Measured effect (GLM proxy)

One small task (three routes on a local test target with one planted unauthenticated secret), 3 runs per profile, each on a fresh database, with report-writer conversations allowed to finish. Credits use Z.ai's published GLM-5.3 multipliers, (uncached input × 6.9 + cached input × 1.7 + output × 24) / 10,000; they are calculated, not read from a bill.

| Profile | Median input + output tokens | Median credits | Planted issue found |
| --- | --- | --- | --- |
| Baseline: previous Z.ai Coding template (effort `max`, context `1000`, output `0`) | 209,233 (172,576–216,452) | 75.1 (64.3–88.6) | 3 of 3 |
| Economy: effort `low`, output `16384`, context `0` | 96,023 (95,060–105,023) | 26.6 (25.0–27.4) | 3 of 3, each reported by the report writer |

- Economy vs baseline: 54% fewer input + output tokens and 65% fewer credits at the median; every economy run was below the cheapest baseline run. The largest single call used 13,287 input and 3,829 output tokens, so neither the 200K window nor the output limit was reached: the saving here comes from effort `low`. The output limit and window bound larger tasks and were not measured. Run-to-run spread: largest/smallest run 1.25× for baseline, 1.10× for economy.
- Share by lane: baseline credits: worker 29.6%, planner 29.3%, report writer 29.1%, goal setup 12.0%; economy credits: report writer 38.8%, planner 34.1%, worker 20.2%, goal setup 6.9%. Runs had no compaction, so no session-summary calls; judge and web search were off.
- Cached share of input: 85.2% baseline, 92.3% economy. Reasoning share: output, which includes reasoning, was 39.3% of baseline credits and 23.4% of economy credits; ScopeWeaver did not yet record reasoning tokens separately when these runs were made.

GLM-5.3 always thinks, so these runs do not measure the effect of turning thinking off.

Live bridge checks (Claude via `claude -p`, OpenAI via `codex exec`) test model behavior through a local bridge. They do not test the API wire format or billing.

A `codex exec` bridge ran the OpenAI template (gpt-6-luna, Responses) through one full task: the tool loop completed, the planted issue was found and written up by the report writer, and a follow-up side question was answered (not reported as interrupted). The run's `llm_usage` rows carried reasoning tokens and cache-read tokens, including a `btw` side-question row. Two more runs did not finish inside the harness time limit because the bridge is slow; that is a bridge speed limit, not a code result. The `claude -p` bridge was dropped: it would not drive the tool protocol. Claude and OpenAI request and usage handling are proven by fixtures.

### Recommended settings

1. Start from your provider's economy template and keep context window `0` unless a task needs more history.
2. Keep a positive output limit. `0` does not omit the field (see below).
3. Keep effort low; raise it only when results get worse.
4. Leave the approval judge, web search and noa context compression off unless you need them (all are off by default).
5. For small tasks, consider fewer work agents than the default 3. The effect is not measured.
6. If you do not need a detailed report per finding, disable the **Report writer** agent's trigger (**System → Agent**).
7. After the first task, check usage per lane (see [Checking where tokens go](#checking-where-tokens-go)).

### Context window, output limit, thinking and effort

| Setting | Behavior on cloud profiles | Guidance |
| --- | --- | --- |
| Context window (K) | Local compaction budget only; not sent to the provider. `0` = 200K; values are clamped to 32–1000. | A larger window delays compaction, so each call carries more history. At 200K, old tool results start being cleared above about 115K tokens and history is summarized at about 179K. |
| Output limit | Per-reply cap. **`0` is not omitted**: agent turns send 32768, direct Anthropic calls send 8192, native Ollama uses its own default. | A cap does not make replies shorter; it stops long ones. Too low truncates replies. Templates use 16384. |
| Thinking | `thinking.type`; empty omits it, except on official Claude 5.5, where empty sends `adaptive`. Reasoning is billed as output. | DeepSeek thinks by default and documents `disabled`. With tools, DeepSeek also resends earlier reasoning as input. GLM-5.3 always thinks. The Claude template disables it. |
| Reasoning effort | `low` to `max`; empty omits it. | Templates use `low`. It was measured only on GLM-5.3; on other providers it is an untested economy default. Supported values differ per provider. |

### Context optimization

- **Conversation compaction** (built in, on): near the context window, older messages are summarized and the latest 8 are kept. The summary is one extra call; its output is capped at the output limit, at most 20000 (20000 when the limit is `0`).
- **Old tool-output clearing:** above 60% of the window, results of Read, Bash, Grep, Glob, WebFetch, WebSearch, Edit and Write older than the newest five are replaced. Graph, evidence and custom tool results stay until the next summary.
- **Tool-output cap:** each text block of a tool result is cut to 30,000 bytes; the full output stays on disk with a pointer.
- **Graph compaction:** inactive parts of the exploration graph are folded into digests, one call per eligible group (output cap 1500).
- **noa context compression** (System settings, experimental, off) replaces built-in compaction.
- The 72% trigger and 16/32 KiB output caps apply to native Ollama only.

**Prompt caching:**

- Claude: ScopeWeaver now always marks the system prompt, and the tool schemas before it, as cacheable in planner, worker, main-agent and chat sessions (including the report writer). Every other Anthropic-format request with a system prompt and no boundary now marks the whole system prompt: goal setup, conversation and graph summaries, side-question preparation and the approval judge. A prompt shorter than the model's minimum cacheable length is not cached even with a marker. The first use of each distinct system prompt writes the cache at 1.25× the input price; later calls within the cache lifetime read it at the cached price. Worker prompts differ per intent, so each intent writes once. Marker placement is verified by tests; **the live saving is unverified** (no Claude key).
- DeepSeek, Kimi and Z.ai cache prefixes automatically. DeepSeek and Kimi document that their Anthropic-compatible endpoints ignore block markers. OpenAI, Qwen and OpenRouter (DeepSeek Flash) also cache matching prefixes automatically.
- Tests confirm that consecutive tool turns in one session keep the same system prompt and tool schemas and only append history, for all three cloud formats. Compaction, wrap-up and newly unlocked deferred tools change the prefix. An edited prompt template that uses `{{.Now}}` changes the system prompt on each run.

### Workers, retries and task settings

- **Worker concurrency · Worker Agent count** (System settings, default 3 per task): each work agent claims a different intent and runs its own session with the full system prompt and tool schemas.
- **Max concurrent LLM requests** (System settings, default `0` = unlimited) and the profile's **Max concurrent requests** pace calls. They do not reduce tokens per call.
- Retries resend the full request. Defaults: **Connection retries** 3, **Empty-response retries** 2 (OpenAI format only), **Same-provider safe-window retries** 2 (before any output), 2 reruns of an intent after a model error, and up to 2 continuations after a reply that only contains reasoning. Set a count to `-1` on the LLM page, per profile or in the global policy, to turn a layer off. Whether a failed attempt is billed depends on the provider.
- When an agent run uses up its turn or time budget, or the task settles, a wrap-up adds up to 10 more turns by default.
- Provider-pool failover (off by default) replays the full request on another profile.

### Known causes of extra calls

- Long sessions: every turn resends the whole history, so later turns cost more than early ones.
- Planner: a new session on each wake (start, intent completion, new finding, idle heartbeat), with several turns each.
- Goal decomposition runs before planning, up to 8 turns.
- **Report writer:** a seeded custom agent with an enabled trigger on each `report_finding` tool call. Each finding starts its own conversation, up to 5 in parallel.
- **DeepSeek web search:** each search is a separate DeepSeek call with the active profile's key (output cap 1024, up to 3 searches per call). Agent searches and the web search test in settings are now recorded in `llm_usage` with lane `web_search` and the active profile's name and model; they are not in LLM recordings. DeepSeek does not document the usage format of these search responses, so ScopeWeaver reads Anthropic-style usage fields. **A response with no usage frame is recorded with 0 tokens: the call is counted, its tokens are unknown.** Check the DeepSeek console for billed tokens.
- Approval judge (off by default): one call per tool action without a matching rule. Side questions: on request only, with up to 12 summary calls before the answer.
- **Test connection** makes a real call (output cap 8192) that is not recorded.
- Fixed accounting error: Anthropic-format streams (Claude and Anthropic-compatible endpoints) over-counted output in `llm_usage`. A test stream with cumulative output counts 10 and 20 was recorded as 30 instead of 20. Only ScopeWeaver's numbers were inflated, not provider billing.
- Fixed: ScopeWeaver's internal session and transcript totals also over-counted streamed output (the same 10-then-20 stream counted 30). They now count 20. These totals are not `llm_usage`; an over-count could only move compaction earlier.
- Fixed: side questions on OpenAI Responses profiles were reported as interrupted because the stream had no stop event. A `response.completed` event now ends the answer normally; `response.incomplete` is still reported as interrupted.

### Checking where tokens go

- **Tasks → task → Overview → LLM token usage (by model)**: calls, input, output, **Reasoning (in output)** and cache read per model, from the always-on `llm_usage` table. Only rows tagged with the task appear; report-writer rows have no task. DeepSeek search rows appear under the active profile's model.
- The **Dashboard** and **Interception rules → Approval token usage** also show **Reasoning (in output)**.
- **LLM recordings**: one row per call with lane, tokens, latency and raw request/response. Off by default; turn on **Recording** before the task. It stores full prompts and responses.
- SQL on `llm_usage` for one time window (lane is the `worker` column: `planner`, `worker`, `goals`, `compactor`, `judge`, `btw` for side questions, `mainagent`, `web_search` for DeepSeek search; empty for report-writer, retest and custom-agent conversations). Conversation summaries and wrap-up keep their agent's lane.

```sql
SELECT COALESCE(worker, '(none)') AS lane, model, COUNT(*) AS calls,
       SUM(input_tokens) AS input, SUM(cache_read) AS cache_read,
       SUM(cache_write) AS cache_write, SUM(output_tokens) AS output,
       SUM(reasoning_tokens) AS reasoning
FROM llm_usage
WHERE ts >= '2026-10-16 09:00' AND ts < '2026-10-16 10:00'  -- your task's time range
GROUP BY 1, 2
ORDER BY SUM(input_tokens) + SUM(output_tokens) DESC;
```

`cache_read` and `cache_write` are already part of `input`, and `reasoning` is already part of `output`; do not add them again.

Cache reads are counted from `prompt_tokens_details.cached_tokens`, DeepSeek's top-level `prompt_cache_hit_tokens` and Kimi's legacy top-level `cached_tokens`. Cache writes are recorded for OpenAI Chat, OpenAI Responses, Kimi K3 and OpenRouter (`cache_write_tokens`) and Qwen (`cache_creation_input_tokens`), as well as Claude. These field mappings are verified with fixtures only.

### Accounting gaps and follow-ups

- Reasoning is 0 when a provider reports no separate figure; a 0 does not prove the model did not reason.
- DeepSeek search usage field names are undocumented (see [Known causes of extra calls](#known-causes-of-extra-calls)).
- Gemini has no template yet; it needs the Norma SDK v0.4.4 upgrade (follow-up).
- Claude history caching (a marker on the last message) needs SDK support (follow-up).
- OpenRouter models that honor only the unified `reasoning.effort` may ignore the template's `reasoning_effort`, which is the only form the SDK can send (follow-up).

## Native Ollama (local inference)

ScopeWeaver talks to Ollama through its native `/api/chat` API. Ollama's OpenAI-compatible endpoint has no way to set the context size ([Ollama OpenAI compatibility](https://docs.ollama.com/api/openai-compatibility)), so a profile pointed at `/v1` cannot guarantee `num_ctx`. The native format sends it on every request. A base URL ending in `/v1`, `/api` or `/api/chat` is normalized to the server root.

Ollama's documented default context differs between pages: the [FAQ](https://docs.ollama.com/faq) states 4,096 tokens, and the [context length page](https://docs.ollama.com/context-length) describes a VRAM-based default (about 4K below 24 GiB, 32K for 24–48 GiB, 256K above). ScopeWeaver therefore does not rely on the server default.

### Templates

Open **LLM → New → Configuration template → Local inference (Ollama)**.

| Field | Same machine | Host from Docker | Bundled Compose service |
| --- | --- | --- | --- |
| Format | Ollama (native /api/chat) | Ollama (native /api/chat) | Ollama (native /api/chat) |
| Base URL | `http://127.0.0.1:11434` | `http://host.docker.internal:11434` | `http://ollama:11434` |
| Model | Installed model from this server | Installed model from this server | Installed model from this server |
| API key | empty | empty | empty |
| Context window (K) | `32` (`num_ctx` 32,768) | `32` | `32` |
| Max concurrent requests | `1` | `1` | `1` |
| Keep-alive | `30m` | `30m` | `30m` |
| Output limit | `4096` | `4096` | `4096` |
| Thinking / effort | omitted (model default) | omitted | omitted |

Selecting an Ollama template reads `/api/tags` and model metadata from the selected endpoint, then chooses an installed model, preferring one that declares `tools`. Choosing the Ollama format for a new profile does the same; a blank endpoint or an unchanged cloud-template endpoint uses the host default. A custom endpoint stays unchanged. The model is blank while discovery runs, and stays blank with an error if discovery fails or the list is empty. Check the endpoint, optional token and proxy, then click **Load models** or enter a model tag manually. Editing the model or connection fields cancels the pending selection; late responses cannot overwrite those edits. Opening an existing profile preserves its stored model. Selection never downloads or runs a model, saves, or activates a profile. **Test connection** runs only when clicked.

**API key behavior:** Ollama templates and switching to the Ollama format clear an unsaved key before discovery. Ollama templates keep the profile name, proxy, session header and other entered preferences, but **clear any unsaved API key** so a cloud credential is never sent to a local endpoint. The GLM templates still keep an entered key. Leave the key blank for local Ollama; enter a token only when a reverse proxy in front of Ollama requires one (it is sent as `Authorization: Bearer`). Keyless Ollama profiles can be tested, saved, activated and used in failover; the profile card shows **No key needed** instead of a missing-key warning. When you edit a saved native Ollama profile, a blank key field keeps the saved token; switching a cloud profile to Ollama with the field blank deletes the old cloud key.

**Keys for the other formats:** Anthropic and OpenAI formats require a key unless the base URL is explicitly local, meaning its host resolves only to loopback or private addresses. A keyless local OpenAI-compatible server such as vLLM or LM Studio therefore works with a blank key. ScopeWeaver then sends an internal non-secret placeholder so the SDK does not inherit `OPENAI_API_KEY` or `ANTHROPIC_API_KEY` from the environment. A non-local endpoint with no key is refused (*llm: API key required for non-local compatible endpoint*).

### Context, output and residency controls

| Setting | Ollama behavior | Cloud formats |
| --- | --- | --- |
| Context window (K) | Binary K sent as `options.num_ctx` (32 = 32,768). `0` uses 32; values below 16 use 16. Also sets the compaction budget. | Unchanged: compaction budget only, `0` = 200K |
| Output limit | Sent as `options.num_predict`; `0` uses 4,096 | `0` sends 32768 on agent turns (direct Anthropic calls: 8192) |
| Keep-alive | Sent as `keep_alive`; blank uses `30m` (Ollama's own default is 5 minutes) | Not sent |
| Max concurrent requests | `0` uses 1. One admission gate is shared per Ollama server, even across profiles; the lowest configured cap applies | `0` inherits **System settings → Max concurrent LLM requests** |

Queued approval-judge, main-agent and side-question requests take the next free slot before background work. Each group is FIFO; active inference is never interrupted. Diagnostics and pre-warm remain background requests.

Raise concurrency only when the server sets `OLLAMA_NUM_PARALLEL` above 1. Ollama's memory use grows with `OLLAMA_NUM_PARALLEL × context length` ([FAQ](https://docs.ollama.com/faq)).

### Runtime safeguards

- **Pre-warm:** before the first inference through a native profile, ScopeWeaver calls `/api/show`, refuses a model that does not declare `tools`, then loads the model with an empty `/api/chat` request using the profile's `num_ctx` and `keep_alive`. Warm-up runs inside the same concurrency slot.
- **Compaction:** for native Ollama with a window of 64K or less, history compaction triggers at 72% of the window and keeps complete tool call/result pairs. If experimental noa compaction is enabled, this safeguard takes priority for those profiles: noa is skipped and a warning is emitted. At 32K or less, the part of each tool output given to the model is capped at 16,384 **bytes** (16 KiB, measured as the SDK's `len(s)`, not Unicode characters); above 32K through 64K, the cap is 32,768 bytes (32 KiB). The full output is preserved in a spill file referenced from the tool result. Cloud formats keep their existing compaction and noa behavior.
- **Approval judge:** an explicitly selected judge profile wins. Otherwise the actual native profile used by the originating agent/run is preferred (including binding, pool or failover); cloud or missing origin uses the active profile. For a native judge, the default timeout is **90 s** and the default failure action is **ask**. Saved timeout/failure overrides win; user approval still expires after 300 seconds with deny by default. Native output is constrained to a decision/comment JSON schema and still validated. The settings form cannot save until its server configuration loads successfully.

### Generic OpenAI-compatible servers (vLLM, LM Studio)

The existing formats remain selectable for these servers: choose **OpenAI (Chat Completions)** and point the base URL at the server's `/v1` endpoint. Do not use the native Ollama format for them.

- **Load models** in the native format reads `/api/tags`; if that returns HTTP 404 or 405 it falls back to `/v1/models`, so a generic server's model list can appear.
- Chat **never** switches silently: a native profile always calls `/api/chat`, which a generic server does not provide. The resulting "model or endpoint not found" error and its `ollama pull` hint do not apply there; pulling a model does not fix a vLLM or LM Studio server. Change the format to OpenAI instead.

### Load models and Test connection

**Load models** reads `/api/tags` and `/api/show`. Each entry shows parameter size, quantization and `tools` / `thinking` / `vision` badges. A model whose metadata omits `tools` shows: *"Warning: This model does not declare native tool-calling support. ScopeWeaver agents require tool calling to update the asset and exploration graphs."* Failed metadata is shown as unknown, not as missing.

**Test connection** checks the model with `/api/show`, runs a one-turn tool-call smoke test with the profile's `num_ctx`, `keep_alive` and `num_predict`, and reads `/api/ps`. The panel shows whether the allocated context covers the request, whether the tool call returned, cold-load time in milliseconds and generation speed in tokens/s. A missing model shows the exact command to run on the server, for example `ollama pull qwen2.5-coder:32b`. ScopeWeaver does not pull models or create Modelfiles.

### Host and container routing

| ScopeWeaver runs | Ollama runs | Base URL |
| --- | --- | --- |
| On the host | On the same host | `http://127.0.0.1:11434` |
| In Docker Compose | On the host | `http://host.docker.internal:11434` |
| In Docker Compose | Bundled `ollama` service | `http://ollama:11434` |

Inside a container, `127.0.0.1` is the container itself. Compose maps `host.docker.internal` to the host gateway (`host-gateway`). Ollama binds `127.0.0.1:11434` by default; on Linux the host Ollama must listen on an address the container can reach (for example `OLLAMA_HOST=0.0.0.0:11434`), which exposes it to the network. Restrict port 11434 with a firewall.

**Optional Compose service:** `docker compose --profile local-llm up -d` starts the `ollama` service with `OLLAMA_NO_CLOUD=1`, `OLLAMA_KEEP_ALIVE=30m`, `OLLAMA_NUM_PARALLEL=1`, `OLLAMA_CONTEXT_LENGTH=32768` and the `ollama_data` volume. Pull a model with `docker compose exec ollama ollama pull qwen2.5-coder:14b`. Without the profile, Compose behaves as before. The bundled service requests no GPU. **On macOS, Docker containers cannot use the Apple GPU, so this service runs on CPU only**; for usable speed, run Ollama natively on the Mac and use the host template.

### Network policy (`local_llm_only`)

Off by default; cloud providers remain available. Turn it on in **System settings → Local-only LLM network** or with `SCOPEWEAVER_LOCAL_LLM_ONLY=true`. When on:

- LLM HTTP (inference, model lists, connection tests, warm-up and the approval judge) permits **loopback plus explicit trusted CIDRs** (`local_llm_allowed_cidrs`). Private IPv4, IPv6 ULA, LAN and Docker addresses are no longer implicitly trusted. Add only inference-server addresses you control, preferably `/32` for IPv4 or `/128` for IPv6.
- Link-local, multicast and unspecified addresses are always refused, even in a trusted CIDR. DNS names are checked against every resolved address when a connection opens; all addresses must be allowed. All redirects and LLM profile proxies are refused.
- **Agent web search (every backend), search connectivity probes, update checks and update downloads are blocked.** The update screen describes this deliberate policy separately from a network failure. Local version status, update progress and rollback remain available; cached remote release information is not returned as a way around the policy.
- Runtime changes apply without a restart, including already-created sessions. A download admitted before strict mode is enabled stops at the next guarded network operation/body read and removes partial staging; traffic already sent cannot be recalled.
- CIDRs from `SCOPEWEAVER_LOCAL_LLM_ALLOWED_CIDRS` (or retained `ARTEX_LOCAL_LLM_ALLOWED_CIDRS`) also apply, are not copied into the stored list and cannot be edited in the UI. An environment `true` for the policy is a floor that the UI cannot disable.
- Target traffic, target tools and the global proxy remain connected. This is an application policy, not a cryptographic or whole-host offline guarantee.

**Migration for existing strict LAN/Docker profiles:** same-machine `http://127.0.0.1:11434` needs no CIDR. A LAN endpoint such as `http://192.168.1.20:11434` needs `192.168.1.20/32`; an IPv6 inference address such as `fd00::20` needs `fd00::20/128`. For Docker, resolve the name **from the ScopeWeaver container**, because the host and container may see different addresses:

```bash
# Existing Compose service; read its actual name resolution and network addresses.
docker compose exec scopeweaver getent ahosts host.docker.internal
docker compose exec scopeweaver getent ahosts ollama
docker inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}} {{.GlobalIPv6Address}}{{end}}' "$(docker compose ps -q ollama)"
```

If the image lacks `getent`, inspect the existing host-gateway mapping with `docker compose exec scopeweaver cat /etc/hosts` and the Ollama container's address with `docker inspect`. Use the inference IP that ScopeWeaver actually reaches, not a copied example or a whole Docker subnet. Add it to **Additional trusted CIDRs** or the environment allowlist, for example `SCOPEWEAVER_LOCAL_LLM_ALLOWED_CIDRS=192.168.1.20/32`. Neither `host.docker.internal` nor the service name `ollama` grants implicit trust. Recheck after Docker recreates the service or changes its address; update the narrow allowlist before retrying **Load models** or **Test connection**.

ScopeWeaver cannot see what an Ollama server does next. **Ollama can forward requests to Ollama cloud models**; the bundled Compose service sets `OLLAMA_NO_CLOUD=1`, but a host or remote Ollama must disable cloud independently (`OLLAMA_NO_CLOUD=1` or `disable_ollama_cloud` in `~/.ollama/server.json`, per the [FAQ](https://docs.ollama.com/faq)).

### Environment and configuration

`SCOPEWEAVER_LLM_*` overrides `ARTEX_LLM_*`; an explicitly empty modern variable also overrides the legacy one. Both override the `llm` block in `config.json` (path from `SCOPEWEAVER_CONFIG` or `ARTEX_CONFIG`).

| Suffix | Meaning |
| --- | --- |
| `PROVIDER` | `anthropic`, `openai`, `openai-responses`, `opencode-go` or `ollama` |
| `MODEL`, `BASE_URL`, `PROXY` | Model, endpoint and optional LLM proxy |
| `API_KEY` | Required for Anthropic/OpenAI formats unless the base URL is explicitly local; optional token for Ollama |
| `CONTEXT_WINDOW_K`, `MAX_TOKENS`, `KEEP_ALIVE`, `MAX_CONCURRENT` | Controls described above |
| `THINKING_TYPE`, `REASONING_EFFORT`, `STREAM` | Existing reasoning and streaming options |

Policy variables: `SCOPEWEAVER_LOCAL_LLM_ONLY` / `ARTEX_LOCAL_LLM_ONLY` and comma-separated `SCOPEWEAVER_LOCAL_LLM_ALLOWED_CIDRS` / `ARTEX_LOCAL_LLM_ALLOWED_CIDRS`.

At startup, Anthropic and OpenAI formats use `ANTHROPIC_API_KEY` or `OPENAI_API_KEY` when no generic key is configured, including for authenticated local gateways. The `opencode-go` provider uses `OPENCODE_API_KEY` instead — never `OPENAI_API_KEY` — and is inferred when only `OPENCODE_API_KEY` is set. To start a deliberately keyless local profile while those provider keys are set, explicitly set `SCOPEWEAVER_LLM_API_KEY=` (or `ARTEX_LLM_API_KEY=`). This overrides file credentials and suppresses provider-key fallback. Native Ollama falls back only to `OLLAMA_API_KEY`, never to Anthropic, OpenAI or OpenCode keys.

Saving a new UI profile with a blank key does not import provider environment credentials. **Test connection** with a blank key can reuse an existing profile's saved key when its profile ID is supplied. Without a profile ID, a local Anthropic/OpenAI test reuses the active key only when the format, normalized base URL and proxy all match the active service, including when testing a new draft. A different local endpoint, format or proxy does not inherit the active key; native Ollama never inherits an active cloud key. Existing non-local connection tests retain their active-key fallback.

```bash
# .env for ScopeWeaver in Docker with Ollama on the host
# Replace your-installed-tool-model with a tool-capable tag shown by Load models.
SCOPEWEAVER_LLM_PROVIDER=ollama
SCOPEWEAVER_LLM_BASE_URL=http://host.docker.internal:11434
SCOPEWEAVER_LLM_MODEL=your-installed-tool-model
SCOPEWEAVER_LLM_CONTEXT_WINDOW_K=32
SCOPEWEAVER_LLM_MAX_CONCURRENT=1
SCOPEWEAVER_LLM_KEEP_ALIVE=30m
SCOPEWEAVER_LLM_MAX_TOKENS=4096
SCOPEWEAVER_LOCAL_LLM_ONLY=false   # add the actual host /32 or /128 before enabling strict mode
# SCOPEWEAVER_LOCAL_LLM_ALLOWED_CIDRS=192.168.1.20/32  # replace with the resolved inference IP
```

```json
{
  "llm": { "provider": "ollama", "base_url": "http://127.0.0.1:11434", "model": "your-installed-tool-model", "context_window_k": 32, "llm_max_concurrent": 1, "keep_alive": "30m", "max_tokens": 4096 },
  "local_llm_only": false,
  "local_llm_allowed_cidrs": []
}
```

### Bounded diagnostics and telemetry

**Test connection** makes a deliberate inference probe; selecting a template only discovers installed metadata. Alongside tool/context checks and load time/speed, native diagnostics show the selected runner's `/api/ps` `size` and `size_vram` as a GPU-memory percentage. Missing or zero total size, or missing VRAM size, means **unknown**, not 0% GPU. Explicit zero VRAM with a positive total means 0%. This ratio is a memory estimate, not an exact weight split, GPU-capacity test or a guarantee that a model stays resident.

A percentage at or below 80% warns that CPU work may slow generation. Other loaded models and an explicitly configured judge using a different model/context on the same endpoint produce separate memory/reload warnings. Using the same model/context may reduce reloads but cannot guarantee residency. No diagnostic downloads a model or saves/activates a profile.

Near-context-capacity warnings are a server-log heuristic based on reported token counts, not a truncation detector. Full raw Ollama timing fields are stored only when raw recording is enabled; token/cache usage remains metered independently. Small models still need task-specific validation; the existing reliability limitations below remain.

### Hardware tiers

Approximate starting points by GPU VRAM or Apple unified memory, using the tool-capable catalog models below. **These are estimates, not benchmarks.** Context listed is the profile's configured **Context window (K)**; it must fit both the model's own context limit and your memory (weights + KV cache, which grows with context and `OLLAMA_NUM_PARALLEL`). Check with **Test connection**, which reports whether the requested context was allocated.

| Memory | Example model | Context window (K) | Role |
| --- | --- | --- | --- |
| 8–12 GB | `qwen2.5-coder:7b`, `qwen3:8b` | `16`–`32` | Approval judge or auxiliary profile; weak as the main agent |
| 16–24 GB | `qwen2.5-coder:14b`, `qwen3:14b` | `32` | Main agent on a workstation  |
| 32–48 GB | `qwen2.5-coder:32b`, `qwen3:32b` | `32` | Main agent. Use `64` only with a model whose limit allows it; these list 32K and 40K |
| 64–128 GB+ | `llama3.3:70b` | `64` (model lists 128K) | Main agent on large-memory hosts; 64K needs the upper part of this range |

Templates choose from models installed on the selected server and use a 32K context. Hardware examples are not automatic model choices.

### Model candidates

Only models whose [Ollama library](https://ollama.com/library) page lists the `tools` capability are listed (checked 2026-10-15). Download sizes are the default quantization. **Memory guidance is an estimate**: weights plus a KV cache that grows with context and parallel requests. ScopeWeaver has not benchmarked these models or hardware, and tool-calling quality varies by model.

| Model | Download | Notes |
| --- | --- | --- |
| `qwen2.5-coder:7b` | 4.7 GB | Smallest practical candidate; also a judge candidate |
| `qwen2.5-coder:14b` | 9.0 GB | Workstation candidate |
| `qwen2.5-coder:32b` | 20 GB | Larger-host candidate |
| `qwen3:8b`, `qwen3:14b`, `qwen3:32b` | 5.2 / 9.3 / 20 GB | Also declare `thinking` |
| `llama3.3:70b` | 43 GB | Large-memory hosts |

`qwen3:72b` and `llama3.2:8b` do not appear in the Ollama library; `llama3.2` ships as 1B and 3B. Confirm `tools` with **Load models** on your own server before use.

**Observed limitation (2026-10-15):** a declared `tools` capability does not guarantee wire tool calls. In branch testing, `qwen2.5-coder:7b` wrote tool-shaped JSON as plain text, so no tool ran, and `qwen3:4b` passed the tool-call diagnostic but did not complete long autonomous tasks. Treat small models as unvalidated for the main agent; see [Verification](VERIFICATION.md#autonomous-task-reliability-is-not-validated).

The usage restrictions in the [README](../README.md) still apply: run ScopeWeaver only in a locally isolated environment, whichever provider you choose.

[한국어 안내](ko/llm-providers.md)
