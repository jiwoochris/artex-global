package server

import "testing"

// TestReporterSeedLabelsLocalized is a regression guard that keeps the display-only labels of the seeded
// reporter agent (reporterAgentName, reporterAgentDescription) in Korean. These two strings are rendered only
// in the system/agents UI via `agentDTO` (server_mgmt.go) and are pure UI labels that never enter any agent's
// system prompt (renderSystem assembles it only from key→段[A] agent.ReporterDefaultPrompt) nor model-based
// agent selection (the reporter trigger is based on report_finding tool calls via OnToolCall, and is
// deterministic). So localizing them to Korean does not touch BRIEF boundary #1 (the brain is not translated).
// Conversely, the reporter's brain body (agent.ReporterDefaultPrompt) and the trigger-injected message
// (reporterToolCallMessage) are model input and keep the original Chinese, so they are not covered by
// this test. The Korean check reuses assertKoreanError (Hangul present, zero Chinese Han characters,
// intercept_archive_localized_test.go). [[F35]] [[G133]]
func TestReporterSeedLabelsLocalized(t *testing.T) {
	assertKoreanError(t, "reporter_name", reporterAgentName)
	assertKoreanError(t, "reporter_description", reporterAgentDescription)
}
