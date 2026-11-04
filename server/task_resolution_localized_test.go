package server

import (
	"testing"

	"github.com/yukiteruamano/artex/db"
)

// TestTaskLLMResolutionLocalized guards F18: every user-facing reason/label that
// GET /api/tasks/{id}/llm/resolution returns (rendered in the task LLM settings
// chain / system/llm UI via resolutionLabel / the chain editor) is its exact English
// source text. Source enum values and the fmt.Errorf wraps are out of scope and are
// not asserted here.
func TestTaskLLMResolutionLocalized(t *testing.T) {
	// 1) Every user-facing constant is its English source text. Reverting any to another
	// language fails this.
	for _, c := range []struct{ label, msg, wantEnglish string }{
		{"reasonLLMProfileMissing", reasonLLMProfileMissing, "LLM profile not found"},
		{"reasonLLMProfileNoAPIKey", reasonLLMProfileNoAPIKey, "No API Key is set for the LLM profile"},
		{"reasonLLMProfileInvalid", reasonLLMProfileInvalid, "The LLM profile format or parameters are invalid"},
		{"reasonTaskLLMChainExhausted", reasonTaskLLMChainExhausted, "All quotas in the task LLM profile chain are exhausted"},
		{"reasonNoLLMAvailable", reasonNoLLMAvailable, "No LLM profile is available"},
		{"sourceNameGlobalConfig", sourceNameGlobalConfig, "Global config"},
	} {
		assertEnglishMessage(t, c.label, c.msg, c.wantEnglish)
	}

	// 2) Pin the two reasons reachable with no DB. resolutionFromProfile returns
	// before touching providerForProfile for a nil profile and for a profile with
	// a blank API key, so a zero-value Server exercises the real code path.
	s := &Server{}

	missing := s.resolutionFromProfile(nil, "task_chain")
	if missing.Available {
		t.Fatalf("nil profile must be unavailable, got %+v", missing)
	}
	if missing.Reason != reasonLLMProfileMissing {
		t.Fatalf("nil profile reason = %q, want %q", missing.Reason, reasonLLMProfileMissing)
	}
	assertEnglishMessage(t, "resolutionFromProfile(nil).Reason", missing.Reason, "LLM profile not found")

	noKey := s.resolutionFromProfile(&db.LLMProfile{Name: "p", Format: "openai", Model: "m"}, "task_chain")
	if noKey.Available {
		t.Fatalf("blank API key must be unavailable, got %+v", noKey)
	}
	if noKey.Reason != reasonLLMProfileNoAPIKey {
		t.Fatalf("blank-key reason = %q, want %q", noKey.Reason, reasonLLMProfileNoAPIKey)
	}
	assertEnglishMessage(t, "resolutionFromProfile(noKey).Reason", noKey.Reason,
		"No API Key is set for the LLM profile")
}
