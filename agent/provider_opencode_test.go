package agent

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

// TestConfigFromOpenCodeGo locks the opencode-go alias wiring: the OpenCode Go
// subscription gateway is OpenAI-compatible, so it resolves to the openai wire
// format at the documented zen/go/v1 root, with glm-5.3-flash as the default
// model. A full endpoint URL is tolerated like the openai provider, and an
// explicit model or base URL always wins.
func TestConfigFromOpenCodeGo(t *testing.T) {
	c := ConfigFrom("opencode-go", "", "", "", "")
	if c.Provider() != "openai" {
		t.Errorf("opencode-go resolved provider %q, want openai (OpenAI-compatible gateway)", c.Provider())
	}
	if c.Format != llm.FormatOpenAI {
		t.Errorf("opencode-go resolved format %q, want %q", c.Format, llm.FormatOpenAI)
	}
	if c.BaseURL != "https://opencode.ai/zen/go/v1" {
		t.Errorf("opencode-go default base URL %q, want https://opencode.ai/zen/go/v1", c.BaseURL)
	}
	if c.Model != "glm-5.3-flash" {
		t.Errorf("opencode-go default model %q, want glm-5.3-flash", c.Model)
	}
	if c.SessionHeaderKey != "x-opencode-session" {
		t.Errorf("opencode-go session header %q, want x-opencode-session (gateway 400 MissingSessionID without it)", c.SessionHeaderKey)
	}

	c = ConfigFrom("opencode-go", "qwen3.7-plus", "https://opencode.ai/zen/go/v1/chat/completions", "", "")
	if c.BaseURL != "https://opencode.ai/zen/go/v1" {
		t.Errorf("opencode-go normalized base URL %q, want https://opencode.ai/zen/go/v1", c.BaseURL)
	}
	if c.Model != "qwen3.7-plus" {
		t.Errorf("opencode-go explicit model %q, want qwen3.7-plus", c.Model)
	}
}

// TestFromEnvOpenCodeGoCredential verifies the credential path: an opencode-go
// provider must take OPENCODE_API_KEY — not OPENAI_API_KEY, even when that cloud
// key is also present in the environment.
func TestFromEnvOpenCodeGoCredential(t *testing.T) {
	isolateLLMEnvironment(t)
	t.Setenv("SCOPEWEAVER_LLM_PROVIDER", "opencode-go")
	t.Setenv("OPENCODE_API_KEY", "opencode-subscription-key")
	cfg, ok := FromEnv()
	if !ok {
		t.Fatal("opencode-go environment configuration is not ready")
	}
	if cfg.Provider() != "openai" || cfg.BaseURL != "https://opencode.ai/zen/go/v1" || cfg.Model != "glm-5.3-flash" {
		t.Fatalf("opencode-go resolved provider=%q base=%q model=%q", cfg.Provider(), cfg.BaseURL, cfg.Model)
	}
	if cfg.APIKey != "opencode-subscription-key" {
		t.Fatalf("opencode-go configured key %q, want the OPENCODE_API_KEY value", cfg.APIKey)
	}
}

// TestFromEnvOpenCodeGoInferred verifies provider inference: OPENCODE_API_KEY
// alone (no Anthropic/OpenAI key) selects the opencode-go provider.
func TestFromEnvOpenCodeGoInferred(t *testing.T) {
	isolateLLMEnvironment(t)
	os.Unsetenv("ANTHROPIC_API_KEY")
	os.Unsetenv("OPENAI_API_KEY")
	t.Setenv("OPENCODE_API_KEY", "opencode-subscription-key")
	cfg, ok := FromEnv()
	if !ok {
		t.Fatal("OPENCODE_API_KEY alone did not resolve an LLM provider")
	}
	if cfg.Provider() != "openai" || cfg.BaseURL != "https://opencode.ai/zen/go/v1" || cfg.APIKey != "opencode-subscription-key" {
		t.Fatalf("inferred provider=%q base=%q key=%q, want openai / zen go root / OPENCODE_API_KEY", cfg.Provider(), cfg.BaseURL, cfg.APIKey)
	}
}

// TestFromEnvOpenCodeGoExplicitBlankKey verifies the API_KEY override contract:
// an explicitly empty SCOPEWEAVER_LLM_API_KEY suppresses the OPENCODE_API_KEY
// fallback. A cloud endpoint with no key is then not ready (Ready=false), the
// same contract as Anthropic/OpenAI; a keyless run needs a local endpoint.
func TestFromEnvOpenCodeGoExplicitBlankKey(t *testing.T) {
	isolateLLMEnvironment(t)
	t.Setenv("SCOPEWEAVER_LLM_PROVIDER", "opencode-go")
	t.Setenv("OPENCODE_API_KEY", "opencode-subscription-key")
	t.Setenv("SCOPEWEAVER_LLM_API_KEY", "")
	cfg, ok := FromEnv()
	if ok {
		t.Fatal("explicit blank API key must suppress the OPENCODE_API_KEY fallback and leave the cloud provider not ready")
	}
	if cfg.APIKey != "" {
		t.Fatalf("explicit blank API key suppressed fallback, got %q", cfg.APIKey)
	}
}

func TestFromEnvOpenCodeGoFileCredentials(t *testing.T) {
	for _, tc := range []struct {
		name, provider, fileKey, legacyKey, modernKey, want string
		legacy, modern, noOpenCodeKey                       bool
	}{
		{name: "plain provider", provider: "opencode-go", want: "opencode-subscription-key"},
		{name: "padded provider", want: "opencode-subscription-key"},
		{name: "missing subscription key never inherits openai", noOpenCodeKey: true},
		{name: "file key", fileKey: "file-key", want: "file-key"},
		{name: "legacy override", fileKey: "file-key", legacy: true, legacyKey: "legacy-key", want: "legacy-key"},
		{name: "modern override", fileKey: "file-key", legacy: true, legacyKey: "legacy-key", modern: true, modernKey: "modern-key", want: "modern-key"},
		{name: "modern blank", fileKey: "file-key", legacy: true, legacyKey: "legacy-key", modern: true},
		{name: "legacy blank", fileKey: "file-key", legacy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateLLMEnvironment(t)
			if !tc.noOpenCodeKey {
				t.Setenv("OPENCODE_API_KEY", "opencode-subscription-key")
			}
			if tc.legacy {
				t.Setenv("ARTEX_LLM_API_KEY", tc.legacyKey)
			}
			if tc.modern {
				t.Setenv("SCOPEWEAVER_LLM_API_KEY", tc.modernKey)
			}
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				key := tc.want
				if key == "" {
					key = "scopeweaver-local"
				}
				if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer "+key {
					t.Errorf("gateway received %s %s Authorization=%q, want POST /chat/completions with Bearer %s", r.Method, r.URL.Path, r.Header.Get("Authorization"), key)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`)
			}))
			defer srv.Close()
			provider := tc.provider
			if provider == "" {
				provider = " \topencode-go\n "
			}
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"llm":{"provider":%q,"api_key":%q,"base_url":%q}}`, provider, tc.fileKey, srv.URL)), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("SCOPEWEAVER_CONFIG", path)
			cfg, ok := FromEnv()
			if !ok {
				t.Fatal("local gateway configuration is not ready")
			}
			cfg.Retry = RetryConfig{ConnectAttempts: -1, EmptyAttempts: -1}
			client, err := cfg.NewProvider()
			if err != nil {
				t.Fatal(err)
			}
			msg, _, _, err := client.Complete(t.Context(), llm.CompletionRequest{Messages: []llm.Message{llm.UserText("ping")}, MaxTokens: 64})
			if err != nil || msg.Text() != "OK" || calls != 1 || cfg.APIKey != tc.want {
				t.Fatalf("gateway reply=%q err=%v calls=%d configured key=%q, want %q", msg.Text(), err, calls, cfg.APIKey, tc.want)
			}
		})
	}
}

func TestFromEnvWhitespaceFileProviderInference(t *testing.T) {
	isolateLLMEnvironment(t)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"llm":{"provider":" \t\n "}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCOPEWEAVER_CONFIG", path)
	os.Unsetenv("ANTHROPIC_API_KEY")
	os.Unsetenv("OPENAI_API_KEY")
	t.Setenv("OPENCODE_API_KEY", "opencode-subscription-key")
	cfg, ok := FromEnv()
	if !ok || cfg.APIKey != "opencode-subscription-key" || cfg.BaseURL != "https://opencode.ai/zen/go/v1" {
		t.Fatalf("whitespace provider inferred base=%q key=%q ready=%v, want OpenCode Go", cfg.BaseURL, cfg.APIKey, ok)
	}
}
