package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/internal/dbtest"
)

// uniqueProfileName returns a profile name that is unique per run.
// llm_profiles.name carries a unique constraint and the test database is
// reused across runs, so a fixed name would collide with a leftover row.
func uniqueProfileName(t *testing.T, prefix string) string {
	return fmt.Sprintf("%s-%d-%s", prefix, time.Now().UnixNano(), t.Name())
}

// TestLLMTestInheritsProfileEndpointAndFormat verifies that testing a saved
// profile exercises the profile's own endpoint, protocol and model.
//
// A connection test that reports "ok" must describe the configuration the
// agent will actually run. Before this regression, testLLM inherited only the
// stored API key and a few tuning fields from the profile: the wire format,
// base URL, model and proxy were not inherited. A profile whose format was not
// "anthropic" therefore fell through to agent.ConfigFrom's default provider,
// so the request was sent to the wrong host (api.anthropic.com or
// api.openai.com instead of the profile's own base_url) and the reported model
// was the provider default rather than the profile's.
func TestLLMTestInheritsProfileEndpointAndFormat(t *testing.T) {
	dbtest.DSN(t) // skip unless ARTEX_TEST_DSN points at a throwaway DB
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("no database available: %v", err)
	}
	t.Cleanup(func() { _ = m.PG().Close() })

	var calls atomic.Int32
	var gotPath, gotAuth, gotModel string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel, _ = body["model"].(string)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer gateway.Close()

	// A profile that is NOT anthropic and NOT on a public host: the saved
	// format, endpoint and model must all be used for the test.
	profileID, err := m.PG().SaveProfile(&db.LLMProfile{
		Name:      uniqueProfileName(t, "openai-compatible"),
		Format:    "openai",
		BaseURL:   gateway.URL + "/custom",
		Model:     "fixture-model",
		APIKey:    "fixture-key",
		Streaming: true,
	})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	s := &Server{m: m}

	// The UI sends the form fields; the connection test must still work when
	// only profile_id identifies the profile (the stored profile is the truth).
	result := postLLMTest(t, s, map[string]any{"profile_id": profileID})
	if result["ok"] != true {
		t.Fatalf("connection test with profile_id only: %v", result)
	}
	if gotPath != "/custom/chat/completions" {
		t.Errorf("request path = %q, want %q (the profile's own endpoint)", gotPath, "/custom/chat/completions")
	}
	if gotAuth != "Bearer fixture-key" {
		t.Errorf("Authorization = %q, want %q (the profile's stored key)", gotAuth, "Bearer fixture-key")
	}
	if gotModel != "fixture-model" {
		t.Errorf("requested model = %q, want %q (the profile's model)", gotModel, "fixture-model")
	}
	if result["model"] != "fixture-model" {
		t.Errorf("reported model = %v, want %q (the profile's model)", result["model"], "fixture-model")
	}
	if calls.Load() != 1 {
		t.Errorf("gateway calls = %d, want 1", calls.Load())
	}
}

// TestLLMTestFormOverridesProfile verifies that an explicitly submitted form
// value still wins over the stored profile: the connection test checks what the
// user is about to save, not what is already stored.
func TestLLMTestFormOverridesProfile(t *testing.T) {
	dbtest.DSN(t)
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("no database available: %v", err)
	}
	t.Cleanup(func() { _ = m.PG().Close() })

	var gotModel string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel, _ = body["model"].(string)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer gateway.Close()

	profileID, err := m.PG().SaveProfile(&db.LLMProfile{
		Name:      uniqueProfileName(t, "stored"),
		Format:    "openai",
		BaseURL:   gateway.URL + "/stored",
		Model:     "stored-model",
		APIKey:    "stored-key",
		Streaming: true,
	})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	s := &Server{m: m}
	result := postLLMTest(t, s, map[string]any{
		"profile_id": profileID,
		"provider":   "openai",
		"model":      "form-model",
		"base_url":   gateway.URL + "/form",
	})
	if result["ok"] != true {
		t.Fatalf("connection test with form overrides: %v", result)
	}
	if gotModel != "form-model" {
		t.Errorf("requested model = %q, want %q (the form value wins)", gotModel, "form-model")
	}
	if result["model"] != "form-model" {
		t.Errorf("reported model = %v, want %q (the form value wins)", result["model"], "form-model")
	}
}

// postLLMTest posts a JSON body to the connection-test handler and decodes the
// result.
func postLLMTest(t *testing.T, s *Server, body map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	s.testLLM(response, httptest.NewRequest(http.MethodPost, "/api/llm/test", bytes.NewReader(raw)))
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusOK {
		t.Fatalf("HTTP %d response=%s decode error=%v", response.Code, response.Body, err)
	}
	return result
}
