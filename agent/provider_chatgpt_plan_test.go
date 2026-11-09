package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type staticTokenSource string

func (s staticTokenSource) AccessToken(context.Context) (string, error) { return string(s), nil }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestChatGPTPlanTransportRewritesResponsesRequest(t *testing.T) {
	original := `{
  "model":"gpt-test",
  "input":[{"role":"user","content":"hello"}],
  "tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}],
  "max_output_tokens":8192,
  "temperature":0.2,
  "stream":false,
  "store":true
}`
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Authorization"); got != "Bearer plan-access-token" {
			t.Fatalf("Authorization = %q", got)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if got["stream"] != true || got["store"] != false {
			t.Fatalf("stream/store = %v/%v", got["stream"], got["store"])
		}
		for _, forbidden := range []string{"tools", "max_output_tokens", "temperature"} {
			if _, exists := got[forbidden]; exists {
				t.Fatalf("unsupported top-level field %q was retained", forbidden)
			}
		}
		input, ok := got["input"].([]any)
		if !ok || len(input) != 2 {
			t.Fatalf("input = %#v", got["input"])
		}
		additional, ok := input[0].(map[string]any)
		if !ok || additional["type"] != "additional_tools" || additional["role"] != "developer" {
			t.Fatalf("additional_tools item = %#v", input[0])
		}
		tools, ok := additional["tools"].([]any)
		if !ok || len(tools) != 1 {
			t.Fatalf("additional tools = %#v", additional["tools"])
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})
	transport := chatGPTPlanTransport{base: base, tokens: staticTokenSource("plan-access-token")}
	req, err := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses", bytes.NewBufferString(original))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer should-be-replaced")
	if _, err := transport.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
}

func TestConfigFromChatGPTPlan(t *testing.T) {
	cfg := ConfigFrom("chatgpt-plan", "", "https://ignored.example", "", "")
	if !cfg.ChatGPTPlan || cfg.Provider() != "chatgpt-plan" {
		t.Fatalf("unexpected config: %#v", cfg)
	}
	if cfg.BaseURL != "https://api.openai.com/v1" || !cfg.Stream {
		t.Fatalf("base/stream = %q/%v", cfg.BaseURL, cfg.Stream)
	}
}

func TestCompletedSSEBodyRejectsIncompleteAndBareEOF(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "incomplete",
			body: "event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"response\":{\"incomplete_details\":{\"reason\":\"content_filter\"}}}\n\n",
			want: "response.incomplete: content_filter",
		},
		{
			name: "bare eof",
			body: "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n",
			want: "stream ended before response.completed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := newCompletedSSEBody(io.NopCloser(strings.NewReader(tt.body)))
			got, err := io.ReadAll(body)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got), "event: response.failed") || !strings.Contains(string(got), tt.want) {
				t.Fatalf("rewritten stream = %q", got)
			}
		})
	}
}

func TestCompletedSSEBodyAcceptsCompleted(t *testing.T) {
	original := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{}}\n\n"
	body := newCompletedSSEBody(io.NopCloser(strings.NewReader(original)))
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("completed stream changed: %q", got)
	}
}
