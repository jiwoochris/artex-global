package db

import (
	"fmt"
	"testing"
	"time"
)

// InsertSettingIfAbsent is the safety net for auth.password_hash: the caller's GetSetting check can
// be neutralised by a database error, and concurrent requests can jump the queue (bcrypt takes tens of
// milliseconds). So the "set exactly once" guarantee must rest on the primary key constraint, not on an
// if decision.
func TestInsertSettingIfAbsentDoesNotOverwrite(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	// Use a test-only key so the real auth.password_hash in the development database is not touched.
	key := fmt.Sprintf("test.insert_if_absent.%d", time.Now().UnixNano())
	defer func() { _, _ = d.Exec(`DELETE FROM settings WHERE key=$1`, key) }()

	inserted, err := d.InsertSettingIfAbsent(key, "first")
	if err != nil {
		t.Fatal(err)
	}
	if !inserted {
		t.Fatal("the first write must be inserted=true")
	}

	inserted, err = d.InsertSettingIfAbsent(key, "second")
	if err != nil {
		t.Fatal(err)
	}
	if inserted {
		t.Fatal("if the key already exists it must be inserted=false")
	}

	got, ok, err := d.GetSetting(key)
	if err != nil || !ok {
		t.Fatalf("GetSetting: ok=%v err=%v", ok, err)
	}
	if got != "first" {
		t.Fatalf("the value was overwritten with %q; it must keep %q", got, "first")
	}
}
