package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestServerMgmtConstantsLocalized pins that every user-facing error/response
// literal extracted from server_mgmt.go is its exact English source text.
// Reverting any one to another language, or rewording it, fails here.
// assertEnglishMessage / decodeErrorField are reused from the F3 suite
// (same package server).
func TestServerMgmtConstantsLocalized(t *testing.T) {
	cases := []struct{ label, msg, wantEnglish string }{
		{"errMgmtPGUnavailable", errMgmtPGUnavailable, "Not connected to the admin console data source (PostgreSQL)"},
		{"errMgmtTaskIDInvalid", errMgmtTaskIDInvalid, "The task id is invalid"},
		{"errMgmtTaskDeleting", errMgmtTaskDeleting, "The task is being deleted"},
		{"errMgmtTaskHasAgents", errMgmtTaskHasAgents, "Task deletion was cancelled because agents are still running"},
		{"errMgmtAgentKeyFormat", errMgmtAgentKeyFormat, "key must start with a lowercase letter and may contain only lowercase letters, digits, and underscores"},
		{"errMgmtNameEmpty", errMgmtNameEmpty, "The name cannot be empty"},
		{"errMgmtAgentKeyExists", errMgmtAgentKeyExists, "The key already exists"},
		{"errMgmtBuiltinNoEditMeta", errMgmtBuiltinNoEditMeta, "Built-in agents cannot have their name or description modified"},
		{"errMgmtBuiltinNoDelete", errMgmtBuiltinNoDelete, "Built-in agents cannot be deleted"},
		{"errMgmtLLMProfileIDFormat", errMgmtLLMProfileIDFormat, "The llm_profile_id format is invalid"},
		{"errMgmtLLMProfileInvalid", errMgmtLLMProfileInvalid, "The specified LLM configuration does not exist or is invalid"},
		{"errMgmtNoBuiltinPrompt", errMgmtNoBuiltinPrompt, "This agent has no built-in default prompt, so it cannot be restored"},
		{"errMgmtToolNotFound", errMgmtToolNotFound, "Tool not found: "},
		{"errMgmtNotBuiltinTool", errMgmtNotBuiltinTool, "Not a built-in tool, or it does not exist: "},
		{"errMgmtMCPNotFound", errMgmtMCPNotFound, "MCP not found"},
		{"errMgmtToolDiscoverFail", errMgmtToolDiscoverFail, "Tool discovery failed: "},
		{"errMgmtSkillNameInvalid", errMgmtSkillNameInvalid, "The skill name is invalid"},
		{"errMgmtSkillNoFile", errMgmtSkillNoFile, "No file was uploaded (form field file) or it exceeded the size limit"},
		{"errMgmtSkillNoMDInZip", errMgmtSkillNoMDInZip, "SKILL.md was not found inside the archive"},
		{"errMgmtSkillReadMDFail", errMgmtSkillReadMDFail, "Failed to read SKILL.md: "},
		{"errMgmtSkillNamePre", errMgmtSkillNamePre, "The skill name is invalid (taken from the name field in SKILL.md): "},
		{"errMgmtSkillNamePost", errMgmtSkillNamePost, " (max 64 characters, must start with a letter, may contain only lowercase letters, digits, hyphens, or non-ASCII letters such as Hangul; spaces, dots, and path separators are not allowed)"},
		{"errMgmtSkillExistsPre", errMgmtSkillExistsPre, "The skill already exists: "},
		{"errMgmtSkillExistsPost", errMgmtSkillExistsPost, " (To overwrite, confirm and try again)"},
		{"errMgmtSkillZipBadPath", errMgmtSkillZipBadPath, "The archive contains an invalid path "},
		{"errMgmtSkillZipTooMany", errMgmtSkillZipTooMany, "The archive contains too many files"},
		{"errMgmtSkillFileTooLarge", errMgmtSkillFileTooLarge, "The file is too large: "},
		{"errMgmtSkillZipTooLarge", errMgmtSkillZipTooLarge, "The uncompressed size is too large"},
		{"errMgmtSkillNoMDAfter", errMgmtSkillNoMDAfter, "SKILL.md is missing after extraction"},
		{"errMgmtSkillInstallFail", errMgmtSkillInstallFail, "Installation failed: "},
		{"errMgmtLLMActiveDelete", errMgmtLLMActiveDelete, "The currently active LLM configuration cannot be deleted. Activate a different configuration first"},
		{"errMgmtLLMRefChanged", errMgmtLLMRefChanged, "The LLM configuration is being modified by a task or conversation. Please try again"},
		{"errMgmtLLMRefTimeout", errMgmtLLMRefTimeout, "Timed out waiting for the LLM configuration reference to be released. Please try again"},
		{"errMgmtNoAPIKey", errMgmtNoAPIKey, "No API Key was provided"},
		{"errMgmtBuildReqFail", errMgmtBuildReqFail, "Failed to build the request: "},
		{"errMgmtReqFail", errMgmtReqFail, "The request failed: "},
		{"errMgmtParseRespFail", errMgmtParseRespFail, "Failed to parse the response: "},
		{"errMgmtNoModelList", errMgmtNoModelList, "Failed to retrieve the model list"},
		{"errMgmtTmplSyntax", errMgmtTmplSyntax, "Template syntax error: "},
		{"errMgmtTmplVarPre", errMgmtTmplVarPre, "Variable {{."},
		{"errMgmtTmplVarPost", errMgmtTmplVarPost, "}} is not in this agent's allowlist"},
		// Format strings are checked against the real formatting result (English + verb preserved).
		{"errMgmtAPIReturned", fmt.Sprintf(errMgmtAPIReturned, 404, "detail"), "The API returned 404: detail"},
	}
	for _, c := range cases {
		assertEnglishMessage(t, c.label, c.msg, c.wantEnglish)
	}
}

// TestServerMgmtAPIReturnedFormat guards that the model-list probe format string
// keeps its status-code and body placeholders (a %d→wrong-verb edit would drop
// them).
func TestServerMgmtAPIReturnedFormat(t *testing.T) {
	msg := fmt.Sprintf(errMgmtAPIReturned, 404, "boom-body")
	assertEnglishMessage(t, "errMgmtAPIReturned", msg, "The API returned 404: boom-body")
	if !strings.Contains(msg, "404") || !strings.Contains(msg, "boom-body") {
		t.Fatalf("the format string must preserve the status code and body: %q", msg)
	}
}

// TestServerMgmtSkillExistsSentinel guards the cross-stack contract: the server's
// duplicate-skill 409 message is shown verbatim in the overwrite-confirm dialog of
// web/src/app/(main)/system/skills/page.tsx (the UI keys the flow off status 409
// and the message text), so it must stay this exact English sentence around the
// skill name. Drift here silently disables overwrite.
func TestServerMgmtSkillExistsSentinel(t *testing.T) {
	msg := errMgmtSkillExistsPre + "my-skill" + errMgmtSkillExistsPost
	assertEnglishMessage(t, "skillExists", msg,
		"The skill already exists: my-skill (To overwrite, confirm and try again)")
	if !strings.Contains(msg, "my-skill") {
		t.Fatalf("the skill name must be included in the message: %q", msg)
	}
}

// TestServerMgmtPGGate503Localized exercises the real shared DB gate every
// management handler passes through: with no PostgreSQL handle, pg() writes 503
// and returns nil. The 503 body must be the English source message.
func TestServerMgmtPGGate503Localized(t *testing.T) {
	s := &Server{m: &Manager{}} // pg == nil
	rec := httptest.NewRecorder()
	if got := s.pg(rec); got != nil {
		t.Fatal("pg() must return nil when there is no DB handle")
	}
	if rec.Code != 503 {
		t.Fatalf("expected status code 503 but got %d", rec.Code)
	}
	assertEnglishMessage(t, "pg.503", decodeErrorField(t, rec.Body.Bytes()),
		"Not connected to the admin console data source (PostgreSQL)")
}

// TestServerMgmtDeleteTaskBadIDLocalized drives pgDeleteTask through its first
// guard (non-numeric id → 400) without any DB or engine.
func TestServerMgmtDeleteTaskBadIDLocalized(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodDelete, "/api/tasks/abc", nil)
	req.SetPathValue("id", "abc")
	rec := httptest.NewRecorder()
	s.pgDeleteTask(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status code 400 but got %d (body %s)", rec.Code, rec.Body.Bytes())
	}
	got := decodeErrorField(t, rec.Body.Bytes())
	assertEnglishMessage(t, "deleteTask.badID", got, "The task id is invalid")
	if got != errMgmtTaskIDInvalid {
		t.Fatalf("must match errMgmtTaskIDInvalid: %q", got)
	}
}

// TestServerMgmtListModelsNoKeyLocalized drives pgListModels with no api key and
// no profile id, so it returns {"ok":false,"error":...} before touching the DB.
func TestServerMgmtListModelsNoKeyLocalized(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/api/llm/models", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	s.pgListModels(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected status code 200 but got %d", rec.Code)
	}
	got := decodeErrorField(t, rec.Body.Bytes())
	assertEnglishMessage(t, "listModels.noKey", got, "No API Key was provided")
	if got != errMgmtNoAPIKey {
		t.Fatalf("must match errMgmtNoAPIKey: %q", got)
	}
}

// TestServerMgmtValidateTemplateLocalized exercises the prompt-template editor
// validator (pure function, no DB): both the parse-error and the
// disallowed-variable branches must return the English source text.
func TestServerMgmtValidateTemplateLocalized(t *testing.T) {
	if msg := validateTemplate("{{", nil); msg == "" {
		t.Fatal("a broken template must return an error message")
	} else {
		assertEnglishPrefix(t, "validateTemplate.syntax", msg, "Template syntax error: ")
	}
	msg := validateTemplate("{{.Bogus}}", nil)
	if msg == "" {
		t.Fatal("a variable not on the allowlist must return an error message")
	}
	assertEnglishMessage(t, "validateTemplate.var", msg,
		"Variable {{.Bogus}} is not in this agent's allowlist")
	if !strings.Contains(msg, "Bogus") {
		t.Fatalf("the variable name must be included in the message: %q", msg)
	}
}

// TestServerMgmtGlobalPromptVarsLocalized pins the template-variable catalog help
// text (rendered in the prompt editor's variable palette) as its exact English source text.
func TestServerMgmtGlobalPromptVarsLocalized(t *testing.T) {
	if len(globalPromptVars) == 0 {
		t.Fatal("globalPromptVars is empty")
	}
	wantEnglish := map[string]string{
		"Now":     "Current server time (refreshed live on every run; subtract a fixed start time from it to compute elapsed time)",
		"DataDir": "Server data root directory (the root of every task/conversation artefact; each agent really writes into a subdirectory below it, e.g. <DataDir>/<taskID>)",
	}
	for _, v := range globalPromptVars {
		assertEnglishMessage(t, "globalPromptVar."+v.Name, v.Description, wantEnglish[v.Name])
	}
}
