package chatstate

import "testing"

// TestSessionRefValidateOwnershipIsExclusive pins the ownership contract: a
// session is EITHER conversation-owned OR task-owned, never both.
//
// This is not a stylistic rule — it is what makes session.task_id a reliable
// "is this a task-owned session?" signal for every caller (dispatch, compact,
// lifecycle, archive), and what keeps the task_id -> tasks CASCADE foreign key
// from deleting a conversation's history.
//
// Source-task attribution (the task a reporter run serves) deliberately does
// NOT live on the session: it rides on the input/run rows
// (chatstate.Run.SourceTaskID).
func TestSessionRefValidateOwnershipIsExclusive(t *testing.T) {
	cases := []struct {
		name    string
		ref     SessionRef
		wantErr bool
	}{
		{"plain conversation", SessionRef{ConversationID: 7}, false},
		{"task-owned main session", SessionRef{TaskID: 2, ExplorationID: 2}, false},
		{"task-owned main session segment 1", SessionRef{TaskID: 2, ExplorationID: 2, MainSeg: 1}, false},
		{"conversation with task but no exploration", SessionRef{ConversationID: 7, TaskID: 2}, true},
		{"conversation with exploration but no task", SessionRef{ConversationID: 7, ExplorationID: 2}, true},
		{"conversation with both task and exploration", SessionRef{ConversationID: 7, TaskID: 2, ExplorationID: 2}, true},
		{"conversation with attribution and main seg", SessionRef{ConversationID: 7, TaskID: 2, ExplorationID: 2, MainSeg: 1}, true},
		{"task without exploration", SessionRef{TaskID: 2}, true},
		{"nothing at all", SessionRef{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.ref.Validate()
			if tc.wantErr && err == nil {
				t.Errorf("Validate() = nil, want error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
}

// TestSessionRefKeyIgnoresAttribution guards a subtle invariant: the
// attribution must not change the session identity. The conversation owns the
// session, so conv-N stays the key. Changing the key would orphan every
// existing conversation's session row.
func TestSessionRefKeyIgnoresAttribution(t *testing.T) {
	plain := SessionRef{ConversationID: 7}
	if got := (SessionRef{ConversationID: 7, TaskID: 2, ExplorationID: 2}).Key(); got != plain.Key() {
		t.Errorf("Key() = %q, want %q — the session identity is the conversation", got, plain.Key())
	}
	if got := (SessionRef{ConversationID: 7, TaskID: 2, ExplorationID: 2}).GateKey(); got != plain.GateKey() {
		t.Errorf("GateKey() = %q, want %q", got, plain.GateKey())
	}
}
