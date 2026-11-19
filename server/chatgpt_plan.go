package server

import (
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/agent"
)

func (s *Server) chatGPTPlanStatus(w http.ResponseWriter, _ *http.Request) {
	if s.chatGPTPlan == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"available": false,
			"connected": false,
			"error":     s.chatGPTPlanErr,
		})
		return
	}
	status := s.chatGPTPlan.Status()
	response := map[string]any{"available": true, "connected": status.Connected}
	if status.Connected {
		response["email"] = status.Email
		response["expires_at"] = status.ExpiresAt
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) chatGPTPlanConnect(w http.ResponseWriter, r *http.Request) {
	if s.chatGPTPlan == nil {
		writeErr(w, http.StatusServiceUnavailable, s.chatGPTPlanErr)
		return
	}
	redirectURI, err := chatGPTPlanRedirectURI(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	authorizationURL, err := s.chatGPTPlan.Start(redirectURI)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	// The URL can contain sensitive login hints in future revisions. Return it to
	// the authenticated browser, but deliberately never log it.
	writeJSON(w, http.StatusOK, map[string]any{"authorization_url": authorizationURL})
}

func chatGPTPlanRedirectURI(r *http.Request) (string, error) {
	if configured := strings.TrimSpace(os.Getenv("ARTEX_CHATGPT_REDIRECT_URI")); configured != "" {
		return configured, nil
	}
	port := ""
	if _, p, err := net.SplitHostPort(r.Host); err == nil {
		port = p
	} else if u, parseErr := url.Parse("//" + r.Host); parseErr == nil {
		port = u.Port()
	}
	if port == "" {
		port = "80"
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("invalid ARTEX browser port %q", port)
	}
	return "http://127.0.0.1:" + port + "/api/auth/chatgpt-plan/callback", nil
}

func (s *Server) chatGPTPlanCallback(w http.ResponseWriter, r *http.Request) {
	if s.chatGPTPlan == nil {
		chatGPTPlanCallbackPage(w, false, s.chatGPTPlanErr)
		return
	}
	status, err := s.chatGPTPlan.Complete(r.Context(), r.URL.Query())
	if err != nil {
		chatGPTPlanCallbackPage(w, false, err.Error())
		return
	}
	s.invalidateProfileAgents()
	s.reapplyActiveProfile()
	message := "Conta ChatGPT conectada ao ARTEX. Você pode fechar esta janela."
	if status.Email != "" {
		message = "Conta " + status.Email + " conectada ao ARTEX. Você pode fechar esta janela."
	}
	chatGPTPlanCallbackPage(w, true, message)
}

func chatGPTPlanCallbackPage(w http.ResponseWriter, ok bool, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'")
	status := "Falha ao conectar"
	color := "#dc2626"
	if ok {
		status = "ChatGPT conectado"
		color = "#16a34a"
	}
	_, _ = fmt.Fprintf(w, `<!doctype html><html lang="pt-BR"><meta charset="utf-8"><title>%s</title>
<body style="font:16px system-ui;max-width:560px;margin:12vh auto;padding:24px;color:#18181b">
<h1 style="color:%s">%s</h1><p>%s</p><button onclick="window.close()">Fechar</button>
<script>if(window.opener){window.opener.postMessage({type:'artex-chatgpt-plan',ok:%t},'*')}</script>
</body></html>`, html.EscapeString(status), color, html.EscapeString(status), html.EscapeString(message), ok)
}

func (s *Server) chatGPTPlanModels(w http.ResponseWriter, r *http.Request) {
	if s.chatGPTPlan == nil {
		writeErr(w, http.StatusServiceUnavailable, s.chatGPTPlanErr)
		return
	}
	models, err := s.chatGPTPlan.Models(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.Slug)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "models": ids})
}

func (s *Server) chatGPTPlanDisconnect(w http.ResponseWriter, r *http.Request) {
	if s.chatGPTPlan == nil {
		writeErr(w, http.StatusServiceUnavailable, s.chatGPTPlanErr)
		return
	}
	err := s.chatGPTPlan.Disconnect(r.Context())
	s.invalidateProfileAgents()
	s.disableChatGPTPlanProvider()
	s.reapplyActiveProfile()
	if err != nil {
		// Local credentials have still been removed, so report success plus the
		// best-effort remote revocation warning.
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "warning": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) disableChatGPTPlanProvider() {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	if !s.llmCfg.ChatGPTPlan {
		return
	}
	s.chatAgent = nil
	s.llmProv = nil
	s.llmDirect = nil
	s.llmCfg = agent.Config{}
	s.llmOn = false
}
