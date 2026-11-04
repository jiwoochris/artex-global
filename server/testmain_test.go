package server

import (
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/yukiteruamano/artex-global/db"
)

// TestMain acquires a PostgreSQL advisory lock (7337741002) for the entire
// server test suite so cross-package DELETE cleanup races with db/agent
// packages are avoided when running `go test ./...`.
func TestMain(m *testing.M) {
	// server.New starts the realtime notifier's background delivery loop
	// (notifyTick) unless this env var is set. The notify integration tests
	// drive stepRealtime/stepDigest directly (see newNotifyFixture) and only
	// that fixture disabled the loop per-test. Other tests construct a server
	// with a live context.Background() that is never cancelled, so their
	// notifier goroutine leaks and keeps ticking for the rest of the binary;
	// when it fires inside a later test's window it claims that test's
	// deliveries out of band and corrupts the exact per-delivery attempt
	// accounting. That is what made TestNotifyDeliveriesHistoryAndRetry flake
	// intermittently ("should be failed after retries are exhausted, got pending"). No test asserts on the
	// background loop itself, so disable it suite-wide for determinism; the
	// loop's dispatch is covered through direct stepRealtime/stepDigest calls.
	os.Setenv(notifyBackgroundDisabledEnv, "1")
	// The server suite verifies the English source text of user-facing output, so pin
	// the default output language to English (config.DefaultLanguage) for the whole test
	// binary. Individual tests that need the Spanish overlay set it explicitly
	// (t.Setenv / config.CurrentLanguage / the settings table).
	os.Setenv("ARTEX_LANG", "en")

	dsn, _, err := db.DSN()
	if err != nil {
		os.Exit(m.Run())
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil || conn.Ping() != nil {
		os.Exit(m.Run())
	}
	defer conn.Close()
	if _, err := conn.Exec(`SELECT pg_advisory_lock(7337741002)`); err != nil {
		os.Exit(m.Run())
	}
	defer conn.Exec(`SELECT pg_advisory_unlock(7337741002)`) //nolint:errcheck
	os.Exit(m.Run())
}
