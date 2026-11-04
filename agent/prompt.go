package agent

import (
	"bytes"
	"fmt"
	"text/template"
	"time"
)

// PromptOverride, if set, returns the stored system-prompt template for an agent
// key and whether one exists. The server wires it to the PG agent_prompts table.
// When nil or no override exists, agents use their built-in default prompt — so
// behavior is identical until a user edits a prompt in the UI.
var PromptOverride func(agentKey string) (string, bool)

// Prompt-variable structs — fields mirror each agent's catalog (docs §5a) so a
// user template referencing a catalog variable renders; referencing anything else
// fails template execution and falls back to the built-in default.
type PlannerVars struct{ Goal, Scope, AssetSummary, DataDir, Now string }
type WorkerVars struct{ ProxyAddr, WorkerName, DataDir, Now string }
type MainVars struct{ Goal, AssetSummary, FindingsSummary, DataDir, Now string }
type GoalsVars struct{ EngagementDescription, DataDir, Now string }

// nowStr is the server-local wall-clock string exposed as the universal {{.Now}}
// prompt variable. renderSystem runs on every agent turn/round, so this is fresh
// each run — a prompt can subtract it from a fixed start stamp to reason about
// elapsed time (e.g. a timed benchmark's "last N hours" window).
func nowStr() string { return time.Now().Format("2006-01-02 15:04:05 MST") }

// renderSystem returns the rendered system-prompt BODY (section [A]) for agentKey.
// Precedence: the DB-stored template (if any) over the built-in default template
// (def). BOTH are Go templates now — the built-in default is seeded into the DB
// verbatim, so the two paths render identically until a user edits the prompt.
// Rendering always runs (def used to be pre-substituted plain text; it is now a
// {{.Var}} template like the DB one). On any render error we fall back to the
// default template, then to the raw default string — an agent never starts with a
// half-rendered prompt. Callers append the code-owned tail (trafficTool / artifact
// output spec) AFTER this, so those can't be edited away via the DB body.
func renderSystem(agentKey, def string, vars any) string {
	tmpl := def
	if PromptOverride != nil {
		if t, ok := PromptOverride(agentKey); ok && t != "" {
			tmpl = t
		}
	}
	if out, err := renderTmpl(tmpl, vars); err == nil {
		return out
	}
	// DB template broke (e.g. references an out-of-catalog var) → code default.
	if out, err := renderTmpl(def, vars); err == nil {
		return out
	}
	return def
}

// langDirective is the output-language tail: a code-owned segment appended AFTER
// the rendered body and the artifact/traffic tails on every user-facing agent role,
// so a DB-edited prompt body can never drop it — the same guarantee artifactSpec
// gives. It does NOT translate the agent "brain": the benchmarked reasoning body
// (section [A]) stays in its own language. It only constrains the LANGUAGE of what
// the agent SHOWS to the user. It is written in English, the source language of the
// prompt bodies, so the directive reads in the same register as the body it follows.
// Raw technical strings (commands, payloads, code, URLs, log/response excerpts) are
// explicitly kept verbatim so evidence and reproduction steps are not mangled by
// translation.
//
// Two anti-drift clauses were added after the end-to-end run (L1): live models
// leaked (1) Chinese into the planner's situation summary — mirroring the then
// Chinese brain body (section [A]) — and (2) the target app/evidence language into
// report_finding's structured fields. The directive names the planner situation
// summary as a user-facing field and forbids mirroring BOTH the instruction language
// AND the target/material language in the display fields.
//
// The display language is a parameter (code = "en"/"es"): the "write in <language>"
// token is swapped per target; every other clause is identical for all supported
// languages.
func langDirective(code string) string {
	phrase := langPhrase[code]
	bare := langBare[code]
	if phrase == "" || bare == "" {
		phrase, bare = langPhrase[defaultOutputLang], langBare[defaultOutputLang]
	}
	return fmt.Sprintf("\n\n**Output language rule (localization, highest priority, cannot be overridden by the prompt body)**: every piece of natural-language text [shown to the user] must be written in **%s** — including record_fact's summary/detail, report_finding's title/description/conclusion/remediation, the planner's situation summary, the final one-sentence summary, and chat replies to the user. But **commands, payloads, code, file paths, URLs, parameter names, and raw excerpts of logs/requests/responses** must be **kept word for word**, not translated or rewritten (evidence command lines and output especially must be copied as-is so they stay reproducible). **Even if the system/role instructions above are written in English, and even if the target system, its pages, evidence, logs or any reference material is in English, Spanish or any other language, the user-facing natural-language fields (title/description/conclusion/remediation/summary/situation summary) must still be written in %s — do not mirror or copy the language of the target or material into these display fields; only the raw technical fragments listed above stay as-is.** **Your analysis/planning/thinking may be in any language, but that is [invisible internal reasoning] and must never be output as body text: the visible reply to the user must be %s from the first character, with no Chinese or other-language preamble in front of it — clarification questions, missing-parameter notes and \"cannot continue\" explanations must also be written directly in %s.** In one sentence: how you think internally is unrestricted, but any body text that reaches the user must be entirely %s (raw technical fragments excepted).", phrase, bare, bare, bare, bare)
}

func renderTmpl(tmpl string, vars any) (string, error) {
	t, err := template.New("p").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, vars); err != nil {
		return "", err
	}
	return b.String(), nil
}
