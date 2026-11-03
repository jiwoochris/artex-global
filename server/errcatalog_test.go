package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/config"
)

// TestErrCatalogFormatVerbsMatch pins that translated API error templates keep the
// Korean template's format verbs in order; trMsg wraps the template before
// fmt.Sprintf/Errorf fill it, so a mismatch would garble the response.
func TestErrCatalogFormatVerbsMatch(t *testing.T) {
	verbs := regexp.MustCompile(`%[-+# 0-9.]*[a-zA-Z%]`)
	for ko, tr := range errCatalog {
		want := strings.Join(verbs.FindAllString(ko, -1), " ")
		for lang, v := range tr {
			if got := strings.Join(verbs.FindAllString(v, -1), " "); got != want {
				t.Errorf("errCatalog[%q][%s] verbs = %q, want %q", ko, lang, got, want)
			}
		}
	}
}

// TestAuthErrorsTranslated pins that every authErr* message the auth endpoints
// return has an en/zh/es translation, so a non-Korean user who fails a login or
// the first-run setup sees the error in the chosen language.
func TestAuthErrorsTranslated(t *testing.T) {
	t.Cleanup(func() { config.CurrentLanguage = nil })
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "auth.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var msgs []string
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "authErr") || i >= len(vs.Values) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					s, _ := strconv.Unquote(lit.Value)
					msgs = append(msgs, s)
				}
			}
		}
	}
	if len(msgs) == 0 {
		t.Fatal("found no authErr constants; the scan is broken")
	}
	for _, lang := range []string{"en", "zh", "es"} {
		config.CurrentLanguage = func() string { return lang }
		for _, m := range msgs {
			if trMsg(m) == m {
				t.Errorf("auth error has no %s translation: %q", lang, m)
			}
		}
	}
}
