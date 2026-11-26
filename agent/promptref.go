package agent

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"strings"
)

// English reference translations of the built-in prompts.
//
// The built-in prompt bodies stay in their original language (Chinese, and Korean for
// some wrap-up prompts) because that is what the agents were benchmarked with. These
// translations exist only so that people can read what the agents are told: the web UI
// shows them next to the original. Nothing in this file is ever sent to a model.
//
// Each translation lives in prompts_en/<ConstantName>.md. Its first line records the
// SHA-256 of the source text it was translated from:
//
//	<!-- source-sha256: <hex> -->
//
// When a built-in prompt changes, its hash no longer matches, the stale translation is
// no longer served, and TestPromptReferencesUpToDate fails until the translation and its
// hash are updated together.

//go:embed prompts_en/*.md
var promptRefFS embed.FS

const (
	promptRefDir          = "prompts_en"
	promptRefHeaderPrefix = "<!-- source-sha256: "
	promptRefHeaderSuffix = " -->"
)

// promptRefSources maps each reference file name (without .md) to the built-in text it
// translates. Every entry must have an up-to-date file in prompts_en/.
func promptRefSources() map[string]string {
	return map[string]string{
		// System prompt bodies (segment [A]), editable in the agent editor.
		"goalsDefaultTmpl":       goalsDefaultTmpl,
		"plannerDefaultTmpl":     plannerDefaultTmpl,
		"mainAgentDefaultTmpl":   mainAgentDefaultTmpl,
		"workerDefaultTmpl":      workerDefaultTmpl,
		"autoDefaultTmpl":        autoDefaultTmpl,
		"pentestDefaultTmpl":     pentestDefaultTmpl,
		"DefaultAssistantPrompt": DefaultAssistantPrompt,
		"ReporterDefaultPrompt":  ReporterDefaultPrompt,
		"RetesterDefaultPrompt":  RetesterDefaultPrompt,
		// Context compaction (not editable, but visible in recorded LLM requests).
		"compressionSystemPrompt": compressionSystemPrompt,
		// Per-run wrap-up prompts.
		"settleWrapUpPrompt":     settleWrapUpPrompt,
		"plannerWrapUpDefault":   plannerWrapUpDefault,
		"mainAgentWrapUpDefault": mainAgentWrapUpDefault,
		"genericWrapUpDefault":   genericWrapUpDefault,
		// Task-level timeout wrap-up prompts.
		"workerTaskTimeoutDefault":  workerTaskTimeoutDefault,
		"plannerTaskTimeoutDefault": plannerTaskTimeoutDefault,
	}
}

// normalizePromptText makes matching insensitive to line endings and to leading or
// trailing whitespace, which the editor and the DB round trip may change.
func normalizePromptText(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
}

// PromptTextHash returns the hex SHA-256 of the normalized text. It identifies the
// source a reference translation was made from.
func PromptTextHash(s string) string {
	sum := sha256.Sum256([]byte(normalizePromptText(s)))
	return hex.EncodeToString(sum[:])
}

// parsePromptRef splits a reference file into the recorded source hash and the
// translation body. ok is false when the header line is missing or malformed.
func parsePromptRef(raw string) (hash, body string, ok bool) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	first, rest, found := strings.Cut(raw, "\n")
	first = strings.TrimSpace(first)
	if !found || !strings.HasPrefix(first, promptRefHeaderPrefix) || !strings.HasSuffix(first, promptRefHeaderSuffix) {
		return "", "", false
	}
	hash = strings.TrimSuffix(strings.TrimPrefix(first, promptRefHeaderPrefix), promptRefHeaderSuffix)
	return hash, strings.TrimSpace(rest), hash != ""
}

// promptRefByHash indexes the up-to-date translations by the hash of their source.
// Stale or malformed files are left out, so a changed prompt never shows an outdated
// translation.
var promptRefByHash = buildPromptRefIndex()

func buildPromptRefIndex() map[string]string {
	idx := make(map[string]string)
	for name, src := range promptRefSources() {
		raw, err := promptRefFS.ReadFile(promptRefDir + "/" + name + ".md")
		if err != nil {
			continue
		}
		hash, body, ok := parsePromptRef(string(raw))
		want := PromptTextHash(src)
		if !ok || hash != want || body == "" {
			continue
		}
		idx[want] = body
	}
	return idx
}

// EnglishReference returns the reviewed English translation of text when text is one
// of the built-in prompts, ignoring line endings and surrounding whitespace. ok is
// false for edited, custom or unknown text; callers can fall back to machine
// translation for those.
func EnglishReference(text string) (string, bool) {
	if normalizePromptText(text) == "" {
		return "", false
	}
	ref, ok := promptRefByHash[PromptTextHash(text)]
	return ref, ok
}
