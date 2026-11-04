package server

import "testing"

// Regression defense test that keeps the work-agent count validation error message
// in manager.go as its exact English source text (assertEnglishMessage). The
// SetWorkers(n<=0) branch returns before it reaches m.pg.SetSetting, so it actually
// runs on this DB-less host with an empty Manager. If someone reverts this literal to
// another language, this test fails.
//
// Call-graph verdict (see the errWorkersPositive constant comment): this message is
// user-only, exposed solely through the settings PUT handler (server.go:3512 → writeErr 400),
// with no agent tool (actool) path, so it is not brain input. The startup recovery error
// at line 384 of the same file is operator startup-log material, outside F3b scope (original preserved), so this test does not cover it.
func TestSetWorkersErrorLocalized(t *testing.T) {
	err := (&Manager{}).SetWorkers(0)
	if err == nil {
		t.Fatal("work-agent count 0 must be rejected")
	}
	assertEnglishMessage(t, "set_workers_nonpositive", err.Error(), "the work agent count must be at least 1")
}
