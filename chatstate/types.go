// Package chatstate holds shared chat protocol and transcript identities.
package chatstate

import (
	"encoding/json"
	"fmt"
	"github.com/Autumn-27/artex/locale"
	"github.com/Autumn-27/norma/llm"
	"time"
)

var (
	ErrConflict         = locale.NewError("chat state changed; reload and try again")
	ErrBusy             = locale.NewError("chat is busy")
	ErrGone             = locale.NewError("chat is unavailable")
	ErrUnknownRun       = locale.NewError("a lost run requires acknowledgement")
	ErrAmbiguousHistory = locale.NewError("historical turn boundary is unavailable")
)

type SessionRef struct {
	ConversationID int64 `json:"conversation_id,string"`
	TaskID         int64 `json:"task_id,string"`
	ExplorationID  int64 `json:"exploration_id,string"`
	MainSeg        int   `json:"main_seg"`
}

func (r SessionRef) Validate() error {
	// A session is either conversation-owned (conversation_id set) or task-owned
	// (task_id + exploration_id set) — never both. This is OWNERSHIP only.
	// Source-task attribution is carried by the input/run (see Run.SourceTaskID),
	// not by the session, so this rule stays exact.
	if r.ConversationID > 0 && r.TaskID == 0 && r.ExplorationID == 0 && r.MainSeg == 0 {
		return nil
	}
	if r.ConversationID == 0 && r.TaskID > 0 && r.ExplorationID > 0 && r.MainSeg >= 0 {
		return nil
	}
	return fmt.Errorf("invalid chat session")
}
func (r SessionRef) Key() string {
	if r.ConversationID > 0 {
		return fmt.Sprintf("conv-%d", r.ConversationID)
	}
	return fmt.Sprintf("task-%d-exp-%d-main-s%d", r.TaskID, r.ExplorationID, r.MainSeg)
}
func (r SessionRef) GateKey() string {
	if r.ConversationID > 0 {
		return r.Key()
	}
	return fmt.Sprintf("task-%d-main", r.TaskID)
}

type Attachment struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
	Abs  string `json:"abs,omitempty"`
}
type Prompt struct {
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments"`
}
type Admission struct {
	ID          string     `json:"id"`
	ClientID    string     `json:"client_request_id"`
	Kind        string     `json:"kind"`
	TargetRunID string     `json:"target_run_id,omitempty"`
	Status      string     `json:"status"`
	Session     SessionRef `json:"session"`
	Prompt      Prompt     `json:"prompt"`
	Revision    int64      `json:"revision,string"`
	Ordinal     int64      `json:"ordinal,string"`
}
type Run struct {
	Language         string     `json:"-"`
	BaselineRecorded bool       `json:"-"`
	BeforeRecordUUID string     `json:"-"`
	SourceKind       string     `json:"source_kind"`
	TurnOrdinal      int64      `json:"turn_ordinal,string"`
	Active           bool       `json:"active"`
	Imported         bool       `json:"imported"`
	SourceRunID      string     `json:"source_run_id,omitempty"`
	// SourceTaskID / SourceExplorationID attribute the run to the task that
	// produced it. A server-side trigger (a reporter run fired by a task
	// finding) runs in its own conversation, so the conversation says nothing
	// about which task it serves — these fields do. 0 means "not attributed"
	// (a plain user chat).
	SourceTaskID        int64 `json:"source_task_id,omitempty,string"`
	SourceExplorationID int64 `json:"source_exploration_id,omitempty,string"`
	ID                 string     `json:"id"`
	InputID          string     `json:"input_id"`
	TurnID           string     `json:"turn_id"`
	SupersedesRunID  string     `json:"supersedes_run_id,omitempty"`
	TranscriptID     string     `json:"transcript_id"`
	StartRecordUUID  string     `json:"start_record_uuid"`
	EndRecordUUID    string     `json:"end_record_uuid,omitempty"`
	Status           string     `json:"status"`
	Session          SessionRef `json:"session"`
	HistoryRevision  int64      `json:"history_revision,string"`
	Prompt           Prompt     `json:"prompt"`
}
type BilledUsage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
	Turns            int `json:"turns"`
}
type ControlState struct {
	BilledUsage     *BilledUsage    `json:"billed_usage,omitempty"`
	ContextChanging bool            `json:"context_changing"`
	LatestTurnID    string          `json:"latest_turn_id,omitempty"`
	LatestRunID     string          `json:"latest_run_id,omitempty"`
	LatestRunStatus string          `json:"latest_run_status,omitempty"`
	StartInputID    string          `json:"-"`
	Parent          *SessionRef     `json:"parent,omitempty"`
	ParentTurnID    string          `json:"parent_turn_id,omitempty"`
	ParentAvailable bool            `json:"parent_available"`
	Session         SessionRef      `json:"session"`
	Revision        int64           `json:"revision,string"`
	HistoryRevision int64           `json:"history_revision,string"`
	Paused          bool            `json:"paused"`
	Active          *Run            `json:"active"`
	Inputs          []Admission     `json:"inputs"`
	Capabilities    map[string]bool `json:"capabilities"`
}
type ContextCategory struct {
	Name      string `json:"name"`
	Tokens    int    `json:"tokens"`
	Estimated bool   `json:"estimated"`
}
type ContextView struct {
	Attribution      json.RawMessage       `json:"-"`
	MeasuredInput    int                   `json:"measured_input"`
	ReserveEstimated bool                  `json:"reserve_estimated"`
	RunID            string                `json:"run_id,omitempty"`
	ProfileIdentity  string                `json:"-"`
	Request          llm.CompletionRequest `json:"-"`
	BeforeTokens     int                   `json:"before_tokens,omitempty"`
	AfterTokens      int                   `json:"after_tokens,omitempty"`
	HistoryRevision  int64                 `json:"history_revision,string"`
	Sequence         int64                 `json:"sequence,string"`
	ProfileID        int64                 `json:"profile_id,string"`
	Model            string                `json:"model"`
	Compactor        string                `json:"compactor"`
	Source           string                `json:"source"`
	Summary          string                `json:"summary"`
	UpdatedAt        time.Time             `json:"updated_at"`
	Window           int                   `json:"window"`
	Used             int                   `json:"used"`
	OutputReserve    int                   `json:"output_reserve"`
	Headroom         int                   `json:"headroom"`
	CompressionCount int                   `json:"compression_count"`
	Estimated        bool                  `json:"estimated"`
	Categories       []ContextCategory     `json:"categories"`
}
type CompactOptions struct {
	TurnStarts []string `json:"-"`
	Focus      string   `json:"focus"`
	KeepRecent int      `json:"keep_recent"`
}
type HistoryChange struct {
	ClientID         string  `json:"client_request_id"`
	Kind             string  `json:"kind"`
	TurnID           string  `json:"turn_id"`
	Title            string  `json:"title"`
	ExpectedRevision int64   `json:"expected_revision,string"`
	Replacement      *Prompt `json:"replacement,omitempty"`
}

// LegacyTranscriptID preserves existing on-disk identities until revision publication.
func (r SessionRef) LegacyTranscriptID() string {
	if r.ConversationID > 0 {
		return r.Key()
	}
	if r.MainSeg > 0 {
		return fmt.Sprintf("exp%d-main-s%d", r.ExplorationID, r.MainSeg)
	}
	return fmt.Sprintf("exp%d-main", r.ExplorationID)
}
