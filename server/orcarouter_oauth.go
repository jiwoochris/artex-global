package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/orcarouter"
)

// orcaLoginManager lazily builds the in-process connect manager. Attempts live
// only in memory; the issued key is written to the project's existing
// llm_profiles secret store.
func (s *Server) orcaLoginManager() *orcarouter.LoginManager {
	s.orcaMu.Lock()
	defer s.orcaMu.Unlock()
	if s.orcaLogin == nil {
		s.orcaLogin = orcarouter.NewLoginManager(s.saveOrcaIssuedKey)
	}
	return s.orcaLogin
}

// saveOrcaIssuedKey stores a PKCE-issued key in llm_profiles. When profileID is
// zero a new, non-default profile is created so the connect button never
// silently activates a provider the user did not choose.
func (s *Server) saveOrcaIssuedKey(ctx context.Context, profileID int64, key string) (int64, error) {
	if s.m == nil || s.m.pg == nil {
		return 0, fmt.Errorf("orcarouter: profile store is unavailable")
	}
	if profileID > 0 {
		p, err := s.m.pg.ProfileByID(profileID)
		if err != nil {
			return 0, err
		}
		if p == nil {
			return 0, db.ErrLLMProfileNotFound
		}
		p.APIKey = key
		p.Format = "orcarouter"
		if strings.TrimSpace(p.BaseURL) == "" {
			p.BaseURL = orcarouter.DefaultAPIBase
		}
		if strings.TrimSpace(p.Model) == "" {
			p.Model = orcarouter.DefaultModel
		}
		return s.m.pg.SaveProfile(p)
	}

	profiles, err := s.m.pg.ListProfiles()
	if err != nil {
		return 0, err
	}
	name := "OrcaRouter Cyber Inference"
	existing := map[string]bool{}
	for _, p := range profiles {
		existing[p.Name] = true
	}
	for i := 2; existing[name]; i++ {
		name = fmt.Sprintf("OrcaRouter Cyber Inference (%d)", i)
	}
	return s.m.pg.SaveProfile(&db.LLMProfile{
		Name:      name,
		Format:    "orcarouter",
		BaseURL:   orcarouter.DefaultAPIBase,
		Model:     orcarouter.DefaultModel,
		APIKey:    key,
		Streaming: true,
	})
}

// orcaEndpoints exposes only public configuration (never a key).
func (s *Server) orcaEndpoints(w http.ResponseWriter, r *http.Request) {
	eps, err := orcarouter.ResolveEndpoints()
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok":            true,
		"auth_base":     eps.AuthBase,
		"api_base":      eps.APIBase,
		"default_model": orcarouter.DefaultModel,
		"provider":      "OrcaRouter Cyber Inference",
	})
}

func (s *Server) orcaLoginStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProfileID int64 `json:"profile_id"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	st, err := s.orcaLoginManager().Start(r.Context(), req.ProfileID)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok":            true,
		"id":            st.ID,
		"state":         st.State,
		"authorize_url": st.AuthorizeURL,
	})
}

func (s *Server) orcaLoginStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	st, ok := s.orcaLoginManager().Status(id)
	if !ok {
		writeJSON(w, 404, map[string]any{"ok": false, "error": "unknown connect attempt"})
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok":         true,
		"id":         st.ID,
		"state":      st.State,
		"error":      st.Error,
		"profile_id": st.ProfileID,
	})
}

func (s *Server) orcaLoginCancel(w http.ResponseWriter, r *http.Request) {
	s.orcaLoginManager().Cancel(r.PathValue("id"))
	writeJSON(w, 200, map[string]any{"ok": true})
}
