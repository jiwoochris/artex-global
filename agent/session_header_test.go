package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/transcript"
)

// fakeRT records the request it saw and returns a minimal 200 response.
type fakeRT struct{ seen *http.Request }

func (f *fakeRT) RoundTrip(req *http.Request) (*http.Response, error) {
	f.seen = req
	return &http.Response{
		StatusCode: 200,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
	}, nil
}

func newReq(ctx context.Context) *http.Request {
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.example.com/v1/messages", strings.NewReader("{}"))
	return req
}

func TestSessionHeaderInjectedFromContext(t *testing.T) {
	base := &fakeRT{}
	rt := quotaAwareTransport{base: base, sessionHeaderKey: "x-session-id"}
	ctx := transcript.WithSessionID(context.Background(), "conv-42")
	if _, err := rt.RoundTrip(newReq(ctx)); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if got := base.seen.Header.Get("x-session-id"); got != "conv-42" {
		t.Fatalf("x-session-id = %q, want conv-42", got)
	}
}

func TestSessionHeaderSkippedWhenKeyEmpty(t *testing.T) {
	base := &fakeRT{}
	rt := quotaAwareTransport{base: base} // no key configured
	ctx := transcript.WithSessionID(context.Background(), "conv-42")
	if _, err := rt.RoundTrip(newReq(ctx)); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	// The header name is whatever the user would have set; with no key, nothing
	// session-related is added. Assert the common key stays absent.
	if got := base.seen.Header.Get("x-session-id"); got != "" {
		t.Fatalf("unexpected session header %q with empty key", got)
	}
}

func TestSessionHeaderSkippedWhenNoSessionID(t *testing.T) {
	base := &fakeRT{}
	rt := quotaAwareTransport{base: base, sessionHeaderKey: "x-session-id"}
	// Context carries no session id (transcript persistence off).
	if _, err := rt.RoundTrip(newReq(context.Background())); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if got := base.seen.Header.Get("x-session-id"); got != "" {
		t.Fatalf("x-session-id = %q, want empty when no session id on context", got)
	}
}

func TestHTTPClientUserAgent(t *testing.T) {
	for _, tc := range []struct {
		name, userAgent, want string
		explicit              bool
	}{
		{name: "default", want: "scopeweaver"},
		{name: "custom", explicit: true, userAgent: "custom-client/1.0", want: "custom-client/1.0"},
		{name: "explicit blank", explicit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen := make(chan string, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen <- r.Header.Get("User-Agent")
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()
			client, err := (Config{}).HTTPClient()
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseIdleConnections()
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.explicit {
				req.Header.Set("User-Agent", tc.userAgent)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if got := <-seen; got != tc.want {
				t.Fatalf("wire User-Agent=%q, want %q", got, tc.want)
			}
		})
	}
}
