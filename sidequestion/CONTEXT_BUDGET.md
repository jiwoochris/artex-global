# Long side-question conversations and the context budget

English · [한국어](CONTEXT_BUDGET.ko.md) · [中文](CONTEXT_BUDGET.zh.md)

Fixed on 2026-09-11. The previous implementation treated the character count of the request JSON directly as the token count and inherited the main task's 32K output reservation as is, so when there was a lot of HTML, JS, or tool output it rejected legitimate side questions prematurely.

> This document is an English translation of the original Chinese document (`CONTEXT_BUDGET.zh.md`). The original is preserved unchanged so that changes in the upstream repository are easy to compare against.

## Comparison with open-source implementations

- [Grok CLI side-question context](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/agent/agent.ts#L739): extracts snippets from the most recent user and assistant text, with a character budget of about 2000 characters and at most 400 characters taken from each entry. This path does not maintain a continuous side-question Q&A history.
- [Grok CLI independent request](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/utils/side-question.ts): uses its own cancellation signal. When the model supports it, the output cap is 2048 tokens. No tools are provided.
- [Grok CLI main-session compaction](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/agent/compaction.ts): estimates tokens, keeps recent content, folds new content into the previous summary, and handles truncation across turn boundaries.
- [OpenCode session compaction](https://github.com/anomalyco/opencode/blob/b3f1a96c6dd7adeb28b36dd11add1998fc84d67b/packages/core/src/session/compaction.ts): estimates the full request, reserves output / buffer space, adds a rolling summary to the recent content, and sends a tool-free summary request. By default this implementation uses a recent budget of 8000 and a summary output cap of 4096 tokens.
- [OpenCode overflow recovery](https://github.com/anomalyco/opencode/blob/b3f1a96c6dd7adeb28b36dd11add1998fc84d67b/packages/core/src/session/runner/llm.ts): attempts overflow recovery only when assistant output has not started yet, and the call made after recovery does not re-enter the same overflow-recovery path.

ARTEX borrows the approach of an independent output budget, recent content plus a rolling summary, and bounded recovery. It keeps norma v0.3.6's structured messages and tool pairing as they are, but does not copy Grok's text-excerpt approach, and it does not write OpenCode-style main-session compaction events into the ARTEX main transcript.

## Request budget and execution

- Messages are estimated the same way norma does it, by UTF-8 bytes per content block with a 4/3 margin. On top of that, the system prompt, tool schemas, and message-wrapping overhead are counted. This estimate is not the model's exact token count.
- Side-question output defaults to at most 8192 tokens and also does not exceed an output cap already set in the main configuration. The service environment variable `ARTEX_BTW_MAX_OUTPUT_TOKENS` can set the cap within the range 256–32768; it does not change the product's default model or the main task's parameters.
- The input budget is the context window minus the output cap and a safety margin; when the window size is unknown, the platform default of 200K is used. The safety margin is 5% of the window, with a minimum of 128 and a maximum of 8192 tokens.
- Successful Q&A pairs are loaded in increasing sequence order, at most 20 pairs per batch. At most 20 pairs are kept verbatim, and their token budget is at most 1/4 of the input budget and no more than 16K.
- Q&A pairs that exceed the budget are folded into the rolling summary. The summary carries the source records and the point in time of the context. Past assistant answers are not equivalent to new tool evidence, so when they conflict, the latest main snapshot takes precedence.
- If the main context is still too long, only the older messages in the copy are summarized, and at most 8K tokens of recent content are kept. The cut point never splits a tool call from its result. If a single pair is too large, the whole pair goes into the summary.
- Summary input is split safely on UTF-8 boundaries to fit the window actually remaining, and the output cap is 2048 tokens. An empty summary, a truncated response, a tool call, or exceeding the summary budget is never written to the cache. A single side question makes at most 12 summary calls, all subject to the same 120-second timeout. On reaching the cap it fails explicitly rather than looping forever.
- If the model returns a context overflow for the first time and has not yet emitted any text or tool call, the request is shrunk further and retried at most once. If the estimated size does not go down, recovery stops immediately. Other model errors and partially streamed output do not trigger this recovery.
- All usage already accounted for, including summaries, failed attempts, and usage at the moment of cancellation, is accumulated on the same side-question request. If the Provider does not return usage, only 0 can be recorded; an estimate is never dressed up as actual usage.

## Persistence and UI

`side_question_sessions.memory` stores the summary of older Q&A, the sequence number (ordinal) it covers, and the main-context summaries cached per snapshot identity. `side_question_requests.context_info` stores the preparation stage, the number of pairs actually replayed, summary usage, and the budget estimates.

A summary is saved only while the original request is still running and the clear version matches. A clear also wipes the cache, and late writes do not bring back data that has already been cleared. A new snapshot does not reuse the summaries of an earlier snapshot. The summary fields are saved along with v3 task archives; when restoring an older v3 that lacks the fields, they are filled with empty objects, and v1 · v2 remain compatible.

POST accepts the request and returns it first; preparation and compaction run in the background without holding the admission lock or a database transaction. The SSE stream and the history view show the preparing, organizing Q&A, compacting the copy, and answering stages. A compaction failure is saved as the failed terminal state of that side-question request. The frontend keeps the error and restores the draft of the question that failed this time, without covering the input box with a floating notification. History polling also no longer clears submission errors.

## Validation record

- Replay of 19, 20, 21, and 50 pairs, preservation of older conclusions beyond 20 pairs, reuse of the summary cache after a restart: automated, passed.
- Very long Chinese answers and code context, chunked request budgets, tool pairing, snapshot immutability, cache invalidation on a new snapshot: automated, passed.
- Summary failure / cancellation / truncation / excessive length / tool returns, clear races, the call cap, exactly one overflow recovery and no retry of a partial stream: automated, passed.
- On an isolated PostgreSQL: paging, restarts, v1 · v2 · v3 archives, archive restore of the summary cache and budget metadata, an older v3 missing the new fields, and 20 parent sessions sharing four concurrency slots: passed.
- Go candidate service build, frontend TypeScript check, Biome check of the modified components, and a Next.js production build in a separate directory: passed.
- Using the built-in browser with an isolated UI fixture at 1280×720 and 390×844, verified the Q&A organizing stage, the summary-scope notice, draft restore after a failure, the absence of floating error notifications, no horizontal overflow, and no console errors. The temporary fixture has been removed.
- A read-only replay of Worker snapshots already present on the local machine passed the new budget check. For example, Worker #3's 293085-character snapshot is no longer wrongly rejected because of the local character count. This item involved no external model calls.
- The real conversation test that would have sent a private Worker snapshot to Grok this time was rejected by the automatic approval review and was not run, so it is not counted as a pass.
- At 2026-09-11 00:37 the local backend was restarted at the user's request, keeping the original database, data directory, and login configuration. The SHA-256 of the running executable matched the candidate binary, and `/api/health` returned healthy on both the backend and the frontend proxy.

Validation commands (using only the isolated test database):

```sh
go test -race ./sidequestion ./db ./server -run 'TestSide|TestCheckpoint|TestSnapshot|TestBuildRequest|TestService|TestMainSide|TestTaskArchive' -count=1
go build ./cmd/artex
npx tsc --noEmit
npm run build -- --webpack
```

The frontend production build used a separate copy so as not to overwrite the `.next` of the current preview. The candidate service is at `/private/tmp/artex-btw-budget-candidate` and was copied to `/private/tmp/artex-btw-preview/artex` and started; the original binary was backed up as `artex.before-context-budget` in the same directory.
