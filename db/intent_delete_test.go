package db

import "testing"

// mustIntent / mustNode / mustLink are terse builders for the delete-cascade tests.
func mustIntent(t *testing.T, es *ExplorationStore, summary string) int64 {
	t.Helper()
	id, err := es.AddIntent(map[string]any{"summary": summary}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustNode(t *testing.T, es *ExplorationStore, kind, summary string) int64 {
	t.Helper()
	id, err := es.AddNode(kind, map[string]any{"summary": summary}, 1, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustLink(t *testing.T, es *ExplorationStore, from int64, rel string, to int64) {
	t.Helper()
	if err := es.Link(from, rel, to); err != nil {
		t.Fatal(err)
	}
}

func gone(t *testing.T, es *ExplorationStore, id int64) bool {
	t.Helper()
	n, err := es.GetNode(id)
	if err != nil {
		t.Fatal(err)
	}
	return n == nil
}

// TestSoftDeleteIntent soft delete sets deleted + delete_reason, keeps the node.
func TestSoftDeleteIntent(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("soft delete", "soft delete")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	intent := mustIntent(t, es, "intent to delete")
	if err := es.SetIntentState(intent, "paused"); err != nil {
		t.Fatal(err)
	}
	summary, err := es.SoftDeleteIntent(intent, "wrong direction call")
	if err != nil {
		t.Fatal(err)
	}
	if summary != "intent to delete" {
		t.Fatalf("summary=%q, want intent to delete", summary)
	}
	n, err := es.GetNode(intent)
	if err != nil || n == nil {
		t.Fatalf("intent removed by soft delete: n=%+v err=%v", n, err)
	}
	if n.State != StateIntentDeleted || n.DeleteReason != "wrong direction call" {
		t.Fatalf("state=%q delete_reason=%q, want deleted/wrong direction call", n.State, n.DeleteReason)
	}
	// Open intents may also be soft-deleted.
	openIntent := mustIntent(t, es, "open intent")
	if _, err := es.SoftDeleteIntent(openIntent, "direction no longer needed"); err != nil {
		t.Fatalf("soft delete open intent: %v", err)
	}
	if n, err := es.GetNode(openIntent); err != nil || n == nil || n.State != StateIntentDeleted {
		t.Fatalf("open intent not soft-deleted: n=%+v err=%v", n, err)
	}

	// Other states such as deleted cannot be soft-deleted again.
	if _, err := es.SoftDeleteIntent(intent, "delete again"); err == nil {
		t.Fatal("soft-deleting an already-deleted intent unexpectedly succeeded")
	}
}

// TestHardDeleteCascadesExclusiveDescendants hard delete cascades exclusive descendants down the chain to the leaves.
func TestHardDeleteCascadesExclusiveDescendants(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("hard cascade", "cascade delete")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	// intent1 --yields--> fact1 --derived_from--> intent2 --yields--> fact2(leaf)
	intent1 := mustIntent(t, es, "root intent")
	fact1 := mustNode(t, es, KindFact, "fact1")
	mustLink(t, es, intent1, RelYields, fact1)
	intent2 := mustIntent(t, es, "derived intent")
	mustLink(t, es, fact1, RelDerivedFrom, intent2)
	fact2 := mustNode(t, es, KindFact, "fact2")
	mustLink(t, es, intent2, RelYields, fact2)

	cleanup, err := es.CancelIntent(intent1)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup.Intents != 2 || cleanup.Facts != 2 {
		t.Fatalf("cleanup=%+v, want 2 intents / 2 facts", cleanup)
	}
	for _, id := range []int64{intent1, fact1, intent2, fact2} {
		if !gone(t, es, id) {
			t.Fatalf("node %d survived cascade", id)
		}
	}
}

// TestHardDeletePreservesSharedAndGoal hard delete preserves shared descendants (that still have another parent) and the goal.
func TestHardDeletePreservesSharedAndGoal(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("hard preserve", "preserve shared/goal")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	goal, err := es.AddGoal(map[string]any{"text": "take down the admin panel"}, "human")
	if err != nil {
		t.Fatal(err)
	}
	// intent1 exclusively owns finding(proves goal); intent1 and intentX share fact1(fact1 derives intent2).
	intent1 := mustIntent(t, es, "intent to delete")
	intentX := mustIntent(t, es, "bypass intent")
	finding := mustNode(t, es, KindFinding, "vulnerability")
	mustLink(t, es, intent1, RelYields, finding)
	mustLink(t, es, finding, RelProves, goal)
	shared := mustNode(t, es, KindFact, "shared fact")
	mustLink(t, es, intent1, RelYields, shared)
	mustLink(t, es, intentX, RelYields, shared)
	intent2 := mustIntent(t, es, "derived from shared fact")
	mustLink(t, es, shared, RelDerivedFrom, intent2)

	cleanup, err := es.CancelIntent(intent1)
	if err != nil {
		t.Fatal(err)
	}
	// Only intent1 and the finding it exclusively owns are deleted; shared (has an intentX parent) and its downstream intent2, goal are all kept.
	if cleanup.Intents != 1 || cleanup.Findings != 1 || cleanup.Facts != 0 {
		t.Fatalf("cleanup=%+v, want 1 intent / 1 finding / 0 fact", cleanup)
	}
	if !gone(t, es, intent1) || !gone(t, es, finding) {
		t.Fatal("intent1/finding should be removed")
	}
	for _, id := range []int64{goal, intentX, shared, intent2} {
		if gone(t, es, id) {
			t.Fatalf("node %d was wrongly cascaded", id)
		}
	}
}
