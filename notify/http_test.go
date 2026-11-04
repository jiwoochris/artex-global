package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// This file covers doJSON's HTTP-layer error classification.
//
// Why test this separately: each channel adapter only handles the platform's own
// business error codes (DingTalk errcode, Feishu code, Telegram ok field); **HTTP-layer**
// classification is doJSON's job — two independent lines of defence. Without this one a
// relay gateway returning 503 looks permanent (retries dropped) and a 403 looks retryable (three wasted backoffs).

func replyServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDoJSONClassifiesHTTPStatus(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		permanent bool
	}{
		{"200 success is not an error", 200, false},
		{"429 rate limited is retryable", 429, false},
		{"408 request timeout is retryable", 408, false},
		{"500 server error is retryable", 500, false},
		{"502 gateway error is retryable", 502, false},
		{"503 service unavailable is retryable", 503, false},
		{"400 bad request is permanent", 400, true},
		{"401 authentication failure is permanent", 401, true},
		{"403 forbidden is permanent", 403, true},
		{"404 not found is permanent", 404, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := replyServer(t, tc.status, `{"detail":"upstream says no"}`)
			_, err := doJSON(context.Background(), "GET", srv.URL, nil, nil)
			if tc.status < 300 {
				if err != nil {
					t.Fatalf("2xx must not be an error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("a non-2xx response must be an error")
			}
			if got := IsPermanent(err); got != tc.permanent {
				t.Fatalf("wrong permanent classification for HTTP %d: want %v, got %v (%v)",
					tc.status, tc.permanent, got, err)
			}
			// The status code must appear in the error, otherwise the user cannot tell whether
			// they misconfigured it or the peer is down. Assert the number rather than Go's English
			// StatusText: this package's wording is its own (as everywhere in the project), and only the number is language-independent and stable to assert.
			if !strings.Contains(err.Error(), strconv.Itoa(tc.status)) {
				t.Errorf("the error must carry the HTTP status code %d, got %v", tc.status, err)
			}
		})
	}
}

// TestDoJSONIncludesResponseSnippet covers snippet: the error explanation the peer
// returns must be carried back, otherwise the user only learns "it failed", not why the peer refused.
func TestDoJSONIncludesResponseSnippet(t *testing.T) {
	srv := replyServer(t, 400, `{"error":"invalid webhook token"}`)
	_, err := doJSON(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("an error is expected")
	}
	if !strings.Contains(err.Error(), "invalid webhook token") {
		t.Errorf("the error must carry the peer's explanation, got %v", err)
	}
}

// TestDoJSONSnippetIsSingleLineAndBounded constrains the shape of snippet: the peer's
// response goes verbatim into the last_error column and the frontend table, where newlines or excessive length break the layout and the payload.
func TestDoJSONSnippetIsSingleLineAndBounded(t *testing.T) {
	// A response with newlines, tabs and 5000 characters of over-long content.
	long := strings.Repeat("x", 5000)
	srv := replyServer(t, 500, "line1\nline2\r\n\tline3 "+long)
	_, err := doJSON(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("an error is expected")
	}
	msg := err.Error()
	if strings.ContainsAny(msg, "\r\n\t") {
		t.Errorf("the error must be flattened to a single line, got %q", msg)
	}
	// The snippet is capped at 200 characters plus a fixed prefix, so the total must be far smaller than the raw response.
	if len(msg) > 400 {
		t.Errorf("the error is too long (%d bytes); snippet should have truncated it: %q", len(msg), msg)
	}
}

// TestDoJSONRejectsOversizedResponse confirms the read is capped: when the peer returns
// an abnormally large body, the whole response must not be read into memory (the delivery history keeps a copy of last_error for every entry).
func TestDoJSONRejectsOversizedResponse(t *testing.T) {
	huge := strings.Repeat("A", 1<<20) // 1 MiB
	srv := replyServer(t, 400, huge)
	_, err := doJSON(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("an error is expected")
	}
	if len(err.Error()) > 400 {
		t.Errorf("an oversized response must be read up to the limit and truncated; the error message is %d bytes", len(err.Error()))
	}
}
