package server

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestLogStreamSurvivesWriteTimeout proves L-4's exemption end to end: with a
// short WriteTimeout on a real listener, the SSE route keeps writing after the
// deadline has passed (it cleared it for this request), so a long-lived log tail
// is not cut off by the timeout that protects ordinary responses.
func TestLogStreamSurvivesWriteTimeout(t *testing.T) {
	prev := logSink
	t.Cleanup(func() { logSink = prev })
	sink := &logSinkT{cap: 10, subs: map[chan LogLine]struct{}{}, out: io.Discard}
	logSink = sink

	s := newHandlerTestServer(t)
	tok, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: s.Handler(), WriteTimeout: 150 * time.Millisecond}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://"+ln.Addr().String()+"/api/logs/stream?token="+url.QueryEscape(tok), nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d, want 200", resp.StatusCode)
	}

	// Well past WriteTimeout, publish a line: the flush must still reach the client.
	time.Sleep(400 * time.Millisecond)
	if _, err := sink.Write([]byte("[test] marker line\n")); err != nil {
		t.Fatalf("publish log line: %v", err)
	}

	br := bufio.NewReader(resp.Body)
	for {
		line, err := br.ReadString('\n')
		if strings.Contains(line, "marker line") {
			return
		}
		if err != nil {
			t.Fatalf("stream ended before the post-timeout event arrived: %v (last line %q)", err, line)
		}
	}
}
