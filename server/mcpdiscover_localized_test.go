package server

import (
	"context"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// Regression defense test that keeps the transport config validation error messages
// returned by connectMCP in mcpdiscover.go as their exact English source text. All four error paths (missing
// stdio command, missing http URL, missing sse URL, unknown transport) return before
// the server is actually dialed, so connectMCP is called directly with no DB or
// network and the returned message is verified too (assertEnglishMessage). The transport
// enum (stdio/http/sse) and URL are wire identifiers, so they stay ASCII. Reverting these
// messages to another language fails both the constant pin and the real path.
func TestConnectMCPErrorsLocalized(t *testing.T) {
	// Constant pin
	for _, c := range []struct{ label, msg, wantEnglish string }{
		{"stdio_no_command", errMCPStdioNoCommand, "The stdio transport has no command"},
		{"http_no_url", errMCPHTTPNoURL, "The http transport has no URL"},
		{"sse_no_url", errMCPSSENoURL, "The sse transport has no URL"},
		{"unknown_transport", errMCPUnknownTransportFmt, "Unknown transport: %q"},
	} {
		assertEnglishMessage(t, c.label, c.msg, c.wantEnglish)
	}

	// Real paths: all four rejection branches return before dial.
	for _, c := range []struct {
		label       string
		srv         *db.MCPServer
		wantEnglish string
	}{
		{"stdio_missing_command", &db.MCPServer{Transport: "stdio", Command: ""}, "The stdio transport has no command"},
		{"http_missing_url", &db.MCPServer{Transport: "http", URL: ""}, "The http transport has no URL"},
		{"sse_missing_url", &db.MCPServer{Transport: "sse", URL: ""}, "The sse transport has no URL"},
		{"unknown_transport_path", &db.MCPServer{Transport: "websocket"}, `Unknown transport: "websocket"`},
	} {
		_, err := connectMCP(context.Background(), c.srv)
		if err == nil {
			t.Fatalf("%s: no error (validation passed)", c.label)
		}
		assertEnglishMessage(t, c.label, err.Error(), c.wantEnglish)
	}
}
