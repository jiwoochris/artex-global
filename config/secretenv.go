package config

import (
	"os"
	"sync"
)

// SecretEnvNames lists the environment variables that carry credentials for the
// server itself. Every child process the server starts (the agent's bash tool,
// MCP stdio servers, custom Python tools) inherits os.Environ(), and those
// children run commands chosen by an LLM that reads attacker-controlled content
// from scan targets. A prompt injection from a hostile target could therefore
// ask for `env` and leak these. ScrubSecretEnv removes them from the process
// environment at startup; Secret keeps serving them to the server's own code.
var SecretEnvNames = []string{
	"ANTHROPIC_API_KEY",
	"OPENAI_API_KEY",
	"ARTEX_PG_DSN",
	"ARTEX_ADMIN_PASSWORD",
	"ARTEX_SETUP_TOKEN",
	"ARTEX_LLM_PROXY", // may embed user:password@ proxy credentials
}

var (
	secretMu    sync.RWMutex
	secretStore = map[string]string{}
)

// ScrubSecretEnv moves every variable in SecretEnvNames out of the process
// environment into an in-memory store and unsets it, so child processes no longer
// inherit it. Call it once, first thing in main. It is idempotent.
//
// Limit: this stops the passive path (env / printenv / os.Environ in a child). It
// does not hide the values from a same-user process that reads /proc/<pid>/environ
// of the launcher (start.sh / PID 1), which still holds the original environment.
// Run the server as a dedicated unprivileged user, or pass secrets via files or
// Docker secrets, if that matters for your deployment.
func ScrubSecretEnv() {
	secretMu.Lock()
	defer secretMu.Unlock()
	for _, name := range SecretEnvNames {
		if v, ok := os.LookupEnv(name); ok {
			if v != "" {
				secretStore[name] = v
			}
			_ = os.Unsetenv(name)
		}
	}
}

// Secret returns the value of a credential variable: the scrubbed copy captured by
// ScrubSecretEnv if there is one, otherwise the live environment (unit tests, and
// any caller that runs before ScrubSecretEnv). Use it instead of os.Getenv for
// every name in SecretEnvNames.
func Secret(name string) string {
	secretMu.RLock()
	v, ok := secretStore[name]
	secretMu.RUnlock()
	if ok {
		return v
	}
	return os.Getenv(name)
}
