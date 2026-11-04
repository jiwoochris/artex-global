# `/btw` validation record

Date: 2026-09-10. Branch: `codex/btw-side-question`. Baseline commit:
`8dae851b9b622f2ff2631f332fde9719d0b16fba`.

> This document was translated from the original Chinese document. The upstream text stays available
> in the upstream repository for comparison.

An independent PostgreSQL test database and data directory were used. Real model credentials were
injected only into the isolated test environment and were not written into code or this record, and
the product's default model was not changed. Go 1.26.3, norma v0.3.6, Next.js 16.2.9.

The real model conversations, the returned objects, the engineering assertions, and the original Qwen
review text are stored in [validation-2026-09-10.json](validation-2026-09-10.json); it contains no API
credentials.

## Engineering checks

All of the items below passed; the test backing each is in parentheses.

- Deep copy of structured messages and tool arguments: passed (`TestCheckpointDeepCopyAndBoundaries`).
- Summarization and compaction requests do not overwrite; completed responses and terminal states are
  published; half-written responses during generation are excluded: passed
  (`TestCheckpointDeepCopyAndBoundaries`, `TestSnapshotExcludesPartialStreamAndSelectsPoolMember`).
- Identity of the real model-pool member: passed (`TestSnapshotExcludesPartialStreamAndSelectsPoolMember`).
- Tool pairing, replay of 20 bundles, budget reduction and over-limit error: passed
  (`TestBuildRequestCompactionToolPairingAndBudget`).
- Parallel execution of main and side question, isolated cancellation in both directions: passed
  (blocking Provider, `TestMainSideConcurrencyAndIndependentCancellation`).
- No tool execution, streaming and non-streaming, existing usage at the moment of failure: passed
  (`TestServiceNoToolsAndUsageOnFailure`).
- Real norma ChatAgent with a local Read tool, isolation from the main transcript and activity: passed
  (the streaming and non-streaming sub-cases of `TestSideActualChatCheckpointToolResultAndTranscriptIsolation`).
- Persistence, paging, idempotency, preserving a partial response across a restart: passed
  (`TestSideHistoryIdempotencyPagingAndRecovery`).
- Race between clear and late writes, parent-resource deletion, version comparison: passed
  (`TestSideClearLateWritersAndDeletedParent`).
- MainAgent and Worker archive and restore (v1, v2, v3): passed (`TestSideTaskArchiveVersions`).
- The three parent interfaces, authentication, resource ownership, logical Worker deletion: passed
  (`TestSideHTTPGlobalLimitTaskWorkerAndDeletion`, `TestSideCheckpointPersistsBeforeAdmissionAndRestart`).
- Side question available even while the main session is busy, independent SSE reconnection and
  disconnection, cancel, clear: passed (`TestSideHTTPBusyIsolationClearAndReconnect`).
- One concurrent run per parent session, four globally: passed (the two `TestSideHTTP…` cases).
- Snapshot stored before submission, follow-up after a restart, an old session cannot forge a
  snapshot: passed (`TestSideCheckpointPersistsBeforeAdmissionAndRestart`).
- Refuses to continue when the cached configuration was deleted or the model changed: passed
  (`TestSideRejectsDeletedOrChangedCachedProfile`).
- Cancels before archiving and waits for the final response and usage to be stored: passed
  (`TestSideTaskDrainPersistsBeforeArchive`).
- Usage is recorded once and attributed to the side question even when the streaming consumer cancels
  early: passed (`TestSideUsageRecordedOnceOnConsumerCancellation`).
- A Worker and deadline execution context restored by a restart keep publishing new snapshots: passed
  (`TestSideRestoredWorkerRuntimePublishesNewCheckpoint`).
- Race check of the relevant packages: passed (the command below).
- TypeScript and the production build: passed (`npx tsc --noEmit`, `npm run build`).
- Biome check of the newly added frontend modules: passed (`biome check`, three new modules).

Setting `ARTEX_PG_DSN` to a throwaway database reproduces the automated checks (do not point it at a
production database):

```sh
go test -race ./agent ./db ./server ./sidequestion ./llmrec ./llmpool \
  -run 'Test(Side|Checkpoint|Snapshot|BuildRequest|Service|MainSide|CaptureRun|TaskArchive|CompleteForwards|StopIntent|CancelIntent)' -count=1
cd web
npx tsc --noEmit
npx biome check src/lib/side-questions.ts src/hooks/use-side-questions.ts src/components/side-question-workspace.tsx
npm run build
```

Not every full Go regression test passes. Two pre-existing tests in the `server` package fail while
cleaning up a temporary directory, and both report `TempDir RemoveAll … directory not empty`:

- `TestInheritedActivityDetailAndRelationDeletion`
- `TestTaskMetadataPatchReturnsRenameAndPin`

Running the `server` package again on the unmodified baseline commit, in the same isolated
environment, reproduces these two cleanup failures exactly. On the baseline run, a target-node-count
assertion failure in `TestCoreTaskLifecyclePG` also appeared separately; the `server` regression of
the final revision did not show that assertion failure. The other packages passed, and the
side-question cases and race checks in this change passed too. Baseline problems were not marked as
passing because of this review, and existing assertions were not changed to hide a problem.

The Next.js build prints pre-existing warnings about multiple lockfiles and workspace-root inference,
but the build completes and every page is generated successfully.

## Browser checks

The Codex built-in browser was pointed at an isolated local Go service and the Next.js development
server. The following manual and automated interactions were performed at desktop and at a 390 × 844
narrow viewport, checking screenshots and browser logs:

- Typing `/btw` while ordinary chat is running shows the main content and the side question at the
  same time, and the desktop sidebar works normally.
- Follow-up questions were then asked. Stopping the side question keeps the part already generated,
  and the main flow continues.
- Closing the panel lets the request continue, and reopening it recovers the completed response.
  After reloading the page, typing bare `/btw` recovers the history.
- In the narrow-viewport drawer, input, buttons, history, and closing work normally, with no
  horizontal overflow.
- Clear shows a confirmation popup, and after clearing the history is gone while the main transcript
  and snapshot stay untouched.
- Asking a task's MainAgent and two Workers respectively and switching between them kept the agent
  tabs and histories separate.
- With a Worker kept running by a blocking local model fixture, `/btw` was submitted from the
  Worker's main input box. After stopping the side question the Worker still showed its live run state
  and its own pause button, and the side question stored a partial response.
- The browser error/warning log was empty.

Controllable fixtures were used to verify concurrency timing precisely, and do not depend on the
output speed of a real model. During debugging, two attempts at the Worker check did not produce a
valid concurrent window (the task had already finished, or the response finished early); the fixture
was fixed and the check re-run, and it passed. Those early attempts are not counted as a valid pass.

## Real model conversations

`grok-4.6` was detected first. The OpenAI-compatible interface is
`http://127.0.0.1:12580/tingly/openai`. The detection result was HTTP 200 with the model name
`grok-4.6` and `READY`, in 2.82 seconds. Because the first choice was available, Tingly's `glm` and
Zhipu's `glm-5.3` fallback chain were not enabled, and those two fallback services were not verified
here.

- Asking about assets, goals, and the marker while the main session is running: it returned
  `redhaze.top`, the first-page read and the goal summary, and `BTW-REAL-0910`, and the side question
  completed (16.97 seconds).
- Asking about the tool evidence after the main session finished reading the first page: WebFetch 200,
  curl's 301 → 302 → 200 redirects, quoted the page title exactly (7.24 seconds).
- Asking the side question to create a test file with Bash: it refused to execute and the target file
  was not created (7.74 seconds).
- A side question asked after completion does not change the main context: the SHA-256 of the main
  transcript and the main activity record still matched, and the side question's tool executions were
  0.
- Stopping and restarting the Go service for real and then asking a follow-up: it kept the three
  previous side-question records, answered the assets, marker, and title straight from the persisted
  snapshot, and did not run the main agent again.
- Using the Grok non-streaming configuration in a new session: it answered the asset and `ATOMIC-0910`
  exactly, and returned and stored the usage (input 11734, output 138, cache_read 11520).

The main session in the assets case read the public first page with WebFetch and Bash/curl; the
landing page was `https://id.redhaze.top/home`, and the title the page returned was "红幕科技 RedHaze
Group · 全球综合集团门户" (quoted as-is because the model returned that text). Bash only saved the
response to a local test file for a moment and executed no write to the remote. This fact was checked
separately from "the side question executed no tools".

Main transcript verification value: `e7e61f135a4a120954b539f357e8c4205d7d5cd7460dcaf3dc0fd066463e1d00`.

**Usage limitation:** Tingly's Grok streaming response did not return usage. Sending
`stream_options.include_usage=true` directly to verify it gave HTTP 200, 12 data frames, and 0 usage
frames. So the 0 in the streaming test means the endpoint does not provide usage; it must not be read
as meaning there was no billing. The non-streaming usage and the fixture's failure and cancellation
usage were all stored correctly.

## Qwen review

The reviewing model was `qwen-flash`, the OpenAI-compatible interface is
`https://dashscope.aliyuncs.com/compatible-mode/v1`, HTTP 200. It was given the three real
side-question conversations above, the main session's tool evidence, and the engineering assertions,
and returned `verdict: accept` and `concerns: []`. It judged that the responses matched the assets,
marker, and page-read evidence, and that the side question's tool refusal conformed to the
constraints. Review usage: prompt 6625, completion 312, total 6937.

This Qwen review does not cover the service restart and non-streaming tests added later. Qwen's "no
writing" generalization is too broad: the main session's curl did create a local response temp file,
which is recorded clearly above. Concurrency, zero tool executions, and transcript isolation are
judged by the engineering assertions; the model review only assists with the quality evaluation of
the responses.
