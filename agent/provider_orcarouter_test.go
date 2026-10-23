package agent

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/orcarouter"
)

// TestConfigFromOrcaRouterDefaults locks the provider wiring: OrcaRouter keeps
// its own identity, its own inference origin and the documented default model,
// while speaking the OpenAI wire format through the existing adapter.
func TestConfigFromOrcaRouterDefaults(t *testing.T) {
	c := ConfigFrom("orcarouter", "", "", "sk-orca-test-key", "")
	if c.Provider() != "orcarouter" {
		t.Fatalf("provider = %q, want orcarouter", c.Provider())
	}
	if c.BaseURL != "https://api.orcarouter.ai/v1" {
		t.Fatalf("base url = %q", c.BaseURL)
	}
	if c.Model != "orcarouter/auto" {
		t.Fatalf("default model = %q, want orcarouter/auto", c.Model)
	}
	if c.Format != ConfigFrom("openai", "gpt-4o", "", "sk-test", "").Format {
		t.Fatalf("format = %v, want the OpenAI wire format", c.Format)
	}

	custom := ConfigFrom("orcarouter", "orcacyber-ultra-1.0", "https://api.orcarouter.ai/v1/chat/completions", "sk-orca-test-key", "")
	if custom.BaseURL != "https://api.orcarouter.ai/v1" {
		t.Fatalf("full endpoint URL not normalized: %q", custom.BaseURL)
	}
	if custom.Model != "orcacyber-ultra-1.0" {
		t.Fatalf("model = %q", custom.Model)
	}
}

// TestOrcaRouterProviderIsDistinctFromOpenAI guards against the profile being
// reloaded as a plain OpenAI endpoint after a DB round trip.
func TestOrcaRouterProviderIsDistinctFromOpenAI(t *testing.T) {
	oai := ConfigFrom("openai", "gpt-4o", "", "sk-test", "")
	orca := ConfigFrom("orcarouter", "", "", "sk-orca-test", "")
	if oai.Provider() == orca.Provider() {
		t.Fatal("orcarouter must stay a named provider, not collapse into openai")
	}
	if oai.BaseURL == orca.BaseURL {
		t.Fatal("orcarouter must use its dedicated inference origin")
	}
}

// TestLiveOrcaRouterCompletion is the pre-PR connectivity check. It runs the
// real provider path (agent.Config.NewProvider -> Norma -> OrcaRouter) and the
// real model catalog, using the verification key from the environment.
//
// The verification key used by CI does not have access to orcarouter/auto or
// orcarouter/fusion (403 model_access_denied), so this connectivity check
// defaults to the public deepseek/deepseek-v4.1-flash. That is a test-only
// choice: the user-facing default stays orcarouter/auto and the documented
// fallback stays orcarouter/fusion. ORCAROUTER_LIVE_MODEL overrides it when a
// cyber-tier key is available.
func TestLiveOrcaRouterCompletion(t *testing.T) {
	key := strings.TrimSpace(os.Getenv("ORCAROUTER_API_KEY"))
	if key == "" {
		t.Skip("ORCAROUTER_API_KEY is not set")
	}
	model := strings.TrimSpace(os.Getenv("ORCAROUTER_LIVE_MODEL"))
	if model == "" {
		model = "deepseek/deepseek-v4.1-flash"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := ConfigFrom("orcarouter", model, "", key, "")
	lat, reply, err := TestConnection(ctx, cfg)
	if err != nil {
		t.Fatalf("live completion via orcarouter/%s failed: %v", model, err)
	}
	if strings.TrimSpace(reply) == "" {
		t.Fatalf("live completion via orcarouter/%s returned no visible text", model)
	}
	t.Logf("live completion ok: model=%s latency=%s", model, lat)

	// The catalog is the authoritative source for the pickers. It must be
	// reachable with the same key and expose chat-capable models with the
	// vendor/model namespace intact.
	client := cfg.NewHTTPClient()
	models, err := orcarouter.FetchCatalog(ctx, client, orcarouter.DefaultAPIBase, key)
	if err != nil {
		t.Fatalf("live model catalog failed: %v", err)
	}
	chat := orcarouter.Filter(models, orcarouter.CapabilityChat, "")
	if len(chat) == 0 {
		t.Fatal("live catalog returned no chat-capable models")
	}
	imageChat := orcarouter.Filter(models, orcarouter.CapabilityChat, "image")
	if len(imageChat) >= len(chat) {
		t.Fatalf("image filter did not narrow the chat catalog: %d/%d", len(imageChat), len(chat))
	}
	wantModel := strings.TrimSpace(os.Getenv("ORCAROUTER_EXPECT_MODEL"))
	if wantModel != "" {
		found := false
		for _, m := range chat {
			if m.ID == wantModel {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected model %q not present in the live chat catalog", wantModel)
		}
	}
	t.Logf("live catalog ok: %d chat models, %d image-capable chat models", len(chat), len(imageChat))
}
