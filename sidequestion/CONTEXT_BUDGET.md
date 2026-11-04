# Side-question long conversations and the context budget

Revised 2026-09-11. The earlier implementation treated the character count of the request JSON as
the token count and inherited the main task's 32K output reservation as is, so when there was a lot of
HTML, JS, and tool output it rejected legitimate side questions up front.

> This document was translated from the original Chinese document. The upstream text stays available
> in the upstream repository for comparison.

## Comparison with open-source implementations

- [Grok CLI side-question context](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/agent/agent.ts#L739):
  it excerpts some fragments from the most recent user and assistant text, with a character budget of
  about 2000 characters and at most 400 characters per item. This path keeps no running
  question-and-answer history.
- [Grok CLI independent request](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/utils/side-question.ts):
  it keeps a separate cancellation signal. Where the model supports it, the output cap is 2048
  tokens. No tools are provided.
- [Grok CLI main-session compaction](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/agent/compaction.ts):
  it estimates tokens, keeps recent content, folds new content into the previous summary, and handles
  cuts that cross a turn boundary.
- [OpenCode session compaction](https://github.com/anomalyco/opencode/blob/b3f1a96c6dd7adeb28b36dd11add1998fc84d67b/packages/core/src/session/compaction.ts):
  it estimates the whole request, reserves output and buffer, adds a rolling summary to recent
  content, and sends a tool-less summarization request. By default this implementation uses a recent
  budget of 8000 and a summary output cap of 4096 tokens.
- [OpenCode overflow recovery](https://github.com/anomalyco/opencode/blob/b3f1a96c6dd7adeb28b36dd11add1998fc84d67b/packages/core/src/session/runner/llm.ts):
  it attempts overflow recovery only while the assistant output has not started yet, and the
  recovered call does not enter the same overflow-recovery path again.

ARTEX drew on the independent output budget, the recent content plus rolling summary, and the limited
recovery. It keeps norma v0.3.6's structured messages and tool pairing as they are, does not adopt
Grok's text-excerpt approach as is, and does not record OpenCode's main-session compaction events in
the ARTEX main transcript.

## Request budget and execution

- Messages use norma's approach as is: estimate UTF-8 bytes per content block and add a 4/3 margin.
  The system prompt, the tool schema, and message-wrapping overhead are counted on top. This estimate
  is not the model's exact token count.
- A side-question output is at most 8192 tokens by default, and never exceeds the output cap already
  set in the main configuration. The service environment variable `ARTEX_BTW_MAX_OUTPUT_TOKENS` sets
  a cap in the 256–32768 range; the product's default model and the main task's parameters are not
  changed.
- The input budget is the context window minus the output cap and a safety margin; when the window
  size is unknown, the platform default of 200K is used. The safety margin is 5% of the window, at
  least 128 tokens and at most 8192 tokens.
- Successful exchanges are loaded by increasing sequence number, up to 20 bundles at a time. At most
  20 bundles of verbatim text are kept, and their token budget stays within 1/4 of the input budget
  and never above 16K.
- Exchanges that exceed the budget are folded into a rolling summary. The summary carries the source
  records and the context timestamp along with it. Past assistant answers are not the same as new
  tool evidence, so on a conflict the most recent main snapshot wins.
- If the main context is still too long, only the oldest messages of the copy are summarized and the
  recent content is kept up to 8K tokens. The cut point never splits a tool call from its result. If a
  single bundle is too large, the whole bundle goes into the summary.
- The summary input is split safely on UTF-8 boundaries to fit the window size actually left, and the
  output cap is 2048 tokens. An empty summary, a truncated response, a tool call, and a summary over
  budget are all kept out of the cache. One side question makes at most 12 summarization calls, which
  share the same 120-second timeout. On reaching the cap it fails clearly instead of repeating
  forever.
- If the model returns a context-overflow error for the first time and has not emitted any text or
  tool call yet, it reduces the context further and retries at most once. If the estimated size does
  not shrink, recovery stops immediately. Other model errors, and output that was only partially
  streamed, do not trigger this recovery.
- Every usage already accumulated is summed onto the same side-question request, including
  summarization, failed attempts, and usage at the moment of cancellation. If the Provider returns no
  usage, only 0 is recorded; an estimate is not dressed up as real usage.

## Persistence and the screen

`side_question_sessions.memory` stores the summaries of older exchanges, the ordinal they were folded
up to, and the main-context summaries cached per snapshot identity. `side_question_requests.context_info`
stores the preparation stage, the number of bundles actually replayed, the summary usage, and the
budget estimate.

A summary is stored only while the original request is still running and the cleanup version matches.
Clear wipes the cache as well, so a late-arriving write cannot resurrect already-cleared data. A new
snapshot does not reuse the previous snapshot's summary. Summary fields are stored together with the
v3 task archive; restoring an older v3 that lacks the fields fills them with an empty object, and v1
and v2 stay compatible.

POST accepts and returns the request first, and preparation and compaction run in the background
without holding the admission lock or a database transaction. SSE and the history screen show the
preparation, exchange-folding, copy-compaction, and response stages. A compaction failure is stored as
the failure terminal state of that side-question request. The frontend keeps the error and restores
the draft of the question that failed, and the floating notification does not cover the input box.
History polling no longer clears a submission error either.

## Verification record

- Replaying 19, 20, 21, and 50 bundles, keeping older conclusions beyond 20 bundles, and reusing the
  summary cache after a restart: automated pass.
- Very long Chinese answers plus code context, request-budget splitting, tool pairing, snapshot
  immutability, and cache invalidation on a new snapshot: automated pass.
- Summary failure, cancellation, truncation, over-length, and a tool return, the clear race, the call
  cap, and the single overflow recovery with no partial-stream retry: automated pass.
- Paging against an independent PostgreSQL, restart, v1/v2/v3 archives, archive restore of the summary
  cache and budget metadata, an older v3 without the new fields, and 20 parent sessions sharing four
  concurrency slots: pass.
- Building the candidate Go service, the frontend TypeScript check, the Biome check of the changed
  components, and the Next.js production build in an isolated directory: pass.
- With the built-in browser and an isolated UI fixture, at 1280×720 and 390×844 resolutions, verified
  the exchange-folding stage, the summary-scope notice, draft recovery after a failure, the absence of
  a floating error notification, the absence of horizontal overflow, and the absence of console
  errors. The temporary fixture was removed.
- Replaying, read-only, a Worker snapshot already present locally passed the new budget check. For
  example, Worker #3's 293,085-character snapshot is no longer wrongly rejected because of a local
  character count. This item made no external model call.
- The real-conversation test that was going to send a private Worker snapshot to Grok was rejected by
  the automatic-approval review and therefore did not run, so it is not counted as a pass.
- At 00:37 on 2026-09-11 the local backend was restarted at the user's request, using the original
  database, data directory, and login settings. The SHA-256 of the executable and of the candidate
  binary matched, and `/api/health` on both the backend and the frontend proxy returned healthy.

Verification commands (isolated test database only):

```sh
go test -race ./sidequestion ./db ./server -run 'TestSide|TestCheckpoint|TestSnapshot|TestBuildRequest|TestService|TestMainSide|TestTaskArchive' -count=1
go build ./cmd/artex
npx tsc --noEmit
npm run build -- --webpack
```

The frontend production build used an isolated copy so that it did not overwrite the current preview's
`.next`. The candidate service was at `/private/tmp/artex-btw-budget-candidate` and was copied to
`/private/tmp/artex-btw-preview/artex` to run. The original binary was backed up as
`artex.before-context-budget` in the same directory.
