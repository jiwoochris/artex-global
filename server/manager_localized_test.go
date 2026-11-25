package server

import "testing"

// Regression guard test that keeps the validation error message of manager.go's work-agent
// count setting in Korean. The Korean check reuses assertKoreanError (contains Hangul · zero
// Chinese Han), built in F3a. The SetWorkers(n<=0) branch returns before reaching
// m.pg.SetSetting, so it can actually be driven with an empty Manager on this DB-less host.
// This test fails if someone reverts this literal to Chinese.
//
// Call-graph ruling (see the comment on the errWorkersPositive constant): this message is
// user-only, exposed only through the settings PUT handler (server.go:3512 → writeErr 400),
// and has no agent tool (actool) path, so it is not brain input. The startup recovery error at
// line 384 of the same file is an operator startup log, outside the F3b scope (original
// preserved), so it is not what this test targets.
func TestSetWorkersErrorLocalized(t *testing.T) {
	err := (&Manager{}).SetWorkers(0)
	if err == nil {
		t.Fatal("워크 에이전트 수 0 은 거부되어야 합니다")
	}
	assertKoreanError(t, "set_workers_nonpositive", err.Error())
}
