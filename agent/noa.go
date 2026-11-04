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

// noa is the "model-driven context compaction" mechanism introduced in norma v0.4.0, an
// experimental platform feature toggled by the user in system settings. It is mutually
// exclusive with the built-in compaction: noaadapter.Enable is the only entry point and
// attaches the context taker-over (Compactor), the Compress tool and three resident prompts
// in one go; not calling Enable means off (built-in compaction keeps working). The switch is
// resolved by the noaEnabledFn injected into each agent and read once per run, so flipping it
// only affects runs started afterwards and needs no agent rebuild.

// enableNoa wires noa into opts when the resolver reports it enabled. archiveRoot is the
// persistence base dir for compressed originals (the global workDir; every agent archives
// under <workDir>/noa rather than spreading across task/intent dirs), and sessionID names the
// archive subdirectory under it (globally unique, so no collisions inside one base dir).
//
// noa is experimental: a failed hookup must not interrupt a real task. On error it is reported
// through onWarn and the built-in compaction is used instead. On success opts.Compaction is
// cleared so agentcore does not warn about "two context managers set at once".
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
			onWarn("failed to enable noa compaction, falling back to built-in compaction: " + err.Error())
		}
		return
	}
	// Compactor overrides Compaction, but agentcore warns on every run when both are set; clear it explicitly.
	opts.Compaction = nil
}
