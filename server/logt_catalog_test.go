package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/yukiteruamano/artex-global/config"
)

// TestLogTTemplatesHaveTranslations pins that every logT("…") template in this
// package has an es entry in config's log catalog. English is the source language, so
// config.T returns the key unchanged when the translation is missing: a key that drifts
// by one character (spacing, an escaped quote) would otherwise ship English console logs
// to a Spanish operator without any test noticing.
func TestLogTTemplatesHaveTranslations(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var keys []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "logT" {
				return true
			}
			// A plain literal is the template itself. A computed argument is a lookup
			// over enum values, so the templates it yields at runtime are the map
			// values — the keys are the input enum, not console text.
			found := 0
			visit := func(arg ast.Expr) {
				ast.Inspect(arg, func(n ast.Node) bool {
					lit, ok := n.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						return true
					}
					s, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatalf("%s: %v", fset.Position(lit.Pos()), err)
					}
					keys = append(keys, s)
					found++
					return true
				})
			}
			arg := call.Args[0]
			if index, ok := arg.(*ast.IndexExpr); ok {
				arg = index.X // map literal lookup: map[...]{...}[action]
			}
			if composite, ok := arg.(*ast.CompositeLit); ok {
				for _, elt := range composite.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						visit(kv.Value)
						continue
					}
					visit(elt)
				}
			} else {
				visit(arg)
			}
			if found == 0 {
				t.Errorf("%s: no template literal found in logT argument", fset.Position(call.Pos()))
			}
			return true
		})
	}
	if len(keys) == 0 {
		t.Fatal("found no logT call sites; the scan is broken")
	}
	// en is the identity mapping, so only the Spanish overlay can be missing.
	t.Setenv("ARTEX_LANG", "es")
	for _, k := range keys {
		if config.T(k) == k {
			t.Errorf("logT template has no es translation: %q", k)
		}
	}
}

// TestErrCatalogFormatVerbsMatch pins that translated API error templates keep the
// English source template's format verbs in order; trMsg wraps the template before
// fmt.Sprintf/Errorf fill it, so a mismatch would garble the response.
func TestErrCatalogFormatVerbsMatch(t *testing.T) {
	verbs := regexp.MustCompile(`%[-+# 0-9.]*[a-zA-Z%]`)
	for source, tr := range errCatalog {
		want := strings.Join(verbs.FindAllString(source, -1), " ")
		for lang, v := range tr {
			if got := strings.Join(verbs.FindAllString(v, -1), " "); got != want {
				t.Errorf("errCatalog[%q][%s] verbs = %q, want %q", source, lang, got, want)
			}
		}
	}
}

// TestAuthErrorsTranslated pins that every authErr* message the auth endpoints
// return has a Spanish translation (and is its own English source text), so a Spanish
// user who fails a login or the first-run setup sees the error in their language.
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
	// en is the identity mapping: the constant itself is what an English user reads.
	config.CurrentLanguage = func() string { return "en" }
	for _, m := range msgs {
		if trMsg(m) != m {
			t.Errorf("auth error must be its own English source text: %q -> %q", m, trMsg(m))
		}
	}
	config.CurrentLanguage = func() string { return "es" }
	for _, m := range msgs {
		if trMsg(m) == m {
			t.Errorf("auth error has no es translation: %q", m)
		}
	}
}
