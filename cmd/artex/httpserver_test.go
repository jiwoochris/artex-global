package main

import (
	"net/http"
	"testing"
	"time"
)

// TestHTTPServerTimeouts pins L-4: the listener sets every deadline, not just the
// header read, and the write deadline stays above the slowest synchronous handler
// (10-minute custom-tool runs) while the SSE routes exempt themselves per request.
func TestHTTPServerTimeouts(t *testing.T) {
	srv := newHTTPServer("127.0.0.1:0", http.NewServeMux())

	if srv.ReadHeaderTimeout <= 0 {
		t.Errorf("ReadHeaderTimeout = %v, want a slow-header bound", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout <= 0 {
		t.Errorf("ReadTimeout = %v, want a whole-request read bound", srv.ReadTimeout)
	}
	if srv.WriteTimeout <= 0 {
		t.Errorf("WriteTimeout = %v, want a response write bound", srv.WriteTimeout)
	}
	if srv.IdleTimeout <= 0 {
		t.Errorf("IdleTimeout = %v, want idle keep-alive connections to be closed", srv.IdleTimeout)
	}

	const slowestHandler = 10 * time.Minute // server's custom-tool / sync request budgets
	if srv.WriteTimeout < slowestHandler {
		t.Errorf("WriteTimeout = %v, want at least the %v slowest handler budget", srv.WriteTimeout, slowestHandler)
	}
	if srv.ReadTimeout < time.Minute {
		t.Errorf("ReadTimeout = %v, too short for a large work-dir upload", srv.ReadTimeout)
	}
}
