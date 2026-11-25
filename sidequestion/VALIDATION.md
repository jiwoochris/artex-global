# `/btw` validation record

English · [한국어](VALIDATION.ko.md) · [中文](VALIDATION.zh.md)

Date: 2026-09-10. Branch: `codex/btw-side-question`. Baseline commit: `8dae851b9b622f2ff2631f332fde9719d0b16fba`.

> This document is an English translation of the original Chinese document (`VALIDATION.zh.md`). The original is preserved unchanged so that changes in the upstream repository are easy to compare against.

A separate PostgreSQL test database and data directory were used. Real model credentials were injected only into the isolated test environment and were not written into the code or this record, and the product's default model was not changed. Go 1.26.3, norma v0.3.6, Next.js 16.2.9.

The real model conversations, returned objects, engineering assertions, and the raw Qwen review text are stored in [validation-2026-09-10.json](validation-2026-09-10.json), which contains no API credentials.

## Engineering checks

| Scope | Result | Evidence |
| --- | --- | --- |
| Deep copy of structured messages and tool arguments | Pass | `TestCheckpointDeepCopyAndBoundaries` |
| Summary / compaction requests do not overwrite; complete replies and terminal states are published; half-finished replies are excluded | Pass | `TestCheckpointDeepCopyAndBoundaries`, `TestSnapshotExcludesPartialStreamAndSelectsPoolMember` |
| Identity of the actual model-pool member | Pass | `TestSnapshotExcludesPartialStreamAndSelectsPoolMember` |
| Tool pairing, 20-pair replay, budget trimming and over-limit errors | Pass | `TestBuildRequestCompactionToolPairingAndBudget` |
| Main and side running in parallel, cancellation isolated in both directions | Pass | Blocking Provider, `TestMainSideConcurrencyAndIndependentCancellation` |
| No tool execution, streaming / non-streaming, usage already accrued at the point of failure | Pass | `TestServiceNoToolsAndUsageOnFailure` |
| Real norma ChatAgent + local Read tool, isolation of the main transcript / activity | Pass | `TestSideActualChatCheckpointToolResultAndTranscriptIsolation`, streaming and non-streaming subcases |
| Persistence, paging, idempotency, partial answers kept across a restart | Pass | `TestSideHistoryIdempotencyPagingAndRecovery` |
| Race between clear and late writes, parent-resource deletion, version comparison | Pass | `TestSideClearLateWritersAndDeletedParent` |
| MainAgent / Worker archive and restore, v1/v2/v3 | Pass | `TestSideTaskArchiveVersions` |
| The three parent interfaces, authentication, resource ownership, Worker logical deletion | Pass | `TestSideHTTPGlobalLimitTaskWorkerAndDeletion`, `TestSideCheckpointPersistsBeforeAdmissionAndRestart` |
| Side questions possible while the main session is busy, independent SSE reconnect / disconnect, cancel, clear | Pass | `TestSideHTTPBusyIsolationClearAndReconnect` |
| Concurrency of 1 per parent session / 4 globally | Pass | Two `TestSideHTTP…` cases |
| Snapshot persisted before submission, follow-up questions after a restart, old sessions cannot forge a snapshot | Pass | `TestSideCheckpointPersistsBeforeAdmissionAndRestart` |
| Refuses to continue when a cached configuration is deleted or its model changes | Pass | `TestSideRejectsDeletedOrChangedCachedProfile` |
| Cancels before archiving and waits for the final answer and usage to be persisted | Pass | `TestSideTaskDrainPersistsBeforeArchive` |
| When a streaming consumer cancels early, usage is recorded only once and attributed to the side question | Pass | `TestSideUsageRecordedOnceOnConsumerCancellation` |
| Worker / deadline run contexts automatically restored after a restart keep publishing new snapshots | Pass | `TestSideRestoredWorkerRuntimePublishesNewCheckpoint` |
| Race checks on the related packages | Pass | The commands below |
| TypeScript and production build | Pass | `npx tsc --noEmit`, `npm run build` |
| Biome on the newly added frontend modules | Pass | `biome check`, 3 new modules |

You can reproduce the automated checks by setting `ARTEX_PG_DSN` to a separate, disposable database (do not point it at a production database):

```sh
go test -race ./agent ./db ./server ./sidequestion ./llmrec ./llmpool \
  -run 'Test(Side|Checkpoint|Snapshot|BuildRequest|Service|MainSide|CaptureRun|TaskArchive|CompleteForwards|StopIntent|CancelIntent)' -count=1
cd web
npx tsc --noEmit
npx biome check src/lib/side-questions.ts src/hooks/use-side-questions.ts src/components/side-question-workspace.tsx
npm run build
```

The full Go regression run is not all green. Two existing tests in the `server` package fail during temporary-directory cleanup, and both report `TempDir RemoveAll … directory not empty`:

- `TestInheritedActivityDetailAndRelationDeletion`
- `TestTaskMetadataPatchReturnsRenameAndPin`

Exporting the source from the unmodified baseline commit above and re-running the `server` package in the same isolated environment reproduces the same two cleanup failures. The baseline run additionally showed an assertion failure on the target node count in `TestCoreTaskLifecyclePG`, but the `server` regression run on the final modified version did not have that assertion failure. The other packages passed, and the side-question cases and race checks for this work passed. The baseline's problems were not marked as passing in this acceptance, and no existing assertion was changed to hide a problem.

The Next.js build prints existing warnings about inferring the root from multiple lockfiles / workspaces, but the build completes and every page is generated successfully.

## Browser checks

The Codex in-app browser was connected to an isolated local Go service and a Next.js dev server. The following manual and automated operations were performed on desktop and on a 390 × 844 narrow screen, and the screenshots and browser logs were checked:

- Entering `/btw` while an ordinary chat was running displays the main content and the side question at the same time; the desktop sidebar works correctly.
- Follow-up questions were asked in a row. Stopping a side question keeps the part already generated, and the main flow continues.
- The request continues when the panel is closed, and reopening it restores the completed answer. After a page reload, entering `/btw` with nothing after it restores the history.
- In the narrow-screen Drawer, input, buttons, history, and closing work correctly, with no horizontal overflow.
- Clear shows a confirmation dialog; after clearing, the history disappears but the main transcript and the snapshot remain.
- When questions were asked of the task's MainAgent and of two Workers separately and the view was switched between them, the agent tabs and histories did not get mixed up.
- With a blocking local model fixture keeping a Worker running, `/btw` was submitted from the Worker's main input box. After the side question was stopped, the Worker still showed its live running state and its own pause button, and the side question saved its partial answer.
- The browser error / warning log is empty.

The controllable fixture was used to verify the concurrency timing precisely and does not depend on how fast a real model produces output. During debugging, two Worker run checks did not produce a valid concurrency window (the task had already finished, or the answer finished early); they were redone after fixing the fixture and passed. These initial operations are not counted as valid passes.

## Real model conversations

`grok-4.6` was probed first. The OpenAI-compatible interface is `http://127.0.0.1:12580/tingly/openai`. The probe returned HTTP 200 with the model name `grok-4.6` and `READY`, and took 2.82 seconds. Because the first choice was available, the fallback chain of Tingly's `glm` or Zhipu's `glm-5.3` was not enabled, and those two fallback services were not validated this time.

| Scenario | Actual result |
| --- | --- |
| Asked about the asset, goal, and marker while the main session was running | Returned `redhaze.top`, reading the home page and summarizing the goal, and `BTW-REAL-0910`; the side question completed in 16.97 seconds |
| Asked about the tool evidence after the main session finished reading the home page | Correctly cited WebFetch 200, curl's 301 → 302 → 200 redirect, and the page title; 7.24 seconds |
| The side question asked Bash to create a test file | Refused to execute; the target file was not created; 7.74 seconds |
| A completed side question does not change the main context | The main transcript's SHA-256 and the main activity record stayed identical; the side question's tool execution count was 0 |
| Follow-up question after actually stopping / restarting the Go service | Kept the 3 earlier side-question history entries and answered the asset, marker, and title directly from the persisted snapshot, without re-running the main agent |
| New session using the Grok non-streaming configuration | Correctly answered the asset and `ATOMIC-0910`; returned and saved usage: input 11734, output 138, cache_read 11520 |

In the asset case, the main session read the public home page with WebFetch and Bash/curl; the landing page was `https://id.redhaze.top/home`, and the title the page returned was "红幕科技 RedHaze Group · 全球综合集团门户" (quoted verbatim as returned by the model; roughly "RedHaze Technology, RedHaze Group · Global Conglomerate Portal"). Bash only stored the response temporarily in a local test file and did not perform any remote write. This fact was verified separately from the point that "the side question did not execute tools."

Main transcript checksum: `e7e61f135a4a120954b539f357e8c4205d7d5cd7460dcaf3dc0fd066463e1d00`.

**Usage limitation:** Tingly's Grok streaming responses did not return usage. A separate check that sent `stream_options.include_usage=true` directly got HTTP 200, 12 data frames, and 0 usage frames. A 0 in the streaming tests therefore means the endpoint does not provide usage, and must not be read as meaning nothing was billed. Non-streaming usage and the fixture's failure / cancellation usage were all saved correctly.

## Qwen review

The review model was `qwen-flash`, the OpenAI-compatible interface was `https://dashscope.aliyuncs.com/compatible-mode/v1`, and the response was HTTP 200. It was given the first three real side-question conversations, the main session's tool evidence, and the engineering assertions, and it returned `verdict: accept` and `concerns: []`, judging that the answers matched the asset, marker, and page-reading evidence and that the side question's refusal to run tools complied with the constraints. Review usage: prompt 6625, completion 312, total 6937.

The scope of this Qwen review does not include the service-restart and non-streaming tests that were added later. Qwen's generalization of "no writes" is too broad: the main session's curl did create a local temporary response file, as recorded explicitly above. Concurrency, zero tool executions, and transcript isolation are judged by the engineering assertions; the model review only assists in assessing answer quality.
