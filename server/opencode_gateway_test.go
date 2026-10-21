package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func TestConnectionSessionHeaderPrecedence(t *testing.T) {
	t.Setenv("SCOPEWEAVER_CONFIG", filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv("SCOPEWEAVER_LOCAL_LLM_ONLY", "false")
	for _, stored := range []string{"no profile", "", "X-Stored-Session"} {
		t.Run("stored="+stored, func(t *testing.T) {
			s := &Server{m: &Manager{}}
			var profileID int64
			if stored != "no profile" {
				s.m = backportTestManager(t)
				var err error
				profileID, err = s.m.pg.SaveProfile(&db.LLMProfile{Name: "session-precedence-" + stored, Format: "openai", Model: "fixture-model", APIKey: "fixture-stored-key", SessionHeaderKey: stored})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := s.m.pg.DeleteProfile(profileID); err != nil {
						t.Error(err)
					}
				})
			}
			for _, format := range []string{"openai", "opencode-go"} {
				for _, field := range []string{"omitted", "null", "empty", "custom"} {
					t.Run(format+"/"+field, func(t *testing.T) {
						headers := make(chan http.Header, 1)
						gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							headers <- r.Header.Clone()
							w.Header().Set("Content-Type", "application/json")
							fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`)
						}))
						defer gateway.Close()
						body := map[string]any{"provider": format, "model": "fixture-model", "base_url": gateway.URL + "/v1", "api_key": "fixture-form-key", "streaming": false}
						if profileID != 0 {
							body["profile_id"] = profileID
						}
						switch field {
						case "null":
							body["session_header_key"] = nil
						case "empty":
							body["session_header_key"] = ""
						case "custom":
							body["session_header_key"] = "X-Requested-Session"
						}
						raw, err := json.Marshal(body)
						if err != nil {
							t.Fatal(err)
						}
						rr := httptest.NewRecorder()
						s.testLLM(rr, httptest.NewRequest(http.MethodPost, "/api/llm/test", bytes.NewReader(raw)))
						var result map[string]any
						if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
							t.Fatal(err)
						}
						if rr.Code != http.StatusOK || result["ok"] != true || result["reply"] != "OK" {
							t.Fatalf("HTTP %d: %s", rr.Code, rr.Body)
						}
						got := <-headers
						want := ""
						if format == "opencode-go" {
							want = "x-opencode-session"
						}
						if stored != "no profile" && stored != "" {
							want = stored
						}
						if field == "custom" {
							want = "X-Requested-Session"
						}
						for _, header := range []string{"x-opencode-session", "X-Stored-Session", "X-Requested-Session"} {
							if strings.EqualFold(header, want) {
								if !strings.HasPrefix(got.Get(header), "conntest-") {
									t.Errorf("%s=%q, want fresh connection-test session ID", header, got.Get(header))
								}
							} else if got.Get(header) != "" {
								t.Errorf("unexpected %s=%q", header, got.Get(header))
							}
						}
					})
				}
			}
		})
	}
}

func TestLegacyConfigPersistsProviderSessionHeader(t *testing.T) {
	t.Setenv("SCOPEWEAVER_CONFIG", filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv("SCOPEWEAVER_LOCAL_LLM_ONLY", "false")
	m := backportTestManager(t)
	s := &Server{m: m}
	previous, err := m.pg.ActiveProfile()
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := m.pg.ListProfiles()
	if err != nil {
		t.Fatal(err)
	}
	var original *db.LLMProfile
	for _, p := range profiles {
		if p.Name == "default" {
			original, err = m.pg.ProfileByID(p.ID)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Cleanup(func() {
		current, err := m.pg.ActiveProfile()
		if err != nil {
			t.Error(err)
			return
		}
		if original != nil {
			if _, err := m.pg.SaveProfile(original); err != nil {
				t.Error(err)
			}
		} else if current != nil && current.Name == "default" {
			if _, err := m.pg.Exec(`UPDATE llm_profiles SET is_default=false WHERE id=$1`, current.ID); err != nil {
				t.Error(err)
			}
			if err := m.pg.DeleteProfile(current.ID); err != nil {
				t.Error(err)
			}
		}
		if previous != nil {
			if err := m.pg.SetActiveProfile(previous.ID); err != nil {
				t.Error(err)
			}
		}
	})
	headers := make(chan http.Header, 1)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`)
	}))
	defer gateway.Close()
	for _, tc := range []struct{ name, format, stored, want string }{
		{"new alias", "opencode-go", "", "x-opencode-session"},
		{"switch existing header to alias", "opencode-go", "X-Previous-Session", "x-opencode-session"},
		{"ordinary config retains stored header", "openai", "X-Previous-Session", "X-Previous-Session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.stored != "" {
				current, err := m.pg.ActiveProfile()
				if err != nil || current == nil {
					t.Fatalf("active profile: %v", err)
				}
				current.SessionHeaderKey = tc.stored
				if _, err := m.pg.SaveProfile(current); err != nil {
					t.Fatal(err)
				}
			}
			cfg := agent.ConfigFrom(tc.format, "fixture-model", gateway.URL+"/v1", "fixture-key", "")
			if err := s.saveLLMConfig(cfg); err != nil {
				t.Fatal(err)
			}
			saved, err := m.pg.ActiveProfile()
			if err != nil || saved == nil {
				t.Fatalf("active profile: %v", err)
			}
			if saved.Format != "openai" || saved.SessionHeaderKey != tc.want {
				t.Errorf("stored format=%q session_header_key=%q; want openai and %q", saved.Format, saved.SessionHeaderKey, tc.want)
			}
			loaded, ok := s.loadLLMConfig()
			if !ok {
				t.Fatal("saved config is not ready")
			}
			loaded.Stream = false
			if _, reply, err := agent.TestConnection(t.Context(), loaded); err != nil || reply != "OK" {
				t.Fatalf("reloaded connection reply=%q error=%v", reply, err)
			}
			got := <-headers
			if got.Get(tc.want) == "" {
				t.Errorf("reloaded request lost %s", tc.want)
			}
		})
	}
}
