package server

import (
	"bytes"
	"strings"
	"testing"
)

// TestSetupTokenRedactedFromAPIServedLogs pins M-6: the first-run setup token stays
// in the operator's console output (the only place it may be read) but is masked in
// the log copies the API serves — the in-memory ring, the SSE fan-out and the
// server_logs rows written from the same struct.
func TestSetupTokenRedactedFromAPIServedLogs(t *testing.T) {
	prev := logSink
	t.Cleanup(func() { logSink = prev })
	console := &bytes.Buffer{}
	sink := &logSinkT{cap: 10, subs: map[chan LogLine]struct{}{}, out: console}
	logSink = sink

	t.Setenv(setupTokenEnv, "") // force a generated token, not the env-var path
	s := &Server{}
	tok := s.currentSetupToken()
	if tok == "" {
		t.Fatal("currentSetupToken returned an empty token")
	}

	// The exact line the server prints, fed straight to the sink (log.Printf itself
	// writes to the process stderr until StartLogCapture runs).
	line := "2026/01/02 03:04:05 [auth] the admin password has not been set yet. Setup token: " + tok
	if _, err := sink.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("sink write: %v", err)
	}

	if !strings.Contains(console.String(), tok) {
		t.Fatalf("the operator console must keep the setup token, got %q", console.String())
	}
	lines, _ := sink.recent(0, 10)
	if len(lines) != 1 {
		t.Fatalf("ring holds %d lines, want 1", len(lines))
	}
	if strings.Contains(lines[0].Text, tok) {
		t.Fatalf("the API-served log line still contains the setup token: %q", lines[0].Text)
	}
	if !strings.Contains(lines[0].Text, "[redacted]") {
		t.Fatalf("the API-served log line is not marked redacted: %q", lines[0].Text)
	}
}

// TestSetupTokenRedactionLeavesOtherLinesAlone proves the redaction only touches
// the registered secret: ordinary auth lines reach /api/logs unchanged.
func TestSetupTokenRedactionLeavesOtherLinesAlone(t *testing.T) {
	prev := logSink
	t.Cleanup(func() { logSink = prev })
	sink := &logSinkT{cap: 10, subs: map[chan LogLine]struct{}{}, out: &bytes.Buffer{}}
	logSink = sink

	sink.addSecret("s3cret-token")
	const ordinary = "[auth] the admin password has not been set yet. Setup token printed below"
	if _, err := sink.Write([]byte(ordinary + "\n")); err != nil {
		t.Fatalf("sink write: %v", err)
	}
	lines, _ := sink.recent(0, 10)
	if len(lines) != 1 || !strings.Contains(lines[0].Text, "Setup token printed below") {
		t.Fatalf("ordinary log line was altered: %+v", lines)
	}
}
