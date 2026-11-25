package server

import (
	"context"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// Regression guard test that keeps in Korean the transport-config validation error messages
// returned by connectMCP in mcpdiscover.go. All four error paths (missing stdio command ·
// missing http URL · missing sse URL · unknown transport) return before actually connecting
// (dialing) to the server, so it calls connectMCP directly, with no DB · network, and checks
// the returned text too. The Korean check reuses assertKoreanError (contains Hangul · zero
// Chinese Han), built in F3a. The transport enum (stdio/http/sse) and URLs are wire
// identifiers, so they stay ASCII and are not caught by the Han check. If someone reverts
// this text to Chinese, both the constant pins and the real paths fail.
func TestConnectMCPErrorsLocalized(t *testing.T) {
	// Pin the constants
	for _, c := range []struct{ label, msg string }{
		{"stdio_no_command", errMCPStdioNoCommand},
		{"http_no_url", errMCPHTTPNoURL},
		{"sse_no_url", errMCPSSENoURL},
		{"unknown_transport", errMCPUnknownTransportFmt},
	} {
		assertKoreanError(t, c.label, c.msg)
	}

	// Real paths: all four rejection branches return before dialing.
	for _, c := range []struct {
		label string
		srv   *db.MCPServer
	}{
		{"stdio_missing_command", &db.MCPServer{Transport: "stdio", Command: ""}},
		{"http_missing_url", &db.MCPServer{Transport: "http", URL: ""}},
		{"sse_missing_url", &db.MCPServer{Transport: "sse", URL: ""}},
		{"unknown_transport_path", &db.MCPServer{Transport: "websocket"}},
	} {
		_, err := connectMCP(context.Background(), c.srv)
		if err == nil {
			t.Fatalf("%s: 오류가 없습니다(검증을 통과함)", c.label)
		}
		assertKoreanError(t, c.label, err.Error())
	}
}
