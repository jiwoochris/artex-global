package agent

import (
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// tmplActionRe matches Go template actions such as {{.Goal}} or {{if .X}}.
var tmplActionRe = regexp.MustCompile(`\{\{[^}]*\}\}`)

// toolNameRe matches snake_case identifiers (tool, field and parameter names), which a
// translation must keep verbatim.
var toolNameRe = regexp.MustCompile(`\b[a-z][a-z0-9]*(?:_[a-z0-9]+)+\b`)

// hanHangulRe matches Chinese and Korean characters.
var hanHangulRe = regexp.MustCompile(`[\p{Han}\p{Hangul}]`)

// parenRe matches a parenthesized, backticked or double-quoted aside, where a translation
// may quote the original wording of a literal the model must emit or recognize.
var parenRe = regexp.MustCompile("\\([^()]*\\)|`[^`]*`|\"[^\"]*\"")

// TestPromptReferencesUpToDate fails when a built-in prompt changed without its English
// reference translation being updated. To fix it, update prompts_en/<name>.md and set
// its header to the hash printed here.
func TestPromptReferencesUpToDate(t *testing.T) {
	for name, src := range promptRefSources() {
		raw, err := promptRefFS.ReadFile(promptRefDir + "/" + name + ".md")
		if err != nil {
			t.Errorf("%s: missing reference translation %s/%s.md", name, promptRefDir, name)
			continue
		}
		hash, body, ok := parsePromptRef(string(raw))
		if !ok {
			t.Errorf("%s: first line must be %q", name, promptRefHeaderPrefix+"<hex>"+promptRefHeaderSuffix)
			continue
		}
		if want := PromptTextHash(src); hash != want {
			t.Errorf("%s: the built-in prompt changed since it was translated. Update the translation, then set its header to %s%s%s",
				name, promptRefHeaderPrefix, want, promptRefHeaderSuffix)
		}
		if body == "" {
			t.Errorf("%s: empty translation", name)
		}
		if got, ok := EnglishReference(src); !ok || got != body {
			t.Errorf("%s: EnglishReference does not return the translation for its own source", name)
		}
	}
}

// TestPromptReferencesNoOrphans keeps prompts_en/ free of files no source points at.
func TestPromptReferencesNoOrphans(t *testing.T) {
	srcs := promptRefSources()
	entries, err := fs.ReadDir(promptRefFS, promptRefDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".md")
		if _, ok := srcs[name]; !ok {
			t.Errorf("%s/%s has no entry in promptRefSources", promptRefDir, e.Name())
		}
	}
}

// TestPromptReferencesKeepLiterals checks that a translation keeps what the model
// reads literally: every template action, and every snake_case tool or field name.
func TestPromptReferencesKeepLiterals(t *testing.T) {
	for name, src := range promptRefSources() {
		ref, ok := EnglishReference(src)
		if !ok {
			continue // reported by TestPromptReferencesUpToDate
		}
		if a, b := sortedMatches(tmplActionRe, src), sortedMatches(tmplActionRe, ref); strings.Join(a, " ") != strings.Join(b, " ") {
			t.Errorf("%s: template actions differ\n source: %v\n english: %v", name, a, b)
		}
		for _, id := range uniqueMatches(toolNameRe, src) {
			if !strings.Contains(ref, id) {
				t.Errorf("%s: identifier %q is missing from the translation", name, id)
			}
		}
	}
}

// TestPromptReferencesAreEnglish allows Chinese or Korean only where a translation
// quotes original wording (inside parentheses, backticks or double quotes).
func TestPromptReferencesAreEnglish(t *testing.T) {
	for name, src := range promptRefSources() {
		ref, ok := EnglishReference(src)
		if !ok {
			continue
		}
		for i, line := range strings.Split(ref, "\n") {
			if hanHangulRe.MatchString(parenRe.ReplaceAllString(line, "")) {
				t.Errorf("%s line %d is not translated: %s", name, i+2, line)
			}
		}
	}
}

func TestEnglishReferenceMatching(t *testing.T) {
	ref, ok := EnglishReference(goalsDefaultTmpl)
	if !ok || ref == "" {
		t.Fatal("no reference for goalsDefaultTmpl")
	}
	crlf := "\r\n  " + strings.ReplaceAll(goalsDefaultTmpl, "\n", "\r\n") + "\n\n"
	if got, ok := EnglishReference(crlf); !ok || got != ref {
		t.Error("matching must ignore CRLF line endings and surrounding whitespace")
	}
	if _, ok := EnglishReference(goalsDefaultTmpl + " edited"); ok {
		t.Error("an edited prompt must not match the built-in reference")
	}
	for _, s := range []string{"", "  \n"} {
		if _, ok := EnglishReference(s); ok {
			t.Errorf("blank text %q must not match", s)
		}
	}
}

func sortedMatches(re *regexp.Regexp, s string) []string {
	m := re.FindAllString(s, -1)
	sort.Strings(m)
	return m
}

func uniqueMatches(re *regexp.Regexp, s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range re.FindAllString(s, -1) {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}
