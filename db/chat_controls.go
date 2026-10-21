package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/Autumn-27/artex/chatstate"
	"github.com/Autumn-27/artex/locale"
)

func chatID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(id[:])
}
func chatFingerprint(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// chatTx always takes the parent lock before the gate and session locks. Parent
// deletion and claims consequently have one admission boundary across segments.
func (d *DB) chatTx(ctx context.Context, ref chatstate.SessionRef, fn func(*sql.Tx) error) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int64
	if ref.ConversationID > 0 {
		err = tx.QueryRowContext(ctx, `SELECT id FROM conversations WHERE id=$1 FOR UPDATE`, ref.ConversationID).Scan(&id)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT id FROM tasks WHERE id=$1 AND exploration_id=$2 AND deleted_at IS NULL AND archived_at IS NULL FOR UPDATE`, ref.TaskID, ref.ExplorationID).Scan(&id)
		if err == nil && ref.MainSeg > 0 {
			var exists bool
			err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM main_sessions WHERE exploration_id=$1 AND seq=$2)`, ref.ExplorationID, ref.MainSeg).Scan(&exists)
			if err == nil && !exists {
				return chatstate.ErrGone
			}
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return chatstate.ErrGone
	}
	if err != nil {
		return err
	}
	// The gate records OWNERSHIP: a conversation gate or a task gate, never both
	// (chat_control_gates_check). The session row keeps the same exclusive
	// shape. Source-task attribution is NOT part of ownership: it rides on the
	// input/run rows, so the gate and session stay unambiguous.
	_, err = tx.ExecContext(ctx, `INSERT INTO chat_control_gates(gate_key,conversation_id,task_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, ref.GateKey(), nullableID(ref.ConversationID), nullableID(ref.TaskID))
	if err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM chat_control_gates WHERE gate_key=$1 FOR UPDATE`, ref.GateKey()).Scan(&id); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO chat_control_sessions(session_key,gate_key,conversation_id,task_id,exploration_id,main_seg) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, ref.Key(), ref.GateKey(), nullableID(ref.ConversationID), nullableID(ref.TaskID), nullableID(ref.ExplorationID), ref.MainSeg)
	if err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, `SELECT history_revision FROM chat_control_sessions WHERE session_key=$1 FOR UPDATE`, ref.Key()).Scan(&id); err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

const admissionCols = `i.id,i.client_id,i.kind,i.target_run_id,i.status,i.prompt,i.revision,i.ordinal,COALESCE(s.conversation_id,0),COALESCE(s.task_id,0),COALESCE(s.exploration_id,0),s.main_seg`

func scanChatAdmission(row interface{ Scan(...any) error }) (a chatstate.Admission, err error) {
	var prompt []byte
	err = row.Scan(&a.ID, &a.ClientID, &a.Kind, &a.TargetRunID, &a.Status, &prompt, &a.Revision, &a.Ordinal, &a.Session.ConversationID, &a.Session.TaskID, &a.Session.ExplorationID, &a.Session.MainSeg)
	if err == nil {
		err = json.Unmarshal(prompt, &a.Prompt)
	}
	if err == nil && a.Prompt.Attachments == nil {
		a.Prompt.Attachments = []chatstate.Attachment{}
	}
	return
}
func chatInput(ctx context.Context, tx *sql.Tx, ref chatstate.SessionRef, id string) (chatstate.Admission, error) {
	return scanChatAdmission(tx.QueryRowContext(ctx, `SELECT `+admissionCols+` FROM chat_control_inputs i JOIN chat_control_sessions s ON s.session_key=i.session_key WHERE i.session_key=$1 AND i.id=$2 FOR UPDATE OF i`, ref.Key(), id))
}
func bumpChatGate(ctx context.Context, tx *sql.Tx, gate string) error {
	_, err := tx.ExecContext(ctx, `UPDATE chat_control_gates SET revision=revision+1 WHERE gate_key=$1`, gate)
	return err
}

func (d *DB) AdmitChatInput(ctx context.Context, ref chatstate.SessionRef, clientID, mode, targetRunID string, prompt chatstate.Prompt) (chatstate.Admission, error) {
	return d.admitChatInput(ctx, ref, clientID, mode, targetRunID, prompt, "user", false)
}

// Only the legacy task endpoint accepts blank prompts; all durable gate and
// ownership checks still apply, and the original prompt is stored unchanged.
func (d *DB) AdmitLegacyTaskChatInput(ctx context.Context, ref chatstate.SessionRef, clientID string, prompt chatstate.Prompt) (chatstate.Admission, error) {
	if ref.TaskID <= 0 || ref.ConversationID != 0 {
		return chatstate.Admission{}, chatstate.ErrGone
	}
	return d.admitChatInput(ctx, ref, clientID, "send", "", prompt, "user", true)
}
func (d *DB) AdmitExternalChatInput(ctx context.Context, ref chatstate.SessionRef, clientID string, prompt chatstate.Prompt) (chatstate.Admission, error) {
	return d.admitChatInput(ctx, ref, clientID, "queue", "", prompt, "external", false)
}

// AdmitAttributedExternalChatInput queues an input that a server-side trigger
// raised on behalf of a task: a reporter run fired by a finding belongs to the
// task that produced the finding, even though it runs in its own conversation.
//
// The attribution is stored on the INPUT row (source_task_id /
// source_exploration_id), never on the session: the session's task_id means
// "this session is owned by that task" and must stay unambiguous. It is copied
// to the run when the input is claimed (see claimChatInput).
//
// sourceTaskID/sourceExplorationID are 0 for a plain user message.
func (d *DB) AdmitAttributedExternalChatInput(ctx context.Context, ref chatstate.SessionRef, clientID string, prompt chatstate.Prompt, sourceTaskID, sourceExplorationID int64) (chatstate.Admission, error) {
	return d.admitChatInputAttributed(ctx, ref, clientID, "queue", "", prompt, "external", false, sourceTaskID, sourceExplorationID)
}
func (d *DB) admitChatInput(ctx context.Context, ref chatstate.SessionRef, clientID, mode, targetRunID string, prompt chatstate.Prompt, sourceKind string, allowBlankTask bool) (out chatstate.Admission, err error) {
	return d.admitChatInputAttributed(ctx, ref, clientID, mode, targetRunID, prompt, sourceKind, allowBlankTask, 0, 0)
}
func (d *DB) admitChatInputAttributed(ctx context.Context, ref chatstate.SessionRef, clientID, mode, targetRunID string, prompt chatstate.Prompt, sourceKind string, allowBlankTask bool, sourceTaskID, sourceExplorationID int64) (out chatstate.Admission, err error) {
	if strings.TrimSpace(clientID) == "" || len(clientID) > 200 || (!allowBlankTask && strings.TrimSpace(prompt.Text) == "" && len(prompt.Attachments) == 0) {
		return out, locale.Errorf("client request ID and prompt are required")
	}
	if mode != "send" && mode != "queue" && mode != "steer" {
		return out, locale.Errorf("invalid input mode")
	}
	// Attribution only makes sense for a conversation-owned session: a
	// task-owned session already says which task it serves.
	if sourceTaskID > 0 && ref.ConversationID == 0 {
		sourceTaskID, sourceExplorationID = 0, 0
	}
	if (sourceTaskID > 0) != (sourceExplorationID > 0) {
		return out, locale.Errorf("incomplete chat input attribution")
	}
	// Canonicalize absent manifests for stable idempotency across JSON clients.
	if prompt.Attachments == nil {
		prompt.Attachments = []chatstate.Attachment{}
	}
	fingerprint := chatFingerprint(struct {
		Mode, Target string
		Prompt       chatstate.Prompt
	}{mode, targetRunID, prompt})
	err = d.chatTx(ctx, ref, func(tx *sql.Tx) error {
		var stored, storedSource string
		e := tx.QueryRowContext(ctx, `SELECT id,fingerprint,source_kind FROM chat_control_inputs WHERE session_key=$1 AND client_id=$2`, ref.Key(), clientID).Scan(&out.ID, &stored, &storedSource)
		if e == nil {
			if stored != fingerprint || storedSource != sourceKind {
				return chatstate.ErrConflict
			}
			out, e = chatInput(ctx, tx, ref, out.ID)
			return e
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var paused bool
		var closed bool
		var operation string
		var active sql.NullString
		if e = tx.QueryRowContext(ctx, `SELECT paused,active_run_id,operation_id,closed FROM chat_control_gates WHERE gate_key=$1`, ref.GateKey()).Scan(&paused, &active, &operation, &closed); e != nil {
			return e
		}
		if closed {
			return chatstate.ErrGone
		}
		if mode == "send" && operation != "" {
			return chatstate.ErrBusy
		}
		if mode == "send" {
			var pending bool
			if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_control_inputs WHERE gate_key=$1 AND kind='turn' AND status='queued')`, ref.GateKey()).Scan(&pending); e != nil {
				return e
			}
			if pending {
				return chatstate.ErrBusy
			}
		}
		kind := "turn"
		if mode == "steer" {
			var approval bool
			if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM intercept_pending WHERE status='pending' AND (conversation_id=$1 OR task_id=$2))`, nullableID(ref.ConversationID), fmt.Sprint(ref.TaskID)).Scan(&approval); e != nil {
				return e
			}
			if approval {
				return chatstate.ErrBusy
			}
			kind = "steer"
			var activeSession string
			if e = tx.QueryRowContext(ctx, `SELECT session_key FROM chat_control_runs WHERE id=$1 AND status='running'`, targetRunID).Scan(&activeSession); e != nil || activeSession != ref.Key() {
				return chatstate.ErrConflict
			}
			if len(prompt.Attachments) > 0 || targetRunID == "" || !active.Valid || active.String != targetRunID {
				return chatstate.ErrConflict
			}
		} else if mode == "send" && (paused || active.Valid) {
			return chatstate.ErrBusy
		}
		var ordinal int64
		if e = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal),0)+1 FROM chat_control_inputs WHERE gate_key=$1`, ref.GateKey()).Scan(&ordinal); e != nil {
			return e
		}
		payload, _ := json.Marshal(prompt)
		out = chatstate.Admission{ID: chatID(), ClientID: clientID, Kind: kind, TargetRunID: targetRunID, Status: "queued", Session: ref, Prompt: prompt, Ordinal: ordinal, Revision: 1}
		_, e = tx.ExecContext(ctx, `INSERT INTO chat_control_inputs(id,session_key,gate_key,client_id,fingerprint,kind,target_run_id,status,prompt,revision,ordinal,language,source_kind,source_task_id,source_exploration_id) VALUES($1,$2,$3,$4,$5,$6,$7,'queued',$8,1,$9,$10,$11,$12,$13)`, out.ID, ref.Key(), ref.GateKey(), clientID, fingerprint, kind, targetRunID, payload, ordinal, string(locale.FromContext(ctx)), sourceKind, nullableID(sourceTaskID), nullableID(sourceExplorationID))
		if e != nil {
			return e
		}
		return bumpChatGate(ctx, tx, ref.GateKey())
	})
	return
}
func (d *DB) ChatControlState(ctx context.Context, ref chatstate.SessionRef) (out chatstate.ControlState, err error) {
	err = d.chatTx(ctx, ref, func(tx *sql.Tx) error { out, err = chatControlStateTx(ctx, tx, ref); return err })
	return
}
func chatControlStateTx(ctx context.Context, tx *sql.Tx, ref chatstate.SessionRef) (out chatstate.ControlState, err error) {
	out = chatstate.ControlState{Session: ref, Inputs: []chatstate.Admission{}, Capabilities: map[string]bool{"queue": true, "steer": true}}
	err = func() error {
		if ref.ConversationID > 0 {
			var e error
			out.BilledUsage, e = chatConversationUsageTx(ctx, tx, ref.ConversationID)
			if e != nil {
				return e
			}
		}
		var active sql.NullString
		var closed bool
		if e := tx.QueryRowContext(ctx, `SELECT g.revision,g.paused,g.active_run_id,s.history_revision,(g.operation_id<>''),g.closed FROM chat_control_gates g JOIN chat_control_sessions s ON s.gate_key=g.gate_key WHERE s.session_key=$1`, ref.Key()).Scan(&out.Revision, &out.Paused, &active, &out.HistoryRevision, &out.ContextChanging, &closed); e != nil {
			return e
		}
		if active.Valid {
			run, e := chatRun(ctx, tx, active.String)
			if e != nil {
				return e
			}
			out.Active = &run
		}
		e := tx.QueryRowContext(ctx, `SELECT turn_id,id,status FROM chat_control_runs WHERE session_key=$1 AND active AND source_kind='user' ORDER BY turn_ordinal DESC,created_at DESC,id DESC LIMIT 1`, ref.Key()).Scan(&out.LatestTurnID, &out.LatestRunID, &out.LatestRunStatus)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		for _, capability := range []string{"retry", "edit", "branch"} {
			out.Capabilities[capability] = out.Active == nil && !out.ContextChanging && !closed
		}
		var parentJSON []byte
		var parentKey sql.NullString
		if e := tx.QueryRowContext(ctx, `SELECT parent_ref,parent_session_key,COALESCE(parent_turn_id,'') FROM chat_control_sessions WHERE session_key=$1`, ref.Key()).Scan(&parentJSON, &parentKey, &out.ParentTurnID); e != nil {
			return e
		}
		if len(parentJSON) > 0 {
			var parent chatstate.SessionRef
			if e := json.Unmarshal(parentJSON, &parent); e != nil {
				return e
			}
			out.Parent = &parent
			out.ParentAvailable = parentKey.Valid
		}
		var approval bool
		if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM intercept_pending WHERE status='pending' AND (conversation_id=$1 OR task_id=$2))`, nullableID(ref.ConversationID), fmt.Sprint(ref.TaskID)).Scan(&approval); e != nil {
			return e
		}
		out.Capabilities["compact"] = out.Active == nil && !out.ContextChanging && !closed && out.LatestTurnID != ""
		out.Capabilities["steer"] = !closed && out.Active != nil && out.Active.Session.Key() == ref.Key() && !approval
		rows, e := tx.QueryContext(ctx, `SELECT `+admissionCols+` FROM chat_control_inputs i JOIN chat_control_sessions s ON s.session_key=i.session_key WHERE i.gate_key=$1 AND i.status IN ('queued','claimed','unknown','undelivered') ORDER BY i.ordinal,i.id`, ref.GateKey())
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			a, e := scanChatAdmission(rows)
			if e != nil {
				return e
			}
			out.Inputs = append(out.Inputs, a)
		}
		return rows.Err()
	}()
	return
}

func (d *DB) UpdateChatInput(ctx context.Context, ref chatstate.SessionRef, id string, expected int64, prompt chatstate.Prompt) (out chatstate.Admission, err error) {
	err = d.chatTx(ctx, ref, func(tx *sql.Tx) error {
		a, e := chatInput(ctx, tx, ref, id)
		if errors.Is(e, sql.ErrNoRows) {
			return chatstate.ErrGone
		}
		if e != nil {
			return e
		}
		if a.Status != "queued" || a.Kind != "turn" || a.Revision != expected {
			return chatstate.ErrConflict
		}
		if strings.TrimSpace(prompt.Text) == "" && len(prompt.Attachments) == 0 {
			return locale.Errorf("prompt is required")
		}
		if prompt.Attachments == nil {
			prompt.Attachments = []chatstate.Attachment{}
		}
		payload, _ := json.Marshal(prompt)
		_, e = tx.ExecContext(ctx, `UPDATE chat_control_inputs SET prompt=$2,revision=revision+1 WHERE id=$1`, id, payload)
		if e != nil {
			return e
		}
		out, e = chatInput(ctx, tx, ref, id)
		if e != nil {
			return e
		}
		return bumpChatGate(ctx, tx, ref.GateKey())
	})
	return
}
func chatExpected(ctx context.Context, tx *sql.Tx, gate string, expected int64) error {
	var revision int64
	var operation string
	var closed bool
	if err := tx.QueryRowContext(ctx, `SELECT revision,operation_id,closed FROM chat_control_gates WHERE gate_key=$1`, gate).Scan(&revision, &operation, &closed); err != nil {
		return err
	}
	if closed {
		return chatstate.ErrGone
	}
	if operation != "" {
		return chatstate.ErrBusy
	}
	if revision != expected {
		return chatstate.ErrConflict
	}
	return nil
}
func (d *DB) ReorderChatInputs(ctx context.Context, ref chatstate.SessionRef, expected int64, ids []string) error {
	return d.chatTx(ctx, ref, func(tx *sql.Tx) error {
		if e := chatExpected(ctx, tx, ref.GateKey(), expected); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, `SELECT id FROM chat_control_inputs WHERE gate_key=$1 AND kind='turn' AND status='queued' ORDER BY ordinal,id`, ref.GateKey())
		if e != nil {
			return e
		}
		var before []string
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			before = append(before, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		members := map[string]bool{}
		for _, id := range before {
			members[id] = true
		}
		if len(ids) != len(before) {
			return chatstate.ErrConflict
		}
		for _, id := range ids {
			if !members[id] {
				return chatstate.ErrConflict
			}
			delete(members, id)
		}
		if reflect.DeepEqual(before, ids) {
			return nil
		}
		for i, id := range ids {
			if _, e = tx.ExecContext(ctx, `UPDATE chat_control_inputs SET ordinal=$2,revision=revision+1 WHERE id=$1`, id, i+1); e != nil {
				return e
			}
		}
		return bumpChatGate(ctx, tx, ref.GateKey())
	})
}

const runCols = `r.id,r.input_id,r.turn_id,COALESCE(r.supersedes_run_id,''),r.transcript_id,r.start_record_uuid,r.end_record_uuid,r.status,r.history_revision,r.prompt,r.turn_ordinal,r.active,r.imported,COALESCE(r.source_run_id,''),r.baseline_recorded,r.before_record_uuid,r.source_kind,r.language,COALESCE(s.conversation_id,0),COALESCE(s.task_id,0),COALESCE(s.exploration_id,0),s.main_seg,COALESCE(r.source_task_id,0),COALESCE(r.source_exploration_id,0)`

func scanChatRun(row interface{ Scan(...any) error }) (r chatstate.Run, err error) {
	var prompt []byte
	err = row.Scan(&r.ID, &r.InputID, &r.TurnID, &r.SupersedesRunID, &r.TranscriptID, &r.StartRecordUUID, &r.EndRecordUUID, &r.Status, &r.HistoryRevision, &prompt, &r.TurnOrdinal, &r.Active, &r.Imported, &r.SourceRunID, &r.BaselineRecorded, &r.BeforeRecordUUID, &r.SourceKind, &r.Language, &r.Session.ConversationID, &r.Session.TaskID, &r.Session.ExplorationID, &r.Session.MainSeg, &r.SourceTaskID, &r.SourceExplorationID)
	if err == nil {
		err = json.Unmarshal(prompt, &r.Prompt)
	}
	return
}
func chatRun(ctx context.Context, tx *sql.Tx, id string) (chatstate.Run, error) {
	return scanChatRun(tx.QueryRowContext(ctx, `SELECT `+runCols+` FROM chat_control_runs r JOIN chat_control_sessions s ON s.session_key=r.session_key WHERE r.id=$1`, id))
}

func (d *DB) ChatGateSession(ctx context.Context, gate string) (ref chatstate.SessionRef, err error) {
	err = d.QueryRowContext(ctx, `SELECT COALESCE(conversation_id,0),COALESCE(task_id,0),COALESCE(exploration_id,0),main_seg FROM chat_control_sessions WHERE gate_key=$1 ORDER BY session_key LIMIT 1`, gate).Scan(&ref.ConversationID, &ref.TaskID, &ref.ExplorationID, &ref.MainSeg)
	if errors.Is(err, sql.ErrNoRows) {
		err = chatstate.ErrGone
	}
	return
}
func (d *DB) ChatRun(ctx context.Context, id string) (chatstate.Run, error) {
	return scanChatRun(d.QueryRowContext(ctx, `SELECT `+runCols+` FROM chat_control_runs r JOIN chat_control_sessions s ON s.session_key=r.session_key WHERE r.id=$1`, id))
}
func (d *DB) ClaimNextChatInput(ctx context.Context, gate string) (*chatstate.Run, error) {
	return d.claimChatInput(ctx, gate, "")
}
func (d *DB) ClaimChatHistoryInput(ctx context.Context, gate, id string) (*chatstate.Run, error) {
	return d.claimChatInput(ctx, gate, id)
}
func (d *DB) claimChatInput(ctx context.Context, gate, selected string) (out *chatstate.Run, err error) {
	ref, err := d.ChatGateSession(ctx, gate)
	if err != nil {
		return nil, err
	}
	err = d.chatTx(ctx, ref, func(tx *sql.Tx) error {
		var paused bool
		var closed bool
		var operation string
		var active sql.NullString
		if e := tx.QueryRowContext(ctx, `SELECT paused,active_run_id,operation_id,closed FROM chat_control_gates WHERE gate_key=$1`, gate).Scan(&paused, &active, &operation, &closed); e != nil {
			return e
		}
		if closed {
			return chatstate.ErrGone
		}
		if (paused && selected == "") || active.Valid || operation != "" {
			return nil
		}
		a, e := scanChatAdmission(tx.QueryRowContext(ctx, `SELECT `+admissionCols+` FROM chat_control_inputs i JOIN chat_control_sessions s ON s.session_key=i.session_key WHERE i.gate_key=$1 AND i.status='queued' AND i.kind='turn' AND ($2='' OR i.id=$2) ORDER BY i.ordinal,i.id LIMIT 1 FOR UPDATE OF i`, gate, selected))
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		r := chatstate.Run{ID: chatID(), InputID: a.ID, TurnID: chatID(), Session: a.Session, Prompt: a.Prompt, Status: "running", Active: true}
		if e = tx.QueryRowContext(ctx, `SELECT language,source_kind FROM chat_control_inputs WHERE id=$1`, a.ID).Scan(&r.Language, &r.SourceKind); e != nil {
			return e
		}
		// Copy the input's source-task attribution onto the run. The run is the
		// canonical record of which task an execution belongs to, so it must
		// carry the attribution even after the input is consumed.
		if e = tx.QueryRowContext(ctx, `SELECT COALESCE(source_task_id,0),COALESCE(source_exploration_id,0) FROM chat_control_inputs WHERE id=$1`, a.ID).Scan(&r.SourceTaskID, &r.SourceExplorationID); e != nil {
			return e
		}
		if e = tx.QueryRowContext(ctx, `SELECT history_revision,transcript_id FROM chat_control_sessions WHERE session_key=$1`, a.Session.Key()).Scan(&r.HistoryRevision, &r.TranscriptID); e != nil {
			return e
		}
		if e = tx.QueryRowContext(ctx, `SELECT COALESCE(turn_id,$2),COALESCE(supersedes_run_id,'') FROM chat_control_inputs WHERE id=$1`, a.ID, r.TurnID).Scan(&r.TurnID, &r.SupersedesRunID); e != nil {
			return e
		}
		if r.SupersedesRunID != "" {
			if e = tx.QueryRowContext(ctx, `SELECT turn_ordinal FROM chat_control_runs WHERE id=$1`, r.SupersedesRunID).Scan(&r.TurnOrdinal); e != nil {
				return e
			}
		} else {
			if e = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(turn_ordinal),0)+1 FROM chat_control_runs WHERE session_key=$1`, r.Session.Key()).Scan(&r.TurnOrdinal); e != nil {
				return e
			}
		}
		if r.TranscriptID == "" {
			r.TranscriptID = r.Session.LegacyTranscriptID()
		}
		payload, _ := json.Marshal(a.Prompt)
		if _, e = tx.ExecContext(ctx, `INSERT INTO chat_control_runs(id,input_id,session_key,turn_id,history_revision,transcript_id,prompt,status,supersedes_run_id,turn_ordinal,language,source_kind,source_task_id,source_exploration_id) VALUES($1,$2,$3,$4,$5,$6,$7,'running',NULLIF($8,''),$9,$10,$11,$12,$13)`, r.ID, a.ID, a.Session.Key(), r.TurnID, r.HistoryRevision, r.TranscriptID, payload, r.SupersedesRunID, r.TurnOrdinal, r.Language, r.SourceKind, nullableID(r.SourceTaskID), nullableID(r.SourceExplorationID)); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE chat_control_inputs SET status='claimed',revision=revision+1 WHERE id=$1`, a.ID); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE chat_control_gates SET active_run_id=$2,revision=revision+1 WHERE gate_key=$1`, gate, r.ID); e != nil {
			return e
		}
		out = &r
		return nil
	})
	return
}
func (d *DB) FinishChatRun(ctx context.Context, runID, status string) error {
	if status != "completed" && status != "failed" && status != "stopped" && status != "unknown" {
		return locale.Errorf("invalid run status")
	}
	r, e := d.ChatRun(ctx, runID)
	if e != nil {
		return e
	}
	return d.chatTx(ctx, r.Session, func(tx *sql.Tx) error {
		var current string
		if e := tx.QueryRowContext(ctx, `SELECT status FROM chat_control_runs WHERE id=$1 FOR UPDATE`, runID).Scan(&current); e != nil {
			return e
		}
		if current != "running" {
			if current == status {
				return nil
			}
			return chatstate.ErrConflict
		}
		for _, table := range []string{"activity", "conversation_activities"} {
			if _, e := tx.ExecContext(ctx, `UPDATE `+table+` SET history_mutable=CASE WHEN kind='user' THEN $2::boolean ELSE $3::boolean END WHERE run_id=$1`, runID, r.SourceKind == "user" && r.StartRecordUUID != "" && status != "unknown", r.SourceKind == "user" && r.StartRecordUUID != "" && r.EndRecordUUID != "" && status == "completed"); e != nil {
				return e
			}
		}
		inputStatus := status
		if status == "stopped" {
			inputStatus = "cancelled"
		}
		if _, e := tx.ExecContext(ctx, `UPDATE chat_control_runs SET status=$2,completed_at=now() WHERE id=$1`, runID, status); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, `UPDATE chat_control_inputs SET status=$2,revision=revision+1 WHERE id=$1`, r.InputID, inputStatus); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, `UPDATE chat_control_inputs SET status='undelivered',revision=revision+1 WHERE target_run_id=$1 AND status IN ('queued','claimed')`, runID); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, `UPDATE chat_control_gates SET active_run_id=NULL,paused=paused OR $3,revision=revision+1 WHERE gate_key=$1 AND active_run_id=$2`, r.Session.GateKey(), runID, status != "completed")
		return e
	})
}
func (d *DB) PauseChatGate(ctx context.Context, ref chatstate.SessionRef, expected int64) error {
	return d.setChatPause(ctx, ref, expected, true)
}
func (d *DB) ResumeChatGate(ctx context.Context, ref chatstate.SessionRef, expected int64) error {
	return d.setChatPause(ctx, ref, expected, false)
}
func (d *DB) setChatPause(ctx context.Context, ref chatstate.SessionRef, expected int64, paused bool) error {
	return d.chatTx(ctx, ref, func(tx *sql.Tx) error {
		if expected >= 0 {
			if e := chatExpected(ctx, tx, ref.GateKey(), expected); e != nil {
				return e
			}
		}
		if !paused {
			var unknown bool
			if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_control_inputs WHERE gate_key=$1 AND status='unknown')`, ref.GateKey()).Scan(&unknown); e != nil {
				return e
			}
			if unknown {
				return chatstate.ErrUnknownRun
			}
		}
		_, e := tx.ExecContext(ctx, `UPDATE chat_control_gates SET paused=$2,revision=revision+1 WHERE gate_key=$1`, ref.GateKey(), paused)
		return e
	})
}
func (d *DB) CancelChatInput(ctx context.Context, ref chatstate.SessionRef, id string, expected int64) error {
	return d.mutateChatInputStatus(ctx, ref, id, expected, "queued", "cancelled")
}
func (d *DB) DiscardUnknownChatRun(ctx context.Context, ref chatstate.SessionRef, id string, expected int64) error {
	return d.mutateChatInputStatus(ctx, ref, id, expected, "unknown", "cancelled")
}
func (d *DB) mutateChatInputStatus(ctx context.Context, ref chatstate.SessionRef, id string, expected int64, from, to string) error {
	return d.chatTx(ctx, ref, func(tx *sql.Tx) error {
		a, e := chatInput(ctx, tx, ref, id)
		if e != nil {
			return e
		}
		// Input operations use their own row revision; Discard follows the gate revision
		// in the HTTP contract because it acknowledges a gate-wide recovery barrier.
		if from == "unknown" {
			if e = chatExpected(ctx, tx, ref.GateKey(), expected); e != nil {
				return e
			}
		} else if a.Revision != expected {
			return chatstate.ErrConflict
		}
		if a.Status != from {
			return chatstate.ErrConflict
		}
		if _, e = tx.ExecContext(ctx, `UPDATE chat_control_inputs SET status=$2,revision=revision+1 WHERE id=$1`, id, to); e != nil {
			return e
		}
		return bumpChatGate(ctx, tx, ref.GateKey())
	})
}
func (d *DB) RecoverChatRuns(ctx context.Context) error {
	if _, err := d.ExecContext(ctx, `UPDATE chat_context_changes SET status='aborted' WHERE status='preparing'`); err != nil {
		return err
	}
	if _, err := d.ExecContext(ctx, `UPDATE chat_control_gates SET operation_id='',revision=revision+1 WHERE operation_id<>''`); err != nil {
		return err
	}
	rows, e := d.QueryContext(ctx, `SELECT id FROM chat_control_runs WHERE status='running'`)
	if e != nil {
		return e
	}
	var ids []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, id := range ids {
		if e = d.FinishChatRun(ctx, id, "unknown"); e != nil && !errors.Is(e, chatstate.ErrGone) && !errors.Is(e, chatstate.ErrConflict) {
			return e
		}
	}
	return nil
}
func (d *DB) PendingChatGates(ctx context.Context) ([]string, error) {
	rows, e := d.QueryContext(ctx, `SELECT gate_key FROM chat_control_gates WHERE NOT paused AND active_run_id IS NULL AND EXISTS(SELECT 1 FROM chat_control_inputs i WHERE i.gate_key=chat_control_gates.gate_key AND status='queued' AND kind='turn')`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if e = rows.Scan(&key); e != nil {
			return nil, e
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (d *DB) TakeSteer(ctx context.Context, runID string) (out *chatstate.Admission, err error) {
	r, err := d.ChatRun(ctx, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	err = d.chatTx(ctx, r.Session, func(tx *sql.Tx) error {
		var active sql.NullString
		if e := tx.QueryRowContext(ctx, `SELECT active_run_id FROM chat_control_gates WHERE gate_key=$1`, r.Session.GateKey()).Scan(&active); e != nil {
			return e
		}
		if !active.Valid || active.String != runID {
			return nil
		}
		a, e := scanChatAdmission(tx.QueryRowContext(ctx, `SELECT `+admissionCols+` FROM chat_control_inputs i JOIN chat_control_sessions s ON s.session_key=i.session_key WHERE i.target_run_id=$1 AND i.kind='steer' AND i.status='queued' ORDER BY i.ordinal,i.id LIMIT 1 FOR UPDATE OF i`, runID))
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE chat_control_inputs SET status='claimed',revision=revision+1 WHERE id=$1`, a.ID); e != nil {
			return e
		}
		a.Status = "claimed"
		a.Revision++
		out = &a
		return bumpChatGate(ctx, tx, r.Session.GateKey())
	})
	return
}
func (d *DB) SteerDelivered(ctx context.Context, runID, inputID string) error {
	r, e := d.ChatRun(ctx, runID)
	if e != nil {
		return e
	}
	return d.chatTx(ctx, r.Session, func(tx *sql.Tx) error {
		result, e := tx.ExecContext(ctx, `UPDATE chat_control_inputs SET status='delivered',revision=revision+1 WHERE id=$1 AND target_run_id=$2 AND kind='steer' AND status='claimed' AND EXISTS(SELECT 1 FROM chat_control_runs WHERE id=$2 AND status='running')`, inputID, runID)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return chatstate.ErrConflict
		}
		return bumpChatGate(ctx, tx, r.Session.GateKey())
	})
}
func (d *DB) RecordBoundary(ctx context.Context, runID, start, end string) error {
	r, e := d.ChatRun(ctx, runID)
	if e != nil {
		return e
	}
	return d.chatTx(ctx, r.Session, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, `UPDATE chat_control_runs SET start_record_uuid=CASE WHEN $2='' THEN start_record_uuid ELSE $2 END,end_record_uuid=CASE WHEN $3='' THEN end_record_uuid ELSE $3 END WHERE id=$1 AND status='running'`, runID, start, end)
		return e
	})
}
