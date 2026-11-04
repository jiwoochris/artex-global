package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// server_localized_test.go pins that user-facing responses from server.go stay the exact
// English source text (F3b-server.go + F34). The response body is extracted with the
// task_categories test's decodeErrorField and compared with the expected English string by
// assertEnglishMessage (same package server). Scope: (1) F3b — the 24 writeErr sites
// plus the 2 writeErrors validateTaskProfileIDs exposes through writeErr; (2) F34 — the 5
// user-facing responses that bypass writeErr (the 3 returns of chatUnavailableReason and the
// 2 writeJSON error variants of the testLLM / web-search probe); (3) F34(d) — the 3 responses
// of the rule-mode chat fallbackChat (2 acknowledgements + the status summary) and trigger
// alias parsing (intent/hint, including the Korean and Chinese aliases). Text handed to the
// agent — prompts, tool descriptions, the seed intent summary, the default task title and
// logs — is preserved verbatim, so it is out of scope here.

// TestServerErrorConstantsLocalized asserts that every response constant is its exact English
// source text. Format string constants (containing %d) are additionally checked after
// formatting, so an interpolated value cannot replace the expected English text either.
func TestServerErrorConstantsLocalized(t *testing.T) {
	for _, c := range []struct{ label, msg, wantEnglish string }{
		{"intent_control", errTaskDeletingIntentControl, "The intent cannot be controlled because the task is being deleted"},
		{"intent_rerun", errTaskDeletingIntentRerun, "The task is being deleted, so the intent cannot be re-run"},
		{"intent_not_rerunnable", errIntentNotRerunnable, "Not in a re-runnable state (only blocked, exhausted, and stopped can be re-run)"},
		{"source_invalid", errCreateTaskSourceInvalid, "The source task id is invalid or duplicated"},
		{"intercept_rules", errCreateTaskInterceptRules, "The task-level interception rule is invalid: "},
		{"category_invalid", errCreateTaskCategoryInvalid, "The task category was not found or is invalid"},
		{"company_invalid", errCreateTaskCompanyInvalid, "The linked company could not be found or is invalid"},
		{"llm_profile_invalid", errLLMProfileInvalid, "The LLM configuration id is invalid or duplicated"},
		{"asset_store_disabled", errAssetStoreDisabled, "The asset store is not enabled"},
		{"asset_id_required", errAssetIDRequired, "asset_id is required"},
		{"python_not_detected", errPythonNotDetected, "python not found (neither python3 nor python is on PATH)"},
		{"notify_base_url", errNotifyBaseURLScheme, "The link base address must start with http:// or https://"},
		{"notify_digest", errNotifyDigestRange, "The summary interval must be between 1 and 1440 minutes"},
		{"new_session", errTaskDeletingNewSession, "Cannot create a new session while the task is being deleted"},
		{"new_message", errTaskDeletingNewMessage, "A new message cannot be sent because the task is being deleted"},
		{"main_agent_busy", errMainAgentBusy, "The main agent is still processing a previous message. Please try again shortly"},
		// F34: user-facing responses that bypass writeErr
		{"chat_no_profile", errChatNoLLMProfile, "No LLM configuration exists yet: add one under System → LLM settings"},
		{"chat_no_active", errChatNoActiveLLMProfile, "No LLM configuration is enabled: in System → LLM Settings, enable one, or specify the configuration to use for this conversation"},
		{"chat_not_ready", errChatLLMNotReady, "Cannot start a conversation because the LLM is not ready: in System → LLM Settings, make sure there is an available and enabled configuration"},
		{"llm_test_no_api_key", errLLMTestNoAPIKey, "No API Key was provided"},
		{"web_search_no_results", errWebSearchProbeNoResults, "The search returned 0 results (the request may have been throttled, or the proxy may not be connected)"},
		// F34(d): the two rule-mode chat acknowledgements (the input text is appended after them)
		{"fallback_intent", fallbackIntentInjected, "Injected one high-priority intent: "},
		{"fallback_hint", fallbackHintRecorded, "The hint has been recorded. The planner will read it next: "},
	} {
		assertEnglishMessage(t, c.label, c.msg, c.wantEnglish)
	}

	for _, c := range []struct{ label, msg, wantEnglish string }{
		{"source_limit", fmt.Sprintf(errCreateTaskSourceLimit, 8), "You can link up to 8 source tasks"},
		{"source_not_found", fmt.Sprintf(errCreateTaskSourceNotFound, 999), "Source task #999 not found"},
		{"company_limit", fmt.Sprintf(errCreateTaskCompanyLimit, 32),
			"The linked companies are invalid: you can select at most 32 valid companies"},
		{"llm_profile_404", fmt.Sprintf(errLLMProfileNotFound, 7), "LLM configuration #7 not found, or its API Key is not set"},
		// F34(d): rule-mode status summary (asset / pending intent / confirmed finding counts)
		{"fallback_status", fmt.Sprintf(fallbackChatStatus, 3, 2, 1),
			"(rule mode, no LLM configured) Current status: 3 assets, 2 pending intents, 1 confirmed findings.\nAvailable commands: inject an intent with \"intent ...\", and pass a hint to the planner with \"hint ...\"."},
	} {
		assertEnglishMessage(t, c.label, c.msg, c.wantEnglish)
	}
}

// TestServerHandlersResponsesLocalized drives handler paths that finish without touching the
// DB and inspects the actual HTTP body, proving the constants really reach the response
// (putting Chinese back into a constant breaks this = adversarial non-vacuity). Paths that
// need a DB, engine or asset store (invalid category/company, missing LLM config, undetected
// python, notify settings, main-agent busy) are pinned by the constant assertions above.
func TestServerHandlersResponsesLocalized(t *testing.T) {
	// Server with the delete barrier up: beginTaskOperation / IsDeleting report "being deleted".
	deletingServer := func() *Server {
		s := &Server{m: &Manager{tasks: map[string]*Task{"t1": {ID: "t1"}}}, engine: &Engine{}}
		s.engine.deleting.Store("t1", true)
		return s
	}
	// Server without the barrier: input validation runs.
	liveServer := func() *Server {
		return &Server{m: &Manager{tasks: map[string]*Task{"t1": {ID: "t1"}}}, engine: &Engine{}}
	}

	cases := []struct {
		name        string
		req         func() *http.Request
		handler     func(*Server) http.HandlerFunc
		server      func() *Server
		code        int
		wantEnglish string
	}{
		{
			name: "controlIntent/task-deleting",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodPost, "/api/tasks/t1/intents/5/control", strings.NewReader(`{"action":"pause"}`))
				r.SetPathValue("id", "t1")
				r.SetPathValue("iid", "5")
				return r
			},
			handler:     func(s *Server) http.HandlerFunc { return s.controlIntent },
			server:      deletingServer,
			code:        http.StatusConflict,
			wantEnglish: "The intent cannot be controlled because the task is being deleted",
		},
		{
			name: "rerunIntent/task-deleting",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodPost, "/api/tasks/t1/intents/5/rerun", nil)
				r.SetPathValue("id", "t1")
				r.SetPathValue("iid", "5")
				return r
			},
			handler:     func(s *Server) http.HandlerFunc { return s.rerunIntent },
			server:      deletingServer,
			code:        http.StatusConflict,
			wantEnglish: "The task is being deleted, so the intent cannot be re-run",
		},
		{
			name: "rerunBlocked/task-deleting",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodPost, "/api/tasks/t1/intents/rerun-blocked", nil)
				r.SetPathValue("id", "t1")
				return r
			},
			handler:     func(s *Server) http.HandlerFunc { return s.rerunBlocked },
			server:      deletingServer,
			code:        http.StatusConflict,
			wantEnglish: "The task is being deleted, so the intent cannot be re-run",
		},
		{
			name: "createTask/source-limit",
			req: func() *http.Request {
				// MaxTaskSourceCount=8 → 9 exceeds the limit (returns before the loop).
				r := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(
					`{"goal":"goal","source_task_ids":["1","2","3","4","5","6","7","8","9"]}`))
				return r
			},
			handler:     func(s *Server) http.HandlerFunc { return s.createTask },
			server:      liveServer,
			code:        http.StatusBadRequest,
			wantEnglish: "You can link up to 8 source tasks",
		},
		{
			name: "createTask/source-invalid",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(
					`{"goal":"goal","source_task_ids":["abc"]}`))
				return r
			},
			handler:     func(s *Server) http.HandlerFunc { return s.createTask },
			server:      liveServer,
			code:        http.StatusBadRequest,
			wantEnglish: "The source task id is invalid or duplicated",
		},
		{
			name: "createTask/source-not-found",
			req: func() *http.Request {
				// 999 is not in the tasks map → not found (no DB access, map lookup only).
				r := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(
					`{"goal":"goal","source_task_ids":["999"]}`))
				return r
			},
			handler:     func(s *Server) http.HandlerFunc { return s.createTask },
			server:      liveServer,
			code:        http.StatusBadRequest,
			wantEnglish: "Source task #999 not found",
		},
		{
			name: "createTask/company-limit",
			req: func() *http.Request {
				// MaxTaskCompanyCount=32 → 33 errors inside NormalizeTaskCompanyIDs (a pure function).
				ids := make([]string, 33)
				for i := range ids {
					ids[i] = fmt.Sprintf("%d", i+1)
				}
				body := `{"goal":"goal","company_ids":[` + strings.Join(ids, ",") + `]}`
				return httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(body))
			},
			handler:     func(s *Server) http.HandlerFunc { return s.createTask },
			server:      liveServer,
			code:        http.StatusBadRequest,
			wantEnglish: "The linked companies are invalid: you can select at most 32 valid companies",
		},
		{
			name: "createTask/llm-profile-invalid",
			req: func() *http.Request {
				// llm_profile_ids:[0] → validateTaskProfileIDs returns before loadProfileConfig(DB).
				r := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(
					`{"goal":"goal","llm_profile_ids":[0]}`))
				return r
			},
			handler:     func(s *Server) http.HandlerFunc { return s.createTask },
			server:      liveServer,
			code:        http.StatusBadRequest,
			wantEnglish: "The LLM configuration id is invalid or duplicated",
		},
		{
			name: "taskCoverageGraph/asset-store-disabled",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/api/tasks/t1/coverage-graph", nil)
				r.SetPathValue("id", "t1")
				return r
			},
			handler:     func(s *Server) http.HandlerFunc { return s.taskCoverageGraph },
			server:      liveServer, // Manager.assets==nil → Assets()==nil → 503
			code:        http.StatusServiceUnavailable,
			wantEnglish: "The asset store is not enabled",
		},
		{
			name: "taskAssetRefs/asset-id-required",
			req: func() *http.Request {
				// no asset_id query → ParseInt("")=0 → <=0 → 400 (before Assets() is called).
				r := httptest.NewRequest(http.MethodGet, "/api/tasks/t1/asset-refs", nil)
				r.SetPathValue("id", "t1")
				return r
			},
			handler:     func(s *Server) http.HandlerFunc { return s.taskAssetRefs },
			server:      liveServer,
			code:        http.StatusBadRequest,
			wantEnglish: "asset_id is required",
		},
		{
			// F34(b): with profile_id empty (no DB lookup) and api_key empty, it returns
			// writeJSON{ok:false,error} before TestConnection (status 200). liveServer also has
			// llmCfg.APIKey == "", so there is no global fallback key either.
			name: "testLLM/no-api-key",
			req: func() *http.Request {
				return httptest.NewRequest(http.MethodPost, "/api/llm/test", strings.NewReader(`{}`))
			},
			handler:     func(s *Server) http.HandlerFunc { return s.testLLM },
			server:      liveServer,
			code:        http.StatusOK,
			wantEnglish: "No API Key was provided",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := c.server()
			rec := httptest.NewRecorder()
			c.handler(s)(rec, c.req())
			if rec.Code != c.code {
				t.Fatalf("status code = %d, want = %d (body %q)", rec.Code, c.code, rec.Body.String())
			}
			got := decodeErrorField(t, rec.Body.Bytes())
			if got != c.wantEnglish {
				t.Fatalf("response message = %q, want = %q", got, c.wantEnglish)
			}
			assertEnglishMessage(t, c.name, got, c.wantEnglish)
		})
	}
}

// TestChatUnavailableReasonLocalized checks that the F34(a) reason chatUnavailableReason()
// actually hands the user is tied to the English constants. With pg nil (no DB in the test) it
// falls straight into the "not ready" path, so that return must be errChatLLMNotReady
// (reverting the constant to Chinese breaks it = adversarial non-vacuity). The other two
// reasons (no config, no active config) need pg (DB), so the constant assertions above pin them.
func TestChatUnavailableReasonLocalized(t *testing.T) {
	s := &Server{m: &Manager{}} // m.pg == nil → third return path
	got := s.chatUnavailableReason()
	if got != errChatLLMNotReady {
		t.Fatalf("chatUnavailableReason() = %q, want = %q", got, errChatLLMNotReady)
	}
	assertEnglishMessage(t, "chat_not_ready", got,
		"Cannot start a conversation because the LLM is not ready: in System → LLM Settings, make sure there is an available and enabled configuration")
}

// TestFallbackCommandParsing checks that the rule-mode chat trigger keyword parser
// (fallbackCommand) recognises the English and Spanish triggers advertised by the
// rule-mode help text, strips only the keyword, and leaves anything else as a
// status request. It is pure input parsing that touches neither Store nor DB, so
// it runs without a DB. Both language cases must keep passing: dropping either
// keyword set breaks its cases (non-vacuity).
func TestFallbackCommandParsing(t *testing.T) {
	cases := []struct {
		name, in, cmd, text string
	}{
		{"english-intent", "intent scan ports", "intent", "scan ports"},
		{"english-hint", "hint try admin:admin", "hint", "try admin:admin"},
		{"spanish-intent", "intención escanear los puertos", "intent", "escanear los puertos"},
		{"spanish-intent-accent-insensitive", "INTENCIÓN escanear", "intent", "escanear"},
		{"spanish-hint", "pista revisa el formulario de acceso", "hint", "revisa el formulario de acceso"},
		{"spanish-hint-uppercase", "Pista revisa la cookie", "hint", "revisa la cookie"},
		{"status-plain", "show me the current status", "", "show me the current status"},
		{"status-spanish", "muéstrame el estado actual", "", "muéstrame el estado actual"},
		{"status-empty", "   ", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd, text := fallbackCommand(c.in)
			if cmd != c.cmd || text != c.text {
				t.Fatalf("fallbackCommand(%q) = (%q, %q), want = (%q, %q)", c.in, cmd, text, c.cmd, c.text)
			}
		})
	}
}
