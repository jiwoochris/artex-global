package server

import "testing"

// TestReporterSeedLabelsLocalized is a regression guard that pins the display-only labels
// (reporterAgentName·reporterAgentDescription) of the seeded reporter agent to their exact
// English source text.
// These two strings are rendered only in the system/agents UI through `agentDTO`(server_mgmt.go),
// no agent system prompt picks them up (renderSystem assembles only key→section[A] agent.ReporterDefaultPrompt,
// nothing else) and no model-based agent selection does either (the reporter trigger is the report_finding tool call
// OnToolCall·deterministic) — they are pure UI labels. So localizing them does not touch BRIEF boundary #1
// (the brain stays untranslated). Conversely the reporter brain body (agent.ReporterDefaultPrompt)
// and the trigger injection message (reporterToolCallMessage) are model input and keep their original text, so
// they are not covered by this test. Assertions use assertEnglishMessage
// (intercept_archive_localized_test.go). [[F35]] [[G133]]
func TestReporterSeedLabelsLocalized(t *testing.T) {
	assertEnglishMessage(t, "reporter_name", reporterAgentName, "Report writing")
	assertEnglishMessage(t, "reporter_description", reporterAgentDescription,
		"Write detailed vulnerability reports: triggered automatically when a vulnerability is found, it reads the evidence and execution history and then writes a Markdown report onto that vulnerability.")
}
