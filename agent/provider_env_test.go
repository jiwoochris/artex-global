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

func isolateLLMEnvironment(t *testing.T) {
	t.Helper()
	for _, prefix := range []string{"SCOPEWEAVER_LLM_", "ARTEX_LLM_"} {
		for _, suffix := range []string{"PROVIDER", "MODEL", "BASE_URL", "API_KEY", "PROXY", "KEEP_ALIVE", "CONTEXT_WINDOW_K", "MAX_CONCURRENT", "MAX_TOKENS", "THINKING_TYPE", "REASONING_EFFORT", "STREAM"} {
			key := prefix + suffix
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("SCOPEWEAVER_CONFIG", filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv("SCOPEWEAVER_LOCAL_LLM_ONLY", "false")
	t.Setenv("SCOPEWEAVER_LOCAL_LLM_ALLOWED_CIDRS", "")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-provider-key")
	t.Setenv("OPENAI_API_KEY", "openai-provider-key")
	t.Setenv("OLLAMA_API_KEY", "ollama-provider-key")
	t.Setenv("OPENCODE_API_KEY", "")
	if err := os.Unsetenv("OPENCODE_API_KEY"); err != nil {
		t.Fatal(err)
	}
}

func TestFromEnvLocalGatewayAuthentication(t *testing.T) {
	isolateLLMEnvironment(t)
	for _, format := range []string{"anthropic", "openai", "openai-responses"} {
		t.Run(format, func(t *testing.T) {
			providerKey := "openai-provider-key"
			if format == "anthropic" {
				providerKey = "anthropic-provider-key"
			}
			for _, tc := range []struct {
				name, fileKey, legacyKey, modernKey, want string
				legacy, modern                            bool
			}{
				{name: "provider environment", want: providerKey},
				{name: "config file", fileKey: "file-key", want: "file-key"},
				{name: "legacy override", fileKey: "file-key", legacy: true, legacyKey: "legacy-key", want: "legacy-key"},
				{name: "modern override", fileKey: "file-key", legacy: true, legacyKey: "legacy-key", modern: true, modernKey: "modern-key", want: "modern-key"},
				{name: "modern blank", fileKey: "file-key", legacy: true, legacyKey: "legacy-key", modern: true},
				{name: "legacy blank", fileKey: "file-key", legacy: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if tc.fileKey != "" {
						path := filepath.Join(t.TempDir(), "config.json")
						if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"llm":{"api_key":%q}}`, tc.fileKey)), 0600); err != nil {
							t.Fatal(err)
						}
						t.Setenv("SCOPEWEAVER_CONFIG", path)
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
						path, header, want := "/custom/chat/completions", "Authorization", "Bearer "+key
						if format == "anthropic" {
							path, header, want = "/custom/v1/messages", "X-Api-Key", key
						} else if format == "openai-responses" {
							path = "/custom/responses"
						}
						if r.Method != http.MethodPost || r.URL.Path != path || r.Header.Get(header) != want {
							t.Errorf("gateway received %s http://%s%s with %s=%q; want POST %s with %q", r.Method, r.Host, r.URL.Path, header, r.Header.Get(header), path, want)
							http.Error(w, "gateway authentication failed", http.StatusUnauthorized)
							return
						}
						w.Header().Set("Content-Type", "application/json")
						switch format {
						case "anthropic":
							fmt.Fprint(w, `{"role":"assistant","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn"}`)
						case "openai":
							fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`)
						case "openai-responses":
							fmt.Fprint(w, `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}]}`)
						}
					}))
					defer srv.Close()
					t.Setenv("ARTEX_LLM_PROVIDER", format)
					t.Setenv("ARTEX_LLM_BASE_URL", srv.URL+"/custom")
					cfg, ok := FromEnv()
					if !ok {
						t.Fatal("local gateway configuration is not ready")
					}
					cfg.Retry = RetryConfig{ConnectAttempts: -1, EmptyAttempts: -1}
					provider, err := cfg.NewProvider()
					if err != nil {
						t.Fatal(err)
					}
					msg, _, _, err := provider.Complete(t.Context(), llm.CompletionRequest{Messages: []llm.Message{llm.UserText("ping")}, MaxTokens: 64})
					if err != nil || msg.Text() != "OK" || calls != 1 || cfg.APIKey != tc.want {
						t.Fatalf("gateway reply=%q err=%v calls=%d configured key=%q, want %q", msg.Text(), err, calls, cfg.APIKey, tc.want)
					}
				})
			}
		})
	}
}

func TestFromEnvNativeCredentialIsolation(t *testing.T) {
	isolateLLMEnvironment(t)
	t.Setenv("ARTEX_LLM_PROVIDER", "ollama")
	for _, tc := range []struct {
		name, ollamaKey, want string
		explicitBlank         bool
	}{
		{name: "cloud keys never inherited"},
		{name: "native provider key", ollamaKey: "native-key", want: "native-key"},
		{name: "explicit blank suppresses native key", ollamaKey: "native-key", explicitBlank: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OLLAMA_API_KEY", tc.ollamaKey)
			if tc.explicitBlank {
				t.Setenv("SCOPEWEAVER_LLM_API_KEY", "")
			}
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				want := ""
				if tc.want != "" {
					want = "Bearer " + tc.want
				}
				if r.Method != http.MethodPost || r.URL.Path != "/api/chat" || r.Header.Get("Authorization") != want || r.Header.Get("X-Api-Key") != "" {
					t.Errorf("native request leaked credentials: %s %s Authorization=%q X-Api-Key=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("X-Api-Key"))
					http.Error(w, "native authentication failed", http.StatusUnauthorized)
					return
				}
				fmt.Fprint(w, `{"done":true,"message":{"content":"OK"}}`)
			}))
			defer srv.Close()
			t.Setenv("ARTEX_LLM_BASE_URL", srv.URL)
			cfg, ok := FromEnv()
			if !ok || cfg.APIKey != tc.want || cfg.Provider() != "ollama" {
				t.Fatalf("native provider=%q key=%q ready=%v, want key=%q", cfg.Provider(), cfg.APIKey, ok, tc.want)
			}
			cfg.Retry = RetryConfig{ConnectAttempts: -1, EmptyAttempts: -1}
			provider, err := cfg.NewProvider()
			if err != nil {
				t.Fatal(err)
			}
			msg, _, _, err := provider.Complete(t.Context(), llm.CompletionRequest{Messages: []llm.Message{llm.UserText("ping")}})
			if err != nil || msg.Text() != "OK" || calls != 1 {
				t.Fatalf("native reply=%q err=%v calls=%d", msg.Text(), err, calls)
			}
		})
	}
}
