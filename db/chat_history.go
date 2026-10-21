package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Autumn-27/artex/chatstate"
	"github.com/Autumn-27/artex/locale"
	"github.com/Autumn-27/norma/transcript"
)

func validatePreparedChatRevision(ref chatstate.SessionRef, revision chatstate.TranscriptRevision) error {
	if revision.SessionKey != ref.Key() || revision.Directory == "" {
		return locale.Errorf("invalid prepared transcript owner")
	}
	return chatstate.ValidateTranscriptRevision(transcript.NewStore(revision.Directory), revision)
}
func publishChatRevision(ctx context.Context, tx *sql.Tx, ref chatstate.SessionRef, revision chatstate.TranscriptRevision) error {
	var history int64
	if err := tx.QueryRowContext(ctx, `SELECT history_revision FROM chat_control_sessions WHERE session_key=$1`, ref.Key()).Scan(&history); err != nil {
		return err
	}
	history++
	if _, err := tx.ExecContext(ctx, `INSERT INTO chat_transcript_revisions(id,session_key,transcript_id,history_revision,end_record_uuid,sha256,byte_length) VALUES($1,$2,$3,$4,$5,$6,$7)`, revision.ID, ref.Key(), revision.TranscriptID, history, revision.EndRecordUUID, revision.SHA256, revision.ByteLength); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE chat_control_sessions SET transcript_id=$2,history_revision=$3,active_revision_id=$4 WHERE session_key=$1`, ref.Key(), revision.TranscriptID, history, revision.ID)
	if err != nil {
		return err
	}
	return invalidateSideSnapshotTx(ctx, tx, chatSideParent(ref), history)
}
func (d *DB) PublishChatHistory(ctx context.Context, ref chatstate.SessionRef, expected int64, revision chatstate.TranscriptRevision) error {
	if err := validatePreparedChatRevision(ref, revision); err != nil {
		return err
	}
	return d.chatTx(ctx, ref, func(tx *sql.Tx) error {
		if err := chatExpected(ctx, tx, ref.GateKey(), expected); err != nil {
			return err
		}
		var active sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT active_run_id FROM chat_control_gates WHERE gate_key=$1`, ref.GateKey()).Scan(&active); err != nil {
			return err
		}
		if active.Valid {
			return chatstate.ErrBusy
		}
		if err := publishChatRevision(ctx, tx, ref, revision); err != nil {
			return err
		}
		return bumpChatGate(ctx, tx, ref.GateKey())
	})
}
func (d *DB) ActiveChatTranscript(ctx context.Context, ref chatstate.SessionRef) (out *chatstate.TranscriptRevision, err error) {
	err = d.chatTx(ctx, ref, func(tx *sql.Tx) error {
		r := chatstate.TranscriptRevision{SessionKey: ref.Key(), TranscriptID: ref.LegacyTranscriptID()}
		var active sql.NullString
		if e := tx.QueryRowContext(ctx, `SELECT active_revision_id,history_revision FROM chat_control_sessions WHERE session_key=$1`, ref.Key()).Scan(&active, &r.HistoryRevision); e != nil {
			return e
		}
		if active.Valid {
			if e := tx.QueryRowContext(ctx, `SELECT id,session_key,transcript_id,history_revision,end_record_uuid,sha256,byte_length FROM chat_transcript_revisions WHERE id=$1 AND session_key=$2`, active.String, ref.Key()).Scan(&r.ID, &r.SessionKey, &r.TranscriptID, &r.HistoryRevision, &r.EndRecordUUID, &r.SHA256, &r.ByteLength); e != nil {
				return e
			}
		}
		out = &r
		return nil
	})
	return
}

func historyChangeFingerprint(change chatstate.HistoryChange) string {
	return chatFingerprint(struct {
		Kind, Turn, Title string
		Replacement       *chatstate.Prompt
	}{change.Kind, change.TurnID, change.Title, change.Replacement})
}
func historyChangeResultTx(ctx context.Context, tx *sql.Tx, ref chatstate.SessionRef, change chatstate.HistoryChange) (*chatstate.ControlState, error) {
	var fingerprint, status string
	var result []byte
	var input sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT fingerprint,status,result,input_id FROM chat_history_changes WHERE session_key=$1 AND client_id=$2`, ref.Key(), change.ClientID).Scan(&fingerprint, &status, &result, &input)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if fingerprint != historyChangeFingerprint(change) {
		return nil, chatstate.ErrConflict
	}
	if status != "published" {
		return nil, nil
	}
	var state chatstate.ControlState
	if err = json.Unmarshal(result, &state); err != nil {
		return nil, err
	}
	state.StartInputID = input.String
	return &state, nil
}
func (d *DB) ChatHistoryChangeResult(ctx context.Context, ref chatstate.SessionRef, change chatstate.HistoryChange) (out *chatstate.ControlState, err error) {
	err = d.chatTx(ctx, ref, func(tx *sql.Tx) error { out, err = historyChangeResultTx(ctx, tx, ref, change); return err })
	return
}
func chatSessionTx(ctx context.Context, tx *sql.Tx, key string) (ref chatstate.SessionRef, err error) {
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(conversation_id,0),COALESCE(task_id,0),COALESCE(exploration_id,0),main_seg FROM chat_control_sessions WHERE session_key=$1`, key).Scan(&ref.ConversationID, &ref.TaskID, &ref.ExplorationID, &ref.MainSeg)
	return
}
func ensureChatSessionTx(ctx context.Context, tx *sql.Tx, ref chatstate.SessionRef) error {
	// The gate and the session record OWNERSHIP only (conversation XOR task —
	// see chat_control_gates_check / chat_control_sessions_check). Source-task
	// attribution rides on the input/run rows, never here.
	if _, err := tx.ExecContext(ctx, `INSERT INTO chat_control_gates(gate_key,conversation_id,task_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, ref.GateKey(), nullableID(ref.ConversationID), nullableID(ref.TaskID)); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO chat_control_sessions(session_key,gate_key,conversation_id,task_id,exploration_id,main_seg) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, ref.Key(), ref.GateKey(), nullableID(ref.ConversationID), nullableID(ref.TaskID), nullableID(ref.ExplorationID), ref.MainSeg)
	return err
}
func (d *DB) PrepareChatHistoryTarget(ctx context.Context, source chatstate.SessionRef, change chatstate.HistoryChange, branch bool, parentTurn string) (target chatstate.SessionRef, err error) {
	if change.ClientID == "" || len(change.ClientID) > 200 {
		return target, locale.Errorf("client request ID is required")
	}
	err = d.chatTx(ctx, source, func(tx *sql.Tx) error {
		var fingerprint string
		var key sql.NullString
		e := tx.QueryRowContext(ctx, `SELECT fingerprint,target_session_key FROM chat_history_changes WHERE session_key=$1 AND client_id=$2`, source.Key(), change.ClientID).Scan(&fingerprint, &key)
		if e == nil {
			if fingerprint != historyChangeFingerprint(change) {
				return chatstate.ErrConflict
			}
			if key.Valid {
				target, e = chatSessionTx(ctx, tx, key.String)
				return e
			}
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if e = chatExpected(ctx, tx, source.GateKey(), change.ExpectedRevision); e != nil {
			return e
		}
		var active sql.NullString
		if e = tx.QueryRowContext(ctx, `SELECT active_run_id FROM chat_control_gates WHERE gate_key=$1`, source.GateKey()).Scan(&active); e != nil {
			return e
		}
		if active.Valid {
			return chatstate.ErrBusy
		}
		var unknown bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_control_inputs WHERE gate_key=$1 AND status='unknown')`, source.GateKey()).Scan(&unknown); e != nil {
			return e
		}
		if unknown {
			return chatstate.ErrUnknownRun
		}
		target = source
		if branch {
			if source.ConversationID > 0 {
				var id int64
				title := strings.TrimSpace(change.Title)
				if title == "" {
					title = "Branch"
				}
				if e = tx.QueryRowContext(ctx, `INSERT INTO conversations(agent_key,title,llm_profile_id,preparing) SELECT agent_key,$2,llm_profile_id,true FROM conversations WHERE id=$1 RETURNING id`, source.ConversationID, title).Scan(&id); e != nil {
					return e
				}
				target = chatstate.SessionRef{ConversationID: id}
			} else {
				target.MainSeg = 0
				if e = tx.QueryRowContext(ctx, `INSERT INTO main_sessions(exploration_id,seq,preparing) VALUES($1,(SELECT COALESCE(MAX(seq),0)+1 FROM main_sessions WHERE exploration_id=$1),true) RETURNING seq`, source.ExplorationID).Scan(&target.MainSeg); e != nil {
					return e
				}
			}
			if e = ensureChatSessionTx(ctx, tx, target); e != nil {
				return e
			}
			parent, _ := json.Marshal(source)
			if _, e = tx.ExecContext(ctx, `UPDATE chat_control_sessions SET preparing=true,parent_session_key=$2,parent_ref=$3,parent_turn_id=$4 WHERE session_key=$1`, target.Key(), source.Key(), parent, parentTurn); e != nil {
				return e
			}
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO chat_history_changes(session_key,client_id,fingerprint,kind,status,target_session_key) VALUES($1,$2,$3,$4,'preparing',$5) ON CONFLICT(session_key,client_id) DO UPDATE SET status='preparing',target_session_key=EXCLUDED.target_session_key`, source.Key(), change.ClientID, historyChangeFingerprint(change), change.Kind, target.Key())
		return e
	})
	return
}
func (d *DB) NewMainSessionBranch(ctx context.Context, ref chatstate.SessionRef, parentTurnID, clientID string) (chatstate.SessionRef, error) {
	if ref.TaskID == 0 {
		return chatstate.SessionRef{}, locale.Errorf("task session required")
	}
	state, err := d.ChatControlState(ctx, ref)
	if err != nil {
		return chatstate.SessionRef{}, err
	}
	return d.PrepareChatHistoryTarget(ctx, ref, chatstate.HistoryChange{Kind: "branch", ClientID: clientID, TurnID: parentTurnID, ExpectedRevision: state.Revision}, true, parentTurnID)
}
func (d *DB) ChatHistoryRuns(ctx context.Context, ref chatstate.SessionRef) ([]chatstate.Run, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+runCols+` FROM chat_control_runs r JOIN chat_control_sessions s ON s.session_key=r.session_key JOIN chat_control_inputs i ON i.id=r.input_id WHERE r.session_key=$1 AND r.active AND r.source_kind='user' ORDER BY r.turn_ordinal,r.created_at,r.id`, ref.Key())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []chatstate.Run{}
	for rows.Next() {
		r, err := scanChatRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (d *DB) ChatRunForInput(ctx context.Context, id string) (chatstate.Run, error) {
	return scanChatRun(d.QueryRowContext(ctx, `SELECT `+runCols+` FROM chat_control_runs r JOIN chat_control_sessions s ON s.session_key=r.session_key WHERE r.input_id=$1`, id))
}

func (d *DB) CommitChatHistoryChange(ctx context.Context, source, target chatstate.SessionRef, change chatstate.HistoryChange, revision chatstate.TranscriptRevision) (out chatstate.ControlState, err error) {
	if err = validatePreparedChatRevision(target, revision); err != nil {
		return
	}
	records, e := transcript.NewStore(revision.Directory).Load(transcript.NewStore(revision.Directory).MainPath(revision.TranscriptID))
	if e != nil {
		return out, e
	}
	included := map[string]bool{}
	for _, r := range records {
		included[r.UUID] = true
	}
	err = d.chatTx(ctx, source, func(tx *sql.Tx) error {
		published, e := historyChangeResultTx(ctx, tx, source, change)
		if e != nil {
			return e
		}
		if published != nil {
			out = *published
			return nil
		}
		if e = chatExpected(ctx, tx, source.GateKey(), change.ExpectedRevision); e != nil {
			return e
		}
		var active sql.NullString
		if e = tx.QueryRowContext(ctx, `SELECT active_run_id FROM chat_control_gates WHERE gate_key=$1`, source.GateKey()).Scan(&active); e != nil {
			return e
		}
		if active.Valid {
			return chatstate.ErrBusy
		}
		selected, e := chatRun(ctx, tx, revision.SourceRunID)
		if e != nil {
			return e
		}
		if selected.Session != source || !selected.Active || selected.StartRecordUUID == "" {
			return chatstate.ErrAmbiguousHistory
		}
		var actualTarget string
		if e = tx.QueryRowContext(ctx, `SELECT target_session_key FROM chat_history_changes WHERE session_key=$1 AND client_id=$2 AND status='preparing'`, source.Key(), change.ClientID).Scan(&actualTarget); e != nil {
			return e
		}
		if actualTarget != target.Key() {
			return chatstate.ErrConflict
		}
		if e = publishChatRevision(ctx, tx, target, revision); e != nil {
			return e
		}
		if target != source {
			if e = cloneChatPrefixTx(ctx, tx, source, target, revision, included); e != nil {
				return e
			}
			if target.ConversationID > 0 {
				_, e = tx.ExecContext(ctx, `UPDATE conversations SET preparing=false WHERE id=$1`, target.ConversationID)
			} else {
				_, e = tx.ExecContext(ctx, `UPDATE main_sessions SET preparing=false WHERE exploration_id=$1 AND seq=$2`, target.ExplorationID, target.MainSeg)
			}
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `UPDATE chat_control_sessions SET preparing=false WHERE session_key=$1`, target.Key()); e != nil {
				return e
			}
		}
		var inputID string
		if change.Kind == "retry" || change.Kind == "edit" {
			prompt := selected.Prompt
			if change.Kind == "edit" {
				if change.Replacement == nil {
					return locale.Errorf("replacement is required")
				}
				prompt = *change.Replacement
			}
			if rewritten, ok := revision.ImportedPrompts[selected.ID]; ok && (change.Kind == "retry" || target != source) {
				prompt = rewritten
			}
			turnID, supersedes := selected.TurnID, selected.ID
			if target != source {
				turnID = chatID()
				supersedes = ""
			} else {
				if _, e = tx.ExecContext(ctx, `UPDATE chat_control_runs SET active=false WHERE id=$1`, selected.ID); e != nil {
					return e
				}
				table := "activity"
				if source.ConversationID > 0 {
					table = "conversation_activities"
				}
				if _, e = tx.ExecContext(ctx, `UPDATE `+table+` SET active_attempt=false,history_mutable=false WHERE run_id=$1`, selected.ID); e != nil {
					return e
				}
			}
			var ordinal int64
			if e = tx.QueryRowContext(ctx, `SELECT COALESCE(MIN(ordinal),1)-1 FROM chat_control_inputs WHERE gate_key=$1 AND kind='turn' AND status='queued'`, target.GateKey()).Scan(&ordinal); e != nil {
				return e
			}
			inputID = chatID()
			payload, _ := json.Marshal(prompt)
			if _, e = tx.ExecContext(ctx, `INSERT INTO chat_control_inputs(id,session_key,gate_key,client_id,fingerprint,kind,status,prompt,revision,ordinal,turn_id,supersedes_run_id,language) VALUES($1,$2,$3,$4,$5,'turn','queued',$6,1,$7,$8,NULLIF($9,''),$10)`, inputID, target.Key(), target.GateKey(), "history-"+change.ClientID, historyChangeFingerprint(change), payload, ordinal, turnID, supersedes, string(locale.FromContext(ctx))); e != nil {
				return e
			}
		}
		if e = bumpChatGate(ctx, tx, source.GateKey()); e != nil {
			return e
		}
		if target.GateKey() != source.GateKey() {
			if e = bumpChatGate(ctx, tx, target.GateKey()); e != nil {
				return e
			}
		}
		out, e = chatControlStateTx(ctx, tx, target)
		if e != nil {
			return e
		}
		out.StartInputID = inputID
		data, _ := json.Marshal(out)
		_, e = tx.ExecContext(ctx, `UPDATE chat_history_changes SET status='published',result=$3,input_id=NULLIF($4,'') WHERE session_key=$1 AND client_id=$2`, source.Key(), change.ClientID, data, inputID)
		return e
	})
	return
}
func cloneChatPrefixTx(ctx context.Context, tx *sql.Tx, source, target chatstate.SessionRef, revision chatstate.TranscriptRevision, included map[string]bool) error {
	rows, err := tx.QueryContext(ctx, `SELECT `+runCols+` FROM chat_control_runs r JOIN chat_control_sessions s ON s.session_key=r.session_key JOIN chat_control_inputs i ON i.id=r.input_id WHERE r.session_key=$1 AND r.active ORDER BY r.turn_ordinal,r.created_at,r.id`, source.Key())
	if err != nil {
		return err
	}
	var runs []chatstate.Run
	for rows.Next() {
		r, e := scanChatRun(rows)
		if e != nil {
			rows.Close()
			return e
		}
		if included[r.StartRecordUUID] && (r.EndRecordUUID == "" || included[r.EndRecordUUID]) {
			runs = append(runs, r)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var history int64
	if err = tx.QueryRowContext(ctx, `SELECT history_revision FROM chat_control_sessions WHERE session_key=$1`, target.Key()).Scan(&history); err != nil {
		return err
	}
	var displayMap []chatDisplayImport
	for index, r := range runs {
		prompt := r.Prompt
		if copied, ok := revision.ImportedPrompts[r.ID]; ok {
			prompt = copied
		}
		payload, _ := json.Marshal(prompt)
		inputID, runID, turnID := chatID(), chatID(), chatID()
		if _, err = tx.ExecContext(ctx, `INSERT INTO chat_control_inputs(id,session_key,gate_key,client_id,fingerprint,kind,status,prompt,revision,ordinal,turn_id) VALUES($1,$2,$3,$4,$5,'turn','completed',$6,1,$7,$8)`, inputID, target.Key(), target.GateKey(), "imported-"+r.ID, chatFingerprint(prompt), payload, index+1, turnID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO chat_control_runs(id,input_id,session_key,turn_id,history_revision,transcript_id,start_record_uuid,end_record_uuid,prompt,status,active,imported,source_run_id,completed_at,turn_ordinal,source_kind) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,true,true,$11,now(),$12,$13)`, runID, inputID, target.Key(), turnID, history, revision.TranscriptID, r.StartRecordUUID, r.EndRecordUUID, payload, r.Status, r.ID, index+1, r.SourceKind); err != nil {
			return err
		}

		// Replace only the user attachment DTO; generated tool/artifact paths retain provenance.
		detail := prompt.Text
		if len(prompt.Attachments) > 0 {
			data, _ := json.Marshal(map[string]any{"text": prompt.Text, "attachments": prompt.Attachments})
			detail = string(data)
		}
		displayMap = append(displayMap, chatDisplayImport{Source: r.ID, Run: runID, Turn: turnID, Detail: detail})
	}
	return cloneChatDisplayPrefixTx(ctx, tx, source, target, displayMap)
}
func (d *DB) AbandonChatHistoryPreparation(ctx context.Context, source chatstate.SessionRef, clientID string) error {
	return d.chatTx(ctx, source, func(tx *sql.Tx) error {
		var key sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT target_session_key FROM chat_history_changes WHERE session_key=$1 AND client_id=$2 AND status='preparing'`, source.Key(), clientID).Scan(&key)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if key.Valid && key.String != source.Key() {
			target, e := chatSessionTx(ctx, tx, key.String)
			if e != nil {
				return e
			}
			if target.ConversationID > 0 {
				_, err = tx.ExecContext(ctx, `DELETE FROM conversations WHERE id=$1 AND preparing`, target.ConversationID)
			} else {
				_, err = tx.ExecContext(ctx, `DELETE FROM main_sessions WHERE exploration_id=$1 AND seq=$2 AND preparing`, target.ExplorationID, target.MainSeg)
				if err == nil {
					_, err = tx.ExecContext(ctx, `DELETE FROM chat_control_sessions WHERE session_key=$1 AND preparing`, target.Key())
				}
			}
			if err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE chat_history_changes SET status='failed',target_session_key=NULL WHERE session_key=$1 AND client_id=$2`, source.Key(), clientID)
		return err
	})
}

type ChatAttempt struct {
	RunID     string    `json:"run_id"`
	Status    string    `json:"status"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
}

func (d *DB) ChatAttempts(ctx context.Context, ref chatstate.SessionRef, turn string) ([]ChatAttempt, error) {
	rows, err := d.QueryContext(ctx, `SELECT id,status,active,created_at FROM chat_control_runs WHERE session_key=$1 AND turn_id=$2 ORDER BY created_at,id`, ref.Key(), turn)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChatAttempt{}
	for rows.Next() {
		var a ChatAttempt
		if err = rows.Scan(&a.RunID, &a.Status, &a.Active, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (d *DB) ChatAttemptActivities(ctx context.Context, ref chatstate.SessionRef, runID string, before int64, limit int) ([]Activity, bool, error) {
	run, err := d.ChatRun(ctx, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, chatstate.ErrGone
	}
	if err != nil {
		return nil, false, err
	}
	if run.Session != ref {
		return nil, false, chatstate.ErrGone
	}
	if limit < 1 || limit > 500 {
		limit = 200
	}
	table := "activity"
	if ref.ConversationID > 0 {
		table = "conversation_activities"
	}
	rows, err := d.QueryContext(ctx, `SELECT id,COALESCE(worker,''),COALESCE(kind,''),COALESCE(tool,''),COALESCE(tool_use_id,''),is_error,COALESCE(summary,''),COALESCE(detail,''),created_at,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,COALESCE(turn_id,''),COALESCE(run_id,''),active_attempt,history_mutable FROM `+table+` WHERE run_id=$1 AND ($2<=0 OR id<$2) ORDER BY id DESC LIMIT $3`, runID, before, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []Activity
	for rows.Next() {
		var a Activity
		if err = rows.Scan(&a.ID, &a.Worker, &a.Kind, &a.Tool, &a.ToolUseID, &a.IsError, &a.Summary, &a.Detail, &a.CreatedAt, &a.InputTokens, &a.OutputTokens, &a.CacheReadTokens, &a.CacheWriteTokens, &a.TurnID, &a.RunID, &a.ActiveAttempt, &a.HistoryMutable); err != nil {
			return nil, false, err
		}
		if ref.TaskID > 0 {
			seg := ref.MainSeg
			a.MainSeg = &seg
		}
		out = append(out, a)
	}
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	for a, b := 0, len(out)-1; a < b; a, b = a+1, b-1 {
		out[a], out[b] = out[b], out[a]
	}
	return out, more, nil
}
func (d *DB) MainChatSegmentAvailable(ctx context.Context, expID int64, seg int) (bool, error) {
	if seg == 0 {
		return true, nil
	}
	var ok bool
	err := d.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM main_sessions WHERE exploration_id=$1 AND seq=$2 AND NOT preparing)`, expID, seg).Scan(&ok)
	return ok, err
}

// DropPreparedChatOrphan handles source deletion between preparation and commit.
// Published children are retained even when their parent no longer exists.
func (d *DB) DropPreparedChatOrphan(ctx context.Context, target chatstate.SessionRef) error {
	if target.ConversationID == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `DELETE FROM conversations WHERE id=$1 AND preparing AND EXISTS(SELECT 1 FROM chat_control_sessions s WHERE s.conversation_id=$1 AND s.preparing AND s.parent_session_key IS NULL)`, target.ConversationID)
	return err
}
func (d *DB) ChatConversationExists(ctx context.Context, id int64) (bool, error) {
	var exists bool
	err := d.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversations WHERE id=$1)`, id).Scan(&exists)
	return exists, err
}

func (d *DB) ChatTurnPrompt(ctx context.Context, ref chatstate.SessionRef, turnID string) (prompt chatstate.Prompt, err error) {
	var data []byte
	err = d.QueryRowContext(ctx, `SELECT prompt FROM chat_control_runs WHERE session_key=$1 AND turn_id=$2 AND active ORDER BY created_at DESC LIMIT 1`, ref.Key(), turnID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return prompt, chatstate.ErrGone
	}
	if err != nil {
		return
	}
	err = json.Unmarshal(data, &prompt)
	return
}
