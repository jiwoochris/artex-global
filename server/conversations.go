package server

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/chatstate"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/locale"
)

const (
	maxConversationRequestBytes  = 64 << 10
	maxConversationAgentKeyRunes = 120
	maxConversationTitleRunes    = 200
)

func decodeConversationRequest(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxConversationRequestBytes)
	if err := decode(r, value); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, locale.Text(responseLanguage(w), "Request body is too large"))
		} else {
			writeError(w, http.StatusBadRequest, err)
		}
		return false
	}
	return true
}

// ---------- conversations (chat page) ----------
//
// A conversation is a ChatGPT-style thread bound to an agent key, independent of
// the pentest task graph. Turns run on ChatAgent; steps persist to
// conversation_activities and the browser POLLS ?since=cursor for live updates
// (no per-conversation SSE broadcaster needed).

type conversationListItem struct {
	*db.Conversation
	Running bool `json:"running"`
}

func (s *Server) pgListConversations(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	cs, err := pg.ListConversations()
	if err != nil {
		writeError(w, 500, err)
		return
	}
	items := make([]conversationListItem, 0, len(cs))
	s.chatMu.Lock()
	for _, c := range cs {
		items = append(items, conversationListItem{Conversation: c, Running: s.chatBusy[s.convBusyKey(c.ID)]})
	}
	s.chatMu.Unlock()
	writeJSON(w, 200, map[string]any{"conversations": items})
}

func (s *Server) pgCreateConversation(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req struct {
		AgentKey     string `json:"agent_key"`
		Title        string `json:"title"`
		LLMProfileID *int64 `json:"llm_profile_id"`
	}
	if !decodeConversationRequest(w, r, &req) {
		return
	}
	req.AgentKey = strings.TrimSpace(req.AgentKey)
	if req.AgentKey == "" {
		writeErr(w, 400, locale.Text(responseLanguage(w), "agent_key is required"))
		return
	}
	if utf8.RuneCountInString(req.AgentKey) > maxConversationAgentKeyRunes {
		writeErr(w, 400, fmt.Sprintf(locale.Text(responseLanguage(w), "agent_key must not exceed %d characters"), maxConversationAgentKeyRunes))
		return
	}
	a, err := pg.GetAgentByKey(req.AgentKey)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if a == nil {
		writeErr(w, 404, locale.Text(responseLanguage(w), "Agent does not exist"))
		return
	}
	if req.LLMProfileID != nil {
		if _, ok := s.loadProfileConfig(*req.LLMProfileID); !ok {
			writeErr(w, 400, locale.Text(responseLanguage(w), "The selected LLM configuration does not exist or has no API key"))
			return
		}
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = locale.Text(responseLanguage(w), "New conversation")
	}
	if utf8.RuneCountInString(title) > maxConversationTitleRunes {
		writeErr(w, 400, fmt.Sprintf(locale.Text(responseLanguage(w), "Title must not exceed %d characters"), maxConversationTitleRunes))
		return
	}
	c, err := pg.CreateConversation(req.AgentKey, title, req.LLMProfileID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, c)
}

func (s *Server) pgUpdateConversation(w http.ResponseWriter, r *http.Request) {
	pg, c, ok := s.convByID(w, r)
	if !ok {
		return
	}
	var req struct {
		LLMProfileID *int64 `json:"llm_profile_id"` // null clears the override
	}
	if !decodeConversationRequest(w, r, &req) {
		return
	}
	if req.LLMProfileID != nil {
		if _, ok := s.loadProfileConfig(*req.LLMProfileID); !ok {
			writeErr(w, 400, locale.Text(responseLanguage(w), "The selected LLM configuration does not exist or has no API key"))
			return
		}
	}
	if err := pg.UpdateConversationProfile(c.ID, req.LLMProfileID); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// convByID resolves the {id} path value to a conversation (404 if absent).
func (s *Server) convByID(w http.ResponseWriter, r *http.Request) (*db.DB, *db.Conversation, bool) {
	pg := s.pg(w)
	if pg == nil {
		return nil, nil, false
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, 400, locale.Text(responseLanguage(w), "bad conversation id"))
		return nil, nil, false
	}
	c, err := pg.GetConversation(id)
	if err != nil {
		writeError(w, 500, err)
		return nil, nil, false
	}
	if c == nil {
		writeErr(w, 404, locale.Text(responseLanguage(w), "conversation not found"))
		return nil, nil, false
	}
	return pg, c, true
}

func (s *Server) pgRenameConversation(w http.ResponseWriter, r *http.Request) {
	pg, c, ok := s.convByID(w, r)
	if !ok {
		return
	}
	var req struct {
		Title  *string `json:"title"`
		Pinned *bool   `json:"pinned"`
	}
	if !decodeConversationRequest(w, r, &req) {
		return
	}
	if req.Title == nil && req.Pinned == nil {
		writeErr(w, 400, locale.Text(responseLanguage(w), "Provide at least title or pinned"))
		return
	}
	if req.Title != nil {
		title := strings.TrimSpace(*req.Title)
		if title == "" {
			writeErr(w, 400, locale.Text(responseLanguage(w), "Title is required"))
			return
		}
		if utf8.RuneCountInString(title) > maxConversationTitleRunes {
			writeErr(w, 400, fmt.Sprintf(locale.Text(responseLanguage(w), "Title must not exceed %d characters"), maxConversationTitleRunes))
			return
		}
		req.Title = &title
	}
	updated, err := pg.UpdateConversation(c.ID, db.ConversationPatch{Title: req.Title, Pinned: req.Pinned})
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if updated == nil {
		writeErr(w, 404, locale.Text(responseLanguage(w), "conversation not found"))
		return
	}
	writeJSON(w, 200, updated)
}

func (s *Server) pgDeleteConversation(w http.ResponseWriter, r *http.Request) {
	pg, c, ok := s.convByID(w, r)
	if !ok {
		return
	}
	drainCtx, stop := context.WithTimeout(r.Context(), taskDeleteDrainTimeout)
	defer stop()
	if err := s.drainConversationChat(drainCtx, c.ID); err != nil {
		chatControlError(w, err)
		return
	}
	if err := pg.DeleteConversation(c.ID); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": c.ID})
}

const maxConversationDeleteBatch = 100

type conversationDeleteItem struct {
	ID    int64  `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func (s *Server) cancelConversation(id int64) {
	busyKey := s.convBusyKey(id)
	s.chatMu.Lock()
	cancel := s.chatCancel[busyKey]
	s.chatMu.Unlock()
	if cancel != nil {
		cancel(agent.AbortChatStoppedByUser)
	}
}

// pgDeleteConversationsBatch deletes up to 100 selected conversations in one
// database statement. Missing ids are reported per item so a stale list does not
// hide what was actually removed.
func (s *Server) pgDeleteConversationsBatch(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var request struct {
		IDs []int64 `json:"ids"`
	}
	if !decodeConversationRequest(w, r, &request) {
		return
	}
	ids := make([]int64, 0, len(request.IDs))
	seen := make(map[int64]struct{}, len(request.IDs))
	for _, id := range request.IDs {
		if id <= 0 {
			writeErr(w, http.StatusBadRequest, locale.Text(responseLanguage(w), "Invalid conversation ID"))
			return
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 || len(ids) > maxConversationDeleteBatch {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf(locale.Text(responseLanguage(w), "ids must contain between 1 and %d entries"), maxConversationDeleteBatch))
		return
	}
	for _, id := range ids {
		drainCtx, stop := context.WithTimeout(r.Context(), taskDeleteDrainTimeout)
		err := s.drainConversationChat(drainCtx, id)
		stop()
		if err != nil && !errors.Is(err, chatstate.ErrGone) {
			chatControlError(w, err)
			return
		}
	}
	deleted, err := pg.DeleteConversations(ids)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	deletedSet := make(map[int64]struct{}, len(deleted))
	for _, id := range deleted {
		deletedSet[id] = struct{}{}
	}
	items := make([]conversationDeleteItem, 0, len(ids))
	for _, id := range ids {
		_, ok := deletedSet[id]
		item := conversationDeleteItem{ID: id, OK: ok}
		if !ok {
			item.Error = locale.Text(responseLanguage(w), "conversation not found")
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) convBusyKey(id int64) string { return "conv-" + strconv.FormatInt(id, 10) }

// pgConversationMessages returns steps after ?since=cursor plus whether a turn is
// still running (so the client knows to keep polling). Same item shape as the task
// activity stream, so the frontend transcript renderer is reused verbatim.
func (s *Server) pgConversationMessages(w http.ResponseWriter, r *http.Request) {
	pg, c, ok := s.convByID(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit := atoiDefault(q.Get("limit"), 200)
	s.chatMu.Lock()
	running := s.chatBusy[s.convBusyKey(c.ID)]
	s.chatMu.Unlock()

	// Incremental tail: ?since=N returns steps after id N (ASC) — used by the live
	// poll and the post-send fetch. A generous cap so a burst is never dropped.
	if sv := q.Get("since"); sv != "" {
		since, _ := strconv.ParseInt(sv, 10, 64)
		items, cursor, err := pg.ConvActivityList(c.ID, since, max(limit, 1000))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"items": activityDTOs(items), "cursor": cursor, "running": running, "hasMore": false})
		return
	}

	// History page (reverse pagination): no ?since → the latest page; ?before=N →
	// the page of older steps ending before id N. hasMore lets the client stop
	// loading earlier history once the top of the thread is reached.
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	items, hasMore, err := pg.ConvActivityPage(c.ID, before, limit)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	cursor := before
	if n := len(items); n > 0 {
		cursor = items[n-1].ID
	}
	writeJSON(w, 200, map[string]any{"items": activityDTOs(items), "cursor": cursor, "running": running, "hasMore": hasMore})
}

// pgConversationMsgDetail lazily returns one step's full detail blob.
func (s *Server) pgConversationMsgDetail(w http.ResponseWriter, r *http.Request) {
	pg, c, ok := s.convByID(w, r)
	if !ok {
		return
	}
	seq, ok := pathInt(r, "seq")
	if !ok {
		writeErr(w, 400, locale.Text(responseLanguage(w), "bad seq"))
		return
	}
	detail, err := pg.ConvActivityDetail(c.ID, seq)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"detail": detail})
}

// pgStopConversation aborts the in-flight run for one conversation (manual stop —
// the run/stop button in the chat UI). It cancels only THIS session's agent run;
// the P3 trigger queue is untouched, so the agent's next queued fire still starts.
func (s *Server) pgStopConversation(w http.ResponseWriter, r *http.Request) {
	pg, c, ok := s.convByID(w, r)
	if !ok {
		return
	}
	if err := pg.PauseChatGate(r.Context(), chatstate.SessionRef{ConversationID: c.ID}, -1); err != nil {
		chatControlError(w, err)
		return
	}
	s.cancelConversation(c.ID)
	writeJSON(w, 200, map[string]any{"status": "stopping"})
}

// pgSendConversationMessage persists the human turn, then runs the agent in the
// background (steps stream to conversation_activities, polled by the client). One
// in-flight turn per conversation.
func (s *Server) pgSendConversationMessage(w http.ResponseWriter, r *http.Request) {
	_, c, ok := s.convByID(w, r)
	if !ok {
		return
	}
	var req struct {
		Message     string           `json:"message"`
		Attachments []chatAttachment `json:"attachments"`
	}
	if !decodeConversationRequest(w, r, &req) {
		return
	}
	s.admitCompatibilityChat(w, r, chatstate.SessionRef{ConversationID: c.ID}, req.Message, req.Attachments)
}

// Generated inputs use the durable dispatcher too. The trigger scheduler waits
// for its own admission, never for whichever input happens to claim first.
//
// taskID/explorationID are the SOURCE task attribution of the input (0 for a
// plain user message). They are recorded on the input, not on the session: the
// session's task_id means "this session is owned by that task" and must stay
// unambiguous. A reporter run triggered by a finding must land on that task,
// not on an anonymous chat.
func (s *Server) queueExternalConversation(request context.Context, c *db.Conversation, msg, busyKey string, attribution ...int64) (chatstate.Admission, error) {
	ctx := locale.WithLang(s.ctx, locale.FromContext(request))
	ref := chatstate.SessionRef{ConversationID: c.ID}
	var sourceTaskID, sourceExplorationID int64
	if len(attribution) >= 2 {
		sourceTaskID, sourceExplorationID = attribution[0], attribution[1]
	}
	input, err := s.m.pg.AdmitAttributedExternalChatInput(ctx, ref, "external-"+rand.Text(), chatstate.Prompt{Text: msg}, sourceTaskID, sourceExplorationID)
	// Older callers reserve an in-memory marker before admitting. It is not an
	// active run; only the common dispatcher registers cancellation and claims.
	s.chatMu.Lock()
	if s.chatCancel[busyKey] == nil {
		delete(s.chatBusy, busyKey)
	}
	s.chatMu.Unlock()
	if err != nil {
		return input, err
	}
	s.dispatchChatGate(input.Session.GateKey())
	return input, nil
}
func (s *Server) runConversation(c *db.Conversation, msg, busyKey string, userMessage ...string) {
	if err := s.runConversationWithContext(s.ctx, c, msg, busyKey, userMessage...); err != nil {
		log.Printf("[chat %s] admission: %v", busyKey, err)
	}
}
func (s *Server) runConversationWithContext(request context.Context, c *db.Conversation, msg, busyKey string, userMessage ...string) error {
	_, err := s.queueExternalConversation(request, c, msg, busyKey)
	return err
}
func (s *Server) runConversationSync(c *db.Conversation, msg, busyKey string, langs ...locale.Lang) error {
	_, err := s.runConversationSyncAttributed(c, msg, busyKey, 0, 0, langs...)
	return err
}

// runConversationSyncAttributed is runConversationSync plus the source task
// attribution (taskID, explorationID). A triggered run passes the task that
// produced the event so the session row records where the run came from; a
// plain user message passes 0/0 and stays unattributed.
func (s *Server) runConversationSyncAttributed(c *db.Conversation, msg, busyKey string, taskID, explorationID int64, langs ...locale.Lang) (chatstate.Admission, error) {
	input, err := s.queueExternalConversation(locale.WithLang(s.ctx, locale.First(langs)), c, msg, busyKey, taskID, explorationID)
	if err != nil {
		return input, err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var status string
		if err = s.m.pg.QueryRowContext(s.ctx, `SELECT status FROM chat_control_inputs WHERE id=$1`, input.ID).Scan(&status); err != nil {
			return input, err
		}
		if status != "queued" && status != "claimed" {
			return input, nil
		}
		select {
		case <-s.ctx.Done():
			return input, s.ctx.Err()
		case <-ticker.C:
		}
	}
}

// Retest bookkeeping is shared by first execution and resumed external input.
// Admission, run finalization and cancellation belong to dispatchChatInput.
func (s *Server) executeExternalConversation(ctx context.Context, c *db.Conversation, msg string) (runErr error) {
	// Only the first turn executes a historical retest. Follow-up conversation
	// turns may explain the sealed result; the result tool refuses to overwrite it.
	runErr = locale.NewError("Retest could not start")
	finishStatus, finishReason := "failed", locale.Text(locale.FromContext(ctx), "Retest could not start")
	if c.AgentKey == db.FindingRetestAgentKey {
		// Read without the run cancellation so an immediate stop still seals pending.
		r, err := s.m.pg.FindingRetestForConversation(context.Background(), c.ID)
		if err != nil {
			// The sealing defer below needs r.ID, which we do not have here. Seal by
			// conversation instead, otherwise the row stays 'pending' forever.
			log.Printf(locale.Text(locale.FromContext(ctx), "[conv %d] load retest: %v"), c.ID, err)
			if err := s.m.pg.FailPendingRetestForConversation(c.ID, locale.Text(locale.FromContext(ctx), "Failed to read retest status; start a new retest")); err != nil {
				log.Printf(locale.Text(locale.FromContext(ctx), "[conv %d] seal retest: %v"), c.ID, err)
			}
			return
		}
		if r != nil && r.Status == "pending" {
			defer func() {
				if ctx.Err() != nil {
					finishStatus, finishReason = "stopped", locale.Text(locale.FromContext(ctx), "Retest stopped or the service shut down")
				}
				s.finishRetest(r.ID, finishStatus, finishReason, locale.FromContext(ctx))
			}()
			if ctx.Err() != nil {
				return
			}
			started, err := s.m.pg.StartFindingRetest(ctx, r.ID)
			if err != nil {
				finishReason = locale.ErrorMessage(locale.FromContext(ctx), err)
				return
			}
			if !started {
				return
			}
		}
	}
	if ctx.Err() != nil {
		return
	}
	// precedence: the conversation agent's own binding → this conversation's pin → global.
	ca := s.resolveChatAgent(c)
	if ca == nil {
		finishReason = s.chatUnavailableReason(locale.FromContext(ctx))
		return
	}
	pg := s.m.pg
	maxTurns := s.agentMaxTurns(c.AgentKey)
	maxDuration := time.Duration(s.agentRunSeconds(c.AgentKey)) * time.Second
	webSearch := false
	if a, err := pg.GetAgentByKey(c.AgentKey); err == nil && a != nil {
		webSearch = a.WebSearch
	}
	sessionID := s.convBusyKey(c.ID) // "conv-<id>" transcript session
	emit := func(rec db.Activity) {
		if _, err := pg.AppendConvActivity(c.ID, rec); err != nil {
			log.Printf(locale.Text(locale.FromContext(ctx), "[conv %d] append activity failed: %v"), c.ID, err)
		}
	}
	// On a manual stop ctx is cancelled; Chat already emits a clean "Stopped manually"
	// step, so skip the raw-error entry — only surface genuine failures.
	if _, err := ca.Chat(ctx, c.AgentKey, sessionID, msg, maxTurns, maxDuration, webSearch, emit); err != nil {
		runErr = err
		finishReason = locale.ErrorMessage(locale.FromContext(ctx), err)
		if errors.Is(err, agent.ErrChatPersistence) {
			finishStatus = "unknown"
		}
		if ctx.Err() == nil {
			_, _ = pg.AppendConvActivity(c.ID, db.Activity{Worker: c.AgentKey, Kind: "text", IsError: true,
				Summary: locale.Text(locale.FromContext(ctx), "(Error: %s)", locale.ErrorMessage(locale.FromContext(ctx), err)), Detail: locale.ErrorMessage(locale.FromContext(ctx), err)})
		}
	} else {
		finishStatus, finishReason = "completed", ""
		runErr = nil
	}
	_ = pg.TouchConversation(c.ID)
	return
}

// triggerBehavior is an agent's cached P3 trigger post-processing policy (see the
// agents table trigger_* columns). Read once per fire in StartTriggeredRun so the
// pump never touches the DB while holding queueMu.
type triggerBehavior struct {
	runMode     string // serial | parallel
	mergeMode   string // by_task | all | none (serial only; parallel ignores merging and uses one conversation per event)
	maxParallel int    // per-agent parallel limit; <=0 means unlimited
}

// readTriggerBehavior loads an agent's policy, falling back to safe defaults
// (serial / all / 5) on any error or unknown enum value.
func (s *Server) readTriggerBehavior(agentKey string) triggerBehavior {
	b := triggerBehavior{runMode: "serial", mergeMode: "all", maxParallel: 5}
	if s.m.pg == nil {
		return b
	}
	a, err := s.m.pg.GetAgentByKey(agentKey)
	if err != nil || a == nil {
		return b
	}
	if a.TriggerRunMode == "parallel" {
		b.runMode = "parallel"
	}
	switch a.TriggerMergeMode {
	case "by_task", "all", "none":
		b.mergeMode = a.TriggerMergeMode
	}
	b.maxParallel = a.TriggerMaxParallel
	return b
}

// StartTriggeredRun enqueues a P3 trigger fire for agentKey and pumps the queue. The
// agent's policy decides concurrency + merge: serial → run one at a time (optionally
// merging by task / all / none); parallel → run each fire in its own concurrent
// conversation up to trigger_max_parallel. Distinct agents always run concurrently.
//
// explorationID is the source task's exploration (0 when the fire has no single
// source task). It is carried through to the run's chat session so the triggered
// agent's tool_usage rows land on that task instead of being unattributed.
func (s *Server) StartTriggeredRun(agentKey, title, message string, taskID, explorationID int64, mergeable bool, taskDesc, taskGoal string, langs ...locale.Lang) {
	if s.m.pg == nil || s.chatAgentRef() == nil {
		return
	}
	lang := locale.First(langs)
	if len(langs) == 0 && taskID > 0 {
		lang = taskLanguage(s.m.pg, strconv.FormatInt(taskID, 10))
	}
	cfg := s.readTriggerBehavior(agentKey) // DB read BEFORE the lock (never under queueMu)
	s.queueMu.Lock()
	s.triggerCfg[agentKey] = cfg
	s.triggerQ[agentKey] = append(s.triggerQ[agentKey], triggeredRun{language: lang, agentKey: agentKey, title: title, message: message, taskID: taskID, explorationID: explorationID, taskDesc: taskDesc, taskGoal: taskGoal, mergeable: mergeable})
	s.pumpLocked(agentKey)
	s.queueMu.Unlock()
}

// pumpLocked launches queued fires for one agent up to its concurrency limit, then
// returns. Serial → limit 1; parallel → limit = maxParallel (<=0 → unlimited). Each
// launched run re-pumps on completion (runAndPump) to fill the freed slot. Caller
// holds queueMu.
func (s *Server) pumpLocked(agentKey string) {
	if s.ctx.Err() != nil {
		return
	}
	cfg := s.triggerCfg[agentKey]
	limit := 1
	if cfg.runMode == "parallel" {
		if cfg.maxParallel <= 0 {
			limit = math.MaxInt
		} else {
			limit = cfg.maxParallel
		}
	}
	for s.triggerActive[agentKey] < limit && len(s.triggerQ[agentKey]) > 0 {
		item := s.nextTriggerRun(agentKey, cfg)
		s.triggerActive[agentKey]++
		go s.runAndPump(agentKey, item)
	}
}

// runAndPump runs one fire to completion, then decrements the active counter and
// re-pumps to fill the freed slot (cleaning up the agent's maps when fully idle).
func (s *Server) runAndPump(agentKey string, item triggeredRun) {
	s.runTriggeredRun(item)
	s.queueMu.Lock()
	s.triggerActive[agentKey]--
	if s.triggerActive[agentKey] <= 0 && len(s.triggerQ[agentKey]) == 0 {
		delete(s.triggerActive, agentKey)
		delete(s.triggerQ, agentKey)
		delete(s.triggerCfg, agentKey)
	} else {
		s.pumpLocked(agentKey)
	}
	s.queueMu.Unlock()
}

// nextTriggerRun pops the next run from the agent's queue per the merge mode:
//
//	parallel / none → one head item, no merge
//	by_task         → head + all queued same-task mergeable fires merged into one
//	all             → the ENTIRE queue merged into one run (regardless of task/type)
//
// Caller holds queueMu.
func (s *Server) nextTriggerRun(agentKey string, cfg triggerBehavior) triggeredRun {
	q := s.triggerQ[agentKey]
	if cfg.runMode == "parallel" || cfg.mergeMode == "none" {
		s.triggerQ[agentKey] = q[1:]
		return q[0]
	}
	if cfg.mergeMode == "all" {
		s.triggerQ[agentKey] = q[:0:0]
		return mergeAllRuns(q)
	}
	// by_task: head + same-task mergeable fires.
	head := q[0]
	if !head.mergeable || head.taskID == 0 {
		s.triggerQ[agentKey] = q[1:]
		return head
	}
	group := []triggeredRun{head}
	rest := q[:0:0] // keep non-matching items in order
	for _, it := range q[1:] {
		if it.mergeable && it.taskID == head.taskID {
			group = append(group, it)
		} else {
			rest = append(rest, it)
		}
	}
	s.triggerQ[agentKey] = rest
	return mergeTriggeredRuns(group)
}

// taskContextHeader renders a task's description/goal once. Same-task fires share
// this block, so the scheduler no longer repeats it per event (a long task goal
// times N fires was the dominant bloat). Returns "" for interval/none triggers
// (taskID==0, no task context). desc/goal are truncated to keep even a single copy
// bounded.
func taskContextHeader(taskID int64, desc, goal string, langs ...locale.Lang) string {
	if desc == "" && goal == "" {
		// No task context to show (interval/none fire, or a merged run that already
		// embedded its per-task headers and cleared these fields).
		return ""
	}
	if goal != "" {
		return fmt.Sprintf(locale.Text(locale.First(langs), "[Task #%d %s (goal: %s)]"), taskID, trunc(desc, 200, locale.First(langs)), trunc(goal, 500, locale.First(langs)))
	}
	return fmt.Sprintf(locale.Text(locale.First(langs), "[Task #%d %s]"), taskID, trunc(desc, 200, locale.First(langs)))
}

// finalTriggerMessage renders the message actually sent to the agent for a single
// (non-merged) fire: the task-context header (written once) followed by the event
// body. Merged runs embed their per-task headers inline and clear taskDesc/taskGoal,
// so this returns their message unchanged.
func finalTriggerMessage(item triggeredRun) string {
	if h := taskContextHeader(item.taskID, item.taskDesc, item.taskGoal, locale.Resolve(item.language)); h != "" {
		return h + "\n" + item.message
	}
	return item.message
}

// mergeTriggeredRuns folds several same-task event fires into one run: the shared
// task-context header written ONCE, then each fire's event body, so the agent handles
// the task's burst in a single conversation without repeating the (possibly long)
// task description/goal per event.
func mergeTriggeredRuns(items []triggeredRun) triggeredRun {
	if len(items) == 1 {
		return items[0]
	}
	first := items[0]
	lang := locale.Resolve(first.language)
	var b strings.Builder
	fmt.Fprintf(&b, locale.Text(lang, "[Task #%d: this conversation combines %d trigger events; process them together]\n"), first.taskID, len(items))
	if h := taskContextHeader(first.taskID, first.taskDesc, first.taskGoal, lang); h != "" {
		fmt.Fprintf(&b, "%s\n", h) // same task → task context appears once
	}
	for i, it := range items {
		fmt.Fprintf(&b, locale.Text(lang, "\n── Trigger %d ──\n%s\n"), i+1, it.message)
	}
	return triggeredRun{
		language: lang,
		agentKey: first.agentKey,
		title:    fmt.Sprintf(locale.Text(lang, "Merged triggers · task#%d · %d events"), first.taskID, len(items)),
		message:  b.String(),
		taskID:   first.taskID,
		// Same task → the source exploration still applies: every event in this
		// merged run came from that one task.
		explorationID: first.explorationID,
		mergeable:     true,
		// taskDesc/taskGoal left empty: header already embedded above.
	}
}

// mergeAllRuns folds the ENTIRE queued burst into one run (merge_mode='all'),
// regardless of type. Events are grouped by task (tasks in first-appearance order,
// events in their original order within a task), so each task's context header — and
// its possibly long description/goal — is written exactly ONCE even when fires from
// different tasks interleave in the queue. Cross-task chronology is not preserved
// (event bodies carry no timestamps, so per-task grouping reads better for the agent).
func mergeAllRuns(items []triggeredRun) triggeredRun {
	if len(items) == 1 {
		return items[0]
	}
	first := items[0]
	lang := locale.Resolve(first.language)
	order := []int64{}
	groups := map[int64][]triggeredRun{}
	for _, it := range items {
		if _, seen := groups[it.taskID]; !seen {
			order = append(order, it.taskID)
		}
		groups[it.taskID] = append(groups[it.taskID], it)
	}
	var b strings.Builder
	fmt.Fprintf(&b, locale.Text(lang, "[This conversation combines %d queued events across %d tasks; process them together]\n"), len(items), len(order))
	seq := 0
	for _, tid := range order {
		g := groups[tid]
		if h := taskContextHeader(tid, g[0].taskDesc, g[0].taskGoal, lang); h != "" {
			fmt.Fprintf(&b, "\n%s\n", h) // same task → context appears once, even if interleaved
		}
		for _, it := range g {
			seq++
			fmt.Fprintf(&b, locale.Text(lang, "\n── Trigger %d (task#%d) ──\n%s\n"), seq, tid, it.message)
		}
	}
	return triggeredRun{
		language: lang,
		agentKey: first.agentKey,
		title:    fmt.Sprintf(locale.Text(lang, "Merged triggers · all · %d events"), len(items)),
		message:  b.String(),
		taskID:   first.taskID,
		// Events from several tasks were folded into one conversation: there is no
		// single source task to attribute this run to, so the attribution is dropped
		// rather than pointing at whichever task happened to come first.
		explorationID: 0,
		mergeable:     true,
		// taskDesc/taskGoal left empty: per-task headers already embedded above.
	}
}

// runTriggeredRun creates the conversation for one queued fire, records the human
// turn, and runs it synchronously (blocks until the run ends). recover keeps a
// panic from killing the drain loop and wedging the agent's queue.
func (s *Server) runTriggeredRun(item triggeredRun) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf(locale.Text(locale.Resolve(item.language), "[trigger] run for %s panicked: %v"), item.agentKey, r)
		}
	}()
	pg := s.m.pg
	c, err := pg.CreateConversation(item.agentKey, firstLine(item.title, 60), nil)
	if err != nil {
		log.Printf(locale.Text(locale.Resolve(item.language), "[trigger] create conversation for %s failed: %v"), item.agentKey, err)
		return
	}
	msg := finalTriggerMessage(item) // prepend the task-context header for single (non-merged) fires
	if _, err := pg.AppendConvActivity(c.ID, db.Activity{Worker: item.agentKey, Kind: "user", Summary: firstLine(msg, 200), Detail: msg}); err != nil {
		log.Printf(locale.Text(locale.Resolve(item.language), "[trigger] append msg failed: %v"), err)
	}
	busyKey := s.convBusyKey(c.ID)
	s.chatMu.Lock()
	s.chatBusy[busyKey] = true
	s.chatMu.Unlock()
	// The source task attribution travels with the run: the session row records
	// which task produced this event, so the reporter's tool_usage rows land on
	// that task instead of being NULL.
	if _, err := s.runConversationSyncAttributed(c, msg, busyKey, item.taskID, item.explorationID, locale.Resolve(item.language)); err != nil {
		log.Printf("[trigger] durable admission for %s: %v", item.agentKey, err)
	}
}

// firstLine returns a single-line, length-capped preview (shared with summaries).
func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len([]rune(s)) > max {
		s = string([]rune(s)[:max]) + "…"
	}
	return s
}

// isDefaultConversationTitle recognizes only exact built-in titles, including the
// legacy Chinese title, so existing empty conversations can still be auto-titled.
func isDefaultConversationTitle(title string) bool {
	if title == "" || title == "新对话" {
		return true
	}
	for _, lang := range locale.Languages() {
		if title == locale.Text(lang, "New conversation") {
			return true
		}
	}
	return false
}
