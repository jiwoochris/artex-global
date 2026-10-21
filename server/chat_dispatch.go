package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/chatstate"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/artex/llmrec"
	"github.com/Autumn-27/artex/locale"
	"github.com/Autumn-27/norma/transcript"
)

func (s *Server) recoverChatControls() error {
	if s.m.pg == nil {
		return nil
	}
	if err := s.recoverChatTranscripts(s.ctx); err != nil {
		return fmt.Errorf("transcript recovery: %w", err)
	}
	abandoned, err := s.m.pg.RecoverChatPreparations(s.ctx)
	if err != nil {
		return fmt.Errorf("preparation recovery: %w", err)
	}
	for _, ref := range abandoned {
		if ref.ConversationID > 0 {
			if exists, e := s.m.pg.ChatConversationExists(s.ctx, ref.ConversationID); e == nil && !exists {
				_ = os.RemoveAll(s.chatPromptDir(ref))
			}
		}
	}
	if err := s.m.pg.RecoverChatRuns(s.ctx); err != nil {
		return fmt.Errorf("run recovery: %w", err)
	}
	return nil
}

func (s *Server) resumeChatControls() error {
	if s.m.pg == nil {
		return nil
	}
	keys, err := s.m.pg.PendingChatGates(s.ctx)
	if err != nil {
		return fmt.Errorf("pending chat queues: %w", err)
	}
	for _, key := range keys {
		s.dispatchChatGate(key)
	}
	return nil
}

func (s *Server) initChatControls() error {
	if err := s.recoverChatControls(); err != nil {
		return err
	}
	return s.resumeChatControls()
}
func chatBusyKey(ref chatstate.SessionRef) string {
	if ref.ConversationID > 0 {
		return ref.Key()
	}
	return strconv.FormatInt(ref.TaskID, 10)
}

// Claim and cancellation registration finish before the accepting HTTP request
// returns. No DB transaction is held while tools or inference execute.
func (s *Server) dispatchChatGate(gate string) { s.dispatchChatHistoryInput(gate, "") }
func (s *Server) dispatchChatHistoryInput(gate, id string) {
	if err := s.dispatchChatInput(gate, id); err != nil {
		log.Printf("[chat %s] dispatch: %v", gate, err)
	}
}
func (s *Server) dispatchChatInput(gate, selected string) error {
	if s.m.pg == nil {
		return chatstate.ErrGone
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	s.chatMu.Lock()
	if s.chatClosing || s.ctx.Err() != nil {
		s.chatMu.Unlock()
		return context.Canceled
	}
	ref, err := s.m.pg.ChatGateSession(s.ctx, gate)
	if err != nil {
		s.chatMu.Unlock()
		return err
	}
	key := chatBusyKey(ref)
	if s.chatBusy[key] {
		s.chatMu.Unlock()
		return nil
	}
	taskID := ""
	if ref.TaskID > 0 {
		taskID = strconv.FormatInt(ref.TaskID, 10)
		if !s.engine.beginTaskOperation(taskID) {
			s.chatMu.Unlock()
			return chatstate.ErrGone
		}
	}
	var run *chatstate.Run
	if selected != "" {
		run, err = s.m.pg.ClaimChatHistoryInput(s.ctx, gate, selected)
	} else {
		run, err = s.m.pg.ClaimNextChatInput(s.ctx, gate)
	}
	if err != nil || run == nil {
		if taskID != "" {
			s.engine.decInflight(taskID)
		}
		s.chatMu.Unlock()
		return err
	}
	ctx, cancel := context.WithCancelCause(s.ctx)
	s.chatBusy[key] = true
	s.chatCancel[key] = cancel
	s.chatWG.Add(1)
	s.chatMu.Unlock()
	go func() {
		defer s.chatWG.Done()
		status := "failed"
		defer func() {
			if problem := recover(); problem != nil {
				log.Printf("[chat %s] panic: %v", run.ID, problem)
				status = "unknown"
			}
			if ctx.Err() != nil {
				status = "stopped"
				if s.ctx.Err() != nil {
					status = "unknown"
				}
			}
			if err := s.m.pg.FinishChatRun(context.Background(), run.ID, status); err != nil && !errors.Is(err, chatstate.ErrGone) {
				log.Printf("[chat %s] finish: %v", run.ID, err)
			}
			s.chatMu.Lock()
			delete(s.chatBusy, key)
			delete(s.chatCancel, key)
			s.chatMu.Unlock()
			cancel(agent.AbortChatTurnFinished)
			if taskID != "" {
				s.engine.decInflight(taskID)
			}
			if status == "completed" {
				s.dispatchChatGate(gate)
			}
		}()
		if err := s.executeChatRun(ctx, *run); err == nil {
			status = "completed"
		} else {
			if errors.Is(err, agent.ErrChatPersistence) {
				status = "unknown"
			}
			log.Printf("[chat %s] run: %v", run.ID, err)
		}
	}()
	return nil
}
// chatRunAttribution attaches the run's source-task attribution to ctx.
//
// A conversation-owned run may carry the task and exploration that produced it (a
// reporter trigger fired by a task finding). The attribution lives on the run,
// not on the session, so it is read from there.
//
// It has to reach TWO ledgers that meter independently:
//
//   - tool_usage goes through agent.RunInfo, captured at tool-assembly time.
//   - llm_usage goes through llmrec, which derives task/worker from the
//     transcript session id via parseSession — and parseSession only understands
//     "exp<N>-<lane>". A chat run's session is "conv-N", so without the explicit
//     llmrec.WithTaskID channel every reporter LLM call is metered with
//     task_id NULL even though its tool_usage rows are correctly attributed.
//
// Both are context values, so the "conv-N" session id the chat agent installs
// downstream does not clobber them (same approach as the side-question path in
// side_questions.go).
//
// A plain user chat has no source task and stays unattributed.
func chatRunAttribution(ctx context.Context, run chatstate.Run) context.Context {
	if run.SourceTaskID <= 0 || run.SourceExplorationID <= 0 {
		return ctx
	}
	ctx = agent.WithRunInfo(ctx, agent.RunInfo{TaskID: run.SourceTaskID, ExplorationID: run.SourceExplorationID})
	return llmrec.WithTaskID(ctx, strconv.FormatInt(run.SourceTaskID, 10))
}

func (s *Server) executeChatRun(ctx context.Context, run chatstate.Run) (runErr error) {
	ctx = locale.WithLang(ctx, locale.Lang(run.Language))
	defer func() {
		if runErr != nil && !errors.Is(runErr, chatstate.ErrGone) && !errors.Is(runErr, agent.ErrChatPersistence) {
			if e := s.displayChatPreflightFailure(ctx, run, runErr); e != nil && !errors.Is(e, chatstate.ErrGone) {
				runErr = fmt.Errorf("%w: %v", agent.ErrChatPersistence, e)
			}
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.validateChatPrompt(run.Session, run.Prompt, true); err != nil {
		return err
	}
	if run.HistoryRevision > 0 {
		revision, err := s.m.pg.ActiveChatTranscript(ctx, run.Session)
		if err != nil {
			return err
		}
		if revision.TranscriptID != run.TranscriptID {
			return agent.ErrChatPersistence
		}
		if err = chatstate.ValidateTranscriptRevision(transcript.NewStore(filepath.Join(s.m.dir, "transcripts")), *revision); err != nil {
			return fmt.Errorf("%w: %v", agent.ErrChatPersistence, err)
		}
	}
	ctx = agent.WithChatRun(ctx, run, s.m.pg)
	ctx = chatRunAttribution(ctx, run)
	message, err := composeChatMentionMessageForLanguage(s.m.pg, run.Prompt.Text, locale.FromContext(ctx))
	if err != nil {
		return err
	}
	message = composeAgentMessageForLanguage(message, run.Prompt.Attachments, s.chatPromptDir(run.Session), locale.FromContext(ctx))
	if run.SourceKind == "user" {
		ctx = intercept.WithReviewContext(ctx, "", intercept.ReviewBackground{Source: intercept.BackgroundUserMessage, Text: run.Prompt.Text})
		ctx = withNativeRole(ctx, "mainagent")
	}
	if run.Session.ConversationID > 0 {
		c, err := s.m.pg.GetConversation(run.Session.ConversationID)
		if err != nil {
			return err
		}
		if c == nil {
			return chatstate.ErrGone
		}
		ctx = intercept.WithConvID(ctx, c.ID)
		// Name the token-ledger lane after the agent driving this conversation.
		// llmrec cannot derive it from a "conv-N" session id (parseSession only
		// understands "exp<N>-<lane>"), so without this every chat LLM call is
		// metered with worker NULL and is invisible in the per-agent token views.
		//
		// Set before the external-source early return below: triggered reporter
		// runs (finding/goal/tool/timeout fires) are external sources, and they are
		// exactly the runs whose token spend must be attributed.
		ctx = llmrec.WithWorker(ctx, c.AgentKey)
		if run.SourceKind == "external" {
			return s.executeExternalConversation(ctx, c, message)
		}
		ca := s.resolveChatAgent(c)
		if ca == nil {
			return locale.Errorf("chat model unavailable")
		}
		ctx = intercept.WithConvID(ctx, c.ID)
		ua := chatRunActivity(run, userActivityWithAttachments(c.AgentKey, run.Prompt.Text, run.Prompt.Attachments))
		if _, err = s.m.pg.AppendConvActivity(c.ID, ua); err != nil {
			return err
		}
		if isDefaultConversationTitle(c.Title) {
			title := firstLine(run.Prompt.Text, 40)
			if title != "" {
				_ = s.m.pg.RenameConversation(c.ID, title)
			}
		}
		webSearch := false
		if a, e := s.m.pg.GetAgentByKey(c.AgentKey); e == nil && a != nil {
			webSearch = a.WebSearch
		}
		var writeErr error
		emit := func(rec db.Activity) {
			rec = chatRunActivity(run, rec)
			if _, err := s.m.pg.AppendConvActivity(c.ID, rec); err != nil {
				writeErr = err
			}
		}
		_, err = ca.Chat(ctx, c.AgentKey, run.Session.Key(), message, s.agentMaxTurns(c.AgentKey), time.Duration(s.agentRunSeconds(c.AgentKey))*time.Second, webSearch, emit)
		_ = s.m.pg.TouchConversation(c.ID)
		if err != nil {
			return err
		}
		return writeErr
	}
	taskID := strconv.FormatInt(run.Session.TaskID, 10)
	t := s.m.ResolveTask(taskID)
	if t == nil || s.engine.IsDeleting(taskID) {
		return chatstate.ErrGone
	}
	seg := run.Session.MainSeg
	emit := func(rec db.Activity) {
		rec = chatRunActivity(run, rec)
		rec.MainSeg = &seg
		s.engine.emitActivity(t, rec)
	}
	emit(userActivityWithAttachments("mainagent", run.Prompt.Text, run.Prompt.Attachments))
	if !s.taskRuntimeAvailable(t, "mainagent") {
		emit(db.Activity{Worker: "mainagent", Kind: "text", Summary: s.fallbackChat(t, run.Prompt.Text, locale.FromContext(ctx))})
		return nil
	}
	ma := s.agentsForTask(t).main
	if ma == nil {
		return locale.Errorf("main agent unavailable")
	}
	s.engine.BeginLLMCall(t.ID)
	defer s.engine.EndLLMCall(t.ID)
	_, err = ma.Chat(ctx, run.Session.TaskID, seg, s.m.Assets(), t.Store, t.Goal, message, emit, t.Notify, func() { s.reviveTask(t) }, t.NotifyGoal, t.NotifyHint)
	return err
}

func chatRunActivity(run chatstate.Run, a db.Activity) db.Activity {
	a.RunID = run.ID
	a.TurnID = run.TurnID
	active := true
	a.ActiveAttempt = &active
	return a
}
