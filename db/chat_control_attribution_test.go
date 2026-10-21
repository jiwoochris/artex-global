package db

import (
	"context"
	"testing"

	"github.com/Autumn-27/artex/chatstate"
)

// TestChatControlInputCarriesSourceAttribution is the P3 database half: a
// reporter run fired by a task finding runs in its OWN conversation, so the
// session says nothing about which task it serves. The attribution therefore
// has to live on the input (and the run it becomes), not on the session.
//
// The session row must stay PURE ownership — conversation_id XOR task_id —
// because every "is this a task-owned session?" check reads session.task_id,
// and because session.task_id cascades on task deletion (a reporter
// conversation's history must not vanish with its source task).
func TestChatControlInputCarriesSourceAttribution(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	// Cleanup callbacks run LIFO: registering d.Close() FIRST means it runs
	// LAST, so the row deletions below still have an open connection. Using
	// `defer d.Close()` here would close the database before they run and
	// silently leave the rows behind (the error is swallowed), which then
	// pollutes the packages that share this database.
	t.Cleanup(func() { _ = d.Close() })

	ctx := context.Background()

	// A real conversation and task are needed: the columns are foreign keys.
	conv, err := d.CreateConversation("reporter", "p3 attribution fixture", nil)
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	// Deleting the conversation cascades to the chat_control rows (gates ->
	// inputs, sessions -> inputs), so this leaves nothing behind for the
	// packages that share this database. The error is reported rather than
	// swallowed: a swallowed cleanup failure is exactly what silently
	// poisoned the server package before.
	t.Cleanup(func() {
		if _, e := d.Exec(`DELETE FROM conversations WHERE id=$1`, conv.ID); e != nil {
			t.Errorf("cleanup conversation: %v", e)
		}
	})

	task, err := d.CreateTask("p3 attribution task", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	t.Cleanup(func() {
		if _, e := d.Exec(`DELETE FROM tasks WHERE id=$1`, task.ID); e != nil {
			t.Errorf("cleanup task: %v", e)
		}
	})

	// The task's exploration id is what the attribution must point at.
	var explorationID int64
	if err := d.QueryRow(`SELECT exploration_id FROM tasks WHERE id=$1`, task.ID).Scan(&explorationID); err != nil {
		t.Fatalf("read task exploration: %v", err)
	}
	if explorationID == 0 {
		t.Fatal("task has no exploration id")
	}

	// A conversation-owned input WITH source attribution must be admitted.
	ref := chatstate.SessionRef{ConversationID: conv.ID}
	adm, err := d.AdmitAttributedExternalChatInput(ctx, ref, "p3-attr-1", chatstate.Prompt{Text: "reporter fire"}, task.ID, explorationID)
	if err != nil {
		t.Fatalf("admit attributed conversation input: %v", err)
	}

	// The SESSION row must stay pure ownership: no task, no exploration.
	var sessTask, sessExp int64
	if err := d.QueryRow(`SELECT COALESCE(task_id,0), COALESCE(exploration_id,0)
FROM chat_control_sessions WHERE session_key=$1`, ref.Key()).Scan(&sessTask, &sessExp); err != nil {
		t.Fatalf("read session row: %v", err)
	}
	if sessTask != 0 || sessExp != 0 {
		t.Fatalf("session row picked up attribution: task %d / exp %d — session.task_id must mean OWNERSHIP only", sessTask, sessExp)
	}

	// The INPUT row must carry the attribution.
	var inTask, inExp int64
	if err := d.QueryRow(`SELECT COALESCE(source_task_id,0), COALESCE(source_exploration_id,0)
FROM chat_control_inputs WHERE id=$1`, adm.ID).Scan(&inTask, &inExp); err != nil {
		t.Fatalf("read input row: %v", err)
	}
	if inTask != task.ID || inExp != explorationID {
		t.Fatalf("input row attribution = task %d / exp %d, want task %d / exp %d",
			inTask, inExp, task.ID, explorationID)
	}

	// A plain conversation input (no attribution) must still work and stay
	// unattributed.
	adm2, err := d.AdmitExternalChatInput(ctx, ref, "p3-plain-1", chatstate.Prompt{Text: "user message"})
	if err != nil {
		t.Fatalf("admit plain conversation input: %v", err)
	}
	var plainTask, plainExp int64
	if err := d.QueryRow(`SELECT COALESCE(source_task_id,0), COALESCE(source_exploration_id,0)
FROM chat_control_inputs WHERE id=$1`, adm2.ID).Scan(&plainTask, &plainExp); err != nil {
		t.Fatalf("read plain input row: %v", err)
	}
	if plainTask != 0 || plainExp != 0 {
		t.Errorf("plain input picked up attribution: task %d / exp %d", plainTask, plainExp)
	}
}

// TestChatControlRunInheritsSourceAttribution is the second half: the run is
// the canonical record of which task an execution belongs to, so it must
// inherit the input's attribution when the input is claimed.
func TestChatControlRunInheritsSourceAttribution(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	ctx := context.Background()

	conv, err := d.CreateConversation("reporter", "p3 run attribution fixture", nil)
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	t.Cleanup(func() {
		if _, e := d.Exec(`DELETE FROM conversations WHERE id=$1`, conv.ID); e != nil {
			t.Errorf("cleanup conversation: %v", e)
		}
	})

	task, err := d.CreateTask("p3 run attribution task", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	t.Cleanup(func() {
		if _, e := d.Exec(`DELETE FROM tasks WHERE id=$1`, task.ID); e != nil {
			t.Errorf("cleanup task: %v", e)
		}
	})

	var explorationID int64
	if err := d.QueryRow(`SELECT exploration_id FROM tasks WHERE id=$1`, task.ID).Scan(&explorationID); err != nil {
		t.Fatalf("read task exploration: %v", err)
	}

	ref := chatstate.SessionRef{ConversationID: conv.ID}
	if _, err = d.AdmitAttributedExternalChatInput(ctx, ref, "p3-run-1", chatstate.Prompt{Text: "reporter fire"}, task.ID, explorationID); err != nil {
		t.Fatalf("admit attributed input: %v", err)
	}

	run, err := d.ClaimNextChatInput(ctx, ref.GateKey())
	if err != nil {
		t.Fatalf("claim input: %v", err)
	}
	if run == nil {
		t.Fatal("claim returned no run")
	}
	if run.SourceTaskID != task.ID || run.SourceExplorationID != explorationID {
		t.Fatalf("run attribution = task %d / exp %d, want task %d / exp %d",
			run.SourceTaskID, run.SourceExplorationID, task.ID, explorationID)
	}
	// The run's session must still be conversation-owned with no task.
	if run.Session.ConversationID != conv.ID || run.Session.TaskID != 0 {
		t.Fatalf("run session = %+v, want conversation %d with no task", run.Session, conv.ID)
	}
}

// TestChatControlInputRejectsPartialAttribution guards the input contract: a
// half-specified attribution (task without exploration) is a caller bug and
// must be rejected rather than silently stored.
func TestChatControlInputRejectsPartialAttribution(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	ctx := context.Background()
	conv, err := d.CreateConversation("reporter", "p3 partial attribution fixture", nil)
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	t.Cleanup(func() {
		if _, e := d.Exec(`DELETE FROM conversations WHERE id=$1`, conv.ID); e != nil {
			t.Errorf("cleanup conversation: %v", e)
		}
	})

	ref := chatstate.SessionRef{ConversationID: conv.ID}
	if _, err = d.AdmitAttributedExternalChatInput(ctx, ref, "p3-partial-1", chatstate.Prompt{Text: "reporter fire"}, 7, 0); err == nil {
		t.Fatal("admit with task but no exploration: want error")
	}
	if _, err = d.AdmitAttributedExternalChatInput(ctx, ref, "p3-partial-2", chatstate.Prompt{Text: "reporter fire"}, 0, 7); err == nil {
		t.Fatal("admit with exploration but no task: want error")
	}
}
