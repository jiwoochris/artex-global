package agent

import (
	"log"
	"path/filepath"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/noaadapter"
)

// noaWarn returns a diagnostics sink tagging non-fatal noa messages with the
// session, routed through the package logger (agents have no per-instance one).
func noaWarn(session string) func(string) {
	return func(msg string) { log.Printf("[noa] %s: %s", session, msg) }
}

// noa is the "model-driven context compression" mechanism introduced in norma v0.4.0; as an experimental platform feature, the user toggles it in
// the system settings. It is mutually exclusive with the built-in compaction: noaadapter.Enable is the only entry point, and in one go it attaches
// the context takeover (Compactor), the Compress tool and the three-part resident prompt; not calling Enable means it is off
// (built-in compaction works as usual). The switch is resolved by the noaEnabledFn injected into each agent and read once per
// run, so toggling only affects runs started afterwards, with no need to rebuild the agent.

// enableNoa wires noa into opts when the resolver reports it as enabled. archiveRoot is the base directory where the compressed originals are persisted
// (the global workDir; all agents land under <workDir>/noa rather than being scattered across task/intent directories), and sessionID
// names the archive subdirectory under it (globally unique, so there are no collisions within the same base directory).
//
// noa is experimental: a failure to wire it in must not interrupt a real task. On error, report via onWarn and fall back to the built-in compression.
// On success, clear opts.Compaction so agentcore doesn't warn that "two context managers are set at the same time".
func enableNoa(opts *agentcore.Options, enabled func() bool, archiveRoot, sessionID string, onWarn func(string)) {
	if enabled == nil || !enabled() {
		return
	}
	if opts.OnWarn == nil {
		opts.OnWarn = onWarn
	}
	if err := noaadapter.Enable(opts, noaadapter.Options{
		ArchiveBaseDir: filepath.Join(archiveRoot, "noa"),
		SessionID:      sessionID,
		OnWarn:         onWarn,
	}); err != nil {
		if onWarn != nil {
			onWarn("noa 压缩启用失败,回退内置压缩:" + err.Error())
		}
		return
	}
	// Compactor overrides Compaction, but when both are present agentcore warns every time; clear it explicitly.
	opts.Compaction = nil
}
