package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Autumn-27/artex/config"
)

// TestOutputLanguageCacheFollowsSettings pins that the cached output language
// (read by every writeErr and label) is refreshed by PUT /api/settings, so a
// switch applies to the very next response without re-reading the settings
// table per message.
func TestOutputLanguageCacheFollowsSettings(t *testing.T) {
	t.Setenv("ARTEX_LANG", "")
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()
	prev, hadPrev, _ := m.pg.GetSetting(settingOutputLanguage)
	t.Cleanup(func() { config.CurrentLanguage = nil })
	// defer (not t.Cleanup) so the restore runs before the deferred m.Close.
	defer func() {
		if hadPrev {
			_ = m.pg.SetSetting(settingOutputLanguage, prev)
		} else {
			_, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, settingOutputLanguage)
		}
	}()
	_, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, settingOutputLanguage)

	s := New(context.Background(), m, t.TempDir(), t.TempDir(), t.TempDir())
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, path string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	errOf := func(rec *httptest.ResponseRecorder) string {
		var v struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &v)
		return v.Error
	}

	if got := s.outputLanguage(); got != "en" {
		t.Fatalf("default output language = %q, want en", got)
	}
	if got := errOf(do(http.MethodPut, "/api/settings", map[string]any{"output_language": "fr"})); got == errOutputLanguageInvalid || got == "" {
		t.Fatalf("at the en default the error should be translated out of Korean, got %q", got)
	}

	if rec := do(http.MethodPut, "/api/settings", map[string]any{"output_language": "ko"}); rec.Code != http.StatusOK {
		t.Fatalf("set ko status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := errOf(do(http.MethodPut, "/api/settings", map[string]any{"output_language": "fr"})); got != errOutputLanguageInvalid {
		t.Fatalf("after switching to ko the error should be the Korean source %q, got %q", errOutputLanguageInvalid, got)
	}
	var settings struct {
		OutputLanguage string `json:"output_language"`
	}
	_ = json.Unmarshal(do(http.MethodGet, "/api/settings", nil).Body.Bytes(), &settings)
	if settings.OutputLanguage != "ko" {
		t.Fatalf("GET /api/settings output_language = %q, want ko", settings.OutputLanguage)
	}
}
