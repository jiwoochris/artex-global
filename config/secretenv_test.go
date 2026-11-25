package config

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// resetSecretStore keeps tests independent of each other.
func resetSecretStore(t *testing.T) {
	t.Helper()
	secretMu.Lock()
	secretStore = map[string]string{}
	secretMu.Unlock()
	t.Cleanup(func() {
		secretMu.Lock()
		secretStore = map[string]string{}
		secretMu.Unlock()
	})
}

func TestScrubSecretEnvHidesFromChildrenButKeepsServerAccess(t *testing.T) {
	resetSecretStore(t)
	for _, name := range SecretEnvNames {
		t.Setenv(name, "value-of-"+name)
	}
	t.Setenv("PATH_LIKE_PUBLIC", "keep-me")

	ScrubSecretEnv()

	// The server's own code still gets every value.
	for _, name := range SecretEnvNames {
		if got, want := Secret(name), "value-of-"+name; got != want {
			t.Errorf("Secret(%s) = %q, want %q", name, got, want)
		}
	}

	// A child process must see none of them, but non-secret variables survive.
	out, err := exec.Command("env").Output()
	if err != nil {
		t.Skipf("env not available: %v", err)
	}
	for _, name := range SecretEnvNames {
		if strings.Contains(string(out), name+"=") || strings.Contains(string(out), "value-of-"+name) {
			t.Errorf("child process still sees %s", name)
		}
	}
	if !strings.Contains(string(out), "PATH_LIKE_PUBLIC=keep-me") {
		t.Error("non-secret variable was removed")
	}
}

func TestSecretFallsBackToLiveEnvAndScrubIsIdempotent(t *testing.T) {
	resetSecretStore(t)
	t.Setenv("OPENAI_API_KEY", "live")
	if got := Secret("OPENAI_API_KEY"); got != "live" {
		t.Fatalf("before scrub: got %q", got)
	}
	ScrubSecretEnv()
	ScrubSecretEnv() // second call must not lose the captured value
	if got := Secret("OPENAI_API_KEY"); got != "live" {
		t.Fatalf("after double scrub: got %q", got)
	}
	if _, ok := os.LookupEnv("OPENAI_API_KEY"); ok {
		t.Fatal("variable still in process environment")
	}
	if got := Secret("ANTHROPIC_API_KEY"); got != "" {
		t.Fatalf("unset secret should be empty, got %q", got)
	}
}
